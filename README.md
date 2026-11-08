# Phos

The Phos library is meant to be a simple-to-use, difficult-to-misuse tracing library.

## Standalone trace dashboard

Requires Go 1.25 or newer. Start the HTTP service:

```sh
go run ./cmd/phos serve
```

Open http://127.0.0.1:9094/dashboard. The service embeds its frontend and stores
history in `.phos/dashboard.db`. It starts with an empty archive; configure one
input to ingest traces, or serve an existing dashboard archive with `--db`.

```sh
# Follow a native Spore/Phos spans_v2 SQLite database without changing it.
go run ./cmd/phos serve --trace-db /path/to/traces.db

# Continuously ingest Google Cloud Trace using Application Default Credentials.
go run ./cmd/phos serve --db .phos/gcp.db --gcp-project your-project

# Continuously ingest a Jaeger API.
go run ./cmd/phos serve --db .phos/jaeger.db --jaeger-url http://127.0.0.1:16686

# Serve an existing archive offline, on another local port.
go run ./cmd/phos serve --addr 127.0.0.1:8080 --db /path/to/dashboard.db
```

The dashboard includes historical and live search, duration and error filters,
time/duration heatmaps, trace waterfalls, attributes, events, error paths,
browser caching and JSON downloads. HTTP/SSE runs directly, with no Spore agent
or NATS connection. Ctrl-C/SIGTERM closes streams and drains HTTP requests.

See [dashboard documentation](internal/dashboard/README.md) for configuration,
historical imports, API contracts and frontend development.

### Task commands

Run `task --list` to list commands. Each input mode uses its own default archive;
override it with `DB=/path/to/dashboard.db`. Named serving modes clear conflicting
live-input environment settings. Extra CLI flags go after `--`.

```sh
task serve                                              # Use CLI/environment defaults.
task serve:archive DB=/path/to/dashboard.db               # Offline archive.
task serve:native TRACE_DB=/path/to/traces.db             # Native spans_v2 input.
task serve:gcp GCP_PROJECT=your-project                  # GCP input using ADC.
task serve:jaeger JAEGER_URL=http://127.0.0.1:16686         # Jaeger input (URL optional).
task serve:demo                                          # Public Jaeger demo input.
task serve:gcp GCP_PROJECT=your-project -- --addr 127.0.0.1:8080

# Historical imports use RFC3339 timestamps and the half-open range [FROM, TO).
task import:gcp GCP_PROJECT=your-project FROM=2026-10-08T10:00:00Z TO=2026-10-08T11:00:00Z
task import:jaeger FROM=2026-10-08T10:00:00Z TO=2026-10-08T11:00:00Z -- --service checkout
task import:demo FROM=2026-10-08T10:00:00Z TO=2026-10-08T11:00:00Z
```

Default archives: `.phos/dashboard.db` (archive), `.phos/native.db` (native),
`.phos/gcp.db` (GCP), `.phos/jaeger.db` (Jaeger), and `.phos/demo.db` (demo).
Matching import and serve tasks share the same default archive.

For local GCP defaults, copy `.env.example` to `.env`, set `PHOS_GCP_PROJECT`,
then run `task serve:gcp`. Task loads `.env` automatically; nonempty shell
defaults override matching `.env` values. `.env` is ignored by Git.

```dotenv
PHOS_GCP_PROJECT=your-project
PHOS_GCP_FILTER=
PHOS_STORE_DB=.phos/gcp.db
PHOS_ADDR=127.0.0.1:9094
```

Both GCP tasks use `PHOS_GCP_PROJECT`, `PHOS_GCP_FILTER`, and `PHOS_STORE_DB`
as defaults; task arguments `GCP_PROJECT`, `GCP_FILTER`, and `DB` override them.
Serving also honors `PHOS_ADDR`. Flags after `--` take precedence. Authentication
uses existing Application Default Credentials; `.env` can also set
`GOOGLE_APPLICATION_CREDENTIALS` or `GOOGLE_CLOUD_QUOTA_PROJECT` when needed.

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
