package phos

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Span struct {
	mu             sync.Mutex
	id             string
	parentID       string
	traceID        string
	traceFlags     string
	traceState     string
	timeStart      time.Time
	timeEnd        time.Time
	kind           SpanKind
	failed         bool
	name           string
	links          []link
	events         []SnapshotEvent
	errors         []SnapshotError
	attrs          []slog.Attr
	exporter       Exporter
	parent         *Span
	children       map[*Span]struct{}
	closing        bool
	rejectedReason string
}

func NewSpan(ctx context.Context, name string, opts ...SpanOption) (context.Context, *Span) {
	ctx = normalizeContext(ctx)
	cfg := newSpanConfig(opts)

	if closedState.Load() {
		return newRejectedSpan(ctx, name, cfg, "after close")
	}

	if parent := activeSpanFromContext(ctx); parent != nil {
		span := parent.newChildSpan(name, cfg)
		if span != nil {
			return context.WithValue(ctx, activeSpanKey, span), span
		}
		return newRejectedSpan(ctx, name, cfg, rejectedCreationReason())
	}

	traceCtx := traceContextFromContext(ctx)
	if drainingState.Load() {
		return newRejectedSpan(ctx, name, cfg, "after drain")
	}

	traceID := traceCtx.traceID
	if traceID == "" {
		traceID = generateTraceID()
		traceCtx.traceID = traceID
		traceCtx.traceFlags = "00"
	}

	exp := exporterFromContext(ctx)
	if exp == nil {
		exp = loadGlobalExporter()
	}

	span := &Span{
		timeStart:  time.Now(),
		id:         generateSpanID(),
		parentID:   traceCtx.parentID,
		traceID:    traceID,
		traceFlags: traceCtx.traceFlags,
		traceState: traceCtx.traceState,
		kind:       cfg.kind,
		name:       name,
		links:      cloneLinks(cfg.links),
		attrs:      cloneAttrs(cfg.attrs),
		exporter:   exp,
		children:   map[*Span]struct{}{},
	}
	if !registerRootSpan(span) {
		return newRejectedSpan(ctx, name, cfg, rejectedCreationReason())
	}
	span.applyTraceContextDiagnostics(traceCtx.diagnostics)
	return context.WithValue(ctx, activeSpanKey, span), span
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
	if s.isRejected() {
		s.signalRejected("Attrs")
		return
	}
	if len(attrs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || !s.timeEnd.IsZero() {
		return
	}
	s.attrs = append(s.attrs, attrs...)
}

func (s *Span) Event(name string, attrs ...slog.Attr) {
	if s.isRejected() {
		s.signalRejected("Event")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || !s.timeEnd.IsZero() {
		return
	}
	s.events = append(s.events, SnapshotEvent{
		Time:  time.Now(),
		Name:  name,
		Attrs: cloneAttrs(attrs),
	})
}

func (s *Span) Error(err error, attrs ...slog.Attr) {
	if s.isRejected() {
		s.signalRejected("Error")
		return
	}
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || !s.timeEnd.IsZero() {
		return
	}
	s.errors = append(s.errors, SnapshotError{
		Err:   err,
		Attrs: cloneAttrs(attrs),
	})
}

func (s *Span) Fail(err error, attrs ...slog.Attr) {
	if s.isRejected() {
		s.signalRejected("Fail")
		return
	}
	if err == nil {
		return
	}
	s.endTreeWith(endTreeOptions{
		Err:   err,
		Attrs: cloneAttrs(attrs),
	}, true, "")
}

func (s *Span) End() {
	if s.isRejected() {
		s.signalRejected("End")
		return
	}
	s.endTreeWith(endTreeOptions{}, false, "")
}

func (s *Span) closeTree(eventName string) {
	if s == nil {
		return
	}
	s.endTreeWith(endTreeOptions{}, false, eventName)
}

type endTreeOptions struct {
	Err   error
	Attrs []slog.Attr
}

func (s *Span) endTreeWith(opts endTreeOptions, failed bool, eventName string) {
	children, ok := s.beginEnd()
	if !ok {
		return
	}
	for _, child := range children {
		child.closeTree(eventName)
	}
	s.finishSpan(opts, failed, eventName)
}

func (s *Span) beginEnd() ([]*Span, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.timeEnd.IsZero() {
		return nil, false
	}
	if s.closing {
		return nil, false
	}
	s.closing = true

	children := make([]*Span, 0, len(s.children))
	for child := range s.children {
		children = append(children, child)
	}
	return children, true
}

func (s *Span) finishSpan(opts endTreeOptions, failed bool, eventName string) {
	s.mu.Lock()
	if !s.timeEnd.IsZero() {
		s.mu.Unlock()
		return
	}
	if eventName != "" {
		s.events = append(s.events, SnapshotEvent{
			Time: time.Now(),
			Name: eventName,
		})
	}
	if opts.Err != nil {
		s.errors = append(s.errors, SnapshotError{
			Err:   opts.Err,
			Attrs: cloneAttrs(opts.Attrs),
		})
	}
	if failed {
		s.failed = true
	}
	s.timeEnd = time.Now()
	exp := s.exporter
	parent := s.parent
	s.mu.Unlock()

	if exp == nil {
		exp = loadGlobalExporter()
	}
	if exp != nil {
		exp.Export(s.Snapshot())
	}

	if parent != nil {
		parent.detachChild(s)
		return
	}
	unregisterRootSpan(s)
}

func (s *Span) Snapshot() Snapshot {
	if s.isRejected() {
		s.signalRejected("Snapshot")
		return Snapshot{
			Name:      s.name,
			TimeStart: s.timeStart,
			Kind:      s.kind,
			Attrs:     cloneAttrs(s.attrs),
			Links:     snapshotLinks(s.links),
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		ID:        s.id,
		Name:      s.name,
		ParentID:  s.parentID,
		TraceID:   s.traceID,
		TimeStart: s.timeStart,
		TimeEnd:   s.timeEnd,
		Kind:      s.kind,
		Failed:    s.failed,
		Attrs:     cloneAttrs(s.attrs),
		Links:     snapshotLinks(s.links),
		Events:    cloneEvents(s.events),
		Errors:    cloneErrorData(s.errors),
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

func newRejectedSpan(ctx context.Context, name string, cfg spanConfig, reason string) (context.Context, *Span) {
	span := &Span{
		timeStart:      time.Now(),
		kind:           cfg.kind,
		name:           name,
		links:          cloneLinks(cfg.links),
		attrs:          cloneAttrs(cfg.attrs),
		rejectedReason: reason,
	}
	span.signalRejected("NewSpan")
	return context.WithValue(ctx, activeSpanKey, span), span
}

func rejectedCreationReason() string {
	if closedState.Load() {
		return "after close"
	}
	if drainingState.Load() {
		return "after drain"
	}
	return "while parent is closing"
}

func (s *Span) newChildSpan(name string, cfg spanConfig) *Span {
	child := &Span{
		timeStart: time.Now(),
		id:        generateSpanID(),
		kind:      cfg.kind,
		name:      name,
		links:     cloneLinks(cfg.links),
		attrs:     cloneAttrs(cfg.attrs),
		children:  map[*Span]struct{}{},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejectedReason != "" || s.closing || !s.timeEnd.IsZero() || closedState.Load() {
		return nil
	}
	child.parent = s
	child.parentID = s.id
	child.traceID = s.traceID
	child.traceFlags = s.traceFlags
	child.traceState = s.traceState
	child.exporter = s.exporter
	if s.children == nil {
		s.children = map[*Span]struct{}{}
	}
	s.children[child] = struct{}{}
	return child
}

func (s *Span) detachChild(child *Span) {
	s.mu.Lock()
	delete(s.children, child)
	s.mu.Unlock()
}

func (s *Span) isActiveParent() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejectedReason == "" && !s.closing && s.timeEnd.IsZero()
}

func (s *Span) isEnded() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.timeEnd.IsZero()
}

func (s *Span) isRejected() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rejectedReason != ""
}

func (s *Span) signalRejected(action string) {
	s.mu.Lock()
	reason := s.rejectedReason
	name := s.name
	s.mu.Unlock()
	signalRejectedSpan(action, reason, name)
}
