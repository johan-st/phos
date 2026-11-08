package dashboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/johan-st/phos"
	"github.com/johan-st/phos/internal/tracejsonl"
)

// dashboardStore owns its database. Native trace databases remain read-only inputs.
// Each transaction publishes spans, summaries, projections and a checkpoint at
// one sequence. Validity intervals keep existing query/detail cursors stable.
type dashboardStore struct {
	db         *sql.DB
	writer     sync.Mutex
	mu         sync.Mutex
	health     sourceHealth
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	resultsDir string
}

func openDashboardStore(path string) (*dashboardStore, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	// Inspect existing files read-only before applying any connection pragmas.
	if _, statErr := os.Stat(abs); statErr == nil {
		probeURL := u
		probeURL.RawQuery = "mode=ro"
		probe, e := sql.Open("sqlite", probeURL.String())
		if e != nil {
			return nil, e
		}
		var tables, owned int
		e = probe.QueryRow(`SELECT count(*),COALESCE(sum(name='dashboard_state'),0) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables, &owned)
		probe.Close()
		if e != nil {
			return nil, e
		}
		if tables > 0 && owned == 0 {
			return nil, errors.New("dashboard database must be separate from existing databases")
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	u.RawQuery = url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)"}}.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &dashboardStore{db: db, health: sourceHealth{Kind: "dashboard sqlite", RetentionKnown: true}}
	fail := func(e error) (*dashboardStore, error) { db.Close(); return nil, e }
	// Refuse the agent writer's database, including when addressed through a symlink.
	var legacy int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='spans_v2'`).Scan(&legacy); err != nil {
		return fail(err)
	}
	if legacy != 0 {
		return fail(errors.New("dashboard database must be separate from the agent trace database"))
	}
	_, err = db.Exec(`
 CREATE TABLE IF NOT EXISTS dashboard_state (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL, epoch TEXT NOT NULL, seq INTEGER NOT NULL, origin TEXT NOT NULL DEFAULT '', notice TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS dashboard_spans (trace TEXT NOT NULL,id TEXT NOT NULL,lo INTEGER NOT NULL,hi INTEGER,start INTEGER NOT NULL,service TEXT NOT NULL,operation TEXT NOT NULL,raw BLOB NOT NULL,PRIMARY KEY(trace,id,lo));
 CREATE UNIQUE INDEX IF NOT EXISTS dashboard_spans_current ON dashboard_spans(trace,id) WHERE hi IS NULL;
 CREATE INDEX IF NOT EXISTS dashboard_spans_time ON dashboard_spans(start,lo,hi,trace);
 CREATE TABLE IF NOT EXISTS dashboard_traces (trace TEXT NOT NULL,lo INTEGER NOT NULL,hi INTEGER,raw BLOB NOT NULL,PRIMARY KEY(trace,lo));
 CREATE TABLE IF NOT EXISTS dashboard_buckets (start INTEGER NOT NULL,service TEXT NOT NULL,operation TEXT NOT NULL,lo INTEGER NOT NULL,hi INTEGER,raw BLOB NOT NULL,PRIMARY KEY(start,service,operation,lo));
 CREATE TABLE IF NOT EXISTS dashboard_checkpoints (name TEXT PRIMARY KEY, value TEXT NOT NULL);
 `)
	if err != nil {
		return fail(err)
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO dashboard_state(id,version,epoch,seq) VALUES(1,1,?,0)`, uuid.NewString())
	if err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRow(`SELECT version,notice FROM dashboard_state WHERE id=1`).Scan(&version, &s.health.Notice); err != nil {
		return fail(err)
	}
	if version != 1 {
		return fail(fmt.Errorf("unsupported dashboard database version %d", version))
	}
	var updated string
	if e := db.QueryRow(`SELECT value FROM dashboard_checkpoints WHERE name='updated_at'`).Scan(&updated); e == nil {
		s.health.LastSuccess, _ = strconv.ParseInt(updated, 10, 64)
	} else if !errors.Is(e, sql.ErrNoRows) {
		return fail(e)
	}
	s.resultsDir, err = os.MkdirTemp("", "dashboard-results-")
	if err != nil {
		return fail(err)
	}
	return s, nil
}
func (s *dashboardStore) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	err := s.db.Close()
	return errors.Join(err, os.RemoveAll(s.resultsDir))
}
func (s *dashboardStore) Health() sourceHealth { s.mu.Lock(); defer s.mu.Unlock(); return s.health }
func (s *dashboardStore) record(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.health.Error = err.Error()
	} else {
		s.health.Error = ""
		s.health.LastSuccess = time.Now().UnixMilli()
	}
}
func (s *dashboardStore) Stamp(ctx context.Context) (w sourceStamp, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT epoch,seq FROM dashboard_state WHERE id=1`).Scan(&w.Epoch, &w.MaxSeq)
	return
}
func (s *dashboardStore) validate(ctx context.Context, w sourceStamp) error {
	current, err := s.Stamp(ctx)
	if err != nil {
		return err
	}
	if current.Epoch != w.Epoch || current.MaxSeq < w.MaxSeq {
		return errSourceChanged
	}
	return nil
}
func traceKey(p phos.Snapshot) string {
	if p.TraceID != "" {
		return p.TraceID
	}
	return p.ID
}
func readStoredTrace(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string, seq int64) (out []phos.Snapshot, err error) {
	rows, err := db.QueryContext(ctx, `SELECT raw FROM dashboard_spans WHERE trace=? AND lo<=? AND (hi IS NULL OR hi>?) ORDER BY id`, id, seq, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	size := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		size += len(raw)
		if size > searchMaxTraceBytes || len(out) >= feedMaxSpans {
			return out, errSourceLimit
		}
		p, e := tracejsonl.Unmarshal(raw)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *dashboardStore) Trace(ctx context.Context, id string, w sourceStamp) ([]phos.Snapshot, error) {
	if err := s.validate(ctx, w); err != nil {
		return nil, err
	}
	return readStoredTrace(ctx, s.db, id, w.MaxSeq)
}
func (s *dashboardStore) Services(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT service FROM dashboard_spans WHERE hi IS NULL ORDER BY service LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *dashboardStore) Visit(ctx context.Context, q searchQuery, w sourceStamp, visit func([]phos.Snapshot) error) (bool, error) {
	if err := s.validate(ctx, w); err != nil {
		return false, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT trace FROM dashboard_spans WHERE start>=? AND start<? AND lo<=? AND (hi IS NULL OR hi>?) AND (?='' OR trace=?) ORDER BY trace`, q.From, q.To, w.MaxSeq, w.MaxSeq, q.TraceID, q.TraceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return false, err
		}
		spans, e := s.Trace(ctx, id, w)
		if e != nil {
			return false, e
		}
		if e = visit(spans); e != nil {
			return false, e
		}
	}
	return false, rows.Err()
}
func (s *dashboardStore) checkpoint(ctx context.Context, name string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM dashboard_checkpoints WHERE name=?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return v, err
}

// One archive has one input origin. Mixing demo/native identities would make
// trace-ID links ambiguous, so require a different archive rather than merge them.
func (s *dashboardStore) bindOrigin(ctx context.Context, origin, notice string) error {
	s.writer.Lock()
	defer s.writer.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE dashboard_state SET origin=?,notice=? WHERE id=1 AND (origin='' OR origin=?)`, origin, notice, origin)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("dashboard database belongs to a different input; choose another database")
	}
	s.mu.Lock()
	s.health.Notice = notice
	s.mu.Unlock()
	return nil
}
func (s *dashboardStore) ingest(ctx context.Context, spans []phos.Snapshot, checkpoint, value string) error {
	s.writer.Lock()
	defer s.writer.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq int64
	if err = tx.QueryRowContext(ctx, `SELECT seq FROM dashboard_state WHERE id=1`).Scan(&seq); err != nil {
		return err
	}
	seq++
	traces := map[string]bool{}
	buckets := map[bucketKey]*storedBucket{}
	for _, p := range spans {
		if p.ID == "" {
			return errors.New("span ID is required")
		}
		raw, e := tracejsonl.Marshal(p)
		if e != nil {
			return e
		}
		if len(raw) > searchMaxTraceBytes {
			return errSourceLimit
		}
		id := traceKey(p)
		var old []byte
		e = tx.QueryRowContext(ctx, `SELECT raw FROM dashboard_spans WHERE trace=? AND id=? AND hi IS NULL`, id, p.ID).Scan(&old)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if string(old) == string(raw) {
			continue
		}
		if old != nil {
			previous, e := tracejsonl.Unmarshal(old)
			if e != nil {
				return e
			}
			if e = updateBucket(ctx, tx, buckets, previous, -1); e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE dashboard_spans SET hi=? WHERE trace=? AND id=? AND hi IS NULL`, seq, id, p.ID); e != nil {
				return e
			}
			// Multiple revisions of the same span in one input batch expose only its
			// final state at this transaction's watermark.
			if _, e = tx.ExecContext(ctx, `DELETE FROM dashboard_spans WHERE trace=? AND id=? AND lo=?`, id, p.ID, seq); e != nil {
				return e
			}
		}
		service, operation := spanNames(p)
		if _, e = tx.ExecContext(ctx, `INSERT INTO dashboard_spans(trace,id,lo,start,service,operation,raw) VALUES(?,?,?,?,?,?,?)`, id, p.ID, seq, p.TimeStart.UnixMilli(), service, operation, raw); e != nil {
			return e
		}
		if e = updateBucket(ctx, tx, buckets, p, 1); e != nil {
			return e
		}
		traces[id] = true
	}
	for id := range traces {
		spans, e := readStoredTrace(ctx, tx, id, seq)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(summarize(spans))
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE dashboard_traces SET hi=? WHERE trace=? AND hi IS NULL`, seq, id); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO dashboard_traces VALUES(?,?,NULL,?)`, id, seq, raw); e != nil {
			return e
		}
	}
	for k, b := range buckets {
		raw, e := json.Marshal(b)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE dashboard_buckets SET hi=? WHERE start=? AND service=? AND operation=? AND hi IS NULL`, seq, k.Start, k.Service, k.Operation); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO dashboard_buckets VALUES(?,?,?,?,NULL,?)`, k.Start, k.Service, k.Operation, seq, raw); e != nil {
			return e
		}
	}
	if len(traces) > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE dashboard_state SET seq=? WHERE id=1`, seq); err != nil {
			return err
		}
	}
	if checkpoint != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO dashboard_checkpoints VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, checkpoint, value); err != nil {
			return err
		}
	}
	if len(traces) > 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO dashboard_checkpoints VALUES('updated_at',?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
			return err
		}
	}
	err = tx.Commit()
	if err == nil {
		s.record(nil)
	}
	return err
}
func originID(path string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(path))) }
