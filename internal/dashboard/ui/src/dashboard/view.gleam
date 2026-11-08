import dashboard/duration_view
import dashboard/monitoring
import dashboard/protocol as p
import dashboard/query
import dashboard/state as s
import dashboard/tooltip
import gleam/dynamic/decode
import gleam/float
import gleam/int
import gleam/list
import gleam/option.{None, Some}
import gleam/result
import gleam/set
import gleam/string
import lustre/attribute as a
import lustre/element.{type Element, text}
import lustre/element/html as h
import lustre/event as e

pub fn render(model: s.Model) -> Element(s.Msg) {
  case model.content {
    s.Loading -> h.div([a.class("empty")], [text("Loading traces…")])
    s.Unavailable(message) ->
      h.div([a.class("empty"), a.role("alert")], [text(message)])
    s.Available(page) ->
      h.div([a.class("dashboard-page page")], [
        tooltip.render(model.tooltip) |> element.map(s.Tooltip),
        summary(model, page),
        h.div(
          [
            a.class("dashboard-overview-slot"),
            a.property("hidden", !model.v3 || !model.overview_open),
          ],
          [monitoring.render(model)],
        ),
        h.nav(
          [
            a.class("workspace-tabs"),
            a.attribute("aria-label", "Workspace panels"),
          ],
          list.map(
            [
              #(s.TraceList, "Traces"),
              #(s.Waterfall, "Waterfall"),
              #(s.Inspector, "Details"),
            ],
            fn(pane) {
              h.button(
                [
                  a.type_("button"),
                  a.class("panel-toggle"),
                  a.attribute(
                    "aria-pressed",
                    s.bool_string(model.active_pane == pane.0),
                  ),
                  e.on_click(s.FocusPane(pane.0)),
                ],
                [text(pane.1)],
              )
            },
          ),
        ),
        h.div(
          [
            a.id("trace-dashboard"),
            a.class(
              "dashboard"
              <> case model.active_pane {
                s.TraceList -> " active-traces"
                s.Waterfall -> " active-waterfall"
                s.Inspector -> " active-details"
              }
              <> when(!model.sidebar_open, " is-sidebar-collapsed")
              <> when(model.inspector_width == 0, " is-inspector-collapsed")
              <> when(model.drag != s.Idle, " is-resizing"),
            ),
            a.style([
              #("--sidebar-width", int.to_string(model.sidebar_width) <> "px"),
              #(
                "--inspector-width",
                int.to_string(model.inspector_width) <> "px",
              ),
            ]),
          ],
          [
            h.div(
              [
                a.id("trace-list-panel"),
                a.class("dashboard-sidebar panel panel-list"),
              ],
              [
                h.div([a.class("panel-hdr panel-hdr-shell panel-hdr-sidebar")], [
                  h.div([a.class("panel-title-group")], [
                    h.button(
                      [
                        a.type_("button"),
                        a.class("panel-toggle"),
                        a.attribute("aria-controls", "trace-list-panel"),
                        a.attribute(
                          "aria-expanded",
                          s.bool_string(model.sidebar_open),
                        ),
                        e.on_click(s.ToggleSidebar),
                      ],
                      [
                        text(case model.sidebar_open {
                          True -> "close"
                          False -> "open"
                        }),
                      ],
                    ),
                    span("panel-title-text", "trace list"),
                  ]),
                  menu(model, s.SortMenu, "sort", "", [
                    nav(
                      page.filters.sort_latest_href,
                      "panel-menu-item"
                        <> when(page.filters.sort_by == "latest", " active"),
                      [text("latest")],
                    ),
                    nav(
                      page.filters.sort_slowest_href,
                      "panel-menu-item"
                        <> when(page.filters.sort_by == "slowest", " active"),
                      [text("slowest")],
                    ),
                    nav(
                      page.filters.sort_errors_href,
                      "panel-menu-item"
                        <> when(page.filters.sort_by == "errors", " active"),
                      [text("errors")],
                    ),
                  ]),
                ]),
                trace_list(page),
                case model.v3 {
                  True -> monitoring.paging(model)
                  False -> pagination(page.trace_paging, s.TraceList)
                },
                show(
                  model.data.coverage.has_more && !model.data.coverage.truncated,
                  h.button(
                    [
                      a.type_("button"),
                      a.class("trace-filter-submit"),
                      e.on_click(s.LoadMore),
                    ],
                    [text("load more traces")],
                  ),
                ),
              ],
            ),
            resize_handle("sidebar", s.TraceList),
            h.div([a.class("dashboard-waterfall panel panel-waterfall")], [
              h.div([a.class("panel-hdr panel-hdr-shell")], [
                h.div([a.class("panel-title-group")], [
                  span("panel-title-text", "waterfall"),
                ]),
              ]),
              trace_detail(model, page),
              pagination(page.detail_paging, s.Waterfall),
            ]),
            resize_handle("inspector", s.Inspector),
            h.div(
              [a.id("trace-inspector-panel"), a.class("panel panel-inspector")],
              [
                h.div(
                  [a.class("panel-hdr panel-hdr-inspector panel-hdr-shell")],
                  [
                    h.div([a.class("inspector-title-group")], [
                      span("panel-title-text", "details"),
                      h.span(
                        [
                          a.id("trace-inspector-summary"),
                          a.class("inspector-summary"),
                        ],
                        [
                          text(case page.selected_item {
                            Some(_) -> "selected item"
                            None -> "no selection"
                          }),
                        ],
                      ),
                    ]),
                  ],
                ),
                h.div([a.class("panel-body panel-body-inspector")], [
                  inspector(model, page),
                ]),
              ],
            ),
          ],
        ),
      ])
  }
}

fn summary(model: s.Model, page: p.Page) -> Element(s.Msg) {
  h.section(
    [
      a.id("trace-summary"),
      a.class("status-bar" <> when(model.v3, " stable-status")),
    ],
    list.append(
      list.map(page.filters.nav_links, fn(link) {
        h.a(
          [
            a.href(link.href),
            a.class("nav-link" <> when(link.key == "dashboard", " nav-active")),
          ],
          [text(link.label)],
        )
      }),
      [
        span("status-sep", ""),
        span("status-metric", count(page.summary.total_traces, " traces")),
        span("status-metric", count(page.summary.total_spans, " spans")),
        span(
          case page.summary.error_traces > 0 {
            True -> "status-error status-error-count"
            False -> "status-metric status-error-count"
          },
          count(page.summary.error_traces, " traces with errors"),
        ),
        h.button(
          [
            a.type_("button"),
            a.class("trace-live-toggle"),
            a.attribute("data-live-pause-toggle", ""),
            a.attribute(
              "aria-pressed",
              s.bool_string(model.connection == s.Paused),
            ),
            a.title(case model.connection {
              s.Paused -> "Resume live dashboard updates"
              _ -> "Pause live dashboard updates"
            }),
            e.on_click(s.ToggleLive),
          ],
          [
            text(case model.connection {
              s.Paused -> "resume live"
              _ -> "pause live"
            }),
          ],
        ),
        show(
          model.v3,
          h.button(
            [
              a.type_("button"),
              a.class("overview-toggle panel-toggle"),
              a.attribute("aria-expanded", s.bool_string(model.overview_open)),
              e.on_click(s.ToggleOverview),
            ],
            [
              text(case model.overview_open {
                True -> "hide overview"
                False -> "show overview"
              }),
            ],
          ),
        ),
        show(
          model.v3,
          menu(
            model,
            s.CacheMenu,
            "cache",
            "search-cache-menu",
            monitoring.cache_controls(model),
          ),
        ),
        show(
          !model.v3 && model.notice != "",
          h.span([a.class("status-error"), a.role("alert")], [
            text(model.notice),
          ]),
        ),
        show(
          !model.v3 && model.connection == s.Reconnecting,
          h.span([a.class("status-label"), a.role("status")], [
            text("reconnecting…"),
          ]),
        ),
        show(!model.v3 && model.refreshing, span("status-label", "refreshing…")),
        show(
          model.data.coverage.source_limited,
          span(
            "status-label",
            "source limited"
              <> case model.data.coverage.source_limit > 0 {
              True ->
                " · "
                <> int.to_string(model.data.coverage.source_limit)
                <> " trace limit"
              False -> ""
            },
          ),
        ),
        show(
          model.data.coverage.truncated,
          span("status-error", "incomplete · response limit · narrow filters"),
        ),
        show(
          !model.v3 && model.cache_notice != "",
          span("status-label", model.cache_notice),
        ),
        h.form(
          [
            a.action("/dashboard"),
            a.method("get"),
            a.class("trace-filter-form"),
            e.on_submit(s.ApplyFilters),
          ],
          [
            h.input([
              a.class("trace-filter-input"),
              a.type_("search"),
              a.name("q"),
              a.value(model.draft.query),
              a.placeholder("filter traces..."),
              a.title("Search traces by name, attribute, or ID"),
              e.on_input(s.QueryChanged),
            ]),
            menu(model, s.FiltersMenu, "filters", "trace-filter-menu", [
              h.label([a.class("filter-field")], [
                span("filter-field-label", "time window"),
                h.select(
                  [
                    a.title("Time window"),
                    a.value(model.range_mode),
                    a.class("trace-filter-select"),
                    e.on_input(s.RangeMode),
                  ],
                  list.map(
                    [
                      #("300000", "last 5 minutes"),
                      #("900000", "last 15 minutes"),
                      #("3600000", "last hour"),
                      #("14400000", "last 4 hrs"),
                      #("43200000", "last 12 hrs"),
                      #("86400000", "last 24 hrs"),
                      #("custom", "custom range"),
                    ],
                    fn(option) {
                      h.option(
                        [
                          a.value(option.0),
                          a.selected(model.range_mode == option.0),
                        ],
                        option.1,
                      )
                    },
                  ),
                ),
              ]),
              show(
                model.range_mode == "custom",
                h.div([], [
                  h.input([
                    a.type_("datetime-local"),
                    a.title("Range start"),
                    a.value(model.range_from),
                    e.on_input(s.RangeFrom),
                  ]),
                  h.input([
                    a.type_("datetime-local"),
                    a.title("Range end"),
                    a.value(model.range_to),
                    e.on_input(s.RangeTo),
                  ]),
                ]),
              ),
              h.button(
                [
                  a.type_("button"),
                  a.class("trace-filter-submit"),
                  e.on_click(s.ApplyRange),
                ],
                [text("apply time range")],
              ),
              show(
                !list.is_empty(page.filters.services),
                h.label([a.class("filter-field")], [
                  span("filter-field-label", "service"),
                  h.select(
                    [
                      a.name("service"),
                      a.class("trace-filter-select"),
                      a.title("Filter by service"),
                      a.value(model.draft.service),
                      e.on_input(s.ServiceChanged),
                    ],
                    [
                      h.option(
                        [a.value(""), a.selected(model.draft.service == "")],
                        "all",
                      ),
                      ..list.map(page.filters.services, fn(service) {
                        h.option(
                          [
                            a.value(service),
                            a.selected(service == model.draft.service),
                          ],
                          service,
                        )
                      })
                    ],
                  ),
                ]),
              ),
              h.label([a.class("filter-field")], [
                span("filter-field-label", "min trace ms"),
                h.input([
                  a.class("trace-filter-dur"),
                  a.type_("number"),
                  a.name("min_dur"),
                  a.value(model.draft.min_duration),
                  a.placeholder("0"),
                  a.attribute("min", "0"),
                  a.title("Minimum duration in milliseconds"),
                  e.on_input(s.DurationChanged),
                ]),
              ]),
              show(
                !model.v3,
                h.label([a.class("filter-field filter-field-check")], [
                  h.input([
                    a.type_("checkbox"),
                    a.name("failed_only"),
                    a.value("1"),
                    a.checked(model.draft.failed_only),
                    e.on_check(s.FailedChanged),
                  ]),
                  span("filter-field-label", "errors only"),
                ]),
              ),
              h.div([], case model.v3 {
                True -> monitoring.filters(model)
                False -> []
              }),
              h.button(
                [
                  a.type_("button"),
                  a.class("trace-filter-submit"),
                  e.on_click(case model.v3 {
                    True -> s.ApplyFilters
                    False -> s.ClearCache
                  }),
                ],
                [
                  text(case model.v3 {
                    True -> "apply filters"
                    False -> "clear cached traces"
                  }),
                ],
              ),
            ]),
            h.button(
              [
                a.type_("submit"),
                a.class("trace-filter-submit"),
                a.title("Apply filter"),
              ],
              [text("apply")],
            ),
          ],
        ),
      ],
    ),
  )
}

fn trace_list(page: p.Page) -> Element(s.Msg) {
  let attrs = pane_attrs(s.TraceList)
  case page.traces {
    [] ->
      h.section(attrs, [
        h.div([a.class("empty")], [
          text(case has_filters(page.filters) {
            True -> "No traces match the current filters."
            False ->
              "No trace evidence yet. Connect a native trace database, Jaeger, or Google Cloud Trace when starting Phos."
          }),
        ]),
      ])
    traces ->
      element.keyed(
        h.section(attrs, _),
        list.map(traces, fn(trace) {
          #(
            trace.trace_id,
            h.a(
              [
                a.href(trace.href),
                a.attribute("data-trace-id", trace.trace_id),
                tooltip.hint(tooltip.trace_preview(trace)),
                a.class(
                  "trace-item trace-row"
                  <> when(trace.selected, " selected")
                  <> when(trace.failed, " has-errors"),
                ),
                e.on_mouse_enter(s.HoverTrace(trace.trace_id)),
                e.on_focus(s.HoverTrace(trace.trace_id)),
                link_click(trace.href),
              ],
              [
                h.div([a.class("trace-row-main")], [
                  h.div([a.class("trace-name")], [
                    span("status-icon", case trace.failed {
                      True -> "✕"
                      False -> "·"
                    }),
                    show(
                      trace.service_name != "",
                      span("badge badge-svc", trace.service_name),
                    ),
                    text(case trace.service_name {
                      "" -> trace.name
                      _ -> " " <> trace.operation_name
                    }),
                  ]),
                  span(
                    "trace-duration duration-" <> trace.duration_severity,
                    trace.duration_label,
                  ),
                ]),
                h.div([a.class("trace-row-meta")], [
                  span("trace-count", count(trace.span_count, " spans")),
                  show(
                    trace.error_count > 0,
                    span("trace-error-count", count(trace.error_count, " err")),
                  ),
                  h.span(
                    [
                      a.class("trace-time"),
                      tooltip.hint(trace.started_at_label),
                    ],
                    [text(trace.relative_time_label)],
                  ),
                ]),
              ],
            ),
          )
        }),
      )
  }
}

fn trace_detail(model: s.Model, page: p.Page) -> Element(s.Msg) {
  h.section(pane_attrs(s.Waterfall), case page.selected_trace {
    None -> [
      h.div([a.class("empty")], [
        text(case page.traces {
          [] -> "Trace details appear here after data arrives."
          _ -> "Select a trace from the list."
        }),
      ]),
    ]
    Some(trace) -> [
      h.div([a.class("trace-detail-header")], [
        h.div([a.class("trace-detail-title")], [
          span("trace-detail-name", trace.name),
          span("trace-detail-id", trace.short_trace_id),
        ]),
        h.div([a.class("trace-detail-stats")], [
          span("", trace.duration_label),
          h.span([], [
            text(
              count(trace.span_count, " spans · ")
              <> count(trace.event_count, " events"),
            ),
            show(
              trace.error_count > 0,
              h.span([], [
                text(" · "),
                span("trace-detail-error", count(trace.error_count, " errors")),
              ]),
            ),
          ]),
        ]),
        trace_view_controls(model, page.filters, trace),
      ]),
      root_cause(trace.root_cause_label),
      h.div(
        [a.class("waterfall-legend")],
        list.map(
          [
            #("ok", "ok"),
            #("err", "error"),
            #("marker", "event"),
            #("selected", "selected"),
          ],
          fn(pair) {
            h.span([a.class("legend-item")], [
              span("legend-swatch legend-" <> pair.0, ""),
              text(pair.1),
            ])
          },
        ),
      ),
      h.div([a.class("waterfall-axis")], [
        h.div([a.class("waterfall-axis-spacer")], []),
        h.div(
          [a.class("waterfall-axis-track")],
          list.map(trace.axis_ticks, fn(tick) {
            h.span(
              [
                a.class("waterfall-axis-tick"),
                a.style([#("left", tick.left_pct <> "%")]),
              ],
              [text(tick.label)],
            )
          }),
        ),
        h.div([a.class("waterfall-axis-end")], []),
      ]),
      element.keyed(
        h.div(
          [
            a.class(
              "waterfall-rows" <> when(trace.has_errors, " trace-has-errors"),
            ),
          ],
          _,
        ),
        list.map(trace.rows, fn(row) {
          #(
            row.kind <> ":" <> row.span_id <> ":" <> int.to_string(row.event_ix),
            waterfall_row(row, page.filters.events_mode),
          )
        }),
      ),
    ]
  })
}

fn trace_view_controls(
  model: s.Model,
  filters: p.Filters,
  trace: p.TraceDetail,
) -> Element(s.Msg) {
  let error_hint = case True {
    _ if trace.has_errors -> "Includes ancestors"
    _ if !model.v3 || model.detail_complete -> "No recorded errors"
    _ if model.detail_loading -> "Loading trace…"
    _ -> "Trace incomplete"
  }
  let error_href = case filters.error_path_only {
    True -> filters.full_trace_href
    False -> filters.error_path_href
  }
  h.div(
    [
      a.class("trace-detail-toolbar"),
      a.attribute("aria-label", "Trace display"),
    ],
    [
      h.div(
        [
          a.class("trace-events-toggle"),
          a.role("group"),
          a.attribute("aria-label", "Event display"),
        ],
        [
          span("trace-control-label", "Events"),
          h.div(
            [a.class("trace-events-segments")],
            list.map(
              [
                #(
                  "Rows",
                  filters.rows_mode_href,
                  filters.events_mode != "markers",
                ),
                #(
                  "Markers",
                  filters.markers_mode_href,
                  filters.events_mode == "markers",
                ),
              ],
              fn(choice) {
                h.button(
                  [
                    a.type_("button"),
                    a.class("trace-filter-chip" <> when(choice.2, " active")),
                    a.attribute("aria-pressed", case choice.2 {
                      True -> "true"
                      False -> "false"
                    }),
                    e.on_click(s.Navigate(choice.1)),
                  ],
                  [text(choice.0)],
                )
              },
            ),
          ),
        ],
      ),
      h.div([a.class("trace-error-control")], [
        h.label([a.class("trace-error-toggle"), a.for("trace-error-path")], [
          h.input([
            a.type_("checkbox"),
            a.id("trace-error-path"),
            a.disabled(!trace.has_errors && !filters.error_path_only),
            a.checked(filters.error_path_only),
            a.attribute("aria-describedby", "trace-error-hint"),
            e.on_check(fn(_) { s.Navigate(error_href) }),
          ]),
          text("Error path"),
        ]),
        h.span([a.id("trace-error-hint"), a.class("trace-control-hint")], [
          text(error_hint),
        ]),
      ]),
    ],
  )
}

fn waterfall_row(row: p.Row, events_mode: String) -> Element(s.Msg) {
  let is_event = row.kind == "event"
  let guides =
    h.span(
      [a.class("tree-guides")],
      list.map(row.tree_guides, fn(guide) {
        span(
          "tree-guide tree-guide-"
            <> guide.kind
            <> when(guide.failed, " tree-fail"),
          "",
        )
      }),
    )
  let main =
    h.a(
      [
        a.href(row.href),
        link_click(row.href),
        a.attribute("data-trace-row-kind", row.kind),
        tooltip.hint(tooltip.span_preview(row)),
        a.class(
          "span-row span-item"
          <> when(is_event, " event-row")
          <> when(row.selected, " selected")
          <> when(row.tree_failed, " tree-failed")
          <> when(row.collapsed, " collapsed")
          <> when(row.on_critical_path, " critical-path"),
        ),
      ],
      [
        case is_event {
          True ->
            h.div([a.class("span-item-main event-item-main")], [
              guides,
              h.div([a.class("event-item-line")], [
                span("span-name", row.name),
                span("badge badge-event", "event"),
                span("event-item-meta", row.short_span_id),
                span("event-item-meta", row.start_label),
                span("event-item-meta", count(row.attr_count, "a")),
              ]),
            ])
          False ->
            h.div([a.class("span-item-main")], [
              guides,
              h.div([a.class("span-item-body")], [
                h.div([a.class("span-item-title")], [
                  span(
                    "status-icon" <> when(row.failed, " status-err"),
                    case row.failed {
                      True -> "✕"
                      False -> "✓"
                    },
                  ),
                  span("span-name", row.name),
                  show(row.root, span("badge badge-root", "root")),
                  show(row.failed, span("badge badge-err", "err")),
                  show(
                    row.first_error,
                    span("badge badge-first-err", "first error"),
                  ),
                ]),
                h.div([a.class("span-item-meta")], [
                  span("", row.short_span_id),
                  span("", row.start_label),
                  show(
                    row.pct_of_trace != "",
                    span("pct-of-trace", row.pct_of_trace),
                  ),
                  h.span([], [
                    text(
                      count(row.attr_count, "a · ")
                      <> count(row.event_count, "ev"),
                    ),
                    show(
                      row.error_count > 0,
                      h.span([], [
                        text(" · "),
                        span(
                          "trace-detail-error",
                          count(row.error_count, "err"),
                        ),
                      ]),
                    ),
                  ]),
                ]),
              ]),
            ])
        },
        h.div([a.class("waterfall-bar")], case is_event {
          True -> [marker(row.left_pct, row.name, row.time_label)]
          False -> [
            h.div(
              [
                a.class(
                  "waterfall-fill "
                  <> case row.failed {
                    True -> "fail"
                    False -> "ok"
                  },
                ),
                a.style([
                  #("left", percent(row.left_pct)),
                  #("width", "max(" <> percent(row.width_pct) <> ", 3px)"),
                ]),
              ],
              [],
            ),
          ]
        }),
        span(
          "span-duration" <> when(!is_event && row.failed, " failed"),
          case is_event {
            True -> row.time_label
            False -> row.duration_label
          },
        ),
      ],
    )
  h.div([a.class("span-row-shell")], [
    main,
    show(
      !is_event && events_mode == "markers",
      h.div([a.class("waterfall-marker-layer")], [
        h.span([], []),
        h.div(
          [a.class("waterfall-bar")],
          list.index_map(row.event_markers, fn(m, ix) {
            let href = query.set(row.href, [#("event_ix", int.to_string(ix))])
            h.a(
              [
                a.href(href),
                link_click(href),
                a.class("waterfall-marker"),
                a.style([#("left", percent(m.left_pct))]),
                tooltip.hint(m.name <> " @ " <> m.time_label),
                a.attribute("aria-label", "Inspect event: " <> m.name),
              ],
              [],
            )
          }),
        ),
        h.span([], []),
      ]),
    ),
  ])
}

fn inspector(model: s.Model, page: p.Page) -> Element(s.Msg) {
  h.section(pane_attrs(s.Inspector), case page.selected_item {
    None -> [h.div([a.class("empty")], [text("Select a row.")])]
    Some(item) -> [
      h.div([a.class("span-detail-header")], [
        h.div([a.class("span-detail-headline")], [
          show(
            item.service_name != "",
            span("badge badge-svc", item.service_name),
          ),
          span("span-detail-name", case item.operation_name {
            "" -> item.name
            _ -> item.operation_name
          }),
          span("span-detail-duration", case item.kind {
            "event" -> item.time_label
            _ -> item.duration_label
          }),
        ]),
        h.div([a.class("span-detail-meta")], [
          span("", item.short_span_id),
          ..case item.kind {
            "event" -> [
              span("", item.parent_name),
              span("", item.time_label),
              span("badge badge-event", "event"),
            ]
            _ -> [
              span("", item.started_at_label),
              show(item.root, span("badge badge-root", "root")),
              show(item.failed, span("badge badge-err", "fail")),
              show(
                item.first_error,
                span("badge badge-first-err", "first error"),
              ),
            ]
          }
        ]),
      ]),
      h.div(
        [
          a.class("span-detail-tabs"),
          a.role("tablist"),
          a.attribute("aria-label", "Selected item detail tabs"),
        ],
        list.map(
          [
            #(s.Overview, "Overview", 0),
            #(s.Errors, "Error", item.error_count),
            #(s.Attributes, "Attributes", 0),
            #(s.Events, "Events", item.event_count),
          ],
          fn(tab) {
            h.button(
              [
                a.type_("button"),
                a.class(
                  "span-detail-tab"
                  <> when(model.inspector_tab == tab.0, " is-active"),
                ),
                a.attribute("data-inspector-tab", s.tab_name(tab.0)),
                a.role("tab"),
                a.attribute(
                  "aria-selected",
                  s.bool_string(model.inspector_tab == tab.0),
                ),
                a.attribute("aria-controls", "inspector-" <> s.tab_name(tab.0)),
                e.on_click(s.SelectTab(tab.0)),
              ],
              [
                text(tab.1),
                show(
                  tab.2 > 0,
                  h.span([], [
                    text(" "),
                    span("section-count", int.to_string(tab.2)),
                  ]),
                ),
              ],
            )
          },
        ),
      ),
      inspector_panel(model, s.Overview, overview(item)),
      inspector_panel(model, s.Errors, [
        show(
          item.kind == "span" && item.failed,
          root_cause(item.root_cause_summary),
        ),
        section("Errors", item.error_count, item_cards(item.errors, True)),
      ]),
      inspector_panel(model, s.Attributes, [
        section("Attributes", 0, case item.attr_groups {
          [] -> [span("section-empty", "none")]
          groups ->
            list.map(groups, fn(group) {
              let open = !set.contains(model.closed_groups, group.name)
              h.details([a.class("attr-group"), a.property("open", open)], [
                h.summary(
                  [
                    a.class("attr-group-name"),
                    prevent_click(s.ToggleGroup(group.name)),
                  ],
                  [
                    text(group.name <> " "),
                    span(
                      "section-count",
                      int.to_string(list.length(group.attrs)),
                    ),
                  ],
                ),
                attr_table(group.attrs, False),
              ])
            })
        }),
      ]),
      inspector_panel(model, s.Events, [
        section("Events", item.event_count, item_cards(item.events, False)),
      ]),
    ]
  })
}

fn overview(item: p.ItemDetail) -> List(Element(s.Msg)) {
  let fields = case item.kind {
    "event" -> [
      #("Timestamp", item.time_label),
      #("Span", item.short_span_id),
      #("Parent span", item.parent_name),
    ]
    _ -> [
      #("Duration", item.duration_label),
      #("Self time", item.self_duration),
      #("% of trace", item.pct_of_trace),
      #("Children", int.to_string(item.child_count)),
      #("Errors", case item.error_count {
        0 -> ""
        _ -> int.to_string(item.error_count)
      }),
      #("Events", int.to_string(item.event_count)),
      #("Started", item.started_at_label),
      #("Ended", item.ended_at_label),
    ]
  }
  [
    section("Summary", 0, [
      h.div(
        [a.class("summary-grid")],
        fields
          |> list.filter(fn(pair) { pair.1 != "" })
          |> list.map(fn(pair) {
            h.div([a.class("summary-item")], [
              span("summary-label", pair.0),
              span(
                "summary-value" <> when(pair.0 == "Errors", " summary-err"),
                pair.1,
              ),
            ])
          }),
      ),
    ]),
    show(
      item.kind == "span" && list.length(item.ancestry) > 1,
      section("Ancestry", 0, [
        h.div(
          [a.class("ancestry-path")],
          list.index_map(item.ancestry, fn(node, index) {
            element.fragment([
              show(index > 0, span("ancestry-sep", "→")),
              span(
                "ancestry-node" <> when(node.failed, " ancestry-failed"),
                node.name,
              ),
            ])
          }),
        ),
      ]),
    ),
    section("Overview", 0, [attr_table(item.overview, False)]),
  ]
}

fn item_cards(items: List(p.Item), errors: Bool) -> List(Element(s.Msg)) {
  case items {
    [] -> [span("section-empty", "none")]
    _ ->
      list.map(items, fn(item) {
        h.div([a.class("item-card")], [
          h.div([a.class("item-head")], [
            span(
              case errors {
                True -> "badge badge-err"
                False -> "badge badge-event"
              },
              item.kind,
            ),
            span("item-time", timestamp(item.time)),
          ]),
          show(
            item.error != "",
            h.div([a.class("item-error")], [text(item.error)]),
          ),
          show(
            item.name != "",
            h.div([a.class("item-name")], [text(item.name)]),
          ),
          show(!list.is_empty(item.attrs), attr_table(item.attrs, True)),
        ])
      })
  }
}

fn attr_table(attrs: List(p.Attr), compact: Bool) -> Element(s.Msg) {
  case attrs {
    [] -> span("section-empty", "none")
    _ ->
      h.div(
        [
          a.class(case compact {
            True -> "item-attrs"
            False -> "attr-table"
          }),
        ],
        list.map(attrs, fn(attr) {
          h.div([a.class("attr-row" <> when(compact, " attr-row-compact"))], [
            span("attr-key", attr.key),
            span(
              case compact {
                True -> "attr-muted"
                False -> "attr-val"
              },
              attr.value,
            ),
          ])
        }),
      )
  }
}

fn inspector_panel(
  model: s.Model,
  tab: s.InspectorTab,
  children: List(Element(s.Msg)),
) -> Element(s.Msg) {
  h.div(
    [
      a.class("inspector-tab-panel"),
      a.id("inspector-" <> s.tab_name(tab)),
      a.attribute("data-inspector-tab-panel", s.tab_name(tab)),
      a.role("tabpanel"),
      a.property("hidden", model.inspector_tab != tab),
    ],
    children,
  )
}

fn section(
  title: String,
  count_: Int,
  children: List(Element(s.Msg)),
) -> Element(s.Msg) {
  h.div([a.class("span-detail-section")], [
    h.h3([a.class("section-title")], [
      text(title),
      show(
        count_ > 0,
        h.span([], [text(" "), span("section-count", int.to_string(count_))]),
      ),
    ]),
    ..children
  ])
}

fn menu(
  model: s.Model,
  which: s.Menu,
  title: String,
  extra: String,
  children: List(Element(s.Msg)),
) -> Element(s.Msg) {
  h.details(
    [a.class("panel-menu " <> extra), a.property("open", model.menu == which)],
    [
      h.summary(
        [
          a.class(
            "panel-menu-trigger"
            <> when(
              which == s.FiltersMenu
                && {
                model.draft.service != ""
                || model.draft.min_duration != ""
                || model.draft.failed_only
              },
              " is-filtered",
            ),
          ),
          prevent_click(s.ToggleMenu(which)),
        ],
        [text(title)],
      ),
      h.div(
        [
          a.class(
            "panel-menu-popover"
            <> when(which == s.FiltersMenu, " trace-filter-popover"),
          ),
        ],
        children,
      ),
    ],
  )
}

fn pagination(paging: p.Pagination, pane: s.Pane) -> Element(s.Msg) {
  show(
    paging.total_pages > 1,
    h.nav(
      [
        a.class("dashboard-pagination"),
        a.attribute("aria-label", case pane {
          s.TraceList -> "Trace pages"
          _ -> "Waterfall pages"
        }),
      ],
      [
        show(
          paging.has_prev,
          nav(s.paging_href(paging.prev_href, pane), "panel-menu-item", [
            text("previous"),
          ]),
        ),
        span(
          "",
          int.to_string(paging.current_page)
            <> " / "
            <> int.to_string(paging.total_pages),
        ),
        show(
          paging.has_next,
          nav(s.paging_href(paging.next_href, pane), "panel-menu-item", [
            text("next"),
          ]),
        ),
      ],
    ),
  )
}

fn resize_handle(name: String, pane: s.Pane) -> Element(s.Msg) {
  h.div(
    [
      a.class(name <> "-resize-handle"),
      a.attribute("data-" <> name <> "-resize-handle", ""),
      a.attribute("aria-hidden", "true"),
      e.on("mousedown", fn(raw) {
        let _ = e.prevent_default(raw)
        decode.run(raw, {
          use x <- decode.field("clientX", decode.int)
          decode.success(s.ResizeStarted(pane, x))
        })
        |> result.map_error(fn(_) { [] })
      }),
      e.on("dblclick", fn(raw) {
        let _ = e.prevent_default(raw)
        Ok(s.ResetWidth(pane))
      }),
    ],
    [],
  )
}

fn pane_attrs(pane: s.Pane) -> List(a.Attribute(s.Msg)) {
  [
    a.id(s.pane_id(pane)),
    a.attribute("tabindex", "-1"),
    e.on("focusin", fn(_) { Ok(s.FocusPane(pane)) }),
    e.on("mousedown", fn(_) { Ok(s.FocusPane(pane)) }),
  ]
}

fn nav(
  href: String,
  class: String,
  children: List(Element(s.Msg)),
) -> Element(s.Msg) {
  h.a([a.href(href), a.class(class), link_click(href)], children)
}

fn link_click(href: String) -> a.Attribute(s.Msg) {
  e.on("click", fn(raw) {
    let decoder = {
      use button <- decode.field("button", decode.int)
      use ctrl <- decode.field("ctrlKey", decode.bool)
      use meta <- decode.field("metaKey", decode.bool)
      use alt <- decode.field("altKey", decode.bool)
      use shift <- decode.field("shiftKey", decode.bool)
      decode.success(button == 0 && !ctrl && !meta && !alt && !shift)
    }
    case decode.run(raw, decoder) {
      Ok(True) -> {
        let _ = e.prevent_default(raw)
        Ok(s.Navigate(href))
      }
      _ -> Error([])
    }
  })
}

fn prevent_click(msg: s.Msg) -> a.Attribute(s.Msg) {
  e.on("click", fn(raw) {
    let _ = e.prevent_default(raw)
    Ok(msg)
  })
}

fn marker(left: Float, name: String, time: String) -> Element(s.Msg) {
  h.span(
    [
      a.class("waterfall-marker"),
      a.style([#("left", percent(left))]),
      tooltip.hint(name <> " @ " <> time),
    ],
    [],
  )
}

fn root_cause(label: String) -> Element(s.Msg) {
  show(
    label != "",
    h.div([a.class("root-cause-banner")], [
      span("root-cause-label", "Root cause"),
      span("root-cause-text", label),
    ]),
  )
}

fn span(class: String, value: String) -> Element(s.Msg) {
  duration_view.label(class, value)
}

fn show(visible: Bool, content: Element(s.Msg)) -> Element(s.Msg) {
  case visible {
    True -> content
    False -> element.none()
  }
}

fn when(condition: Bool, value: String) -> String {
  case condition {
    True -> value
    False -> ""
  }
}

fn count(value: Int, suffix: String) -> String {
  int.to_string(value) <> suffix
}

fn percent(value: Float) -> String {
  float.to_string(value) <> "%"
}

fn has_filters(f: p.Filters) -> Bool {
  f.query != ""
  || f.failed_only
  || f.error_path_only
  || f.service_filter != ""
  || f.min_duration_ms > 0
  || f.sort_by != "latest"
}

fn timestamp(value: String) -> String {
  case string.starts_with(value, "0001-") {
    True -> "n/a"
    False -> string.slice(value, 11, 12)
  }
}
