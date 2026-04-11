package phos_test

import (
	"context"
	"log/slog"

	"github.com/johan-st/phos"
)

func ExampleStart() {
	ctx := context.Background()
	ctx, span := phos.Start(ctx, "request", slog.String("method", "GET"))
	defer span.End()

	phos.Attrs(ctx, slog.String("user", "alice"))
	phos.Event(ctx, "authorized")

	// Output:
}
