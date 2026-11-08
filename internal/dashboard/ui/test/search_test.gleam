import dashboard/heatmap
import dashboard/query
import dashboard/search
import dashboard/search_cache as cache
import dashboard/spans
import dashboard/state as s
import dashboard/tooltip
import gleam/dict
import gleam/list
import gleam/option.{None, Some}
import gleam/result
import gleam/string
import gleeunit/should
import lustre/element

fn initial() -> s.Model {
  let #(model, _) =
    s.init(
      "/dashboard",
      "{\"schema_version\":3,\"source\":\"s\"}",
      dict.new(),
      1440,
    )
  model
}

fn frame() -> search.Frame {
  search.Frame(
    ..search.empty().frame,
    source: "s",
    epoch: "e",
    query: "q",
    seq: 1,
    now: 2_000_000,
    summaries: [
      search.Trace(
        "t",
        "svc:op",
        "svc",
        "op",
        1.0,
        10.0,
        9.0,
        2,
        0,
        1,
        "HTTP 200",
        False,
      ),
    ],
    order: ["t"],
    coverage: search.Coverage(
      1_100_000,
      2_000_000,
      True,
      True,
      False,
      1,
      "",
      False,
    ),
  )
}

pub fn summary_codec_and_sequence_recovery_test() {
  let assert Ok(data) = search.apply(search.empty(), frame())
  search.parse(search.encode(data)) |> should.equal(Ok(frame()))
  search.apply(data, search.Frame(..frame(), kind: "delta", base: 3, seq: 4))
  |> result.is_error
  |> should.be_true
  search.apply(
    data,
    search.Frame(..frame(), kind: "delta", epoch: "other", base: 1, seq: 2),
  )
  |> result.is_error
  |> should.be_true
  let assert Ok(unchanged) =
    search.apply(data, search.Frame(..frame(), seq: 0, summaries: []))
  unchanged |> should.equal(data)
}

pub fn bucket_deltas_merge_without_erasing_unchanged_intervals_test() {
  let base = frame()
  let base =
    search.Frame(
      ..base,
      aggregates: search.Aggregates(..base.aggregates, buckets: [
        search.Bucket(1_100_000, 1_200_000, 1, 0, [], 0, None),
        search.Bucket(1_200_000, 1_300_000, 2, 0, [], 0, None),
      ]),
    )
  let assert Ok(data) = search.apply(search.empty(), base)
  let delta =
    search.Frame(
      ..base,
      kind: "delta",
      base: 1,
      seq: 2,
      summaries: [],
      bucket_delta: True,
      aggregates: search.Aggregates(..base.aggregates, buckets: [
        search.Bucket(1_200_000, 1_300_000, 3, 1, [], 0, None),
      ]),
    )
  let assert Ok(next) = search.apply(data, delta)
  next.frame.aggregates.buckets
  |> should.equal([
    search.Bucket(1_100_000, 1_200_000, 1, 0, [], 0, None),
    search.Bucket(1_200_000, 1_300_000, 3, 1, [], 0, None),
  ])
  let assert Ok(cached) = search.parse(search.encode(next))
  cached.bucket_delta |> should.be_false
  cached.aggregates.buckets |> should.equal(next.frame.aggregates.buckets)
}

pub fn rolling_windows_reject_cross_grid_deltas_and_never_accumulate_test() {
  let base = frame()
  let base =
    search.Frame(
      ..base,
      aggregates: search.Aggregates(
        ..base.aggregates,
        buckets: list.map(list.range(0, 59), fn(i) {
          search.Bucket(
            1_100_000 + i * 15_000,
            1_100_000 + { i + 1 } * 15_000,
            1,
            0,
            [],
            0,
            None,
          )
        }),
      ),
    )
  let assert Ok(initial) = search.apply(search.empty(), base)
  let final =
    list.fold(list.range(1, 180), initial, fn(data, tick) {
      let next =
        search.Frame(
          ..data.frame,
          kind: "delta",
          base: data.frame.seq,
          seq: data.frame.seq + 1,
          bucket_delta: False,
          coverage: search.Coverage(
            ..data.frame.coverage,
            from: 1_100_000 + tick * 5000,
            to: 2_000_000 + tick * 5000,
          ),
          aggregates: search.Aggregates(
            ..data.frame.aggregates,
            buckets: list.map(base.aggregates.buckets, fn(b) {
              search.Bucket(
                ..b,
                from: b.from + tick * 5000,
                to: b.to + tick * 5000,
              )
            }),
          ),
        )
      search.apply(data, search.Frame(..next, bucket_delta: True))
      |> result.is_error
      |> should.be_true
      let assert Ok(next) = search.apply(data, next)
      list.length(next.frame.aggregates.buckets) |> should.equal(60)
      next
    })
  list.length(final.frame.aggregates.buckets) |> should.equal(60)
}

pub fn accumulated_bucket_cache_is_discarded_test() {
  let bad =
    search.Data(
      ..search.empty(),
      frame: search.Frame(
        ..frame(),
        aggregates: search.Aggregates(
          ..frame().aggregates,
          buckets: list.map(list.range(0, 100), fn(i) {
            search.Bucket(i, i + 1, 0, 0, [], 0, None)
          }),
        ),
      ),
    )
  search.parse(search.encode(bad)) |> result.is_error |> should.be_true
}

pub fn search_pause_stale_callbacks_and_refresh_detail_test() {
  let #(model, _) = s.update(initial(), s.SearchBatch(0, Ok(frame())))
  model.detail_id |> should.equal("t")
  let #(paused, commands) = s.update(model, s.ToggleLive)
  paused.detail_loading |> should.be_false
  commands |> list.contains(s.CloseStream) |> should.be_true
  let #(unchanged, _) = s.update(paused, s.SearchBatch(0, Ok(frame())))
  unchanged |> should.equal(paused)
  let #(refreshed, commands) = s.update(paused, s.RefreshSearch)
  commands
  |> list.contains(s.FetchTrace("t", "", refreshed.generation))
  |> should.be_true
}

pub fn pin_policy_never_evicts_pins_and_counts_summaries_test() {
  let pin = cache.Entry("p", "p", "trace", 1, cache.capacity, 1, True)
  cache.victims([pin], 1, cache.capacity, "new")
  |> result.is_error
  |> should.be_true
  let idle = cache.Entry("old", "old", "query", 0, 100, 1, False)
  cache.victims([idle], 1, cache.capacity, "new") |> should.equal(Ok([]))
  cache.victims([], cache.capacity + 1, cache.capacity, "new")
  |> result.is_error
  |> should.be_true
}

pub fn saved_searches_survive_clear_and_utc_preference_applies_test() {
  let #(model, _) =
    s.init(
      "/dashboard?from=1790290800000&to=1790294400000",
      "{\"schema_version\":3}",
      dict.from_list([#("timezoneUTC", "true")]),
      1440,
    )
  model.utc |> should.be_true
  let #(named, _) = s.update(model, s.SavedName("incident"))
  let #(saved, _) = s.update(named, s.SaveSearch)
  let #(cleared, _) = s.update(saved, s.ClearCache)
  cleared.saved |> should.equal(saved.saved)
}

@external(javascript, "./fixture_ffi.mjs", "spans")
fn raw_spans() -> String

pub fn trace_display_changes_preserve_local_state_and_chunk_transfer_test() {
  let #(model, _) = s.update(initial(), s.SearchBatch(0, Ok(frame())))
  let assert Ok(raw) = spans.parse(raw_spans())
  let assert [item, ..] = raw.upserts
  let item = spans.Span(..item, trace_id: "t")
  let assert [summary] = frame().summaries
  let part =
    search.Detail("s", "e", "t", "a", summary, [item], "next", False, "")
  let #(loading, _) = s.update(model, s.TraceDetail(0, Ok(part)))
  let loading =
    s.Model(
      ..loading,
      route: query.set(loading.route, [
        #("span_id", item.id),
        #("event_ix", "0"),
      ]),
      active_pane: s.Waterfall,
      inspector_tab: s.Attributes,
      menu: s.FiltersMenu,
      range_mode: "custom",
      range_from: "2026-09-24T12:00",
      range_to: "2026-09-24T13:00",
      draft: s.Draft("unfinished", "svc", "12", False),
      extra: dict.from_list([#("operation", "unfinished")]),
    )
  let changed =
    list.fold(
      [#("events", "markers"), #("events", "rows"), #("error_path", "1")],
      loading,
      fn(before, setting) {
        let route = query.set(before.route, [setting])
        let #(after, commands) = s.update(before, s.Navigate(route))
        commands |> should.equal([s.PushHistory(route)])
        s.Model(..after, route: before.route, content: before.content)
        |> should.equal(before)
        after
      },
    )
  let #(back, commands) = s.update(changed, s.LocationChanged(loading.route))
  commands |> should.equal([])
  back.detail_parts |> should.equal(loading.detail_parts)
  back.generation |> should.equal(loading.generation)
  let #(completed, _) =
    s.update(
      back,
      s.TraceDetail(0, Ok(search.Detail(..part, cursor: "", complete: True))),
    )
  completed.detail_complete |> should.be_true
  // Query changes must still invalidate old callbacks.
  let #(searched, commands) =
    s.update(changed, s.Navigate(query.set(changed.route, [#("q", "new")])))
  commands |> list.contains(s.CloseStream) |> should.be_true
  searched.generation |> should.not_equal(changed.generation)
}

pub fn chunked_trace_never_exports_an_interrupted_revision_test() {
  let #(model, _) = s.update(initial(), s.SearchBatch(0, Ok(frame())))
  let assert Ok(raw) = spans.parse(raw_spans())
  let assert [item, ..] = raw.upserts
  let item = spans.Span(..item, trace_id: "t")
  let assert [summary] = frame().summaries
  let part =
    search.Detail("s", "e", "t", "a", summary, [item], "next", False, "")
  let #(loading, commands) = s.update(model, s.TraceDetail(0, Ok(part)))
  loading.detail_complete |> should.be_false
  commands |> list.contains(s.FetchTrace("t", "next", 0)) |> should.be_true
  let #(_, commands) = s.update(loading, s.ExportTrace)
  commands |> should.equal([])
  let #(restarted, commands) =
    s.update(
      loading,
      s.TraceDetail(
        0,
        Ok(search.Detail(..part, revision: "b", cursor: "", complete: True)),
      ),
    )
  restarted.detail_parts |> should.equal([])
  commands |> list.contains(s.FetchTrace("t", "", 0)) |> should.be_true
  let #(complete, _) =
    s.update(
      restarted,
      s.TraceDetail(
        0,
        Ok(search.Detail(..part, revision: "b", cursor: "", complete: True)),
      ),
    )
  complete.detail_complete |> should.be_true
  let delta =
    search.Change(
      "s",
      "e",
      "t",
      "b",
      "c",
      summary,
      [spans.Span(..item, name: "updated")],
      [],
    )
  let #(updated, _) = s.update(complete, s.TraceDelta(0, delta))
  updated.detail_revision |> should.equal("c")
  dict.get(updated.data.spans, #("t", item.id))
  |> result.map(fn(p) { p.name })
  |> should.equal(Ok("updated"))
  let #(same, _) = s.update(updated, s.TraceDelta(0, delta))
  same |> should.equal(updated)
  let #(recovering, commands) =
    s.update(
      updated,
      s.TraceDelta(0, search.Change(..delta, base: "wrong", revision: "d")),
    )
  recovering.detail_loading |> should.be_true
  commands |> list.contains(s.FetchTrace("t", "", 0)) |> should.be_true
  let #(_, commands) = s.update(complete, s.ExportTrace)
  list.length(commands) |> should.equal(1)
}

pub fn duration_buckets_survive_cache_and_duration_only_deltas_test() {
  let base = frame()
  let first = search.Bucket(1_100_000, 1_200_000, 3, 0, [2, 1], 0, None)
  let second = search.Bucket(1_200_000, 1_300_000, 2, 0, [0, 1], 1, None)
  let base =
    search.Frame(
      ..base,
      aggregates: search.Aggregates(
        ..base.aggregates,
        buckets: [first, second],
        duration_bounds: [1.0, 0.0],
        heatmap_max: 2,
        unknown_duration: 1,
        services: search.Facet([search.FacetValue("svc", 5)], 0),
        operations: search.Facet([search.FacetValue("op", 4)], 1),
        bucket_version: 1,
        bucket_width: 100_000,
      ),
    )
  let assert Ok(data) = search.apply(search.empty(), base)
  search.parse(search.encode(data)) |> should.equal(Ok(base))
  let changed = search.Bucket(..first, durations: [1, 2])
  let delta =
    search.Frame(
      ..base,
      kind: "delta",
      base: 1,
      seq: 2,
      bucket_delta: True,
      aggregates: search.Aggregates(..base.aggregates, buckets: [changed]),
    )
  let assert Ok(next) = search.apply(data, delta)
  next.frame.aggregates.buckets |> should.equal([changed, second])
  let assert Ok(cached) = search.parse(search.encode(next))
  cached.aggregates |> should.equal(next.frame.aggregates)
}

pub fn heatmap_labels_preserve_subminute_intervals_and_duration_units_test() {
  let model = initial()
  let base = frame()
  let base =
    search.Frame(
      ..base,
      coverage: search.Coverage(..base.coverage, from: 1_100_123, to: 1_100_456),
      aggregates: search.Aggregates(
        ..base.aggregates,
        duration_bounds: [0.1, 1000.0, 0.0],
        buckets: [search.Bucket(1_100_123, 1_100_456, 3, 0, [1, 1, 1], 0, None)],
        heatmap_max: 1,
      ),
    )
  let assert Ok(data) = search.apply(search.empty(), base)
  let html =
    heatmap.render(s.Model(..model, search: data, utc: True))
    |> element.to_string
  string.contains(html, "1970-01-01T00:18:20.123 to 1970-01-01T00:18:20.456")
  |> should.be_true
  string.contains(html, "100µs") |> should.be_true
  string.contains(html, "≤ 1s") |> should.be_true
}

pub fn column_stats_survive_cache_and_hover_without_changing_query_test() {
  let stats =
    search.BucketStats(
      search.Facet([search.FacetValue("checkout", 3)], 0),
      search.Facet([search.FacetValue("POST", 2)], 1),
      2,
      4.0,
      8.0,
      9.0,
    )
  let bucket = search.Bucket(1_100_123, 1_100_456, 3, 1, [1, 1], 1, Some(stats))
  let base = frame()
  let base =
    search.Frame(
      ..base,
      aggregates: search.Aggregates(
        ..base.aggregates,
        bucket_width: 500,
        buckets: [bucket],
      ),
    )
  let assert Ok(data) = search.apply(search.empty(), base)
  search.parse(search.encode(data)) |> should.equal(Ok(base))
  let model = s.Model(..initial(), search: data, utc: True)
  let key = search.bucket_key(bucket)
  let #(pending, commands) =
    s.update(model, s.ColumnHover(tooltip.Hover(key, 10, 20, False, False)))
  commands
  |> should.equal([
    s.After(100, s.ColumnHover(tooltip.Settle(pending.column_hover.token))),
  ])
  pending.preview_column |> should.equal("")
  let #(exited, _) = s.update(pending, s.ColumnHoverEnded)
  let #(late, _) =
    s.update(exited, s.ColumnHover(tooltip.Settle(pending.column_hover.token)))
  late.preview_column |> should.equal("")
  let #(moved, _) =
    s.update(pending, s.ColumnHover(tooltip.Hover(key, 11, 20, False, False)))
  let #(stale, _) =
    s.update(moved, s.ColumnHover(tooltip.Settle(pending.column_hover.token)))
  stale.preview_column |> should.equal("")
  let #(settled, commands) =
    s.update(stale, s.ColumnHover(tooltip.Settle(moved.column_hover.token)))
  settled.preview_column |> should.equal(key)
  let #(reading, _) = s.update(settled, s.ColumnHoverEnded)
  reading.preview_column |> should.equal(key)
  settled.route |> should.equal(model.route)
  commands |> should.equal([])
  let html = heatmap.render(settled) |> element.to_string
  [
    "333 ms",
    "Clipped edge column",
    "checkout",
    "POST",
    "p50",
    "p95",
    "p99",
    "2 timed spans",
    "1 unknown duration",
    "Other / unnamed · 1",
  ]
  |> list.each(fn(value) { string.contains(html, value) |> should.be_true })
  let #(left, _) = s.update(settled, s.ColumnHover(tooltip.Leave))
  left.preview_column |> should.equal("")
  let #(focused, commands) =
    s.update(model, s.ColumnHover(tooltip.Hover(key, 0, 0, False, True)))
  focused.preview_column |> should.equal(key)
  commands |> should.equal([])
  let #(navigated, _) =
    s.update(focused, s.Navigate("/dashboard?service=other"))
  navigated.preview_column |> should.equal("")
}

pub fn empty_column_has_no_latency_instead_of_zero_test() {
  let bucket =
    search.Bucket(
      1,
      1000,
      0,
      0,
      [],
      0,
      Some(search.BucketStats(
        search.Facet([], 0),
        search.Facet([], 0),
        0,
        0.0,
        0.0,
        0.0,
      )),
    )
  let model = initial()
  let frame = model.search.frame
  let frame =
    search.Frame(
      ..frame,
      aggregates: search.Aggregates(..frame.aggregates, buckets: [bucket]),
    )
  let model =
    s.Model(
      ..model,
      search: search.Data(..model.search, frame: frame),
      preview_column: search.bucket_key(bucket),
    )
  let html = heatmap.render(model) |> element.to_string
  string.contains(html, "No matching spans") |> should.be_true
  string.contains(html, "<strong>—</strong>") |> should.be_true
}
