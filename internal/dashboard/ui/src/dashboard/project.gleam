// Pure span -> UI projection. No browser I/O and no server-computed layout.
import dashboard/format as f
import dashboard/protocol as p
import dashboard/query as q
import dashboard/spans as s
import gleam/dict.{type Dict}
import gleam/float
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/order.{Eq}
import gleam/result
import gleam/set
import gleam/string

type Trace {
  Trace(
    id: String,
    root: s.Span,
    spans: List(s.Span),
    start: Float,
    end: Float,
    errors: Int,
    events: Int,
  )
}

pub opaque type Index {
  Index(traces: Dict(String, Trace))
}

pub fn empty_index() -> Index {
  Index(dict.new())
}

pub fn index(data: s.Data) -> Index {
  let grouped =
    list.fold(s.all(data), dict.new(), fn(acc, span) {
      dict.insert(acc, span.trace_id, [
        span,
        ..dict.get(acc, span.trace_id)
        |> result.unwrap([])
      ])
    })
  Index(dict.map_values(grouped, make_trace))
}

pub fn update_index(previous: Index, data: s.Data, frame: s.Frame) -> Index {
  case frame.kind {
    "reset" -> index(data)
    _ -> {
      let affected =
        set.from_list(
          list.flatten([
            list.map(frame.upserts, fn(span) { span.trace_id }),
            list.map(frame.removed, fn(key) { key.0 }),
            frame.added,
            frame.dropped,
          ]),
        )
      let grouped =
        list.fold(s.all(data), dict.new(), fn(acc, span) {
          case set.contains(affected, span.trace_id) {
            True ->
              dict.insert(acc, span.trace_id, [
                span,
                ..dict.get(acc, span.trace_id)
                |> result.unwrap([])
              ])
            False -> acc
          }
        })
      let retained =
        dict.filter(previous.traces, fn(id, _) { !set.contains(affected, id) })
      Index(dict.merge(retained, dict.map_values(grouped, make_trace)))
    }
  }
}

type Node {
  Node(
    span: s.Span,
    guides: List(p.TreeGuide),
    child_guides: List(p.TreeGuide),
    failed: Bool,
  )
}

fn compare_spans(a: s.Span, b: s.Span) {
  case float.compare(a.start, b.start) {
    Eq -> string.compare(a.id, b.id)
    other -> other
  }
}

fn split_name(name: String) -> #(String, String) {
  string.split_once(name, ":") |> result.unwrap(#("", name))
}

fn root(spans: List(s.Span)) -> s.Span {
  let ids = set.from_list(list.map(spans, fn(s) { s.id }))
  let assert Ok(first) = list.first(spans)
  list.find(spans, fn(s) {
    s.parent_id == "" || !set.contains(ids, s.parent_id)
  })
  |> result.unwrap(first)
}

fn make_trace(id: String, spans: List(s.Span)) -> Trace {
  let spans = list.sort(spans, compare_spans)
  let r = root(spans)
  Trace(
    id,
    r,
    spans,
    list.fold(spans, r.start, fn(v, s) { float.min(v, s.start) }),
    list.fold(spans, r.end, fn(v, s) { float.max(v, s.end) }),
    list.count(spans, fn(s) { s.failed || !list.is_empty(s.errors) }),
    list.fold(spans, 0, fn(v, s) { v + list.length(s.events) }),
  )
}

fn attr_text(attr: s.Attr) -> String {
  case attr.kind {
    "group" ->
      "["
      <> string.join(
        list.map(attr.group, fn(a) { a.key <> "=" <> attr_text(a) }),
        " ",
      )
      <> "]"
    "duration" ->
      case int.parse(attr.value) {
        Ok(n) -> f.exact_duration(int.to_float(n) /. 1_000_000.0)
        Error(_) -> attr.value
      }
    _ -> attr.value
  }
}

fn attrs(items: List(s.Attr)) -> List(p.Attr) {
  list.map(items, fn(a) { p.Attr(a.key, attr_text(a)) })
}

fn matches(t: Trace, route: String) -> Bool {
  let search = string.lowercase(q.get(route, "q"))
  let svc = q.get(route, "service")
  let haystack =
    t.id
    <> " "
    <> string.join(
      list.map(t.spans, fn(span) {
        span.id
        <> " "
        <> span.name
        <> " "
        <> string.join(
          list.map(span.attrs, fn(a) { a.key <> " " <> attr_text(a) }),
          " ",
        )
        <> " "
        <> string.join(
          list.map(span.events, fn(e) {
            e.name
            <> " "
            <> string.join(
              list.map(e.attrs, fn(a) { a.key <> " " <> attr_text(a) }),
              " ",
            )
          }),
          " ",
        )
        <> " "
        <> string.join(
          list.map(span.errors, fn(e) {
            e.message
            <> " "
            <> string.join(
              list.map(e.attrs, fn(a) { a.key <> " " <> attr_text(a) }),
              " ",
            )
          }),
          " ",
        )
      }),
      " ",
    )
  { q.get(route, "failed_only") == "" || t.errors > 0 }
  && {
    svc == ""
    || string.lowercase(split_name(t.root.name).0) == string.lowercase(svc)
  }
  && t.end -. t.start >=. int.to_float(q.number(route, "min_dur", 0))
  && { search == "" || string.contains(string.lowercase(haystack), search) }
}

fn select_href(
  route: String,
  trace: String,
  span: String,
  event: Int,
) -> String {
  q.set(route, [
    #("trace_id", trace),
    #("span_id", span),
    #("event_ix", case event >= 0 {
      True -> int.to_string(event)
      False -> ""
    }),
  ])
}

fn paginate(
  total: Int,
  wanted: Int,
  size: Int,
  route: String,
  key: String,
) -> p.Pagination {
  let pages = int.max(1, { total + size - 1 } / size)
  let page = int.clamp(wanted, 1, pages)
  p.Pagination(
    page,
    size,
    total,
    pages,
    case total == 0 {
      True -> 0
      False -> { page - 1 } * size + 1
    },
    int.min(page * size, total),
    page > 1,
    page < pages,
    q.set(route, [#(key, int.to_string(page - 1))]),
    q.set(route, [#(key, int.to_string(page + 1))]),
    [],
  )
}

pub fn page(data: s.Data, route: String) -> p.Page {
  page_index(data, route, index(data))
}

pub fn page_index(data: s.Data, route: String, index: Index) -> p.Page {
  let traces = dict.values(index.traces)
  let services =
    traces
    |> list.map(fn(t) { split_name(t.root.name).0 })
    |> list.filter(fn(x) { x != "" })
    |> list.unique
    |> list.sort(string.compare)
  let all_traces = traces
  let traces = list.filter(traces, fn(t) { matches(t, route) })
  let sort = q.get(route, "sort")
  let traces =
    list.sort(traces, fn(a, b) {
      let primary = case sort {
        "slowest" -> float.compare(b.end -. b.start, a.end -. a.start)
        "errors" -> int.compare(b.errors, a.errors)
        _ -> float.compare(b.start, a.start)
      }
      case primary {
        Eq -> string.compare(b.id, a.id)
        other -> other
      }
    })
  let size = int.clamp(q.number(route, "page_size", 40), 1, 200)
  let paging =
    paginate(
      list.length(traces),
      q.number(route, "page", 1),
      size,
      route,
      "page",
    )
  let visible =
    traces |> list.drop({ paging.current_page - 1 } * size) |> list.take(size)
  let selected = case
    list.find(all_traces, fn(t) { t.id == q.get(route, "trace_id") })
  {
    Ok(t) -> Some(t)
    Error(_) ->
      case q.get(route, "trace_id") == "" {
        False -> None
        True ->
          case list.first(visible) {
            Ok(t) -> Some(t)
            Error(_) -> None
          }
      }
  }
  let selected_id = case selected {
    Some(t) -> t.id
    None -> ""
  }
  let durations =
    traces |> list.map(fn(t) { t.end -. t.start }) |> list.sort(float.compare)
  let pct75 =
    list.drop(durations, list.length(durations) * 75 / 100)
    |> list.first
    |> result.unwrap(0.0)
  let pct95 =
    list.drop(durations, list.length(durations) * 95 / 100)
    |> list.first
    |> result.unwrap(0.0)
  let summaries =
    list.map(visible, fn(t) {
      let dur = t.end -. t.start
      let href = q.set(select_href(route, t.id, "", -1), [#("detail_page", "")])
      p.Trace(
        t.id,
        f.short(t.id),
        t.root.name,
        split_name(t.root.name).0,
        split_name(t.root.name).1,
        f.timestamp(t.start),
        f.age(t.start, data.now),
        f.exact_duration(dur),
        case dur >=. pct95 && dur >. 0.0 {
          True -> "very-slow"
          False ->
            case dur >=. pct75 && dur >. 0.0 {
              True -> "slow"
              False -> "normal"
            }
        },
        list.length(t.spans),
        t.events,
        t.errors,
        t.errors > 0,
        t.id == selected_id,
        href,
        href,
      )
    })
  let #(detail, item, detail_paging) = case selected {
    None -> #(None, None, paginate(0, 1, 40, route, "detail_page"))
    Some(t) -> detail(t, route)
  }
  let span_id = case item {
    Some(i) -> i.span_id
    None -> ""
  }
  let filters =
    p.Filters(
      q.get(route, "q"),
      q.get(route, "failed_only") != "",
      q.get(route, "error_path") != "",
      q.get(route, "service"),
      q.number(route, "min_dur", 0),
      services,
      case sort {
        "slowest" | "errors" -> sort
        _ -> "latest"
      },
      case q.get(route, "events") {
        "markers" -> "markers"
        _ -> "rows"
      },
      selected_id,
      span_id,
      q.number(route, "event_ix", -1),
      paging.current_page,
      size,
      detail_paging.current_page,
      detail_paging.page_size,
      "/dashboard",
      "dashboard",
      "dashboard",
      [
        p.NavLink("/", "phos", "home"),
        p.NavLink("/dashboard", "dashboard", "dashboard"),
      ],
      q.set(route, [#("error_path", "1")]),
      q.set(route, [#("error_path", "")]),
      q.set(route, [#("sort", "latest"), #("page", "")]),
      q.set(route, [#("sort", "slowest"), #("page", "")]),
      q.set(route, [#("sort", "errors"), #("page", "")]),
      q.set(route, [#("events", "rows")]),
      q.set(route, [#("events", "markers")]),
    )
  p.Page(
    p.Summary(
      list.length(traces),
      list.fold(traces, 0, fn(n, t) { n + list.length(t.spans) }),
      list.count(traces, fn(t) { t.errors > 0 }),
    ),
    filters,
    paging,
    detail_paging,
    summaries,
    detail,
    item,
    "",
  )
}

// Clock ticks only touch the visible age labels, never trees or span grouping.
pub fn clock(page: p.Page, data: s.Data, index: Index) -> p.Page {
  p.Page(
    ..page,
    traces: list.map(page.traces, fn(trace) {
      case dict.get(index.traces, trace.trace_id) {
        Ok(t) -> p.Trace(..trace, relative_time_label: f.age(t.start, data.now))
        Error(_) -> trace
      }
    }),
  )
}

fn ancestry(span: s.Span, by_id: Dict(String, s.Span)) -> List(s.Span) {
  ancestors(span, by_id, set.new(), [], 0)
}

fn ancestors(
  span: s.Span,
  by_id: Dict(String, s.Span),
  seen: set.Set(String),
  path: List(s.Span),
  depth: Int,
) -> List(s.Span) {
  case set.contains(seen, span.id) || depth >= 128 {
    True -> path
    False -> {
      let path = [span, ..path]
      case dict.get(by_id, span.parent_id) {
        Ok(parent) ->
          ancestors(parent, by_id, set.insert(seen, span.id), path, depth + 1)
        Error(_) -> path
      }
    }
  }
}

fn tree(
  t: Trace,
  by_id: Dict(String, s.Span),
  kids: Dict(String, List(s.Span)),
  error_ids: set.Set(String),
) -> List(Node) {
  let roots =
    list.filter(t.spans, fn(s) {
      s.parent_id == "" || !dict.has_key(by_id, s.parent_id)
    })
  let nodes =
    list.flat_map(roots, fn(s) {
      walk(s, kids, error_ids, [], [], set.new(), 0)
    })
  let seen = set.from_list(list.map(nodes, fn(n) { n.span.id }))
  // Cycles/depth-limited malformed input remains inspectable as top-level rows.
  list.append(
    nodes,
    t.spans
      |> list.filter(fn(s) { !set.contains(seen, s.id) })
      |> list.map(fn(s) { Node(s, [], [], set.contains(error_ids, s.id)) }),
  )
}

fn walk(
  span: s.Span,
  kids: Dict(String, List(s.Span)),
  errors: set.Set(String),
  guides: List(p.TreeGuide),
  child_guides: List(p.TreeGuide),
  seen: set.Set(String),
  depth: Int,
) -> List(Node) {
  case set.contains(seen, span.id) || depth >= 128 {
    True -> []
    False -> {
      let children = dict.get(kids, span.id) |> result.unwrap([])
      let descendants =
        children
        |> list.index_map(fn(child, index) {
          let last = index == list.length(children) - 1
          let remaining =
            children
            |> list.drop(index + 1)
            |> list.any(fn(c) { set.contains(errors, c.id) })
          let branch =
            list.append(child_guides, [
              p.TreeGuide(
                case last {
                  True -> "corner"
                  False -> "tee"
                },
                set.contains(errors, child.id) || remaining,
              ),
            ])
          let below =
            list.append(child_guides, [
              p.TreeGuide(
                case last {
                  True -> "blank"
                  False -> "pipe"
                },
                !last && remaining,
              ),
            ])
          walk(
            child,
            kids,
            errors,
            branch,
            below,
            set.insert(seen, span.id),
            depth + 1,
          )
        })
        |> list.flatten
      [
        Node(span, guides, child_guides, set.contains(errors, span.id)),
        ..descendants
      ]
    }
  }
}

fn longest(
  span: s.Span,
  kids: Dict(String, List(s.Span)),
  seen: set.Set(String),
  depth: Int,
) -> #(Float, List(String)) {
  case depth >= 128 || set.contains(seen, span.id) {
    True -> #(0.0, [])
    False -> {
      let best =
        dict.get(kids, span.id)
        |> result.unwrap([])
        |> list.fold(#(0.0, []), fn(best, child) {
          let next = longest(child, kids, set.insert(seen, span.id), depth + 1)
          case next.0 >. best.0 {
            True -> next
            False -> best
          }
        })
      #(float.max(0.0, span.end -. span.start) +. best.0, [span.id, ..best.1])
    }
  }
}

fn sorted_events(span: s.Span) -> List(s.Event) {
  list.sort(span.events, fn(a, b) {
    case float.compare(a.time, b.time) {
      Eq -> string.compare(a.name, b.name)
      other -> other
    }
  })
}

fn detail(
  t: Trace,
  route: String,
) -> #(Option(p.TraceDetail), Option(p.ItemDetail), p.Pagination) {
  let by_id = dict.from_list(list.map(t.spans, fn(s) { #(s.id, s) }))
  let kids =
    list.fold(t.spans, dict.new(), fn(acc, s) {
      dict.insert(
        acc,
        s.parent_id,
        list.append(dict.get(acc, s.parent_id) |> result.unwrap([]), [s]),
      )
    })
  let failed =
    list.filter(t.spans, fn(s) { s.failed || !list.is_empty(s.errors) })
  let error_ids =
    failed
    |> list.flat_map(fn(s) { ancestry(s, by_id) })
    |> list.map(fn(s) { s.id })
    |> set.from_list
  let first =
    failed |> list.first |> result.map(fn(s) { s.id }) |> result.unwrap("")
  let cause =
    failed
    |> list.filter(fn(s) {
      dict.get(kids, s.id)
      |> result.unwrap([])
      |> list.all(fn(c) { !c.failed && list.is_empty(c.errors) })
    })
    |> list.last
  let cause_label = case cause {
    Ok(s) ->
      s.name
      <> case list.first(s.errors) {
        Ok(e) -> " → " <> e.message
        Error(_) -> " (failed)"
      }
    Error(_) -> ""
  }
  let nodes = tree(t, by_id, kids, error_ids)
  let nodes = case q.get(route, "error_path") != "" && t.errors > 0 {
    True -> list.filter(nodes, fn(n) { set.contains(error_ids, n.span.id) })
    False -> nodes
  }
  let size = int.clamp(q.number(route, "detail_page_size", 100), 1, 500)
  let selected = q.get(route, "span_id")
  let selected_index =
    nodes
    |> list.index_map(fn(n, i) { #(n.span.id, i) })
    |> list.find(fn(p) { p.0 == selected })
    |> result.map(fn(p) { p.1 })
  let wanted = case selected_index {
    Ok(i) -> i / size + 1
    Error(_) -> q.number(route, "detail_page", 1)
  }
  let paging = paginate(list.length(nodes), wanted, size, route, "detail_page")
  let shown =
    nodes |> list.drop({ paging.current_page - 1 } * size) |> list.take(size)
  let fallback =
    shown |> list.first |> result.map(fn(n) { n.span }) |> result.unwrap(t.root)
  let span = dict.get(by_id, selected) |> result.unwrap(fallback)
  let markers = q.get(route, "events") == "markers"
  let event_index = q.number(route, "event_ix", -1)
  let duration = float.max(t.end -. t.start, 1.0)
  let critical =
    nodes
    |> list.filter(fn(n) { list.is_empty(n.guides) })
    |> list.fold(#(0.0, []), fn(best, n) {
      let next = longest(n.span, kids, set.new(), 0)
      case next.0 >. best.0 {
        True -> next
        False -> best
      }
    })
  let critical = set.from_list(critical.1)
  let rows =
    list.flat_map(shown, fn(n) {
      let s = n.span
      let events = sorted_events(s)
      let width = f.percent(s.end -. s.start, duration)
      let href = select_href(route, t.id, s.id, -1)
      let row =
        p.Row(
          "span",
          s.id,
          -1,
          f.short(s.id),
          s.name,
          n.guides,
          n.failed,
          f.relative(s.start -. t.start),
          "",
          f.exact_duration(s.end -. s.start),
          f.percent(s.start -. t.start, duration),
          width,
          s.failed,
          s.id == first,
          list.is_empty(n.guides),
          s.id == span.id && event_index < 0,
          t.errors > 0
            && !n.failed
            && !list.is_empty(n.guides)
            && s.id != span.id
            && list.is_empty(events)
            && width <. 5.0,
          set.contains(critical, s.id),
          f.fixed(width, 0) <> "%",
          list.length(s.attrs),
          list.length(events),
          list.length(s.errors),
          list.map(events, fn(e) {
            p.Marker(
              f.percent(
                float.clamp(e.time, s.start, s.end) -. t.start,
                duration,
              ),
              e.name,
              f.timestamp(e.time),
            )
          }),
          href,
          href,
        )
      let event_rows = case markers {
        True -> []
        False ->
          list.index_map(events, fn(e, ix) {
            let href = select_href(route, t.id, s.id, ix)
            p.Row(
              "event",
              s.id,
              ix,
              f.short(s.id),
              e.name,
              list.append(n.child_guides, [p.TreeGuide("dot", n.failed)]),
              n.failed,
              f.relative(e.time -. t.start),
              f.timestamp(e.time),
              "",
              f.percent(
                float.clamp(e.time, s.start, s.end) -. t.start,
                duration,
              ),
              0.0,
              False,
              False,
              False,
              s.id == span.id && ix == event_index,
              False,
              False,
              "",
              list.length(e.attrs),
              0,
              0,
              [],
              href,
              href,
            )
          })
      }
      [row, ..event_rows]
    })
  let item = item(span, t, by_id, kids, first, cause_label)
  let item = case event_index >= 0 {
    False -> item
    True ->
      case sorted_events(span) |> list.drop(event_index) |> list.first {
        Error(_) -> item
        Ok(e) ->
          p.ItemDetail(
            ..item,
            kind: "event",
            name: e.name,
            operation_name: "",
            parent_span_id: span.id,
            parent_name: span.name,
            time_label: f.timestamp(e.time),
            failed: False,
            root: False,
            first_error: False,
            root_cause_summary: "",
            duration_label: "",
            self_duration: "",
            pct_of_trace: "",
            child_count: 0,
            error_count: 0,
            event_count: 0,
            errors: [],
            events: [],
            overview: [
              p.Attr("trace id", t.id),
              p.Attr("span id", span.id),
              p.Attr("event", e.name),
            ],
            attr_groups: group_attrs(attrs(e.attrs)),
          )
      }
  }
  let ticks =
    list.range(0, 5)
    |> list.map(fn(i) {
      p.AxisTick(
        int.to_string(i * 20),
        f.duration(duration *. int.to_float(i) /. 5.0),
      )
    })
  #(
    Some(p.TraceDetail(
      t.id,
      f.short(t.id),
      t.root.name,
      f.timestamp(t.start),
      f.exact_duration(t.end -. t.start),
      list.length(t.spans),
      t.events,
      t.errors,
      t.errors > 0,
      cause_label,
      rows,
      ticks,
    )),
    Some(item),
    paging,
  )
}

fn item(
  span: s.Span,
  t: Trace,
  by_id: Dict(String, s.Span),
  kids: Dict(String, List(s.Span)),
  first: String,
  cause: String,
) -> p.ItemDetail {
  let path = ancestry(span, by_id)
  let children = dict.get(kids, span.id) |> result.unwrap([])
  // Subtract the union, not the sum, of overlapping child intervals.
  let intervals =
    children
    |> list.map(fn(c) {
      #(float.max(span.start, c.start), float.min(span.end, c.end))
    })
    |> list.sort(fn(a, b) { float.compare(a.0, b.0) })
  let covered =
    list.fold(intervals, #(span.start, 0.0), fn(acc, i) {
      #(
        float.max(acc.0, i.1),
        acc.1 +. float.max(0.0, i.1 -. float.max(acc.0, i.0)),
      )
    })
  let attributes = attrs(span.attrs)
  let overview_keys = [
    "service",
    "operation",
    "status_code",
    "status_text",
    "method",
    "path",
  ]
  let overview =
    list.append(
      [p.Attr("trace id", span.trace_id), p.Attr("span id", span.id)],
      case span.parent_id != "" {
        True -> [p.Attr("parent", span.parent_id)]
        False -> []
      },
    )
  let overview =
    list.append(
      overview,
      list.filter(attributes, fn(a) { list.contains(overview_keys, a.key) }),
    )
  let remaining =
    list.filter(attributes, fn(a) { !list.contains(overview_keys, a.key) })
  let link_attrs =
    list.index_map(span.links, fn(l, i) {
      p.Attr("link " <> int.to_string(i + 1), l.trace_id <> "/" <> l.span_id)
    })
  let groups = group_attrs(remaining)
  let groups = case link_attrs {
    [] -> groups
    _ -> list.append(groups, [p.AttrGroup("Links", link_attrs)])
  }
  p.ItemDetail(
    "span",
    span.trace_id,
    span.id,
    f.short(span.id),
    span.parent_id,
    dict.get(by_id, span.parent_id)
      |> result.map(fn(p) { p.name })
      |> result.unwrap(""),
    span.name,
    split_name(span.name).0,
    split_name(span.name).1,
    f.timestamp(span.start),
    f.timestamp(span.end),
    "",
    f.exact_duration(span.end -. span.start),
    f.exact_duration(span.end -. span.start -. covered.1),
    f.fixed(f.percent(span.end -. span.start, t.end -. t.start), 0) <> "%",
    list.length(children),
    int.max(0, list.length(path) - 1),
    span.failed,
    span.parent_id == "" || !dict.has_key(by_id, span.parent_id),
    span.id == first,
    cause,
    list.map(path, fn(a) { p.Ancestor(a.name, a.id, a.failed) }),
    overview,
    groups,
    list.length(span.events),
    list.length(span.errors),
    list.map(sorted_events(span), fn(e) {
      p.Item("event", f.timestamp(e.time), e.name, "", "", "", attrs(e.attrs))
    }),
    list.map(span.errors, fn(e) {
      p.Item(
        "error",
        f.timestamp(span.end),
        "",
        e.message,
        "",
        "",
        attrs(e.attrs),
      )
    }),
  )
}

fn attr_group(key: String) -> String {
  case True {
    _
      if key == "method"
      || key == "path"
      || key == "status_code"
      || key == "status_text"
      || key == "client.address"
      || key == "server.address"
      || key == "net.transport"
    -> "HTTP"
    _ if key == "cluster" || key == "region" || key == "zone" -> "Infrastructure"
    _
      if key == "user_id"
      || key == "account_id"
      || key == "session_id"
      || key == "tenant_id"
    -> "Identity"
    _ ->
      [
        #("HTTP", ["http."]),
        #("Database", ["db."]),
        #("Cache", ["cache."]),
        #("Messaging", ["messaging.", "queue."]),
        #("Infrastructure", ["k8s.", "cloud.", "deployment."]),
        #("Runtime", [
          "otel.",
          "build.",
          "release.",
          "trace.",
          "worker.",
          "job.",
        ]),
      ]
      |> list.find(fn(g) {
        list.any(g.1, fn(prefix) { string.starts_with(key, prefix) })
      })
      |> result.map(fn(g) { g.0 })
      |> result.unwrap("Other")
  }
}

fn group_attrs(attributes: List(p.Attr)) -> List(p.AttrGroup) {
  [
    "HTTP",
    "Database",
    "Cache",
    "Messaging",
    "Infrastructure",
    "Identity",
    "Runtime",
    "Other",
  ]
  |> list.filter_map(fn(name) {
    let items = list.filter(attributes, fn(a) { attr_group(a.key) == name })
    case items {
      [] -> Error(Nil)
      _ -> Ok(p.AttrGroup(name, items))
    }
  })
}
