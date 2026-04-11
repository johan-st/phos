package phos

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type spanKeyType struct{}
type traceContextKeyType struct{}
type exporterContextKeyType struct{}

var spanKey = spanKeyType{}
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

type Exporter interface {
	Export(distiller DataViewer)
}

type NoopExporter struct{}

func (e *NoopExporter) Export(distiller DataViewer) {
}

// SetExporter replaces the package-level exporter and returns a restore
// function for callers that need to put the previous exporter back.
// It is safe for concurrent use with Start and span completion.
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

type Span interface {
	End()                                  // Ends the span and logs it
	Attrs(attrs ...slog.Attr)              // Adds attributes to the span
	Event(name string, attrs ...slog.Attr) // Adds an event to the span
	Error(err error, attrs ...slog.Attr)   // Adds an error to the span
	Fail()                                 // Sets SpanData.Failed for exporters; phos does not implement sampling.
}

type DataViewer interface {
	View() SpanData
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

func Start(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, Span) {
	ctx = normalizeContext(ctx)

	var (
		spanID   string
		parentID string
		traceID  string
		traceCtx traceContext
		exp      Exporter
	)

	if parent, ok := ctx.Value(spanKey).(*span); ok {
		parentID, traceID, traceCtx = parent.parentTraceData()
		exp = parent.exporter
	} else {
		traceCtx = traceContextFromContext(ctx)
		parentID = traceCtx.parentID
		traceID = traceCtx.traceID
		if traceID == "" {
			traceID = generateTraceID()
			traceCtx.traceID = traceID
			traceCtx.traceFlags = "00"
		}
		exp = exporterFromContext(ctx)
		if exp == nil {
			exp = loadGlobalExporter()
		}
	}

	spanID = generateSpanID()
	span := &span{
		startTime:  time.Now(),
		id:         spanID,
		parentID:   parentID,
		traceID:    traceID,
		traceFlags: traceCtx.traceFlags,
		traceState: traceCtx.traceState,
		name:       name,
		attrs:      cloneAttrs(attrs),
		exporter:   exp,
	}
	applyTraceContextDiagnostics(span, traceCtx.diagnostics)
	return context.WithValue(ctx, spanKey, span), span
}

func applyTraceContextDiagnostics(s *span, diagnostics []traceContextDiagnostic) {
	for _, diagnostic := range diagnostics {
		s.Event(diagnostic.event,
			slog.String("header", diagnostic.header),
			slog.String("value", diagnostic.value),
			slog.String("reason", diagnostic.reason),
		)
	}
}

func InSpan(ctx context.Context, name string, fn func(context.Context), attrs ...slog.Attr) {
	ctx, span := Start(ctx, name, attrs...)
	defer span.End()
	fn(ctx)
}
func InSpanE(ctx context.Context, name string, fn func(context.Context) error, attrs ...slog.Attr) error {
	ctx, span := Start(ctx, name, attrs...)
	defer span.End()
	return fn(ctx)
}

func Attrs(ctx context.Context, attrs ...slog.Attr) {
	span := spanFromContext(ctx)
	if span == nil {
		return
	}

	span.Attrs(attrs...)
}

func Event(ctx context.Context, name string, attrs ...slog.Attr) {
	span := spanFromContext(ctx)
	if span == nil {
		return
	}

	span.Event(name, attrs...)
}

func Error(ctx context.Context, err error, attrs ...slog.Attr) {
	span := spanFromContext(ctx)
	if span == nil {
		return
	}

	span.Error(err, attrs...)
}

func Fail(ctx context.Context) {
	span := spanFromContext(ctx)
	if span == nil {
		return
	}

	span.Fail()
}

// -- Internal --
// -- span --

type span struct {
	mu         sync.Mutex
	startTime  time.Time
	id         string
	parentID   string
	traceID    string
	traceFlags string
	traceState string
	endTime    time.Time
	ended      bool
	failed     bool
	name       string
	attrs      []slog.Attr
	events     []EventData
	errors     []ErrorData
	exporter   Exporter
}

func spanFromContext(ctx context.Context) *span {
	if ctx == nil {
		return nil
	}
	if span, ok := ctx.Value(spanKey).(*span); ok {
		return span
	}
	return nil
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

func (s *span) Attrs(attrs ...slog.Attr) {
	if len(attrs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.attrs = append(s.attrs, attrs...)
}

func (s *span) Event(name string, attrs ...slog.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.events = append(s.events, EventData{
		Time:  time.Now(),
		Name:  name,
		Attrs: cloneAttrs(attrs),
	})
}

func (s *span) Error(err error, attrs ...slog.Attr) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.errors = append(s.errors, ErrorData{
		Err:   err,
		Attrs: cloneAttrs(attrs),
	})
}

func (s *span) Fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.failed = true
}

func (s *span) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.endTime = time.Now()
	exp := s.exporter
	s.mu.Unlock()
	if exp == nil {
		exp = loadGlobalExporter()
	}
	if exp != nil {
		exp.Export(s)
	}
}

func (s *span) View() SpanData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SpanData{
		ID:        s.id,
		Name:      s.name,
		ParentID:  s.parentID,
		TraceID:   s.traceID,
		StartTime: s.startTime,
		EndTime:   s.endTime,
		Failed:    s.failed,
		Attrs:     cloneAttrs(s.attrs),
		Events:    cloneEvents(s.events),
		Errors:    cloneErrorData(s.errors),
		Root:      s.parentID == "",
	}
}

func (s *span) parentTraceData() (string, string, traceContext) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.id, s.traceID, traceContext{
		traceID:    s.traceID,
		traceFlags: s.traceFlags,
		traceState: s.traceState,
	}
}

func (s *span) traceIDValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceID
}

func (s *span) traceStateValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceState
}

func (s *span) traceFlagsValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceFlags
}

func (s *span) spanIDValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
