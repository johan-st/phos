package phos

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

func TestEndedSpanIsAbsentFromContext(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	ctx, span := NewSpan(context.Background(), "ended")
	span.End()

	if got := SpanFromContext(ctx); got != nil {
		t.Fatalf("SpanFromContext(ctx) = %#v, want nil after end", got)
	}

	Attrs(ctx, slog.String("ignored", "value"))
	Event(ctx, "ignored")
	Error(ctx, errInvalidTraceID)
	Fail(ctx, errInvalidTraceID)

	data := findSpanDataByName(t, getSpans(), "ended")
	if len(data.Attrs) != 0 {
		t.Fatalf("len(Attrs) = %d, want 0", len(data.Attrs))
	}
	if len(data.Events) != 0 {
		t.Fatalf("len(Events) = %d, want 0", len(data.Events))
	}
	if len(data.Errors) != 0 {
		t.Fatalf("len(Errors) = %d, want 0", len(data.Errors))
	}
}

func TestDrainAndCloseBlocksNewRootsButAllowsChildren(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, root := NewSpan(context.Background(), "root")

	DrainAndClose(context.Background())

	blockedCtx, blocked := NewSpan(context.Background(), "blocked-root")
	if blocked == nil {
		t.Fatal("blocked root span = nil, want noop span")
	}
	if got := SpanFromContext(blockedCtx); got != nil {
		t.Fatalf("SpanFromContext(blockedCtx) = %#v, want nil for noop span", got)
	}

	childCtx, child := NewSpan(rootCtx, "child")
	Event(childCtx, "allowed")
	child.End()

	Attrs(blockedCtx, slog.String("ignored", "value"))
	Event(blockedCtx, "ignored")
	Error(blockedCtx, errInvalidTraceID)
	Fail(blockedCtx, errInvalidTraceID)
	blocked.End()
	root.End()
	WaitForClosed()

	spans := getSpans()
	if len(spans) != 2 {
		t.Fatalf("len(spans) = %d, want 2", len(spans))
	}
	for _, span := range spans {
		if span.Name == "blocked-root" {
			t.Fatal("blocked root span should not be exported")
		}
	}
}

func TestBlockedNewSpanIsNoopAcrossOperations(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	_, root := NewSpan(context.Background(), "root")
	DrainAndClose(context.Background())

	blockedCtx, blocked := NewSpan(context.Background(), "blocked")
	Attrs(blockedCtx, slog.String("ignored", "value"))
	Event(blockedCtx, "ignored")
	Error(blockedCtx, errInvalidTraceID)
	Fail(blockedCtx, errInvalidTraceID)
	blocked.End()
	if got := blocked.Snapshot(); got.Name != "blocked" {
		t.Fatalf("Snapshot().Name = %q, want %q", got.Name, "blocked")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	DrainAndClose(cancelled)
	cancel()

	root.End()
	WaitForClosed()

	_, closedBlocked := NewSpan(context.Background(), "closed-blocked")
	closedBlocked.End()
	if got := getSpans(); len(got) != 1 || got[0].Name != "root" {
		t.Fatalf("exported spans = %#v, want only root", got)
	}
	for _, name := range []string{"blocked", "closed-blocked"} {
		for _, span := range getSpans() {
			if span.Name == name {
				t.Fatalf("%s should not be exported", name)
			}
		}
	}
}

func TestDrainAndCloseClosesOpenRootsOnContextCancel(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, _ := NewSpan(context.Background(), "root")
	childCtx, _ := NewSpan(rootCtx, "child")
	_, _ = NewSpan(childCtx, "grandchild")

	ctx, cancel := context.WithCancel(context.Background())
	DrainAndClose(ctx)
	cancel()
	WaitForClosed()

	spans := getSpans()
	if len(spans) != 3 {
		t.Fatalf("len(spans) = %d, want 3", len(spans))
	}
	for _, name := range []string{"root", "child", "grandchild"} {
		data := findSpanDataByName(t, spans, name)
		findEventDataByName(t, data.Events, shutdownTimeoutEvent)
		if data.TimeEnd.IsZero() {
			t.Fatalf("%s TimeEnd is zero, want ended span", name)
		}
	}

	rootData := findSpanDataByName(t, spans, "root")
	childData := findSpanDataByName(t, spans, "child")
	grandchildData := findSpanDataByName(t, spans, "grandchild")
	if childData.ParentID != rootData.ID {
		t.Fatalf("child ParentID = %q, want %q", childData.ParentID, rootData.ID)
	}
	if grandchildData.ParentID != childData.ID {
		t.Fatalf("grandchild ParentID = %q, want %q", grandchildData.ParentID, childData.ID)
	}
}

func TestEndClosesChildrenBottomUp(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, root := NewSpan(context.Background(), "root")
	childCtx, _ := NewSpan(rootCtx, "child")
	_, _ = NewSpan(childCtx, "grandchild")

	root.End()

	spans := getSpans()
	if len(spans) != 3 {
		t.Fatalf("len(spans) = %d, want 3", len(spans))
	}
	if spans[0].Name != "grandchild" || spans[1].Name != "child" || spans[2].Name != "root" {
		t.Fatalf("export order = [%s %s %s], want [grandchild child root]", spans[0].Name, spans[1].Name, spans[2].Name)
	}
}

func TestWaitForClosedBlocksUntilDrainCompletes(t *testing.T) {
	exp := &captureExporter{}
	withExporter(t, exp)

	_, root := NewSpan(context.Background(), "root")
	DrainAndClose(context.Background())

	done := make(chan struct{})
	go func() {
		WaitForClosed()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("WaitForClosed returned before root ended")
	case <-time.After(100 * time.Millisecond):
	}

	root.End()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForClosed did not return after root ended")
	}
}

type shutdownExporter func(Snapshot)

func (export shutdownExporter) Export(snapshot Snapshot) {
	export(snapshot)
}

func TestShutdownWaitsForSubtreeExports(t *testing.T) {
	for _, name := range []string{"root", "child", "grandchild"} {
		for _, cancelDrain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel=%t", name, cancelDrain), func(t *testing.T) {
				entered := make(chan struct{})
				release := make(chan struct{})
				finished := make(chan struct{})
				withExporter(t, shutdownExporter(func(snapshot Snapshot) {
					if snapshot.Name == name {
						close(entered)
						<-release
					}
				}))
				rootCtx, root := NewSpan(context.Background(), "root")
				childCtx, child := NewSpan(rootCtx, "child")
				_, grandchild := NewSpan(childCtx, "grandchild")
				blocked := map[string]*Span{"root": root, "child": child, "grandchild": grandchild}[name]
				go func() {
					blocked.End()
					close(finished)
				}()
				<-entered
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				DrainAndClose(ctx)
				if cancelDrain {
					cancel()
				} else {
					root.End()
				}
				closed := make(chan struct{})
				go func() {
					WaitForClosed()
					close(closed)
				}()
				select {
				case <-closed:
					t.Error("shutdown completed while an export was blocked")
				case <-time.After(20 * time.Millisecond):
				}
				close(release)
				<-finished
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("shutdown did not complete after all exports returned")
				}
			})
		}
	}
}

func TestExporterCanEndParent(t *testing.T) {
	var root *Span
	var exports []string
	withExporter(t, shutdownExporter(func(snapshot Snapshot) {
		exports = append(exports, snapshot.Name)
		if snapshot.Name == "child" {
			root.End()
		}
	}))
	ctx, started := NewSpan(context.Background(), "root")
	root = started
	_, child := NewSpan(ctx, "child")
	DrainAndClose(context.Background())
	done := make(chan struct{})
	go func() {
		child.End()
		WaitForClosed()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("exporter reentry deadlocked")
	}
	if len(exports) != 2 || exports[0] != "child" || exports[1] != "root" {
		t.Fatalf("exports = %v, want [child root]", exports)
	}
}
