package dashboard

import (
	"database/sql"

	"github.com/johan-st/phos"
	"github.com/johan-st/phos/internal/tracejsonl"
)

// Native source fixtures use the original agent schema without its runtime.
type nativeTestStore struct{ db *sql.DB }

func newNativeTestStore(path string) (*nativeTestStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS spans_v2 (
		seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL, trace_id TEXT NOT NULL,
		start_time_unix_nano INTEGER NOT NULL, snapshot_json TEXT NOT NULL);
		CREATE INDEX IF NOT EXISTS idx_spans_v2_trace_id ON spans_v2(trace_id);
		CREATE INDEX IF NOT EXISTS idx_spans_v2_start_time ON spans_v2(start_time_unix_nano);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &nativeTestStore{db: db}, nil
}

func (s *nativeTestStore) Close() error { return s.db.Close() }

func (s *nativeTestStore) Append(span phos.Snapshot) {
	raw, err := tracejsonl.Marshal(span)
	if err != nil {
		panic(err)
	}
	if _, err = s.db.Exec(`INSERT INTO spans_v2(id,trace_id,start_time_unix_nano,snapshot_json) VALUES(?,?,?,?)`, span.ID, span.TraceID, span.TimeStart.UnixNano(), raw); err != nil {
		panic(err)
	}
}

func (s *nativeTestStore) Clear() {
	if _, err := s.db.Exec(`DELETE FROM spans_v2`); err != nil {
		panic(err)
	}
}
