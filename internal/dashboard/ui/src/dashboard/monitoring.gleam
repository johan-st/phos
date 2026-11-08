import dashboard/duration_view
import dashboard/format as f
import dashboard/heatmap
import dashboard/query
import dashboard/search_cache
import dashboard/state as s
import gleam/dict
import gleam/int
import gleam/list
import gleam/option
import gleam/result
import lustre/attribute as a
import lustre/element.{type Element, text}
import lustre/element/html as h
import lustre/event as e

pub fn render(model: s.Model) -> Element(s.Msg) {
  let frame = model.search.frame
  let totals = frame.aggregates
  let selected_summary = case model.detail_summary {
    option.Some(summary) if summary.id == model.detail_id -> Ok(summary)
    _ -> dict.get(model.search.traces, model.detail_id)
  }
  let root_status = case selected_summary {
    Ok(trace) ->
      trace.root_status
      <> " · "
      <> int.to_string(trace.errors)
      <> " error spans"
      <> case trace.incomplete {
        True -> " · incomplete context"
        False -> ""
      }
    Error(_) -> "unknown"
  }
  h.section(
    [
      a.class("search-overview"),
      a.attribute("aria-label", "Trace search overview"),
    ],
    [
      h.div([a.class("search-controls search-actions")], [
        button(
          "previous interval",
          s.PreviousInterval,
          frame.coverage.to <= frame.coverage.from,
        ),
        button(
          "next interval",
          s.NextInterval,
          frame.coverage.to <= frame.coverage.from,
        ),
        button("refresh", s.RefreshSearch, False),
        button(
          case model.utc {
            True -> "UTC"
            False -> f.timezone()
          },
          s.ToggleUTC,
          False,
        ),
      ]),
      h.div([a.class("search-status")], [
        h.span([a.class("status-label")], [
          text(case frame.coverage.rolling {
            True ->
              case model.connection {
                s.Paused -> "paused window"
                s.Reconnecting -> "reconnecting…"
                _ -> "rolling window"
              }
            False -> "fixed historical interval"
          }),
        ]),
        h.span([a.class("status-label")], [
          text(case frame.coverage.running {
            True ->
              "searching · "
              <> int.to_string(frame.coverage.scanned)
              <> " traces scanned"
            False ->
              case frame.coverage.complete {
                True -> "complete stored-data query"
                False -> "partial coverage"
              }
          }),
        ]),
        h.span([a.class("status-label")], [
          text(
            "source: "
            <> frame.health.kind
            <> " · "
            <> case frame.health.last_success {
              0 -> "not refreshed"
              _ ->
                f.age(int.to_float(frame.health.last_success), model.data.now)
            },
          ),
        ]),
      ]),
      h.div([a.class("search-metrics")], [
        h.span([], [
          text(
            int.to_string(totals.spans)
            <> " matching spans · "
            <> int.to_string(totals.errors)
            <> " error spans",
          ),
        ]),
        h.span([], [
          text("span latency ≈ p50 "),
          duration_view.label("", f.exact_duration(totals.p50)),
          text(" · p95 "),
          duration_view.label("", f.exact_duration(totals.p95)),
        ]),
        h.span([a.class("status-label")], [
          text("Stored observations, not all application traffic"),
        ]),
      ]),
      heatmap.render(model),
      h.div([a.class("search-notice status-label"), a.role("status")], [
        text(frame.health.notice),
      ]),
      h.div([a.class("search-notice status-label"), a.role("status")], [
        text(case model.notice, frame.health.error, model.cache_notice {
          "", "", "" -> frame.coverage.reason
          "", "", cache -> cache
          "", source, _ -> source
          message, _, _ -> message
        }),
      ]),
      h.div([a.class("search-inspection-status")], [
        h.span([a.title("root status: " <> root_status)], [
          text("root status: " <> root_status),
        ]),
        h.span([a.class("status-label"), a.role("status")], [
          text(case model.detail_loading {
            True -> "loading trace…"
            False -> model.detail_notice
          }),
        ]),
      ]),
    ],
  )
}

pub fn cache_controls(model: s.Model) -> List(Element(s.Msg)) {
  let cache_bytes =
    list.fold(model.cache_entries, 0, fn(n, row) { n + row.cost })
  let cache_count =
    list.fold(model.cache_entries, 0, fn(n, row) { n + row.count })
  [
    h.span([a.class("cache-usage")], [
      text(
        int.to_string(cache_count)
        <> " cached spans · "
        <> f.fixed(int.to_float(cache_bytes) /. 1_048_576.0, 1)
        <> "/50 MiB",
      ),
    ]),
    button(
      case s.trace_pinned(model) {
        True -> "unpin trace"
        False -> "pin trace"
      },
      s.PinTrace,
      !model.detail_complete || model.detail_loading,
    ),
    button(
      "export trace JSON",
      s.ExportTrace,
      !model.detail_complete || model.detail_loading,
    ),
    button("clear cached traces", s.ClearCache, False),
    h.span([a.class("cache-help")], [
      text(
        "Browser cache only. Source history and saved searches are unchanged.",
      ),
    ]),
    h.div(
      [a.class("search-pins"), a.attribute("aria-label", "Pinned traces")],
      list.map(
        list.filter(model.cache_entries, fn(row) {
          row.pinned
          && row.key == search_cache.key(model.source, "trace", row.id)
        }),
        fn(row) {
          button(
            "pinned " <> f.short(row.id),
            s.Navigate(
              query.set(model.route, [
                #("trace_id", row.id),
                #("span_id", ""),
                #("event_ix", ""),
              ]),
            ),
            False,
          )
        },
      ),
    ),
  ]
}

pub fn filters(model: s.Model) -> List(Element(s.Msg)) {
  [
    field(
      "operation",
      extra(model, "operation"),
      fn(value) { s.ExtraChanged("operation", value) },
      "operation",
    ),
    field(
      "trace ID",
      extra(model, "exact_trace"),
      fn(value) { s.ExtraChanged("exact_trace", value) },
      "exact trace ID",
    ),
    field(
      "max trace ms",
      extra(model, "max_dur"),
      fn(value) { s.ExtraChanged("max_dur", value) },
      "maximum trace duration",
    ),
    field(
      "exact attribute",
      extra(model, "attr"),
      fn(value) { s.ExtraChanged("attr", value) },
      "key=value",
    ),
    h.label([a.class("filter-field")], [
      h.span([a.class("filter-field-label")], [text("trace errors")]),
      h.select(
        [
          a.value(extra(model, "errors")),
          a.class("trace-filter-select"),
          a.title("Trace error filter"),
          e.on_input(fn(value) { s.ExtraChanged("errors", value) }),
        ],
        list.map(
          [
            #("", "all"),
            #("with", "contains recorded errors"),
            #("without", "no recorded errors"),
          ],
          fn(option) {
            h.option(
              [
                a.value(option.0),
                a.selected(extra(model, "errors") == option.0),
              ],
              option.1,
            )
          },
        ),
      ),
    ]),
    field("saved search", model.saved_name, s.SavedName, "search name"),
    h.div([a.class("search-controls")], [
      button("save", s.SaveSearch, False),
      button("delete saved", s.DeleteSaved, model.saved_name == ""),
    ]),
    h.select(
      [
        a.title("Saved searches"),
        a.class("trace-filter-select"),
        e.on_input(s.OpenSaved),
      ],
      [
        h.option([a.value("")], "load saved search"),
        ..list.map(dict.keys(model.saved), fn(name) {
          h.option([a.value(name)], name)
        })
      ],
    ),
  ]
}

pub fn paging(model: s.Model) -> Element(s.Msg) {
  let frame = model.search.frame
  h.div([a.class("dashboard-pagination")], [
    button("prev", s.PreviousSearchPage, frame.page <= 1),
    h.span([], [
      text(
        "page "
        <> int.to_string(frame.page)
        <> "/"
        <> int.to_string(frame.pages)
        <> " · "
        <> int.to_string(list.length(frame.order))
        <> " shown",
      ),
    ]),
    button(
      "next",
      s.NextSearchPage,
      frame.cursor == "" || frame.coverage.running,
    ),
  ])
}

fn extra(model: s.Model, key: String) -> String {
  dict.get(model.extra, key) |> result.unwrap("")
}

fn field(
  label: String,
  value: String,
  message: fn(String) -> s.Msg,
  placeholder: String,
) -> Element(s.Msg) {
  h.label([a.class("filter-field")], [
    h.span([a.class("filter-field-label")], [text(label)]),
    h.input([
      a.type_("text"),
      a.class("trace-filter-input"),
      a.title(label),
      a.value(value),
      a.placeholder(placeholder),
      e.on_input(message),
    ]),
  ])
}

fn button(label: String, message: s.Msg, disabled: Bool) -> Element(s.Msg) {
  h.button(
    [
      a.type_("button"),
      a.class("trace-filter-submit"),
      a.disabled(disabled),
      e.on_click(message),
    ],
    [text(label)],
  )
}
