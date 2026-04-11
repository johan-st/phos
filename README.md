# Phos

The Phos library is meant to be a simple-to-use, difficult-to-misuse tracing library.

## Quickstart

```go
import "github.com/johan-st/phos"

ctx, span := phos.Start(ctx, "handler", slog.String("route", "/api"))
defer span.End()

phos.Attrs(ctx, slog.String("user", id))
phos.Event(ctx, "cache.hit")
```

Set a default exporter (optional), or use [`WithExporter`](https://pkg.go.dev/github.com/johan-st/phos#WithExporter) on the request context:

```go
restore := phos.SetExporter(myExporter)
defer restore()

ctx = phos.WithExporter(ctx, myExporter)
```

HTTP propagation (W3C Trace Context):

```go
phos.InjectTraceContext(ctx, phos.HTTPHeaderCarrier{Header: resp.Header()})
ctx = phos.ExtractTraceContext(ctx, phos.HTTPHeaderCarrier{Header: req.Header})
```

## References

https://www.w3.org/TR/trace-context
