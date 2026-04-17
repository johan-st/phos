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

func (s Snapshot) MarshalJSON() ([]byte, error) {
	type snapshotJSON struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		ParentID  string          `json:"parent_id"`
		TraceID   string          `json:"trace_id"`
		TimeStart time.Time       `json:"time_start"`
		TimeEnd   time.Time       `json:"time_end"`
		Kind      SpanKind        `json:"kind"`
		Failed    bool            `json:"failed"`
		Attrs     []attrJSON      `json:"attrs"`
		Links     []SnapshotLink  `json:"links"`
		Events    []SnapshotEvent `json:"events"`
		Errors    []SnapshotError `json:"errors"`
	}
	return json.Marshal(snapshotJSON{
		ID:        s.ID,
		Name:      s.Name,
		ParentID:  s.ParentID,
		TraceID:   s.TraceID,
		TimeStart: s.TimeStart,
		TimeEnd:   s.TimeEnd,
		Kind:      s.Kind,
		Failed:    s.Failed,
		Attrs:     marshalAttrs(s.Attrs),
		Links:     s.Links,
		Events:    s.Events,
		Errors:    s.Errors,
	})
}

type SnapshotLink struct {
	TraceID string      `json:"trace_id"`
	SpanID  string      `json:"span_id"`
	Attrs   []slog.Attr `json:"attrs"`
}

func (l SnapshotLink) MarshalJSON() ([]byte, error) {
	type linkJSON struct {
		TraceID string     `json:"trace_id"`
		SpanID  string     `json:"span_id"`
		Attrs   []attrJSON `json:"attrs"`
	}
	return json.Marshal(linkJSON{
		TraceID: l.TraceID,
		SpanID:  l.SpanID,
		Attrs:   marshalAttrs(l.Attrs),
	})
}

type SnapshotEvent struct {
	Time  time.Time   `json:"time"`
	Name  string      `json:"name"`
	Attrs []slog.Attr `json:"attrs"`
}

func (e SnapshotEvent) MarshalJSON() ([]byte, error) {
	type eventJSON struct {
		Time  time.Time  `json:"time"`
		Name  string     `json:"name"`
		Attrs []attrJSON `json:"attrs"`
	}
	return json.Marshal(eventJSON{
		Time:  e.Time,
		Name:  e.Name,
		Attrs: marshalAttrs(e.Attrs),
	})
}

type SnapshotError struct {
	Err   error       `json:"-"`
	Attrs []slog.Attr `json:"attrs"`
}

func (e SnapshotError) MarshalJSON() ([]byte, error) {
	type errorJSON struct {
		Message string     `json:"message"`
		Attrs   []attrJSON `json:"attrs"`
	}
	return json.Marshal(errorJSON{
		Message: errorMessage(e.Err),
		Attrs:   marshalAttrs(e.Attrs),
	})
}

type attrJSON struct {
	Key   string `json:"Key"`
	Value any    `json:"Value"`
}

func marshalAttrs(attrs []slog.Attr) []attrJSON {
	if len(attrs) == 0 {
		return nil
	}

	out := make([]attrJSON, len(attrs))
	for i, attr := range attrs {
		resolved := attr.Value.Resolve()
		out[i] = attrJSON{
			Key:   attr.Key,
			Value: slogValueToAny(resolved),
		}
	}
	return out
}

func slogValueToAny(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindTime:
		return value.Time()
	case slog.KindGroup:
		return marshalAttrs(value.Group())
	case slog.KindAny:
		return value.Any()
	default:
		return value.Any()
	}
}

func errorMessage(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
