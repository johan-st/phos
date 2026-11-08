package tracejsonl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/johan-st/phos"
)

const schemaVersion = 2

type attrJSON struct {
	Key   string          `json:"key"`
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

type eventJSON struct {
	Time  time.Time  `json:"time"`
	Name  string     `json:"name"`
	Attrs []attrJSON `json:"attrs,omitempty"`
}

type errorJSON struct {
	Message string     `json:"message"`
	Attrs   []attrJSON `json:"attrs,omitempty"`
}

type linkJSON struct {
	TraceID string     `json:"trace_id"`
	SpanID  string     `json:"span_id"`
	Attrs   []attrJSON `json:"attrs,omitempty"`
}

type snapshotJSON struct {
	SchemaVersion int         `json:"schema_version"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	ParentID      string      `json:"parent_id"`
	TraceID       string      `json:"trace_id"`
	TimeStart     time.Time   `json:"time_start"`
	TimeEnd       time.Time   `json:"time_end"`
	Kind          int         `json:"kind"`
	Failed        bool        `json:"failed"`
	Attrs         []attrJSON  `json:"attrs"`
	Links         []linkJSON  `json:"links"`
	Events        []eventJSON `json:"events"`
	Errors        []errorJSON `json:"errors"`
}

type LineExporter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewLineExporter(w io.Writer) *LineExporter { return &LineExporter{w: w} }

func (e *LineExporter) Export(snapshot phos.Snapshot) {
	if e == nil || e.w == nil {
		return
	}
	body, err := Marshal(snapshot)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(append(body, '\n'))
}

func Marshal(snapshot phos.Snapshot) ([]byte, error) { return json.Marshal(toJSON(snapshot)) }

func Unmarshal(data []byte) (phos.Snapshot, error) {
	var payload snapshotJSON
	if err := json.Unmarshal(data, &payload); err != nil {
		return phos.Snapshot{}, err
	}
	if payload.SchemaVersion != schemaVersion {
		return phos.Snapshot{}, fmt.Errorf("unsupported trace snapshot schema version %d", payload.SchemaVersion)
	}
	return fromJSON(payload)
}

func LooksLikeSnapshotLine(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	return strings.HasPrefix(trimmed, "{") &&
		strings.Contains(trimmed, `"trace_id"`) &&
		strings.Contains(trimmed, `"time_start"`) &&
		strings.Contains(trimmed, `"time_end"`)
}

func toJSON(snapshot phos.Snapshot) snapshotJSON {
	payload := snapshotJSON{
		SchemaVersion: schemaVersion,
		ID:            snapshot.ID, Name: snapshot.Name, ParentID: snapshot.ParentID, TraceID: snapshot.TraceID,
		TimeStart: snapshot.TimeStart, TimeEnd: snapshot.TimeEnd, Kind: int(snapshot.Kind), Failed: snapshot.Failed,
		Attrs: marshalAttrs(snapshot.Attrs),
	}
	for _, link := range snapshot.Links {
		payload.Links = append(payload.Links, linkJSON{TraceID: link.TraceID, SpanID: link.SpanID, Attrs: marshalAttrs(link.Attrs)})
	}
	for _, event := range snapshot.Events {
		payload.Events = append(payload.Events, eventJSON{Time: event.Time, Name: event.Name, Attrs: marshalAttrs(event.Attrs)})
	}
	for _, item := range snapshot.Errors {
		message := ""
		if item.Err != nil {
			message = item.Err.Error()
		}
		payload.Errors = append(payload.Errors, errorJSON{Message: message, Attrs: marshalAttrs(item.Attrs)})
	}
	return payload
}

func fromJSON(payload snapshotJSON) (phos.Snapshot, error) {
	attrs, err := unmarshalAttrs(payload.Attrs)
	if err != nil {
		return phos.Snapshot{}, err
	}
	snapshot := phos.Snapshot{
		ID: payload.ID, Name: payload.Name, ParentID: payload.ParentID, TraceID: payload.TraceID,
		TimeStart: payload.TimeStart, TimeEnd: payload.TimeEnd, Kind: phos.SpanKind(payload.Kind),
		Failed: payload.Failed, Attrs: attrs,
	}
	for _, link := range payload.Links {
		attrs, err := unmarshalAttrs(link.Attrs)
		if err != nil {
			return phos.Snapshot{}, err
		}
		snapshot.Links = append(snapshot.Links, phos.SnapshotLink{TraceID: link.TraceID, SpanID: link.SpanID, Attrs: attrs})
	}
	for _, event := range payload.Events {
		attrs, err := unmarshalAttrs(event.Attrs)
		if err != nil {
			return phos.Snapshot{}, err
		}
		snapshot.Events = append(snapshot.Events, phos.SnapshotEvent{Time: event.Time, Name: event.Name, Attrs: attrs})
	}
	for _, item := range payload.Errors {
		attrs, err := unmarshalAttrs(item.Attrs)
		if err != nil {
			return phos.Snapshot{}, err
		}
		var itemErr error
		if item.Message != "" {
			itemErr = errors.New(item.Message)
		}
		snapshot.Errors = append(snapshot.Errors, phos.SnapshotError{Err: itemErr, Attrs: attrs})
	}
	return snapshot, nil
}

func marshalAttrs(attrs []slog.Attr) []attrJSON {
	out := make([]attrJSON, 0, len(attrs))
	for _, attr := range attrs {
		value := attr.Value.Resolve()
		var kind string
		var item any
		switch value.Kind() {
		case slog.KindString:
			kind, item = "string", value.String()
		case slog.KindInt64:
			kind, item = "int64", value.Int64()
		case slog.KindUint64:
			kind, item = "uint64", value.Uint64()
		case slog.KindFloat64:
			if v := value.Float64(); !math.IsNaN(v) && !math.IsInf(v, 0) {
				kind, item = "float64", v
			} else {
				kind, item = "string", value.String()
			}
		case slog.KindBool:
			kind, item = "bool", value.Bool()
		case slog.KindDuration:
			kind, item = "duration", int64(value.Duration())
		case slog.KindTime:
			kind, item = "time", value.Time()
		case slog.KindGroup:
			kind, item = "group", marshalAttrs(value.Group())
		default:
			kind, item = "string", value.String()
		}
		encoded, _ := json.Marshal(item)
		out = append(out, attrJSON{Key: attr.Key, Kind: kind, Value: encoded})
	}
	return out
}

func unmarshalAttrs(attrs []attrJSON) ([]slog.Attr, error) {
	out := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		var value slog.Value
		switch attr.Kind {
		case "string":
			var v string
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.StringValue(v)
		case "int64":
			var v int64
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.Int64Value(v)
		case "uint64":
			var v uint64
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.Uint64Value(v)
		case "float64":
			var v float64
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.Float64Value(v)
		case "bool":
			var v bool
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.BoolValue(v)
		case "duration":
			var v int64
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.DurationValue(time.Duration(v))
		case "time":
			var v time.Time
			if err := json.Unmarshal(attr.Value, &v); err != nil {
				return nil, err
			}
			value = slog.TimeValue(v)
		case "group":
			var items []attrJSON
			if err := json.Unmarshal(attr.Value, &items); err != nil {
				return nil, err
			}
			group, err := unmarshalAttrs(items)
			if err != nil {
				return nil, err
			}
			value = slog.GroupValue(group...)
		default:
			return nil, fmt.Errorf("unsupported trace attribute kind %q", attr.Kind)
		}
		out = append(out, slog.Attr{Key: attr.Key, Value: value})
	}
	return out, nil
}
