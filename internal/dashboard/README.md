# Phos dashboard

Ported from Spore Cloud's `internal/dashboardworkload` at commit
`5ddf76e09d4a59df26715b8dee671f383dde2a4f` (local `gcp-trace-adapter` branch).
The Go query/store/adapters, Gleam model/view, CSS and browser cache tests are
retained. `app.go` and `http.go` replace the agent/NATS workload transport with
`net/http`; the old schema-2 span feed and server-rendered legacy UI are omitted.

## Run

From the repository root:

```sh
go run ./cmd/phos serve
go build -o /tmp/phos ./cmd/phos
/tmp/phos --addr 127.0.0.1:9094 --db .phos/dashboard.db
```

Go 1.25+ is required by the retained SQLite version. The frontend is embedded
in the executable: running the service requires no Node, Bun or Gleam install.
The default address is loopback. The service has no authentication; use it
locally or behind an authenticated proxy if binding to a shared interface.

| Flag | Environment | Default |
| --- | --- | --- |
| `--addr` | `PHOS_ADDR` | `127.0.0.1:9094` |
| `--db` | `PHOS_STORE_DB` | `.phos/dashboard.db` |
| `--trace-db` | `PHOS_TRACE_DB` | unset |
| `--jaeger-url` | `PHOS_JAEGER_URL` | unset |
| `--gcp-project` | `PHOS_GCP_PROJECT` | unset |
| `--gcp-filter` | `PHOS_GCP_FILTER` | unset |

Flags override environment values. `serve` is optional. Configure at most one
live input. `--gcp-filter` requires `--gcp-project`. No input serves an archive
offline. The store refuses to migrate an existing unrelated/native database.
Use a separate archive per input origin; changing origins is rejected.
Native input follows `spans_v2` committed sequences in bounded batches and
preserves ingested history when the source is pruned/reset. Its `snapshot_json`
uses the typed schema-2 codec in `internal/tracejsonl`.

Google Cloud Trace uses ADC and the `trace.readonly` scope; the account needs
trace list/read access. `GOOGLE_CLOUD_QUOTA_PROJECT` can override the ADC quota
project. GCP polls recent windows and catches up after downtime independently
of browser connections. Credentials and live upstream access are deployment
inputs, rather than requirements for offline use.

## Historical import

The importer writes the same archive used by the service. Fixed ranges use
RFC3339 timestamps and the half-open interval `[from,to)`.

```sh
go run ./cmd/dashboard-import --db .phos/gcp.db \
  --gcp-project your-project \
  --from 2026-10-08T10:00:00Z --to 2026-10-08T11:00:00Z

go run ./cmd/dashboard-import --db .phos/jaeger.db \
  --jaeger http://127.0.0.1:16686 --service checkout \
  --from 2026-10-08T10:00:00Z --to 2026-10-08T11:00:00Z
```

With no GCP project or Jaeger URL, the importer uses the original public Jaeger
demo endpoint. Completed pages checkpoint durably; interrupted imports can be
rerun. Optional `--gcp-filter` belongs to GCP; `--service` belongs to Jaeger.
The importer prints the dashboard URL query for the imported interval.

## HTTP API (schema 3)

- `GET /` redirects to `/dashboard`, preserving query parameters.
- `GET /healthz` reports HTTP process liveness; input errors appear in metadata/search health.
- `GET /dashboard` serves the embedded application.
- `GET /dashboard/assets/dashboard.js` and `dashboard.css` serve embedded assets.
- `GET /dashboard/search` returns summaries, aggregates, coverage, health and page cursors.
- `GET /dashboard/trace?trace_id=...` returns typed span detail with revision-checked continuations.
- `GET /dashboard/metadata` returns capabilities, input health and bounded suggestions.
- `GET /dashboard/stream` emits SSE `connection`, `search-batch`, `trace-delta`, and `trace-changed` events.
- `GET /dashboard/live/close?connection_id=...` closes a subscription.
- `/dashboard/spans` and `/dashboard/live` alias schema-3 search and streaming.

Queries accept `window` in milliseconds (default 15 minutes), or fixed `from`
and `to` Unix milliseconds, `service`, `operation`, `exact_trace`, repeated
`attr=key=value`, full text `q`, trace-duration `min_dur`/`max_dur` in milliseconds,
`errors=all|with|without`, `sort=latest|slowest|errors`, and opaque `cursor`.
Structured predicates and the interval match the same span; duration/errors
inspect the complete available trace. Root HTTP status is separate from
recorded descendant errors; absent status remains unknown.

Pages contain at most 100 summaries. Aggregates cover matching stored data,
independent of the displayed page. Retained query watermarks keep pagination
stable as ingestion continues. Incomplete/canceled queries explicitly report
partial coverage. Selected traces are bounded to 25,000 spans / 50 MiB and
chunked around 256 KiB. Large HTTP responses use gzip; large SSE payloads use
the existing `-gzip` event variants with base64-encoded gzip data. The frontend
decodes these before applying its generation/revision checks.

Streams have a 45-second lease and the frontend renews them. The transport
permits 128 concurrent streams, bounds each outgoing queue, and closes a slow
subscriber so it can reconnect using retained checkpoints. HTTP disconnect,
explicit close and process shutdown cancel the associated query subscription.

SQLite history persists across restarts and has no automatic deletion.
IndexedDB is a disposable 50 MiB cost-based LRU working cache. Clearing browser
cache leaves the SQLite archive and saved searches intact.

## Development

`task ci` runs the frontend checks and rebuild before the Go build, tests and
static analysis. It requires Gleam, Node, npm and Bun on `PATH`, plus the Go
analysis tools installed by `task install-tools`. Use a current patched Go
toolchain for the vulnerability check.

```sh
go test ./...
go test -race ./...
go build ./...
go vet ./...

cd internal/dashboard/ui
npm ci
bash build.sh
```

`build.sh` checks Gleam formatting, runs the Gleam and JavaScript cache/browser
tests, then bundles `entry.mjs` into `../assets/dashboard.js`. Development needs
Gleam, Node, npm and Bun; the pinned Gleam/npm manifests are included. Commit
the generated asset together with source changes. Build products, frontend
dependencies and local database files are ignored.
