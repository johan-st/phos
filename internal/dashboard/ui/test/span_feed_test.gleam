import dashboard/cache_policy as cache
import dashboard/project
import dashboard/protocol as p
import dashboard/spans as raw
import dashboard/state as s
import gleam/dict
import gleam/list
import gleam/option.{Some}
import gleam/result
import gleam/string
import gleeunit/should

@external(javascript, "./fixture_ffi.mjs", "spans")
fn fixture() -> String

fn frame() -> raw.Frame {
  let assert Ok(f) = raw.parse(fixture())
  f
}

fn data() -> raw.Data {
  let assert Ok(data) = raw.apply(raw.empty(), frame())
  data
}

fn model() -> s.Model {
  let #(m, _) =
    s.init(
      "/dashboard?events=markers&sort=slowest",
      "{\"source\":\"memory\"}",
      dict.new(),
      1440,
    )
  let #(m, _) = s.update(m, s.Batch(0, fixture(), Ok(frame())))
  m
}

pub fn raw_fixture_projects_to_existing_ui_test() {
  let page =
    project.page(
      data(),
      "/dashboard?events=markers&trace_id=trace-1&span_id=fail",
    )
  page.summary |> should.equal(p.Summary(2, 3, 1))
  let assert Some(trace) = page.selected_trace
  list.length(trace.rows) |> should.equal(2)
  trace.root_cause_label
  |> should.equal("worker:<script>alert(1)</script> → ROOT_CAUSE")
  let assert Some(item) = page.selected_item
  item.failed |> should.be_true
  item.self_duration |> should.equal("400.0ms")
  list.length(item.ancestry) |> should.equal(2)
  let orphan = project.page(data(), "/dashboard?trace_id=trace-2")
  let assert Some(item) = orphan.selected_item
  item.root |> should.be_true
}

pub fn event_details_are_not_parent_span_details_test() {
  list.each(["rows", "markers"], fn(mode) {
    let page =
      project.page(
        data(),
        "/dashboard?trace_id=trace-1&span_id=fail&event_ix=0&events=" <> mode,
      )
    let assert Some(item) = page.selected_item
    item.kind |> should.equal("event")
    item.name |> should.equal("queued")
    item.operation_name |> should.equal("")
    item.failed |> should.be_false
    item.events |> should.equal([])
    item.errors |> should.equal([])
    item.overview
    |> should.equal([
      p.Attr("trace id", "trace-1"),
      p.Attr("span id", "fail"),
      p.Attr("event", "queued"),
    ])
    item.attr_groups
    |> should.equal([p.AttrGroup("Other", [p.Attr("attempt", "1")])])
  })
}

pub fn local_selection_and_display_modes_need_no_network_test() {
  let m = model()
  let #(next, commands) =
    s.update(
      m,
      s.Navigate("/dashboard?events=rows&trace_id=trace-1&span_id=fail"),
    )
  next.generation |> should.equal(m.generation)
  list.length(commands) |> should.equal(1)
  commands |> should.equal([s.PushHistory(next.route)])
  let assert s.Available(page) = next.content
  let assert Some(trace) = page.selected_trace
  list.length(trace.rows) |> should.equal(3)
}

pub fn duplicate_gap_and_reset_batches_test() {
  let d = data()
  let base = frame()
  let stale = raw.Frame(..base, kind: "delta", base: 0, upserts: [], added: [])
  raw.apply(d, stale) |> should.equal(Ok(d))
  let gap = raw.Frame(..stale, base: 5, seq: 6)
  raw.apply(d, gap) |> result.is_error |> should.be_true
  let cleared =
    raw.Frame(
      ..base,
      seq: 2,
      kind: "delta",
      base: 1,
      upserts: [],
      added: [],
      dropped: ["trace-1"],
      removed: [#("trace-1", "root"), #("trace-1", "fail")],
    )
  let assert Ok(next) = raw.apply(d, cleared)
  list.length(raw.all(next)) |> should.equal(1)
  let reset = raw.Frame(..base, epoch: "restart", seq: 1)
  let assert Ok(next) = raw.apply(next, reset)
  list.length(raw.all(next)) |> should.equal(3)
}

pub fn cache_cursor_and_draft_are_preserved_test() {
  let #(initial, _) =
    s.init("/dashboard", "{\"source\":\"memory\"}", dict.new(), 1440)
  let #(cached, commands) = s.update(initial, s.CacheLoaded(0, Ok(fixture())))
  cached.refreshing |> should.be_true
  commands
  |> list.any(fn(c) {
    case c {
      s.Fetch(route, _) ->
        string.contains(route, "after=1")
        && string.contains(route, "fixture-epoch")
      _ -> False
    }
  })
  |> should.be_true
  let #(typing, _) = s.update(cached, s.QueryChanged("draft"))
  let #(updated, _) = s.update(typing, s.Batch(0, fixture(), Ok(frame())))
  updated.draft.query |> should.equal("draft")
}

pub fn cache_eviction_is_bounded_and_whole_trace_test() {
  let traces = [
    cache.Trace("a", "selected", 20_000, 40 * 1024 * 1024, 100),
    cache.Trace("b", "other", 10_000, 20 * 1024 * 1024, 200),
  ]
  cache.evict(traces, 1000, "selected") |> should.equal(["b"])
  cache.evict(
    [cache.Trace("huge", "selected", 25_001, 1, 100)],
    1000,
    "selected",
  )
  |> should.equal(["huge"])
  cache.evict([cache.Trace("old", "old", 1, 1, 1)], 3_600_002, "")
  |> should.equal(["old"])
}

pub fn cyclic_spans_do_not_hang_projection_test() {
  let d = data()
  let assert Ok(root) = dict.get(d.spans, #("trace-1", "root"))
  let d =
    raw.Data(
      ..d,
      spans: dict.insert(
        d.spans,
        #("trace-1", "root"),
        raw.Span(..root, parent_id: "fail"),
      ),
    )
  let page = project.page(d, "/dashboard?trace_id=trace-1&events=markers")
  let assert Some(trace) = page.selected_trace
  list.length(trace.rows) |> should.equal(2)
}

pub fn stream_restart_ignores_old_callbacks_test() {
  let m = model()
  let #(paused, _) = s.update(m, s.ToggleLive)
  let #(ignored, _) =
    s.update(paused, s.Batch(m.generation, fixture(), Ok(frame())))
  ignored |> should.equal(paused)
}

pub fn raw_schema_and_cross_query_checkpoint_test() {
  fixture()
  |> string.replace("\"schema_version\":2", "\"schema_version\":99")
  |> raw.parse
  |> result.is_error
  |> should.be_true
  let base = frame()
  let d = raw.Data(..data(), seq: 20)
  let changed =
    raw.Frame(
      ..base,
      kind: "delta",
      query: "filtered",
      base_query: base.query,
      base: 20,
      seq: 1,
      upserts: [],
      added: [],
      dropped: ["trace-2"],
      removed: [#("trace-2", "orphan")],
    )
  let assert Ok(next) = raw.apply(d, changed)
  next.query |> should.equal("filtered")
  next.seq |> should.equal(1)
  list.length(raw.all(next)) |> should.equal(2)
  raw.apply(raw.Data(..d, seq: 21), changed)
  |> result.is_error
  |> should.be_true
  let stale = raw.Frame(..base, seq: 19)
  raw.apply(d, stale) |> should.equal(Ok(d))
}

pub fn filter_reuses_cursor_and_clear_cache_reseeds_test() {
  let m = model()
  let #(filtered, commands) =
    s.update(m, s.Navigate("/dashboard?failed_only=1"))
  filtered.data |> should.equal(m.data)
  commands
  |> list.any(fn(c) {
    case c {
      s.Fetch(route, _) ->
        string.contains(route, "after=1")
        && string.contains(route, "query_token=fixture-query")
      _ -> False
    }
  })
  |> should.be_true
  let #(cleared, commands) = s.update(m, s.CacheCleared(Ok(Nil)))
  cleared.data.seq |> should.equal(0)
  commands |> list.contains(s.CloseStream) |> should.be_true
  cleared.generation |> should.equal(m.generation + 1)
}

pub fn missing_deep_link_fetches_context_once_test() {
  let #(m, _) =
    s.init(
      "/dashboard?trace_id=older",
      "{\"source\":\"memory\"}",
      dict.new(),
      1440,
    )
  let #(next, commands) = s.update(m, s.Batch(0, fixture(), Ok(frame())))
  string.contains(next.route, "include_trace=older") |> should.be_true
  commands
  |> list.any(fn(c) {
    case c {
      s.Fetch(route, _) -> string.contains(route, "include_trace=older")
      _ -> False
    }
  })
  |> should.be_true
  let #(again, _) =
    s.update(next, s.Batch(next.generation, fixture(), Ok(frame())))
  again.generation |> should.equal(next.generation)
}

pub fn incremental_index_matches_full_projection_test() {
  let base = data()
  let f = frame()
  let assert Ok(child) = dict.get(base.spans, #("trace-1", "fail"))
  let changed =
    raw.Frame(
      ..f,
      kind: "delta",
      base: 1,
      seq: 2,
      upserts: [raw.Span(..child, name: "worker:updated")],
      added: [],
    )
  let assert Ok(next) = raw.apply(base, changed)
  let index = project.update_index(project.index(base), next, changed)
  project.page_index(next, "/dashboard?trace_id=trace-1", index)
  |> should.equal(project.page(next, "/dashboard?trace_id=trace-1"))
}

pub fn cached_unchanged_checkpoint_still_refreshes_clock_test() {
  let d = data()
  let f = frame()
  let ack =
    raw.Frame(
      ..f,
      kind: "delta",
      base: f.seq,
      now: f.now + 3_600_000,
      upserts: [],
      added: [],
    )
  let assert Ok(next) = raw.apply(d, ack)
  next.now |> should.equal(ack.now)
  next.spans |> should.equal(d.spans)
  next.seq |> should.equal(d.seq)
}
