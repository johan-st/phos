package phos_test

import (
	"context"
	"log/slog"

	"github.com/johan-st/phos"
)

func ExampleNewSpan() {
	ctx := context.Background()
	ctx, span := phos.NewSpan(ctx, "request", slog.String("method", "GET"))
	defer span.End()

	phos.Attrs(ctx, slog.String("user", "alice"))
	phos.Event(ctx, "authorized")

	// Output:
}
