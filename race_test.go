package phos

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
)

func TestConcurrentSharedSpanMutations(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	ctx, started := Start(context.Background(), "shared")
	var wg sync.WaitGroup

	for i := range 64 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			Attrs(ctx, slog.Int("i", n))
			Event(ctx, "event", slog.Int("i", n))
			Error(ctx, errors.New("boom"), slog.Int("i", n))
			Fail(ctx)
		}(i)
	}

	wg.Wait()
	started.End()

	data := findSpanDataByName(t, getSpans(), "shared")
	if len(data.Attrs) != 64 {
		t.Fatalf("len(Attrs) = %d, want %d", len(data.Attrs), 64)
	}
	if len(data.Events) != 64 {
		t.Fatalf("len(Events) = %d, want %d", len(data.Events), 64)
	}
	if len(data.Errors) != 64 {
		t.Fatalf("len(Errors) = %d, want %d", len(data.Errors), 64)
	}
	if !data.Failed {
		t.Fatal("Fail() from concurrent mutation should be recorded")
	}
}

func TestConcurrentParallelChildSpans(t *testing.T) {
	exp := &captureExporter{}
	getSpans := withExporter(t, exp)

	rootCtx, root := Start(context.Background(), "root")
	rootID := root.(*span).id

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx, child := Start(rootCtx, "child", slog.Int("i", n))
			Event(ctx, "work", slog.Int("i", n))
			child.End()
		}(i)
	}

	wg.Wait()
	root.End()

	spans := getSpans()
	if len(spans) != 51 {
		t.Fatalf("len(spans) = %d, want 51", len(spans))
	}

	childCount := 0
	for _, span := range spans {
		if span.Name != "child" {
			continue
		}
		childCount++
		if span.ParentID != rootID {
			t.Fatalf("child ParentID = %q, want %q", span.ParentID, rootID)
		}
	}
	if childCount != 50 {
		t.Fatalf("child count = %d, want 50", childCount)
	}
}

func TestConcurrentInjectExtractWithIsolatedCarriers(t *testing.T) {
	ctx, _ := Start(context.Background(), "root")

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []string
	)
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			carrier := MapCarrier{}
			InjectTraceContext(ctx, carrier)
			next := ExtractTraceContext(context.Background(), carrier)
			if traceContextFromContext(next).traceID == "" {
				mu.Lock()
				errs = append(errs, "ExtractTraceContext() lost trace id")
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatal(errs[0])
	}
}
