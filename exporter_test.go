package phos

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

func TestExporterEndIsIdempotent(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	_, sp := NewSpan(context.Background(), "once")
	sp.End()
	sp.End()
	sp.End()

	if got := len(getSpans()); got != 1 {
		t.Fatalf("len(spans) = %d, want 1", got)
	}
}

func TestInMemExportImporterSnapshotsAreStable(t *testing.T) {
	exp := NewInMemExportImporter()
	withExporter(t, exp)

	ctx, started := NewSpan(context.Background(), "root", WithAttrs(slog.String("service", "api")))
	internal := started
	Attrs(ctx, slog.String("first", "value"))
	Event(ctx, "evt", slog.String("phase", "before-end"))
	rootErr := errors.New("snap")
	Error(ctx, rootErr, slog.String("scope", "before-end"))
	started.End()

	saved := exp.Spans()[internal.id]
	if len(saved.Attrs) != 2 {
		t.Fatalf("len(saved.Attrs) = %d, want 2", len(saved.Attrs))
	}
	if len(saved.Events) != 1 || saved.Events[0].Name != "evt" {
		t.Fatalf("saved.Events = %#v, want one named event", saved.Events)
	}
	if len(saved.Errors) != 1 || saved.Errors[0].Err != rootErr {
		t.Fatalf("saved.Errors = %#v, want [%v]", saved.Errors, rootErr)
	}

	internal.Attrs(slog.String("later", "mutation"))
	internal.Event("later-event", slog.String("phase", "after-end"))
	internal.Error(errors.New("mutated"), slog.String("scope", "after-end"))

	afterMutation := exp.Spans()[internal.id]
	if len(afterMutation.Attrs) != 2 {
		t.Fatalf("len(afterMutation.Attrs) = %d, want 2", len(afterMutation.Attrs))
	}
	if len(afterMutation.Events) != 1 {
		t.Fatalf("len(afterMutation.Events) = %d, want 1", len(afterMutation.Events))
	}
	if len(afterMutation.Errors) != 1 {
		t.Fatalf("len(afterMutation.Errors) = %d, want 1", len(afterMutation.Errors))
	}
}

func TestSnapshotReturnsDetachedSnapshot(t *testing.T) {
	_, started := NewSpan(
		context.Background(),
		"view",
		WithAttrs(slog.String("service", "api")),
		WithKind(Client),
		WithLink("trace-id", "span-id", slog.String("scope", "link")),
	)
	sp := started
	sp.Event("evt", slog.String("phase", "view"))
	sp.Error(errors.New("boom"), slog.String("scope", "view"))

	first := sp.Snapshot()
	first.Attrs[0] = slog.String("service", "mutated")
	first.Links[0].Attrs[0] = slog.String("scope", "mutated")
	first.Events[0].Name = "mutated"
	first.Events[0].Attrs[0] = slog.String("phase", "mutated")
	first.Errors[0].Attrs[0] = slog.String("scope", "mutated")

	second := sp.Snapshot()
	if second.Kind != Client {
		t.Fatalf("second.Kind = %q, want %q", second.Kind.String(), Client.String())
	}
	requireAttrValue(t, second.Attrs, "service", "api")
	requireAttrValue(t, second.Links[0].Attrs, "scope", "link")
	if second.Events[0].Name != "evt" {
		t.Fatalf("second.Events[0].Name = %q, want %q", second.Events[0].Name, "evt")
	}
	requireAttrValue(t, second.Events[0].Attrs, "phase", "view")
	requireAttrValue(t, second.Errors[0].Attrs, "scope", "view")
}
