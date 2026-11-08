package dashboard

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/johan-st/phos"
	"github.com/johan-st/phos/internal/tracejsonl"
	_ "modernc.org/sqlite"
)

// The dashboard never creates or migrates the writer's database. Connections
// are reopened if the file is replaced; cursors never cross that boundary.
type sqliteQuerySource struct {
	stampMu     sync.Mutex
	mu          sync.Mutex
	path        string
	db          *sql.DB
	file        os.FileInfo
	epoch       string
	first, last int64
	health      sourceHealth
}

func newSQLiteQuerySource(path string) (*sqliteQuerySource, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("sqlite trace db path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return &sqliteQuerySource{path: abs, epoch: uuid.NewString(), health: sourceHealth{Kind: "sqlite", RetentionKnown: true}}, nil
}
func (s *sqliteQuerySource) Health() sourceHealth { s.mu.Lock(); defer s.mu.Unlock(); return s.health }
func (s *sqliteQuerySource) record(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.health.Error = err.Error()
	} else {
		s.health.Error = ""
		s.health.LastSuccess = time.Now().UnixMilli()
	}
}
func (s *sqliteQuerySource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
func (s *sqliteQuerySource) connection() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, err
	}
	if s.db != nil && s.file != nil && os.SameFile(info, s.file) {
		return s.db, nil
	}
	if s.db != nil {
		_ = s.db.Close()
	}
	u := url.URL{Scheme: "file", Path: s.path}
	v := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(1000)"}}
	u.RawQuery = v.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s.db = db
	s.file = info
	s.epoch = uuid.NewString()
	s.first = 0
	s.last = 0
	return db, nil
}
func (s *sqliteQuerySource) Stamp(ctx context.Context) (stamp sourceStamp, err error) {
	// Prevent older concurrent reads from moving the watermark backwards and
	// falsely reporting a clear while the writer is only appending.
	s.stampMu.Lock()
	defer s.stampMu.Unlock()
	defer func() { s.record(err) }()
	db, err := s.connection()
	if err != nil {
		return stamp, err
	}
	var first, last int64
	// Separate MIN/MAX subqueries use the primary-key extrema optimization;
	// a combined aggregate/COUNT would scan all historical rows on each poll.
	err = db.QueryRowContext(ctx, `SELECT COALESCE((SELECT MIN(seq) FROM spans_v2),0),COALESCE((SELECT MAX(seq) FROM spans_v2),0)`).Scan(&first, &last)
	if err != nil {
		return stamp, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first != 0 && (first != s.first || last < s.last) {
		s.epoch = uuid.NewString()
	}
	s.first = first
	s.last = last
	return sourceStamp{s.epoch, last}, nil
}
func (s *sqliteQuerySource) validate(ctx context.Context, w sourceStamp) error {
	got, err := s.Stamp(ctx)
	if err != nil {
		return err
	}
	if got.Epoch != w.Epoch || got.MaxSeq < w.MaxSeq {
		return errSourceChanged
	}
	return nil
}
func (s *sqliteQuerySource) Trace(ctx context.Context, id string, w sourceStamp) (out []phos.Snapshot, err error) {
	defer func() { s.record(err) }()
	db, err := s.connection()
	if err != nil {
		return nil, err
	}
	// The trace index bounds context lookup; MAX(seq) resolves updates as of the
	// search watermark, including changes that move a span outside the interval.
	rows, err := db.QueryContext(ctx, `SELECT snapshot_json FROM spans_v2 WHERE seq IN
	 (SELECT MAX(seq) FROM spans_v2 WHERE (trace_id=? OR (trace_id='' AND id=?)) AND seq<=? GROUP BY trace_id,id) ORDER BY seq`, id, id, w.MaxSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bytes := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		bytes += len(raw)
		if bytes > searchMaxTraceBytes || len(out) >= feedMaxSpans {
			return out, errSourceLimit
		}
		span, e := tracejsonl.Unmarshal(raw)
		if e != nil {
			return nil, e
		}
		out = append(out, span)
	}
	return out, rows.Err()
}
func (s *sqliteQuerySource) Visit(ctx context.Context, q searchQuery, w sourceStamp, visit func([]phos.Snapshot) error) (limited bool, err error) {
	defer func() { s.record(err) }()
	db, err := s.connection()
	if err != nil {
		return false, err
	}
	if q.TraceID != "" {
		spans, e := s.Trace(ctx, q.TraceID, w)
		if e != nil {
			return false, e
		}
		if len(spans) > 0 {
			return false, visit(spans)
		}
		return false, nil
	}
	// Candidate IDs only, never the entire span table in memory. SQLite may use
	// a temporary DISTINCT B-tree; payloads remain in the historical store.
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT CASE WHEN trace_id='' THEN id ELSE trace_id END FROM spans_v2 INDEXED BY idx_spans_v2_start_time
	 WHERE start_time_unix_nano>=? AND start_time_unix_nano<? AND seq<=?`, q.From*1000000, q.To*1000000, w.MaxSeq)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return false, err
		}
		var id string
		if err = rows.Scan(&id); err != nil {
			return false, err
		}
		spans, e := s.Trace(ctx, id, w)
		if e != nil {
			return false, e
		}
		if len(spans) > 0 {
			if err = visit(spans); err != nil {
				return false, err
			}
		}
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	return false, s.validate(ctx, w)
}
