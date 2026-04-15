package phos

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

func TestSpanDataJSONShape(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, root := NewSpan(context.Background(), "root", slog.String("service", "api"))
	Event(rootCtx, "query", slog.String("table", "users"))
	Error(rootCtx, errors.New("boom"), slog.String("phase", "db"))
	childCtx, child := NewSpan(rootCtx, "child", slog.String("component", "repo"))
	Attrs(childCtx, slog.String("cache", "miss"))
	child.End()
	root.End()

	rootJSON := decodeJSONMap(t, mustJSON(t, findSpanDataByName(t, getSpans(), "root")))
	childJSON := decodeJSONMap(t, mustJSON(t, findSpanDataByName(t, getSpans(), "child")))

	if rootJSON["name"] != "root" {
		t.Fatalf("root name = %#v, want %q", rootJSON["name"], "root")
	}
	if rootJSON["root"] != true {
		t.Fatalf("root root = %#v, want true", rootJSON["root"])
	}
	if rootJSON["parent_id"] != "" {
		t.Fatalf("root parent_id = %#v, want empty", rootJSON["parent_id"])
	}
	events := rootJSON["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("len(root events) = %d, want 1", len(events))
	}
	if events[0].(map[string]any)["name"] != "query" {
		t.Fatalf("root event name = %#v, want %q", events[0].(map[string]any)["name"], "query")
	}
	if events[0].(map[string]any)["time"] == nil {
		t.Fatalf("root event time = %#v, want non-nil", events[0].(map[string]any)["time"])
	}
	errorsJSON := rootJSON["errors"].([]any)
	if len(errorsJSON) != 1 {
		t.Fatalf("len(root errors) = %d, want 1", len(errorsJSON))
	}
	if errorsJSON[0].(map[string]any)["message"] != "boom" {
		t.Fatalf("root error message = %#v, want %q", errorsJSON[0].(map[string]any)["message"], "boom")
	}

	if childJSON["name"] != "child" {
		t.Fatalf("child name = %#v, want %q", childJSON["name"], "child")
	}
	if childJSON["root"] != false {
		t.Fatalf("child root = %#v, want false", childJSON["root"])
	}
	if childJSON["parent_id"] == "" {
		t.Fatal("child parent_id is empty")
	}
}

func TestSpanDataAttrsJSONStability(t *testing.T) {
	data := Snapshot{
		Name: "root",
		Attrs: []slog.Attr{
			slog.String("service", "api"),
			slog.Int("count", 7),
			slog.Bool("cached", false),
		},
	}
	m := decodeJSONMap(t, mustJSON(t, data))
	rawAttrs, ok := m["attrs"].([]any)
	if !ok {
		t.Fatalf("attrs type = %T", m["attrs"])
	}
	if len(rawAttrs) != 3 {
		t.Fatalf("len(attrs) = %d, want 3", len(rawAttrs))
	}
	wantKeys := []string{"service", "count", "cached"}
	for i, wantKey := range wantKeys {
		obj, ok := rawAttrs[i].(map[string]any)
		if !ok {
			t.Fatalf("attrs[%d] type = %T", i, rawAttrs[i])
		}
		if got := obj["Key"]; got != wantKey {
			t.Fatalf("attrs[%d].Key = %v, want %q", i, got, wantKey)
		}
		if _, ok := obj["Value"]; !ok {
			t.Fatalf("attrs[%d] missing Value", i)
		}
	}
}

func TestErrorDataMarshalJSON(t *testing.T) {
	data := SnapshotError{
		Err:   errors.New("broken"),
		Attrs: []slog.Attr{slog.String("phase", "sync")},
	}

	payload := decodeJSONMap(t, mustJSON(t, data))
	if payload["message"] != "broken" {
		t.Fatalf("message = %#v, want %q", payload["message"], "broken")
	}
	attrs, ok := payload["attrs"].([]any)
	if !ok || len(attrs) != 1 {
		t.Fatalf("attrs = %#v, want one entry", payload["attrs"])
	}
}

func TestDiagnosticEventsMarshalInSpanJSON(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	ctx := ExtractTraceContext(context.Background(), MapCarrier{
		TraceParentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
	})
	_, sp := NewSpan(ctx, "root")
	sp.End()

	rootJSON := decodeJSONMap(t, mustJSON(t, findSpanDataByName(t, getSpans(), "root")))
	events := rootJSON["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}

	event := events[0].(map[string]any)
	if event["name"] != "tracecontext.invalid_traceparent" {
		t.Fatalf("event name = %#v, want %q", event["name"], "tracecontext.invalid_traceparent")
	}
	if event["time"] == nil {
		t.Fatalf("event time = %#v, want non-nil", event["time"])
	}

	attrs, ok := event["attrs"].([]any)
	if !ok || len(attrs) != 3 {
		t.Fatalf("attrs = %#v, want 3 entries", event["attrs"])
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()

	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return out
}

func decodeJSONMap(t *testing.T, data []byte) map[string]any {
	t.Helper()

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return out
}
