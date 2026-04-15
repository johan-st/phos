package phos

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Span struct {
	mu         sync.Mutex
	id         string
	parentID   string
	traceID    string
	traceFlags string
	traceState string
	timeStart  time.Time
	timeEnd    time.Time
	failed     bool
	name       string
	events     []SnapshotEvent
	errors     []SnapshotError
	attrs      []slog.Attr
	exporter   Exporter
}

func NewSpan(ctx context.Context, name string, attrs ...slog.Attr) (context.Context, *Span) {
	ctx = normalizeContext(ctx)

	var (
		spanID   string
		parentID string
		traceID  string
		traceCtx traceContext
		exp      Exporter
	)

	if parent, ok := ctx.Value(spanKey).(*Span); ok {
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
	span := &Span{
		timeStart:  time.Now(),
		id:         spanID,
		parentID:   parentID,
		traceID:    traceID,
		traceFlags: traceCtx.traceFlags,
		traceState: traceCtx.traceState,
		name:       name,
		attrs:      cloneAttrs(attrs),
		exporter:   exp,
	}
	span.applyTraceContextDiagnostics(traceCtx.diagnostics)
	return context.WithValue(ctx, spanKey, span), span
}

func (s *Span) applyTraceContextDiagnostics(diagnostics []traceContextDiagnostic) {
	for _, diagnostic := range diagnostics {
		s.Event(diagnostic.event,
			slog.String("header", diagnostic.header),
			slog.String("value", diagnostic.value),
			slog.String("reason", diagnostic.reason),
		)
	}
}

func (s *Span) Attrs(attrs ...slog.Attr) {
	if len(attrs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.timeEnd.IsZero() {
		return
	}
	s.attrs = append(s.attrs, attrs...)
}

func (s *Span) Event(name string, attrs ...slog.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.timeEnd.IsZero() {
		return
	}
	s.events = append(s.events, SnapshotEvent{
		Time:  time.Now(),
		Name:  name,
		Attrs: cloneAttrs(attrs),
	})
}

func (s *Span) Error(err error, attrs ...slog.Attr) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.timeEnd.IsZero() {
		return
	}
	s.errors = append(s.errors, SnapshotError{
		Err:   err,
		Attrs: cloneAttrs(attrs),
	})
}

func (s *Span) Fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.timeEnd.IsZero() {
		return
	}
	s.failed = true
}

func (s *Span) End() {
	s.mu.Lock()
	if !s.timeEnd.IsZero() {
		s.mu.Unlock()
		return
	}
	s.timeEnd = time.Now()
	exp := s.exporter
	s.mu.Unlock()
	if exp == nil {
		exp = loadGlobalExporter()
	}
	if exp != nil {
		exp.Export(s.View())
	}
}

func (s *Span) View() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		ID:        s.id,
		Name:      s.name,
		ParentID:  s.parentID,
		TraceID:   s.traceID,
		TimeStart: s.timeStart,
		TimeEnd:   s.timeEnd,
		Failed:    s.failed,
		Attrs:     cloneAttrs(s.attrs),
		Events:    cloneEvents(s.events),
		Errors:    cloneErrorData(s.errors),
		Root:      s.parentID == "",
	}
}

func (s *Span) parentTraceData() (string, string, traceContext) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.id, s.traceID, traceContext{
		traceID:    s.traceID,
		traceFlags: s.traceFlags,
		traceState: s.traceState,
	}
}

func (s *Span) traceIDValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceID
}

func (s *Span) traceStateValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceState
}

func (s *Span) traceFlagsValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.traceFlags
}

func (s *Span) spanIDValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
