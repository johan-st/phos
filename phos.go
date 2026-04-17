package phos

import (
	"context"
	"log/slog"
	"sync"
)

type activeSpanKeyType struct{}
type traceContextKeyType struct{}
type exporterContextKeyType struct{}

var activeSpanKey = activeSpanKeyType{}
var traceContextKey = traceContextKeyType{}
var exporterContextKey = exporterContextKeyType{}

var (
	exporterMu sync.RWMutex
	exporter   Exporter = &NoopExporter{}
)

func loadGlobalExporter() Exporter {
	exporterMu.RLock()
	defer exporterMu.RUnlock()
	return exporter
}

// Exporter receives completed span snapshots.
//
// Export must be safe for concurrent use. Phos considers export complete when
// Export returns, so exporters should avoid blocking indefinitely. Any
// exporter-specific buffering or shutdown coordination is owned by the
// exporter implementation, not Phos.
type Exporter interface {
	Export(snapshot Snapshot)
}

type NoopExporter struct{}

func (e *NoopExporter) Export(_ Snapshot) {
}

// SetExporter replaces the package-level exporter and returns a restore
// function for callers that need to put the previous exporter back.
// It is safe for concurrent use with NewSpan and span completion.
func SetExporter(exp Exporter) func() {
	if exp == nil {
		exp = &NoopExporter{}
	}
	exporterMu.Lock()
	prev := exporter
	exporter = exp
	exporterMu.Unlock()
	return func() {
		exporterMu.Lock()
		exporter = prev
		exporterMu.Unlock()
	}
}

func WithExporter(ctx context.Context, exp Exporter) context.Context {
	ctx = normalizeContext(ctx)
	if exp == nil {
		exp = &NoopExporter{}
	}
	return context.WithValue(ctx, exporterContextKey, exp)
}

type traceContext struct {
	traceID     string
	parentID    string
	traceFlags  string
	traceState  string
	diagnostics []traceContextDiagnostic
}

type traceContextDiagnostic struct {
	event  string
	header string
	value  string
	reason string
}

func Attrs(ctx context.Context, attrs ...slog.Attr) {
	span := spanForMutationFromContext(ctx)
	if span == nil {
		return
	}

	span.Attrs(attrs...)
}

func Event(ctx context.Context, name string, attrs ...slog.Attr) {
	span := spanForMutationFromContext(ctx)
	if span == nil {
		return
	}

	span.Event(name, attrs...)
}

func Error(ctx context.Context, err error, attrs ...slog.Attr) {
	span := spanForMutationFromContext(ctx)
	if span == nil {
		return
	}

	span.Error(err, attrs...)
}

func Fail(ctx context.Context, err error, attrs ...slog.Attr) {
	span := spanForMutationFromContext(ctx)
	if span == nil {
		return
	}

	span.Fail(err, attrs...)
}

// -- Internal --
// -- span --

func SpanFromContext(ctx context.Context) *Span {
	return activeSpanFromContext(ctx)
}

func spanFromContextValue(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	if span, ok := ctx.Value(activeSpanKey).(*Span); ok {
		return span
	}
	return nil
}

func activeSpanFromContext(ctx context.Context) *Span {
	span := spanFromContextValue(ctx)
	if span == nil {
		return nil
	}
	if span.isActiveParent() {
		return span
	}
	return nil
}

func spanForMutationFromContext(ctx context.Context) *Span {
	span := spanFromContextValue(ctx)
	if span == nil {
		return nil
	}
	if span.isEnded() {
		return nil
	}
	return span
}

func traceContextFromContext(ctx context.Context) traceContext {
	if ctx == nil {
		return traceContext{}
	}
	if traceCtx, ok := ctx.Value(traceContextKey).(traceContext); ok {
		return traceCtx
	}
	return traceContext{}
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func exporterFromContext(ctx context.Context) Exporter {
	if ctx == nil {
		return nil
	}
	if exp, ok := ctx.Value(exporterContextKey).(Exporter); ok {
		return exp
	}
	return nil
}
