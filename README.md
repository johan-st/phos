# Phos

The Phos library is meant to be a simple-to-use, difficult-to-misuse tracing library.

## Quickstart

```go
import "github.com/johan-st/phos"

ctx, span := phos.NewSpan(ctx, "handler",
    phos.WithAttrs(slog.String("route", "/api")),
    phos.WithKind(phos.Server),
)
defer span.End()

phos.Attrs(ctx, slog.String("user", id))
phos.Event(ctx, "cache.hit")
err := doSomething()
if err != nil {
    phos.Fail(ctx, err, slog.String("phase", "handler"))
    return
}
```

Set a default exporter (optional), or use [`WithExporter`](https://pkg.go.dev/github.com/johan-st/phos#WithExporter) on the request context:

```go
restore := phos.SetExporter(myExporter)
defer restore()

ctx = phos.WithExporter(ctx, myExporter)
```

Before process exit, start draining and wait until Phos is closed:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

phos.DrainAndClose(ctx)
phos.WaitForClosed()
```

While draining, new root spans return no-op spans, but child spans on still-open
local parents are still allowed. If the drain context expires first, Phos closes the
remaining open span trees bottom-up and records the event
`"phos.Shutdown timeout reached"` on affected spans.

HTTP propagation (W3C Trace Context):

```go
phos.InjectTraceContext(ctx, phos.HTTPHeaderCarrier{Header: resp.Header()})
ctx = phos.ExtractTraceContext(ctx, phos.HTTPHeaderCarrier{Header: req.Header})
```

## References

https://www.w3.org/TR/trace-context
