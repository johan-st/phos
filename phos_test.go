package phos

import (
	"log/slog"
	"sync"
	"testing"
)

type captureExporter struct {
	mu    sync.Mutex
	spans []SpanData
}

func (e *captureExporter) Export(span DataViewer) {
	data := span.View()
	data.Attrs = cloneAttrs(data.Attrs)
	data.Events = cloneEvents(data.Events)
	data.Errors = cloneErrorData(data.Errors)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, data)
}

func (e *captureExporter) snapshot() []SpanData {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]SpanData, len(e.spans))
	for i, data := range e.spans {
		data.Attrs = cloneAttrs(data.Attrs)
		data.Events = cloneEvents(data.Events)
		data.Errors = cloneErrorData(data.Errors)
		out[i] = data
	}
	return out
}

func withExporter(t *testing.T, exp Exporter) func() []SpanData {
	t.Helper()

	restore := SetExporter(exp)
	t.Cleanup(restore)

	switch typed := exp.(type) {
	case *captureExporter:
		return typed.snapshot
	case *InMemExportImporter:
		return func() []SpanData {
			spans := typed.Snapshot()
			out := make([]SpanData, 0, len(spans))
			for _, data := range spans {
				out = append(out, data)
			}
			return out
		}
	default:
		return func() []SpanData { return nil }
	}
}

func findSpanDataByName(t *testing.T, spans []SpanData, name string) SpanData {
	t.Helper()

	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span named %q not found", name)
	return SpanData{}
}

func findEventDataByName(t *testing.T, events []EventData, name string) EventData {
	t.Helper()

	for _, event := range events {
		if event.Name == name {
			return event
		}
	}
	t.Fatalf("event named %q not found", name)
	return EventData{}
}

func requireAttrValue(t *testing.T, attrs []slog.Attr, key, want string) {
	t.Helper()

	for _, attr := range attrs {
		if attr.Key != key {
			continue
		}
		if got := attr.Value.String(); got != want {
			t.Fatalf("attr %q = %q, want %q", key, got, want)
		}
		return
	}
	t.Fatalf("attr %q not found", key)
}
