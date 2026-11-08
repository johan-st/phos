// Source-independent query data. No browser I/O or rendered HTML.
import dashboard/format as f
import dashboard/protocol as p
import dashboard/query as q
import dashboard/spans
import gleam/dict.{type Dict}
import gleam/dynamic/decode as d
import gleam/int
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result

pub type Trace {
  Trace(
    id: String,
    name: String,
    service: String,
    operation: String,
    start: Float,
    end: Float,
    duration: Float,
    spans: Int,
    events: Int,
    errors: Int,
    root_status: String,
    incomplete: Bool,
  )
}

pub type Bucket {
  Bucket(
    from: Int,
    to: Int,
    spans: Int,
    errors: Int,
    durations: List(Int),
    unknown_duration: Int,
    stats: Option(BucketStats),
  )
}

pub type BucketStats {
  BucketStats(
    services: Facet,
    operations: Facet,
    samples: Int,
    p50: Float,
    p95: Float,
    p99: Float,
  )
}

pub fn bucket_key(bucket: Bucket) -> String {
  int.to_string(bucket.from) <> ":" <> int.to_string(bucket.to)
}

pub type Latency {
  Latency(to: Float, count: Int)
}

pub type FacetValue {
  FacetValue(value: String, count: Int)
}

pub type Facet {
  Facet(values: List(FacetValue), other: Int)
}

pub type Aggregates {
  Aggregates(
    traces: Int,
    spans: Int,
    errors: Int,
    error_traces: Int,
    p50: Float,
    p95: Float,
    buckets: List(Bucket),
    latency: List(Latency),
    duration_bounds: List(Float),
    heatmap_max: Int,
    unknown_duration: Int,
    services: Facet,
    operations: Facet,
    bucket_version: Int,
    bucket_width: Int,
  )
}

pub type Coverage {
  Coverage(
    from: Int,
    to: Int,
    rolling: Bool,
    complete: Bool,
    running: Bool,
    scanned: Int,
    reason: String,
    limited: Bool,
  )
}

pub type Health {
  Health(kind: String, last_success: Int, error: String, notice: String)
}

pub type Frame {
  Frame(
    source: String,
    epoch: String,
    query: String,
    kind: String,
    base: Int,
    seq: Int,
    now: Int,
    summaries: List(Trace),
    removed: List(String),
    order: List(String),
    aggregates: Aggregates,
    bucket_delta: Bool,
    coverage: Coverage,
    health: Health,
    cursor: String,
    page: Int,
    pages: Int,
  )
}

pub type Data {
  Data(
    frame: Frame,
    traces: Dict(String, Trace),
    services: List(String),
    operations: List(String),
    attributes: List(String),
  )
}

pub type Detail {
  Detail(
    source: String,
    epoch: String,
    id: String,
    revision: String,
    summary: Trace,
    spans: List(spans.Span),
    cursor: String,
    complete: Bool,
    reason: String,
  )
}

pub type Metadata {
  Metadata(
    services: List(String),
    operations: List(String),
    attributes: List(String),
    health: Health,
  )
}

pub type Change {
  Change(
    source: String,
    epoch: String,
    id: String,
    base: String,
    revision: String,
    summary: Trace,
    upserts: List(spans.Span),
    removed: List(String),
  )
}

pub fn change(raw: String) -> Result(Change, String) {
  let decoder = {
    use schema <- d.field("schema_version", d.int)
    use source <- d.field("source", d.string)
    use epoch <- d.field("epoch", d.string)
    use id <- d.field("trace_id", d.string)
    use base <- d.field("base_revision", d.string)
    use revision <- d.field("revision", d.string)
    use summary <- d.field("summary", trace_decoder())
    use upserts <- d.field("upserts", d.list(spans.span_decoder()))
    use removed <- d.field("removed", d.list(d.string))
    let change =
      Change(source, epoch, id, base, revision, summary, upserts, removed)
    case schema == 3 {
      True -> d.success(change)
      False -> d.failure(change, "schema 3")
    }
  }
  json.parse(raw, decoder) |> result.map_error(fn(_) { "Invalid trace change" })
}

pub fn empty() -> Data {
  Data(
    Frame(
      "",
      "",
      "",
      "reset",
      0,
      0,
      0,
      [],
      [],
      [],
      Aggregates(
        0,
        0,
        0,
        0,
        0.0,
        0.0,
        [],
        [],
        [],
        0,
        0,
        Facet([], 0),
        Facet([], 0),
        0,
        0,
      ),
      False,
      Coverage(0, 0, True, False, False, 0, "", False),
      Health("", 0, "", ""),
      "",
      1,
      1,
    ),
    dict.new(),
    [],
    [],
    [],
  )
}

pub fn parse(raw: String) -> Result(Frame, String) {
  json.parse(raw, frame_decoder())
  |> result.map_error(fn(_) { "Invalid search response; reload the dashboard" })
}

pub fn apply(data: Data, frame: Frame) -> Result(Data, String) {
  case frame.kind {
    "delta"
      if frame.source != data.frame.source
      || frame.epoch != data.frame.epoch
      || frame.query != data.frame.query
    -> Error("Search baseline changed")
    "delta" if frame.base > data.frame.seq -> Error("Search stream gap")
    _ if frame.query == data.frame.query && frame.seq < data.frame.seq ->
      Ok(data)
    "delta"
      if frame.bucket_delta
      && {
        frame.coverage.from != data.frame.coverage.from
        || frame.coverage.to != data.frame.coverage.to
      }
    -> Error("Timeline partition changed; reload baseline")
    _ -> {
      let current = case frame.kind {
        "reset" -> dict.new()
        _ -> data.traces
      }
      let current = list.fold(frame.removed, current, dict.delete)
      let current =
        list.fold(frame.summaries, current, fn(acc, trace) {
          dict.insert(acc, trace.id, trace)
        })
      let frame = case frame.kind == "delta" && frame.bucket_delta {
        False -> frame
        True -> {
          let buckets =
            dict.from_list(
              list.map(data.frame.aggregates.buckets, fn(b) { #(b.from, b) }),
            )
          let buckets =
            list.fold(frame.aggregates.buckets, buckets, fn(acc, b) {
              dict.insert(acc, b.from, b)
            })
          let buckets =
            dict.values(buckets)
            |> list.filter(fn(b) {
              b.from >= frame.coverage.from && b.from < frame.coverage.to
            })
            |> list.sort(fn(a, b) { int.compare(a.from, b.from) })
          Frame(
            ..frame,
            bucket_delta: False,
            aggregates: Aggregates(..frame.aggregates, buckets: buckets),
          )
        }
      }
      Ok(Data(..data, frame: frame, traces: current))
    }
  }
}

fn frame_decoder() {
  use schema <- d.field("schema_version", d.int)
  use source <- d.field("source", d.string)
  use epoch <- d.field("epoch", d.string)
  use query <- d.field("query", d.string)
  use kind <- d.field("kind", d.string)
  use base <- d.field("base_seq", d.int)
  use seq <- d.field("seq", d.int)
  use now <- d.field("now_ms", d.int)
  use summaries <- d.field("summaries", d.list(trace_decoder()))
  use removed <- d.field("removed", d.list(d.string))
  use order <- d.field("order", d.list(d.string))
  use aggregates <- d.field("aggregates", aggregate_decoder())
  use bucket_delta <- d.optional_field("bucket_delta", False, d.bool)
  use coverage <- d.field("coverage", coverage_decoder())
  use health <- d.field("health", health_decoder())
  use cursor <- d.field("cursor", d.string)
  use page <- d.field("page", d.int)
  use pages <- d.field("pages", d.int)
  let frame =
    Frame(
      source,
      epoch,
      query,
      kind,
      base,
      seq,
      now,
      summaries,
      removed,
      order,
      aggregates,
      bucket_delta,
      coverage,
      health,
      cursor,
      page,
      pages,
    )
  case
    schema == 3
    && seq > 0
    && base >= 0
    && seq >= base
    && list.contains(["reset", "delta"], kind)
    && list.length(aggregates.buckets) <= 60
  {
    True -> d.success(frame)
    False -> d.failure(frame, "search schema 3")
  }
}

fn trace_decoder() {
  use id <- d.field("trace_id", d.string)
  use name <- d.field("name", d.string)
  use service <- d.field("service", d.string)
  use operation <- d.field("operation", d.string)
  use start <- d.field("start_ms", d.float)
  use end <- d.field("end_ms", d.float)
  use duration <- d.field("duration_ms", d.float)
  use spans <- d.field("span_count", d.int)
  use events <- d.field("event_count", d.int)
  use errors <- d.field("error_count", d.int)
  use root_status <- d.field("root_status", d.string)
  use incomplete <- d.field("incomplete", d.bool)
  d.success(Trace(
    id,
    name,
    service,
    operation,
    start,
    end,
    duration,
    spans,
    events,
    errors,
    root_status,
    incomplete,
  ))
}

fn bucket_decoder() {
  use from <- d.field("from_ms", d.int)
  use to <- d.field("to_ms", d.int)
  use spans <- d.field("spans", d.int)
  use errors <- d.field("errors", d.int)
  use durations <- d.optional_field("duration_counts", [], d.list(d.int))
  use unknown <- d.optional_field("unknown_duration", 0, d.int)
  use stats <- d.optional_field(
    "stats",
    None,
    d.optional(bucket_stats_decoder()),
  )
  d.success(Bucket(from, to, spans, errors, durations, unknown, stats))
}

fn bucket_stats_decoder() {
  use services <- d.field("services", facet_decoder())
  use operations <- d.field("operations", facet_decoder())
  use samples <- d.field("samples", d.int)
  use p50 <- d.field("p50_ms", d.float)
  use p95 <- d.field("p95_ms", d.float)
  use p99 <- d.field("p99_ms", d.float)
  d.success(BucketStats(services, operations, samples, p50, p95, p99))
}

fn latency_decoder() {
  use to <- d.field("to_ms", d.float)
  use count <- d.field("count", d.int)
  d.success(Latency(to, count))
}

fn facet_decoder() {
  use values <- d.field(
    "values",
    d.list({
      use value <- d.field("value", d.string)
      use count <- d.field("count", d.int)
      d.success(FacetValue(value, count))
    }),
  )
  use other <- d.field("other", d.int)
  d.success(Facet(values, other))
}

fn aggregate_decoder() {
  use traces <- d.field("traces", d.int)
  use spans <- d.field("spans", d.int)
  use errors <- d.field("errors", d.int)
  use error_traces <- d.field("error_traces", d.int)
  use p50 <- d.field("p50_ms", d.float)
  use p95 <- d.field("p95_ms", d.float)
  use buckets <- d.field("buckets", d.list(bucket_decoder()))
  use latency <- d.field("latency", d.list(latency_decoder()))
  use bounds <- d.optional_field("duration_bounds_ms", [], d.list(d.float))
  use peak <- d.optional_field("heatmap_max", 0, d.int)
  use unknown <- d.optional_field("unknown_duration", 0, d.int)
  use services <- d.optional_field("services", Facet([], 0), facet_decoder())
  use operations <- d.optional_field(
    "operations",
    Facet([], 0),
    facet_decoder(),
  )
  use version <- d.optional_field("bucket_version", 0, d.int)
  use width <- d.optional_field("bucket_width_ms", 0, d.int)
  d.success(Aggregates(
    traces,
    spans,
    errors,
    error_traces,
    p50,
    p95,
    buckets,
    latency,
    bounds,
    peak,
    unknown,
    services,
    operations,
    version,
    width,
  ))
}

fn coverage_decoder() {
  use from <- d.field("from_ms", d.int)
  use to <- d.field("to_ms", d.int)
  use rolling <- d.field("rolling", d.bool)
  use complete <- d.field("complete", d.bool)
  use running <- d.field("running", d.bool)
  use scanned <- d.field("scanned_traces", d.int)
  use reason <- d.field("reason", d.string)
  use limited <- d.field("source_limited", d.bool)
  d.success(Coverage(
    from,
    to,
    rolling,
    complete,
    running,
    scanned,
    reason,
    limited,
  ))
}

fn health_decoder() {
  use kind <- d.field("kind", d.string)
  use last_success <- d.field("last_success_ms", d.int)
  use error <- d.field("error", d.string)
  use notice <- d.optional_field("notice", "", d.string)
  d.success(Health(kind, last_success, error, notice))
}

pub fn metadata(raw: String) -> Result(Metadata, String) {
  let decoder = {
    use services <- d.field("services", d.list(d.string))
    use operations <- d.field("operations", d.list(d.string))
    use attributes <- d.field("attributes", d.list(d.string))
    use health <- d.field("health", health_decoder())
    d.success(Metadata(services, operations, attributes, health))
  }
  json.parse(raw, decoder)
  |> result.map_error(fn(_) { "Source metadata unavailable" })
}

pub fn detail(raw: String) -> Result(Detail, String) {
  let decoder = {
    use schema <- d.field("schema_version", d.int)
    use source <- d.field("source", d.string)
    use epoch <- d.field("epoch", d.string)
    use id <- d.field("trace_id", d.string)
    use revision <- d.field("revision", d.string)
    use summary <- d.field("summary", trace_decoder())
    use spans <- d.field("spans", d.list(spans.span_decoder()))
    use cursor <- d.field("cursor", d.string)
    use complete <- d.field("complete", d.bool)
    use reason <- d.field("reason", d.string)
    let detail =
      Detail(
        source,
        epoch,
        id,
        revision,
        summary,
        spans,
        cursor,
        complete,
        reason,
      )
    case schema == 3 {
      True -> d.success(detail)
      False -> d.failure(detail, "trace schema 3")
    }
  }
  json.parse(raw, decoder) |> result.map_error(fn(_) { "Invalid trace detail" })
}

pub fn page(data: Data, base: p.Page, route: String, now: Int) -> p.Page {
  let frame = data.frame
  let traces =
    list.filter_map(frame.order, fn(id) { dict.get(data.traces, id) })
  let selected = q.get(route, "trace_id")
  let rows =
    list.map(traces, fn(t) {
      let href =
        q.set(route, [
          #("trace_id", t.id),
          #("span_id", ""),
          #("event_ix", ""),
          #("detail_page", ""),
        ])
      p.Trace(
        t.id,
        f.short(t.id),
        t.name,
        t.service,
        t.operation,
        f.timestamp(t.start),
        f.age(t.start, now),
        f.duration(t.duration),
        "normal",
        t.spans,
        t.events,
        t.errors,
        t.errors > 0,
        t.id == selected,
        href,
        href,
      )
    })
  let paging =
    p.Pagination(
      frame.page,
      100,
      frame.aggregates.traces,
      frame.pages,
      { frame.page - 1 } * 100 + 1,
      { frame.page - 1 } * 100 + list.length(rows),
      frame.page > 1,
      frame.cursor != "",
      q.set(route, [#("cursor", "")]),
      q.set(route, [#("cursor", frame.cursor)]),
      [],
    )
  p.Page(
    ..base,
    summary: p.Summary(
      frame.aggregates.traces,
      frame.aggregates.spans,
      frame.aggregates.error_traces,
    ),
    traces: rows,
    trace_paging: paging,
    filters: p.Filters(
      ..base.filters,
      sort_latest_href: q.set(route, [#("sort", "latest"), #("cursor", "")]),
      sort_slowest_href: q.set(route, [#("sort", "slowest"), #("cursor", "")]),
      sort_errors_href: q.set(route, [#("sort", "errors"), #("cursor", "")]),
      services: data.services,
      selected_trace_id: selected,
      trace_page: frame.page,
      trace_page_size: 100,
    ),
  )
}

// Persist a reconciled snapshot, never a delta without its baseline.
pub fn encode(data: Data) -> String {
  let x = data.frame
  json.object([
    #("schema_version", json.int(3)),
    #("bucket_delta", json.bool(False)),
    #("source", json.string(x.source)),
    #("epoch", json.string(x.epoch)),
    #("query", json.string(x.query)),
    #("kind", json.string("reset")),
    #("base_seq", json.int(0)),
    #("seq", json.int(x.seq)),
    #("now_ms", json.int(x.now)),
    #("summaries", json.array(dict.values(data.traces), encode_trace)),
    #("removed", json.array([], json.string)),
    #("order", json.array(x.order, json.string)),
    #("cursor", json.string(x.cursor)),
    #("page", json.int(x.page)),
    #("pages", json.int(x.pages)),
    #(
      "aggregates",
      json.object([
        #("bucket_version", json.int(x.aggregates.bucket_version)),
        #("bucket_width_ms", json.int(x.aggregates.bucket_width)),
        #(
          "duration_bounds_ms",
          json.array(x.aggregates.duration_bounds, json.float),
        ),
        #("heatmap_max", json.int(x.aggregates.heatmap_max)),
        #("unknown_duration", json.int(x.aggregates.unknown_duration)),
        #("services", encode_facet(x.aggregates.services)),
        #("operations", encode_facet(x.aggregates.operations)),
        #("traces", json.int(x.aggregates.traces)),
        #("spans", json.int(x.aggregates.spans)),
        #("errors", json.int(x.aggregates.errors)),
        #("error_traces", json.int(x.aggregates.error_traces)),
        #("p50_ms", json.float(x.aggregates.p50)),
        #("p95_ms", json.float(x.aggregates.p95)),
        #(
          "buckets",
          json.array(x.aggregates.buckets, fn(b) {
            json.object([
              #("from_ms", json.int(b.from)),
              #("to_ms", json.int(b.to)),
              #("spans", json.int(b.spans)),
              #("errors", json.int(b.errors)),
              #("duration_counts", json.array(b.durations, json.int)),
              #("unknown_duration", json.int(b.unknown_duration)),
              #("stats", case b.stats {
                None -> json.null()
                Some(stats) ->
                  json.object([
                    #("services", encode_facet(stats.services)),
                    #("operations", encode_facet(stats.operations)),
                    #("samples", json.int(stats.samples)),
                    #("p50_ms", json.float(stats.p50)),
                    #("p95_ms", json.float(stats.p95)),
                    #("p99_ms", json.float(stats.p99)),
                  ])
              }),
            ])
          }),
        ),
        #(
          "latency",
          json.array(x.aggregates.latency, fn(b) {
            json.object([
              #("to_ms", json.float(b.to)),
              #("count", json.int(b.count)),
            ])
          }),
        ),
      ]),
    ),
    #(
      "coverage",
      json.object([
        #("from_ms", json.int(x.coverage.from)),
        #("to_ms", json.int(x.coverage.to)),
        #("rolling", json.bool(x.coverage.rolling)),
        #("complete", json.bool(x.coverage.complete)),
        #("running", json.bool(x.coverage.running)),
        #("scanned_traces", json.int(x.coverage.scanned)),
        #("reason", json.string(x.coverage.reason)),
        #("source_limited", json.bool(x.coverage.limited)),
      ]),
    ),
    #(
      "health",
      json.object([
        #("kind", json.string(x.health.kind)),
        #("notice", json.string(x.health.notice)),
        #("last_success_ms", json.int(x.health.last_success)),
        #("error", json.string(x.health.error)),
      ]),
    ),
  ])
  |> json.to_string
}

fn encode_trace(t: Trace) {
  json.object([
    #("trace_id", json.string(t.id)),
    #("name", json.string(t.name)),
    #("service", json.string(t.service)),
    #("operation", json.string(t.operation)),
    #("start_ms", json.float(t.start)),
    #("end_ms", json.float(t.end)),
    #("duration_ms", json.float(t.duration)),
    #("span_count", json.int(t.spans)),
    #("event_count", json.int(t.events)),
    #("error_count", json.int(t.errors)),
    #("root_status", json.string(t.root_status)),
    #("incomplete", json.bool(t.incomplete)),
  ])
}

fn encode_facet(facet: Facet) {
  json.object([
    #(
      "values",
      json.array(facet.values, fn(value) {
        json.object([
          #("value", json.string(value.value)),
          #("count", json.int(value.count)),
        ])
      }),
    ),
    #("other", json.int(facet.other)),
  ])
}
