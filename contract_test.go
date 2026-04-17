package phos

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

func TestContractStartLifecycle(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, root := NewSpan(context.Background(), "root", WithAttrs(slog.String("service", "api")))
	if rootCtx == nil {
		t.Fatal("NewSpan(nil) returned nil context")
	}

	Attrs(rootCtx, slog.String("region", "eu-west-1"))
	Event(rootCtx, "db.query", slog.String("table", "users"))

	childCtx, child := NewSpan(rootCtx, "child", WithAttrs(slog.String("component", "repo")))
	Attrs(childCtx, slog.String("cache", "miss"))
	childErr := errors.New("child failed")
	Fail(childCtx, childErr, slog.String("attempt", "first"))

	rootErr := errors.New("root failed")
	Fail(rootCtx, rootErr, slog.String("phase", "handler"))

	child.End()
	root.End()

	spans := getSpans()
	if len(spans) != 2 {
		t.Fatalf("len(spans) = %d, want 2", len(spans))
	}

	rootData := findSpanDataByName(t, spans, "root")
	childData := findSpanDataByName(t, spans, "child")

	if rootData.ParentID != "" {
		t.Fatalf("root ParentID = %q, want empty", rootData.ParentID)
	}
	if rootData.ID == "" || rootData.TraceID == "" {
		t.Fatalf("root ids = (%q, %q), want non-empty", rootData.ID, rootData.TraceID)
	}
	if rootData.TimeEnd.Before(rootData.TimeStart) {
		t.Fatalf("root EndTime %v before StartTime %v", rootData.TimeEnd, rootData.TimeStart)
	}
	if !rootData.Failed {
		t.Fatal("root Failed = false, want true")
	}
	if len(rootData.Attrs) != 2 {
		t.Fatalf("len(root Attrs) = %d, want 2", len(rootData.Attrs))
	}
	requireAttrValue(t, rootData.Attrs, "service", "api")
	requireAttrValue(t, rootData.Attrs, "region", "eu-west-1")
	if len(rootData.Events) != 1 {
		t.Fatalf("len(root Events) = %d, want 1", len(rootData.Events))
	}
	if rootData.Events[0].Name != "db.query" {
		t.Fatalf("event name = %q, want %q", rootData.Events[0].Name, "db.query")
	}
	if rootData.Events[0].Time.IsZero() {
		t.Fatal("event Time is zero, want recorded timestamp")
	}
	if rootData.Events[0].Time.Before(rootData.TimeStart) || rootData.Events[0].Time.After(rootData.TimeEnd) {
		t.Fatalf("event Time %v outside span bounds [%v, %v]", rootData.Events[0].Time, rootData.TimeStart, rootData.TimeEnd)
	}
	requireAttrValue(t, rootData.Events[0].Attrs, "table", "users")
	if len(rootData.Errors) != 1 {
		t.Fatalf("len(root Errors) = %d, want 1", len(rootData.Errors))
	}
	if rootData.Errors[0].Err != rootErr {
		t.Fatalf("root error = %v, want %v", rootData.Errors[0].Err, rootErr)
	}
	requireAttrValue(t, rootData.Errors[0].Attrs, "phase", "handler")

	if childData.ParentID != rootData.ID {
		t.Fatalf("child ParentID = %q, want %q", childData.ParentID, rootData.ID)
	}
	if childData.TraceID != rootData.TraceID {
		t.Fatalf("child TraceID = %q, want %q", childData.TraceID, rootData.TraceID)
	}
	if !childData.Failed {
		t.Fatal("child Failed = false, want true")
	}
	if len(childData.Attrs) != 2 {
		t.Fatalf("len(child Attrs) = %d, want 2", len(childData.Attrs))
	}
	requireAttrValue(t, childData.Attrs, "component", "repo")
	requireAttrValue(t, childData.Attrs, "cache", "miss")
	if len(childData.Errors) != 1 {
		t.Fatalf("len(child Errors) = %d, want 1", len(childData.Errors))
	}
	if childData.Errors[0].Err != childErr {
		t.Fatalf("child error = %v, want %v", childData.Errors[0].Err, childErr)
	}
	requireAttrValue(t, childData.Errors[0].Attrs, "attempt", "first")
}

func TestContractHierarchy(t *testing.T) {
	rootCtx, root := NewSpan(context.Background(), "root")
	childCtx, child := NewSpan(rootCtx, "child")
	_, sibling := NewSpan(rootCtx, "sibling")
	_, grandchild := NewSpan(childCtx, "grandchild")

	rootSpan := root
	childSpan := child
	siblingSpan := sibling
	grandchildSpan := grandchild

	if childSpan.parentID != rootSpan.id {
		t.Fatalf("child ParentID = %q, want %q", childSpan.parentID, rootSpan.id)
	}
	if siblingSpan.parentID != rootSpan.id {
		t.Fatalf("sibling ParentID = %q, want %q", siblingSpan.parentID, rootSpan.id)
	}
	if siblingSpan.parentID == childSpan.id {
		t.Fatal("sibling incorrectly parented to child")
	}
	if grandchildSpan.parentID != childSpan.id {
		t.Fatalf("grandchild ParentID = %q, want %q", grandchildSpan.parentID, childSpan.id)
	}
	if rootSpan.traceID != childSpan.traceID || childSpan.traceID != siblingSpan.traceID || siblingSpan.traceID != grandchildSpan.traceID {
		t.Fatal("all related spans should share one trace id")
	}
}

func TestContractHelpersWithoutSpanAreNoOps(t *testing.T) {
	Attrs(context.Background(), slog.String("unused", "value"))
	Event(context.Background(), "event", slog.String("unused", "value"))
	Error(context.Background(), errors.New("ignored"), slog.String("unused", "value"))
	Error(context.Background(), nil)
	Fail(context.Background(), errors.New("ignored"), slog.String("unused", "value"))
	Fail(context.Background(), nil)
}

func TestContractPostEndMutationsIgnored(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	ctx, started := NewSpan(context.Background(), "ended", WithAttrs(slog.String("state", "before")))
	sp := started
	Event(ctx, "before", slog.String("order", "1"))
	started.End()

	Attrs(ctx, slog.String("state", "after"))
	Event(ctx, "after", slog.String("order", "2"))
	Error(ctx, errors.New("after"), slog.String("phase", "after"))
	Fail(ctx, errors.New("after"), slog.String("phase", "after"))
	sp.End()

	data := findSpanDataByName(t, getSpans(), "ended")
	if len(data.Attrs) != 1 {
		t.Fatalf("len(Attrs) = %d, want 1", len(data.Attrs))
	}
	if len(data.Events) != 1 {
		t.Fatalf("len(Events) = %d, want 1", len(data.Events))
	}
	if len(data.Errors) != 0 {
		t.Fatalf("len(Errors) = %d, want 0", len(data.Errors))
	}
	if data.Failed {
		t.Fatal("Fail after End should be ignored")
	}
	if sp.Snapshot().TimeEnd.IsZero() {
		t.Fatal("End should still set EndTime")
	}
}

func TestContractNilErrorIgnored(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	ctx, sp := NewSpan(context.Background(), "nil-error")
	Error(ctx, nil, slog.String("phase", "ignored"))
	Fail(ctx, nil, slog.String("phase", "ignored"))
	sp.End()

	data := findSpanDataByName(t, getSpans(), "nil-error")
	if len(data.Errors) != 0 {
		t.Fatalf("len(Errors) = %d, want 0", len(data.Errors))
	}
}

func TestContractStartSnapshotsInitialAttrs(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	attrs := []slog.Attr{
		slog.String("service", "api"),
		slog.String("region", "eu-west-1"),
	}
	_, sp := NewSpan(context.Background(), "snapshotted", WithAttrs(attrs...))
	attrs[0] = slog.String("service", "mutated")
	attrs[1] = slog.String("region", "us-east-1")
	sp.End()

	data := findSpanDataByName(t, getSpans(), "snapshotted")
	requireAttrValue(t, data.Attrs, "service", "api")
	requireAttrValue(t, data.Attrs, "region", "eu-west-1")
}

func TestContractInvalidTraceStateDroppedAndRecorded(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	extracted := ExtractTraceContext(context.Background(), MapCarrier{
		TraceParentHeader: validVersion00TraceParent,
		TraceStateHeader:  "bad=\nvalue",
	})
	ctx, sp := NewSpan(extracted, "child")
	started := sp
	sp.End()

	if started.traceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("traceID = %q, want remote trace id", started.traceID)
	}
	if started.parentID != "00f067aa0ba902b7" {
		t.Fatalf("parentID = %q, want remote parent id", started.parentID)
	}
	if started.traceState != "" {
		t.Fatalf("traceState = %q, want dropped invalid tracestate", started.traceState)
	}

	outgoing := MapCarrier{}
	InjectTraceContext(ctx, outgoing)
	if _, ok := outgoing[TraceStateHeader]; ok {
		t.Fatalf("outgoing tracestate = %q, want omitted", outgoing[TraceStateHeader])
	}

	data := findSpanDataByName(t, getSpans(), "child")
	diagnostic := findEventDataByName(t, data.Events, "tracecontext.invalid_tracestate")
	requireAttrValue(t, diagnostic.Attrs, "header", TraceStateHeader)
	requireAttrValue(t, diagnostic.Attrs, "value", "bad=\nvalue")
	requireAttrValue(t, diagnostic.Attrs, "reason", "tracestate value contains non-printable characters")
}

func TestContractInvalidTraceParentStartsNewTraceAndRecordsDiagnostic(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	extracted := ExtractTraceContext(context.Background(), MapCarrier{
		TraceParentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		TraceStateHeader:  "rojo=1",
	})
	ctx, sp := NewSpan(extracted, "root")
	started := sp
	sp.End()

	if started.parentID != "" {
		t.Fatalf("parentID = %q, want empty for fresh local root", started.parentID)
	}
	if started.traceID == "" {
		t.Fatal("traceID is empty, want new local trace id")
	}
	if started.traceID == "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal("traceID reused invalid upstream trace id")
	}

	outgoing := MapCarrier{}
	InjectTraceContext(ctx, outgoing)
	if len(outgoing) != 0 {
		t.Fatalf("outgoing headers = %#v, want none after span end", outgoing)
	}

	data := findSpanDataByName(t, getSpans(), "root")
	diagnostic := findEventDataByName(t, data.Events, "tracecontext.invalid_traceparent")
	requireAttrValue(t, diagnostic.Attrs, "header", TraceParentHeader)
	requireAttrValue(t, diagnostic.Attrs, "value", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01")
	requireAttrValue(t, diagnostic.Attrs, "reason", errInvalidParentID.Error())
}
