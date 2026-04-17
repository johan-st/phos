package phos

import (
	"context"
	"log/slog"
	"strings"
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
	signals := captureRejectedSignals(t)

	rootCtx, root := NewSpan(context.Background(), "root")

	DrainAndClose(context.Background())

	_, blocked := NewSpan(context.Background(), "blocked-root")
	if blocked == nil {
		t.Fatal("blocked root span = nil, want rejected span")
	}

	childCtx, child := NewSpan(rootCtx, "child")
	Event(childCtx, "allowed")
	child.End()
	root.End()
	WaitForClosed()

	spans := getSpans()
	if len(spans) != 2 {
		t.Fatalf("len(spans) = %d, want 2", len(spans))
	}
	if strings.Contains(RenderTraces(spans), "blocked-root") {
		t.Fatal("blocked root span should not be exported")
	}

	output := signals.String()
	if !strings.Contains(output, "blocked-root") || !strings.Contains(output, "after drain") {
		t.Fatalf("signals = %q, want blocked root rejection", output)
	}
}

func TestRejectedSpanSignalsOnAllOperations(t *testing.T) {
	exp := &captureExporter{}
	withExporter(t, exp)
	signals := captureRejectedSignals(t)

	_, root := NewSpan(context.Background(), "root")
	DrainAndClose(context.Background())

	blockedCtx, blocked := NewSpan(context.Background(), "blocked")
	Attrs(blockedCtx, slog.String("ignored", "value"))
	Event(blockedCtx, "ignored")
	Error(blockedCtx, errInvalidTraceID)
	Fail(blockedCtx, errInvalidTraceID)
	blocked.End()

	cancelled, cancel := context.WithCancel(context.Background())
	DrainAndClose(cancelled)
	cancel()

	root.End()
	WaitForClosed()

	_, closedBlocked := NewSpan(context.Background(), "closed-blocked")
	closedBlocked.End()

	output := signals.String()
	for _, want := range []string{
		`NewSpan on rejected span "blocked" (after drain)`,
		`Attrs on rejected span "blocked" (after drain)`,
		`Event on rejected span "blocked" (after drain)`,
		`Error on rejected span "blocked" (after drain)`,
		`Fail on rejected span "blocked" (after drain)`,
		`End on rejected span "blocked" (after drain)`,
		`NewSpan on rejected span "closed-blocked" (after close)`,
		`End on rejected span "closed-blocked" (after close)`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("signals = %q, want substring %q", output, want)
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
	case <-time.After(20 * time.Millisecond):
	}

	root.End()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForClosed did not return after root ended")
	}
}
