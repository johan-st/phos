package phos

import (
	"log/slog"
	"sync"
)

type InMemExportImporter struct {
	mu    sync.Mutex
	spans map[string]Snapshot
}

func NewInMemExportImporter() *InMemExportImporter {
	return &InMemExportImporter{spans: make(map[string]Snapshot)}
}

func (e *InMemExportImporter) Export(data Snapshot) {
	data.Attrs = cloneAttrs(data.Attrs)
	data.Links = cloneSnapshotLinks(data.Links)
	data.Events = cloneEvents(data.Events)
	data.Errors = cloneErrorData(data.Errors)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans[data.ID] = data
}

// Spans returns a deep copy of all exported spans keyed by span ID.
func (e *InMemExportImporter) Spans() map[string]Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make(map[string]Snapshot, len(e.spans))
	for id, data := range e.spans {
		data.Attrs = cloneAttrs(data.Attrs)
		data.Links = cloneSnapshotLinks(data.Links)
		data.Events = cloneEvents(data.Events)
		data.Errors = cloneErrorData(data.Errors)
		out[id] = data
	}
	return out
}

func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	cloned := make([]slog.Attr, len(attrs))
	copy(cloned, attrs)
	return cloned
}

func cloneEvents(events []SnapshotEvent) []SnapshotEvent {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]SnapshotEvent, len(events))
	copy(cloned, events)
	for i := range cloned {
		cloned[i].Attrs = cloneAttrs(cloned[i].Attrs)
	}
	return cloned
}

func cloneErrorData(errs []SnapshotError) []SnapshotError {
	if len(errs) == 0 {
		return nil
	}
	cloned := make([]SnapshotError, len(errs))
	copy(cloned, errs)
	for i := range cloned {
		cloned[i].Attrs = cloneAttrs(cloned[i].Attrs)
	}
	return cloned
}
