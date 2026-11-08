package dashboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/johan-st/phos"
	"github.com/johan-st/phos/internal/tracejsonl"
)

type nativeCheckpoint struct {
	First string
	Seq   int64
	Hash  string
}

func (s *dashboardStore) followNative(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err = s.bindOrigin(context.Background(), "native:"+originID(abs), "Native spans; waiting for ingestion"); err != nil {
		return err
	}
	source, err := newSQLiteQuerySource(abs)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer source.Close()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			caughtUp, e := s.syncNative(ctx, source)
			s.record(e)
			if ctx.Err() != nil {
				return
			}
			if e == nil && !caughtUp {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

// Read one bounded source transaction, then commit its data and checkpoint to
// the dashboard. Failure between these transactions safely replays the batch.
func (s *dashboardStore) syncNative(ctx context.Context, source *sqliteQuerySource) (bool, error) {
	stamp, err := source.Stamp(ctx)
	if err != nil {
		return false, err
	}
	db, err := source.connection()
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	saved, err := s.checkpoint(ctx, "native")
	if err != nil {
		return false, err
	}
	var cp nativeCheckpoint
	if saved != "" {
		if err = json.Unmarshal([]byte(saved), &cp); err != nil {
			return false, err
		}
	}
	var firstSeq int64
	var firstRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT seq,snapshot_json FROM spans_v2 ORDER BY seq LIMIT 1`).Scan(&firstSeq, &firstRaw)
	if errors.Is(err, sql.ErrNoRows) {
		s.setNotice("Native source empty; retained dashboard history remains available")
		return true, nil
	}
	if err != nil {
		return false, err
	}
	first := fmt.Sprintf("%d:%x", firstSeq, sha256.Sum256(firstRaw))
	var lastRaw []byte
	if cp.Seq > 0 {
		err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM spans_v2 WHERE seq=?`, cp.Seq).Scan(&lastRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	reset := saved != "" && (cp.First != first || cp.Seq > stamp.MaxSeq || fmt.Sprintf("%x", sha256.Sum256(lastRaw)) != cp.Hash)
	if reset {
		cp = nativeCheckpoint{}
	}
	cp.First = first
	rows, err := tx.QueryContext(ctx, `SELECT seq,snapshot_json FROM spans_v2 WHERE seq>? AND seq<=? ORDER BY seq LIMIT 256`, cp.Seq, stamp.MaxSeq)
	if err != nil {
		return false, err
	}
	spans := []phos.Snapshot{}
	size := 0
	for rows.Next() {
		var seq int64
		var raw []byte
		if err = rows.Scan(&seq, &raw); err != nil {
			break
		}
		if len(raw) > searchMaxTraceBytes {
			err = errSourceLimit
			break
		}
		if size+len(raw) > searchMaxTraceBytes && len(spans) > 0 {
			break
		}
		var p phos.Snapshot
		p, err = tracejsonl.Unmarshal(raw)
		if err != nil {
			break
		}
		spans = append(spans, p)
		size += len(raw)
		cp.Seq = seq
		cp.Hash = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	// Release the read transaction before validation can reopen a replaced file.
	if err = source.validate(ctx, stamp); err != nil {
		return false, err
	}
	if len(spans) > 0 {
		raw, _ := json.Marshal(cp)
		if err = s.ingest(ctx, spans, "native", string(raw)); err != nil {
			return false, err
		}
	}
	caughtUp := cp.Seq >= stamp.MaxSeq
	notice := fmt.Sprintf("Native ingestion: %d/%d source updates", cp.Seq, stamp.MaxSeq)
	if reset {
		notice += "; input reset detected, previous dashboard history retained"
	}
	s.setNotice(notice)
	return caughtUp, nil
}
func (s *dashboardStore) setNotice(notice string) {
	s.mu.Lock()
	s.health.Notice = notice
	s.mu.Unlock()
}
