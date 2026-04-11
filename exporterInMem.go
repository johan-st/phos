package phos

import (
	"log/slog"
	"sync"
)

type InMemExportImporter struct {
	mu    sync.Mutex
	spans map[string]SpanData
}

func NewInMemExportImporter() *InMemExportImporter {
	return &InMemExportImporter{spans: make(map[string]SpanData)}
}

func (e *InMemExportImporter) Export(span DataViewer) {
	data := span.View()
	data.Attrs = cloneAttrs(data.Attrs)
	data.Events = cloneEvents(data.Events)
	data.Errors = cloneErrorData(data.Errors)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans[data.ID] = data
}

// Snapshot returns a deep copy of all exported spans keyed by span ID.
func (e *InMemExportImporter) Snapshot() map[string]SpanData {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make(map[string]SpanData, len(e.spans))
	for id, data := range e.spans {
		data.Attrs = cloneAttrs(data.Attrs)
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

func cloneEvents(events []EventData) []EventData {
	if len(events) == 0 {
		return nil
	}
	cloned := make([]EventData, len(events))
	copy(cloned, events)
	for i := range cloned {
		cloned[i].Attrs = cloneAttrs(cloned[i].Attrs)
	}
	return cloned
}

func cloneErrorData(errs []ErrorData) []ErrorData {
	if len(errs) == 0 {
		return nil
	}
	cloned := make([]ErrorData, len(errs))
	copy(cloned, errs)
	for i := range cloned {
		cloned[i].Attrs = cloneAttrs(cloned[i].Attrs)
	}
	return cloned
}
