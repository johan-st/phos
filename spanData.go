package phos

import (
	"encoding/json"
	"log/slog"
	"time"
)

type EventData struct {
	Time  time.Time   `json:"time"`
	Name  string      `json:"name"`
	Attrs []slog.Attr `json:"attrs"`
}

type ErrorData struct {
	Err   error       `json:"-"`
	Attrs []slog.Attr `json:"attrs"`
}

func (e ErrorData) MarshalJSON() ([]byte, error) {
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
		return ""
	}
	return err.Error()
}

type SpanData struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	ParentID  string      `json:"parent_id"`
	TraceID   string      `json:"trace_id"`
	StartTime time.Time   `json:"start_time"`
	EndTime   time.Time   `json:"end_time"`
	Failed    bool        `json:"failed"`
	Attrs     []slog.Attr `json:"attrs"`
	Events    []EventData `json:"events"`
	Errors    []ErrorData `json:"errors"`
	Root      bool        `json:"root"`
}
