import dashboard/duration_view
import dashboard/format as f
import dashboard/query
import dashboard/search
import dashboard/state as s
import dashboard/tooltip
import gleam/dynamic/decode as d
import gleam/float
import gleam/int
import gleam/list
import gleam/option.{None, Some}
import gleam/result
import gleam/string
import lustre/attribute as a
import lustre/element.{type Element, text}
import lustre/element/html as h
import lustre/event as e

pub fn render(model: s.Model) -> Element(s.Msg) {
  let frame = model.search.frame
  let totals = frame.aggregates
  let bounds = totals.duration_bounds
  let bands = list.zip([0.0, ..bounds], bounds)
  let preview =
    list.find(totals.buckets, fn(bucket) {
      search.bucket_key(bucket) == model.preview_column
    })
  h.div(
    [
      a.class("duration-overview"),
      e.on_mouse_leave(s.ColumnHover(tooltip.Leave)),
    ],
    [
      h.aside(
        [
          a.class("overview-facets"),
          a.attribute("aria-label", case preview {
            Ok(_) -> "Column statistics"
            Error(_) -> "Matching span filters"
          }),
        ],
        case preview {
          Ok(bucket) -> column_summary(model, bucket)
          Error(_) -> [
            h.div([a.class("column-prompt")], [
              text("Hover a column for its statistics"),
            ]),
            facet(model, "Service", "service", totals.services),
            facet(model, "Operation", "operation", totals.operations),
          ]
        },
      ),
      h.div([a.class("duration-plot")], [
        h.div([a.class("duration-heading")], [
          h.strong([], [text("Span duration")]),
          h.span(
            [
              tooltip.hint(
                "First and last columns may be clipped to the selected range. Hover for exact width; click to zoom.",
              ),
            ],
            [
              text(
                "Columns: "
                <> f.duration(int.to_float(totals.bucket_width))
                <> " · hover for stats · click to zoom",
              ),
            ],
          ),
        ]),
        h.div([a.class("duration-chart")], [
          h.div(
            [
              a.class("duration-grid"),
              a.attribute("aria-label", "Span duration heatmap"),
            ],
            list.map(totals.buckets, fn(bucket) {
              let label =
                instant(bucket.from, model.utc)
                <> " to "
                <> instant(bucket.to, model.utc)
                <> " · width "
                <> f.duration(int.to_float(bucket.to - bucket.from))
                <> " · "
                <> int.to_string(bucket.spans)
                <> " matching spans"
              h.button(
                [
                  a.type_("button"),
                  a.class(
                    "duration-column"
                    <> case search.bucket_key(bucket) == model.preview_column {
                      True -> " is-previewed"
                      False -> ""
                    },
                  ),
                  a.attribute("aria-label", label),
                  tooltip.hint(label),
                  e.on_mouse_leave(s.ColumnHoverEnded),
                  e.on_blur(s.ColumnHoverEnded),
                  e.on("pointermove", fn(raw) {
                    d.run(raw, {
                      use x <- d.field("clientX", d.float)
                      use y <- d.field("clientY", d.float)
                      d.success(
                        s.ColumnHover(tooltip.Hover(
                          search.bucket_key(bucket),
                          float.round(x),
                          float.round(y),
                          False,
                          False,
                        )),
                      )
                    })
                    |> result.map_error(fn(_) { [] })
                  }),
                  e.on_focus(
                    s.ColumnHover(tooltip.Hover(
                      search.bucket_key(bucket),
                      0,
                      0,
                      False,
                      True,
                    )),
                  ),
                  a.style([
                    #(
                      "left",
                      position(
                        bucket.from - frame.coverage.from,
                        frame.coverage.to - frame.coverage.from,
                      ),
                    ),
                    #(
                      "width",
                      position(
                        bucket.to - bucket.from,
                        frame.coverage.to - frame.coverage.from,
                      ),
                    ),
                  ]),
                  e.on_click(s.SelectInterval(bucket.from, bucket.to)),
                ],
                list.zip(bands, bucket.durations)
                  |> list.reverse
                  |> list.map(fn(cell) {
                    let label =
                      band_label(cell.0.0, cell.0.1)
                      <> " · "
                      <> int.to_string(cell.1)
                      <> " spans"
                    h.span(
                      [
                        a.class("duration-cell"),
                        tooltip.hint(label),
                        a.style([
                          #("--density", case cell.1 {
                            0 -> "0"
                            _ ->
                              f.fixed(
                                0.2
                                  +. 0.8
                                  *. int.to_float(cell.1)
                                  /. int.to_float(int.max(1, totals.heatmap_max)),
                                3,
                              )
                          }),
                        ]),
                      ],
                      [],
                    )
                  }),
              )
            }),
          ),
          h.div(
            [a.class("duration-labels"), a.attribute("aria-hidden", "true")],
            list.reverse(bands)
              |> list.map(fn(band) {
                h.span([tooltip.hint(band_label(band.0, band.1))], [
                  text(case band.1 {
                    0.0 -> "> " <> duration(band.0)
                    _ -> "≤ " <> duration(band.1)
                  }),
                ])
              }),
          ),
          case totals.heatmap_max {
            0 ->
              h.div([a.class("duration-empty"), a.role("status")], [
                text(case frame.coverage.running {
                  True -> "Collecting span durations…"
                  False -> "No completed span durations in this interval"
                }),
              ])
            _ -> text("")
          },
        ]),
        h.div([a.class("search-axis")], [
          axis_label(
            frame.coverage.from,
            model.utc,
            frame.coverage.to - frame.coverage.from,
          ),
          h.span([], [
            text(case model.utc {
              True -> "UTC"
              False -> f.timezone()
            }),
          ]),
          axis_label(
            frame.coverage.to,
            model.utc,
            frame.coverage.to - frame.coverage.from,
          ),
        ]),
        h.div([a.class("duration-legend")], [
          h.span([], [text("Spans per cell")]),
          h.span([], [text("0")]),
          h.span(
            [a.class("duration-key"), a.attribute("aria-hidden", "true")],
            list.map([20, 40, 60, 80, 100], fn(level) {
              h.span(
                [
                  a.style([
                    #("opacity", f.fixed(int.to_float(level) /. 100.0, 2)),
                  ]),
                ],
                [],
              )
            }),
          ),
          h.span([], [text(int.to_string(totals.heatmap_max))]),
          h.span([a.class("duration-unknown")], [
            text(
              int.to_string(totals.unknown_duration)
              <> " missing/invalid durations",
            ),
          ]),
        ]),
      ]),
    ],
  )
}

fn column_summary(
  model: s.Model,
  bucket: search.Bucket,
) -> List(Element(s.Msg)) {
  let width = bucket.to - bucket.from
  [
    h.div([a.class("column-heading")], [
      h.strong([], [
        text("Column · "),
        duration_view.label("", f.exact_duration(int.to_float(width))),
      ]),
      h.button(
        [
          a.type_("button"),
          a.class("facet-clear"),
          e.on_click(s.ColumnHover(tooltip.Leave)),
        ],
        [text("Whole range")],
      ),
    ]),
    h.div([a.class("column-range")], [
      h.div([], [text(instant(bucket.from, model.utc))]),
      h.div([], [text("to " <> instant(bucket.to, model.utc))]),
      h.div([], [
        text(
          case model.utc {
            True -> "UTC"
            False -> f.timezone()
          }
          <> " · end exclusive",
        ),
      ]),
      case width != model.search.frame.aggregates.bucket_width {
        True -> h.div([], [text("Clipped edge column")])
        False -> text("")
      },
    ]),
    h.div([a.class("column-metrics")], [
      metric("Spans", int.to_string(bucket.spans)),
      metric(
        "Errors",
        int.to_string(bucket.errors)
          <> " · "
          <> f.fixed(
          f.percent(int.to_float(bucket.errors), int.to_float(bucket.spans)),
          1,
        )
          <> "%",
      ),
      metric(
        "Spans / s",
        f.fixed(
          int.to_float(bucket.spans)
            *. 1000.0
            /. int.to_float(int.max(1, width)),
          2,
        ),
      ),
    ]),
    case bucket.stats {
      None ->
        h.p([a.class("column-prompt")], [
          text("Column breakdown unavailable. Refresh to load it."),
        ])
      Some(stats) ->
        h.div([], [
          h.div(
            [a.class("column-percentiles")],
            list.map(
              [#("p50", stats.p50), #("p95", stats.p95), #("p99", stats.p99)],
              fn(value) {
                h.div([], [
                  h.span([], [text(value.0 <> " ≈")]),
                  case stats.samples {
                    0 -> h.strong([], [text("—")])
                    _ -> duration_view.label("", f.exact_duration(value.1))
                  },
                ])
              },
            ),
          ),
          h.div([a.class("column-prompt")], [
            text(
              int.to_string(stats.samples)
              <> " timed spans · "
              <> int.to_string(bucket.unknown_duration)
              <> " unknown duration",
            ),
          ]),
          h.div([a.class("column-breakdowns")], [
            breakdown("Services", stats.services),
            breakdown("Operations", stats.operations),
          ]),
        ])
    },
  ]
}

fn metric(label: String, value: String) -> Element(s.Msg) {
  h.div([a.class("column-metric")], [
    h.span([], [text(label)]),
    h.strong([], [text(value)]),
  ])
}

fn breakdown(title: String, facet: search.Facet) -> Element(s.Msg) {
  h.section([a.class("overview-facet column-breakdown")], [
    h.div([a.class("facet-heading")], [
      h.strong([], [text(title)]),
      h.span([], [text("spans")]),
    ]),
    case facet.values, facet.other {
      [], 0 -> h.div([a.class("column-prompt")], [text("No matching spans")])
      _, _ -> text("")
    },
    h.div(
      [],
      list.map(facet.values, fn(value) {
        h.div([a.class("column-facet"), tooltip.hint(value.value)], [
          h.span([], [text(value.value)]),
          h.strong([], [text(int.to_string(value.count))]),
        ])
      }),
    ),
    case facet.other {
      0 -> text("")
      _ ->
        h.div([a.class("facet-other")], [
          text("Other / unnamed · " <> int.to_string(facet.other)),
        ])
    },
  ])
}

fn position(part: Int, total: Int) -> String {
  f.fixed(f.percent(int.to_float(part), int.to_float(total)), 6) <> "%"
}

fn band_label(lower: Float, upper: Float) -> String {
  case upper {
    0.0 -> "> " <> duration(lower)
    _ ->
      case lower {
        0.0 -> "0–" <> duration(upper)
        _ -> "> " <> duration(lower) <> " to ≤ " <> duration(upper)
      }
  }
}

fn facet(
  model: s.Model,
  title: String,
  key: String,
  facet: search.Facet,
) -> Element(s.Msg) {
  let selected = query.get(model.route, key)
  h.section([a.class("overview-facet")], [
    h.div([a.class("facet-heading")], [
      h.strong([], [text(title)]),
      h.span([], [text("spans")]),
    ]),
    case selected {
      "" -> text("")
      _ ->
        h.button(
          [
            a.type_("button"),
            a.class("facet-clear"),
            e.on_click(
              s.Navigate(query.set(model.route, [#(key, ""), #("cursor", "")])),
            ),
          ],
          [text("Clear " <> title <> " filter")],
        )
    },
    h.div(
      [],
      list.map(facet.values, fn(value) {
        h.button(
          [
            a.type_("button"),
            a.class("facet-value"),
            tooltip.hint(value.value),
            a.attribute("aria-pressed", s.bool_string(selected == value.value)),
            e.on_click(
              s.Navigate(
                query.set(model.route, [
                  #(key, case selected == value.value {
                    True -> ""
                    False -> value.value
                  }),
                  #("cursor", ""),
                ]),
              ),
            ),
          ],
          [
            h.span([], [text(value.value)]),
            h.span([], [text(int.to_string(value.count))]),
          ],
        )
      }),
    ),
    case facet.other {
      0 -> text("")
      _ ->
        h.div([a.class("facet-other")], [
          text("Other / unnamed · " <> int.to_string(facet.other)),
        ])
    },
  ])
}

fn duration(ms: Float) -> String {
  case ms {
    n if n >=. 60_000.0 -> f.fixed(n /. 60_000.0, 0) <> "min"
    n if n >=. 1000.0 -> f.fixed(n /. 1000.0, 0) <> "s"
    n if n >=. 1.0 -> f.fixed(n, 0) <> "ms"
    n -> f.fixed(n *. 1000.0, 0) <> "µs"
  }
}

fn instant(ms: Int, utc: Bool) -> String {
  let seconds = ms / 1000 - ms / 60_000 * 60
  let millis = ms - ms / 1000 * 1000
  f.input_zone(ms, utc)
  <> ":"
  <> string.pad_start(int.to_string(seconds), 2, "0")
  <> "."
  <> string.pad_start(int.to_string(millis), 3, "0")
}

fn axis_label(ms: Int, utc: Bool, window: Int) -> Element(s.Msg) {
  let full = instant(ms, utc)
  h.span([tooltip.hint(full)], [
    text(case window < 3_600_000 {
      True -> string.slice(full, 11, 12)
      False -> f.input_zone(ms, utc)
    }),
  ])
}
