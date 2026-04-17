package phos

import (
	"encoding/json"
	"log/slog"
	"time"
)

type Snapshot struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ParentID  string          `json:"parent_id"`
	TraceID   string          `json:"trace_id"`
	TimeStart time.Time       `json:"time_start"`
	TimeEnd   time.Time       `json:"time_end"`
	Kind      SpanKind        `json:"kind"`
	Failed    bool            `json:"failed"`
	Attrs     []slog.Attr     `json:"attrs"`
	Links     []SnapshotLink  `json:"links"`
	Events    []SnapshotEvent `json:"events"`
	Errors    []SnapshotError `json:"errors"`
}

type SnapshotLink struct {
	TraceID string      `json:"trace_id"`
	SpanID  string      `json:"span_id"`
	Attrs   []slog.Attr `json:"attrs"`
}

type SnapshotEvent struct {
	Time  time.Time   `json:"time"`
	Name  string      `json:"name"`
	Attrs []slog.Attr `json:"attrs"`
}

type SnapshotError struct {
	Err   error       `json:"-"`
	Attrs []slog.Attr `json:"attrs"`
}

func (e SnapshotError) MarshalJSON() ([]byte, error) {
	type errorJSON struct {
		Message string      `json:"message"`
		Attrs   []slog.Attr `json:"attrs"`
	}
	return json.Marshal(errorJSON{
		Message: errorMessage(e.Err),
		Attrs:   e.Attrs,
	})
}

func errorMessage(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
