import dashboard/protocol
import dashboard/query
import dashboard/spans
import dashboard/state as s
import dashboard/tooltip
import dashboard/view
import gleam/dict
import gleam/list
import gleam/option
import gleam/result
import gleam/string
import gleeunit
import gleeunit/should
import lustre/element

pub fn main() {
  gleeunit.main()
}

@external(javascript, "./fixture_ffi.mjs", "snapshot")
fn snapshot() -> String

@external(javascript, "./fixture_ffi.mjs", "spans")
fn raw() -> String

fn model() -> s.Model {
  let #(model, _) =
    s.init(
      "/dashboard?events=markers&sort=slowest",
      snapshot(),
      dict.new(),
      1440,
    )
  let assert Ok(frame) = spans.parse(raw())
  let #(model, _) = s.update(model, s.Batch(0, raw(), Ok(frame)))
  model
}

pub fn actual_go_snapshot_decodes_test() {
  let assert Ok(data) = protocol.parse(snapshot())
  data.page.summary.total_traces |> should.equal(2)
  data.page.summary.error_traces |> should.equal(1)
  data.page.selected_item |> should.not_equal(option.None)
}

pub fn event_navigation_and_marker_links_test() {
  let #(selected, _) = s.update(model(), s.SelectTab(s.Errors))
  let #(selected, _) =
    s.update(
      selected,
      s.Navigate(
        "/dashboard?events=markers&trace_id=trace-1&span_id=fail&event_ix=0",
      ),
    )
  selected.inspector_tab |> should.equal(s.Overview)
  selected.active_pane |> should.equal(s.Inspector)
  let html = view.render(selected) |> element.to_string
  html |> string.contains("span-detail-name\">queued</span>") |> should.be_true
  html
  |> string.contains("aria-label=\"Inspect event: queued\"")
  |> should.be_true
  html |> string.contains("event_ix=0") |> should.be_true
}

pub fn trace_view_controls_keep_event_selection_and_expose_state_test() {
  let #(selected, _) =
    s.update(
      model(),
      s.Navigate(
        "/dashboard?events=markers&trace_id=trace-1&span_id=fail&event_ix=0",
      ),
    )
  let assert s.Available(page) = selected.content
  query.get(page.filters.rows_mode_href, "event_ix") |> should.equal("0")
  let #(rows, _) = s.update(selected, s.Navigate(page.filters.rows_mode_href))
  let html = view.render(rows) |> element.to_string
  html |> string.contains("span-detail-name\">queued</span>") |> should.be_true
  html |> string.contains("aria-label=\"Event display\"") |> should.be_true
  html |> string.contains("aria-pressed=\"true\"") |> should.be_true
  html |> string.contains("id=\"trace-error-path\"") |> should.be_true
  html |> string.contains("trace-detail-menu") |> should.be_false
  let #(filtered, _) =
    s.update(rows, s.Navigate(query.set(rows.route, [#("error_path", "1")])))
  let html = view.render(filtered) |> element.to_string
  html |> string.contains("checked") |> should.be_true
  html |> string.contains("span-detail-name\">queued</span>") |> should.be_true
  let #(no_errors, _) =
    s.update(rows, s.Navigate("/dashboard?trace_id=trace-2"))
  let html = view.render(no_errors) |> element.to_string
  html |> string.contains("No recorded errors") |> should.be_true
  html |> string.contains("id=\"trace-error-path\" disabled") |> should.be_true
  let html =
    view.render(
      s.Model(
        ..no_errors,
        v3: True,
        detail_loading: True,
        detail_complete: False,
      ),
    )
    |> element.to_string
  html |> string.contains("Loading trace…") |> should.be_true
  html |> string.contains("No recorded errors") |> should.be_false
  // A shared URL may already enable Error path on a clean trace. Keep it
  // operable so the user can turn it off instead of trapping a checked toggle.
  let #(enabled, _) =
    s.update(
      no_errors,
      s.Navigate(query.set(no_errors.route, [#("error_path", "1")])),
    )
  let html = view.render(enabled) |> element.to_string
  html |> string.contains("id=\"trace-error-path\" disabled") |> should.be_false
}

pub fn overview_and_workspace_controls_are_local_test() {
  let original = model()
  let #(hidden, commands) = s.update(original, s.ToggleOverview)
  hidden.overview_open |> should.be_false
  commands |> should.equal([])
  hidden.data |> should.equal(original.data)
  let #(focused, commands) = s.update(hidden, s.FocusPane(s.Waterfall))
  focused.active_pane |> should.equal(s.Waterfall)
  focused.route |> should.equal(original.route)
  commands |> should.equal([])
  let #(opened, _) = s.update(focused, s.ToggleMenu(s.CacheMenu))
  opened.menu |> should.equal(s.CacheMenu)
  let #(closed, _) = s.update(opened, s.KeyPressed("Escape", False))
  closed.menu |> should.equal(s.NoMenu)
}

pub fn unsupported_schema_rejected_test() {
  snapshot()
  |> string.replace("\"schema_version\":1", "\"schema_version\":999")
  |> protocol.parse
  |> result.is_error
  |> should.be_true
}

pub fn stale_generation_and_revision_are_ignored_test() {
  let initial = model()
  let assert Ok(data) = spans.parse(raw())
  let #(changed, _) = s.update(initial, s.Navigate("/dashboard?q=other"))
  let #(ignored, _) = s.update(changed, s.Batch(0, raw(), Ok(data)))
  ignored |> should.equal(changed)
  let #(current, _) =
    s.update(changed, s.Batch(1, raw(), Ok(spans.Frame(..data, seq: 20))))
  let #(ignored, _) =
    s.update(current, s.Batch(1, raw(), Ok(spans.Frame(..data, seq: 19))))
  ignored |> should.equal(current)
}

pub fn pause_rejects_inflight_stream_and_stale_retry_test() {
  let initial = model()
  let #(paused, commands) = s.update(initial, s.ToggleLive)
  paused.connection |> should.equal(s.Paused)
  commands |> list.contains(s.CloseStream) |> should.be_true
  let assert Ok(data) = spans.parse(raw())
  let #(unchanged, _) = s.update(paused, s.Batch(0, raw(), Ok(data)))
  unchanged |> should.equal(paused)
  let #(unchanged, commands) = s.update(paused, s.Retry(0))
  unchanged |> should.equal(paused)
  commands |> should.equal([])
  let #(resumed, commands) = s.update(paused, s.ToggleLive)
  resumed.connection |> should.equal(s.Connecting)
  commands
  |> list.any(fn(command) {
    case command {
      s.OpenStream(_, _) -> True
      _ -> False
    }
  })
  |> should.be_true
}

pub fn updates_preserve_unsaved_form_tabs_and_layout_test() {
  let #(draft, _) = s.update(model(), s.QueryChanged("typing"))
  let #(draft, _) = s.update(draft, s.SelectTab(s.Errors))
  let #(draft, _) = s.update(draft, s.ResizeStarted(s.Inspector, 900))
  let #(draft, _) = s.update(draft, s.PointerMoved(820))
  let assert Ok(data) = spans.parse(raw())
  let #(updated, _) = s.update(draft, s.Batch(0, raw(), Ok(data)))
  updated.draft.query |> should.equal("typing")
  updated.inspector_tab |> should.equal(s.Errors)
  updated.inspector_width |> should.equal(draft.inspector_width)
}

pub fn filters_keep_display_modes_but_clear_selection_test() {
  let href =
    s.filter_route(
      "/dashboard?events=markers&sort=slowest&trace_id=old&page=2",
      s.Draft("x & y", "", "", True),
    )
  let params = dict.from_list(s.route_params(href))
  dict.get(params, "q") |> should.equal(Ok("x & y"))
  dict.get(params, "events") |> should.equal(Ok("markers"))
  dict.get(params, "sort") |> should.equal(Ok("slowest"))
  dict.has_key(params, "trace_id") |> should.be_false
  dict.has_key(params, "page") |> should.be_false
}

pub fn navigation_and_reconnect_are_effect_descriptions_test() {
  let #(changed, commands) =
    s.update(model(), s.LocationChanged("/dashboard?q=back"))
  commands
  |> list.contains(s.PushHistory("/dashboard?q=back"))
  |> should.be_false
  changed.route |> should.equal("/dashboard?q=back")
  let #(lost, commands) = s.update(changed, s.Disconnected(changed.generation))
  lost.connection |> should.equal(s.Reconnecting)
  commands
  |> list.contains(s.After(1000, s.Retry(lost.generation)))
  |> should.be_true
}

pub fn markup_escapes_trace_data_and_retains_dashboard_sections_test() {
  let html = model() |> view.render |> element.to_string
  for_each(
    [
      "trace-summary",
      "trace-dashboard",
      "trace-list",
      "trace-detail",
      "span-detail",
      "waterfall-fill",
      "ROOT_CAUSE",
      "&lt;script&gt;",
    ],
    fn(expected) { string.contains(html, expected) |> should.be_true },
  )
  string.contains(html, "<script>alert") |> should.be_false
  string.contains(html, "data-on:") |> should.be_false
}

fn for_each(items: List(a), action: fn(a) -> Nil) -> Nil {
  list.each(items, action)
}

pub fn responsive_clamping_does_not_destroy_saved_widths_test() {
  let prefs =
    dict.from_list([#("sidebarWidth", "360"), #("inspectorWidth", "480")])
  let #(initial, _) = s.init("/dashboard", snapshot(), prefs, 1440)
  let #(narrow, _) = s.update(initial, s.ViewportChanged(500))
  narrow.inspector_width |> should.equal(250)
  let #(wide, _) = s.update(narrow, s.ViewportChanged(1440))
  wide.inspector_width |> should.equal(480)
  wide.sidebar_width |> should.equal(360)
}

pub fn default_panel_widths_follow_viewport_test() {
  let #(initial, _) = s.init("/dashboard", snapshot(), dict.new(), 800)
  let #(wide, _) = s.update(initial, s.ViewportChanged(1440))
  wide.inspector_width |> should.equal(403)
  wide.sidebar_width |> should.equal(345)
}

pub fn keyboard_navigation_and_resize_commands_test() {
  let #(initial, _) = s.update(model(), s.FocusPane(s.TraceList))
  let #(next, commands) = s.update(initial, s.KeyPressed("j", False))
  string.contains(next.route, "trace-2") |> should.be_true
  commands |> list.contains(s.PushHistory(next.route)) |> should.be_true
  let #(resizing, commands) =
    s.update(initial, s.ResizeStarted(s.Inspector, 800))
  commands |> should.equal([s.WatchPointer])
  let #(resizing, _) = s.update(resizing, s.PointerMoved(790))
  let #(finished, commands) = s.update(resizing, s.PointerReleased)
  finished.drag |> should.equal(s.Idle)
  commands |> list.contains(s.UnwatchPointer) |> should.be_true
}

pub fn detail_paging_clears_only_the_span_selection_test() {
  let params =
    s.paging_href(
      "/dashboard?trace_id=t&span_id=s&event_ix=0&detail_page=2",
      s.Waterfall,
    )
    |> s.route_params
    |> dict.from_list
  dict.get(params, "trace_id") |> should.equal(Ok("t"))
  dict.get(params, "detail_page") |> should.equal(Ok("2"))
  dict.has_key(params, "span_id") |> should.be_false
}

pub fn hovering_another_span_previews_it_without_changing_selection_test() {
  let model = model()
  let assert s.Available(page) = model.content
  let assert option.Some(trace) = page.selected_trace
  let assert [row, ..] =
    list.filter(trace.rows, fn(row) { !row.selected && row.kind == "span" })
  let row =
    protocol.Row(
      ..row,
      duration_label: "691.916015625ms",
      start_label: "+11.183ms",
    )
  let preview = tooltip.span_preview(row)
  preview |> string.contains(row.name) |> should.be_true
  preview |> string.contains("Span: " <> row.span_id) |> should.be_true
  preview |> string.contains("Duration:") |> should.be_true
  preview |> string.contains("Trace offset:") |> should.be_true
  preview |> string.contains("Duration: 692 ms") |> should.be_true
  preview |> string.contains("Trace offset: +11.2 ms") |> should.be_true
  preview |> string.contains("691.916015625") |> should.be_false
  let #(pending, _) =
    s.update(model, s.Tooltip(tooltip.Hover(preview, 10, 20, False, False)))
  let #(hovered, commands) =
    s.update(pending, s.Tooltip(tooltip.Settle(pending.tooltip.token)))
  hovered.tooltip.visible |> should.be_true
  hovered.tooltip.value |> should.equal(preview)
  hovered.content |> should.equal(model.content)
  hovered.route |> should.equal(model.route)
  hovered.active_pane |> should.equal(model.active_pane)
  commands |> should.equal([])
  let html = view.render(hovered) |> element.to_string
  html |> string.contains("Span: " <> row.span_id) |> should.be_true
  html |> string.contains("tooltip-heading") |> should.be_true
}

pub fn event_preview_uses_event_details_test() {
  let #(model, _) =
    s.update(model(), s.Navigate("/dashboard?events=rows&trace_id=trace-1"))
  let assert s.Available(page) = model.content
  let assert option.Some(trace) = page.selected_trace
  let assert [row, ..] =
    list.filter(trace.rows, fn(row) { row.kind == "event" })
  let preview = tooltip.span_preview(row)
  preview |> string.contains(row.name) |> should.be_true
  preview |> string.contains("Event at: " <> row.time_label) |> should.be_true
  preview |> string.contains("Duration:") |> should.be_false
}
