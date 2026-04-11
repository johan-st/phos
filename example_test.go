package phos

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// TestExample_TraceVisualization builds a realistic trace with overlapping
// spans and prints a CLI timeline showing how spans relate to each other.
//
// Run with: go test -v -run Example_Trace ./phos/...
func TestExample_TraceVisualization(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, root := Start(context.Background(), "http.request",
		slog.String("method", "GET"),
		slog.String("path", "/api/users"),
	)
	Event(rootCtx, "received")
	time.Sleep(2 * time.Millisecond)

	authCtx, auth := Start(rootCtx, "auth")
	Event(authCtx, "lookup")
	time.Sleep(time.Millisecond)
	Event(authCtx, "granted")

	_, tokenVerify := Start(authCtx, "token.verify")
	time.Sleep(3 * time.Millisecond)
	tokenVerify.End()

	Attrs(authCtx, slog.String("user", "alice"))
	time.Sleep(time.Millisecond)
	auth.End()

	dbCtx, dbQuery := Start(rootCtx, "db.query",
		slog.String("table", "users"),
	)
	cacheCtx, cacheCheck := Start(rootCtx, "cache.check",
		slog.String("key", "users:list"),
	)

	Event(dbCtx, "rows", slog.Int("count", 42))
	Event(cacheCtx, "miss")
	time.Sleep(3 * time.Millisecond)
	cacheCheck.End()

	time.Sleep(4 * time.Millisecond)
	Error(dbCtx, errors.New("slow query"),
		slog.Duration("threshold", 100*time.Millisecond),
	)
	dbQuery.End()

	time.Sleep(time.Millisecond)

	serializeCtx, serialize := Start(rootCtx, "serialize")
	time.Sleep(time.Millisecond)

	_, compress := Start(serializeCtx, "compress")
	time.Sleep(2 * time.Millisecond)
	compress.End()

	Event(serializeCtx, "done")
	time.Sleep(time.Millisecond)
	serialize.End()

	Event(rootCtx, "complete")
	root.End()

	t.Logf("\n%s", RenderTraces(getSpans()))
}
