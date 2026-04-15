package phos

import (
	"log/slog"
	"sync"
	"testing"
)

type captureExporter struct {
	mu    sync.Mutex
	spans []Snapshot
}

func (e *captureExporter) Export(data Snapshot) {
	data.Attrs = cloneAttrs(data.Attrs)
	data.Events = cloneEvents(data.Events)
	data.Errors = cloneErrorData(data.Errors)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, data)
}

func (e *captureExporter) snapshot() []Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]Snapshot, len(e.spans))
	for i, data := range e.spans {
		data.Attrs = cloneAttrs(data.Attrs)
		data.Events = cloneEvents(data.Events)
		data.Errors = cloneErrorData(data.Errors)
		out[i] = data
	}
	return out
}

func withExporter(t *testing.T, exp Exporter) func() []Snapshot {
	t.Helper()

	restore := SetExporter(exp)
	t.Cleanup(restore)

	switch typed := exp.(type) {
	case *captureExporter:
		return typed.snapshot
	case *InMemExportImporter:
		return func() []Snapshot {
			spans := typed.Snapshot()
			out := make([]Snapshot, 0, len(spans))
			for _, data := range spans {
				out = append(out, data)
			}
			return out
		}
	default:
		return func() []Snapshot { return nil }
	}
}

func findSpanDataByName(t *testing.T, spans []Snapshot, name string) Snapshot {
	t.Helper()

	for _, span := range spans {
		if span.Name == name {
			return span
		}
	}
	t.Fatalf("span named %q not found", name)
	return Snapshot{}
}

func findEventDataByName(t *testing.T, events []SnapshotEvent, name string) SnapshotEvent {
	t.Helper()

	for _, event := range events {
		if event.Name == name {
			return event
		}
	}
	t.Fatalf("event named %q not found", name)
	return SnapshotEvent{}
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
