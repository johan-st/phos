// Lossless data boundary. Numeric attribute text preserves 64-bit integers.
import gleam/dict.{type Dict}
import gleam/dynamic/decode as d
import gleam/int
import gleam/json
import gleam/list
import gleam/result
import gleam/set

pub type Attr {
  Attr(key: String, kind: String, value: String, group: List(Attr))
}

pub type Event {
  Event(name: String, time: Float, attrs: List(Attr))
}

pub type Failure {
  Failure(message: String, attrs: List(Attr))
}

pub type Link {
  Link(trace_id: String, span_id: String, attrs: List(Attr))
}

pub type Span {
  Span(
    id: String,
    trace_id: String,
    parent_id: String,
    name: String,
    start: Float,
    end: Float,
    start_time: String,
    end_time: String,
    kind: Int,
    failed: Bool,
    attrs: List(Attr),
    events: List(Event),
    errors: List(Failure),
    links: List(Link),
  )
}

pub type Coverage {
  Coverage(
    from_ms: Int,
    to_ms: Int,
    rolling: Bool,
    source_limited: Bool,
    source_limit: Int,
    has_more: Bool,
    truncated: Bool,
    available_traces: Int,
    loaded_traces: Int,
  )
}

pub type Frame {
  Frame(
    source: String,
    epoch: String,
    query: String,
    base_query: String,
    kind: String,
    base: Int,
    seq: Int,
    now: Int,
    upserts: List(Span),
    removed: List(#(String, String)),
    added: List(String),
    dropped: List(String),
    coverage: Coverage,
  )
}

pub type Data {
  Data(
    source: String,
    epoch: String,
    query: String,
    seq: Int,
    spans: Dict(#(String, String), Span),
    members: set.Set(String),
    coverage: Coverage,
    now: Int,
  )
}

pub fn empty() -> Data {
  Data(
    "",
    "",
    "",
    0,
    dict.new(),
    set.new(),
    Coverage(0, 0, True, False, 0, False, False, 0, 0),
    0,
  )
}

pub fn apply(data: Data, frame: Frame) -> Result(Data, String) {
  case frame.kind {
    "reset"
      if frame.source == data.source
      && frame.epoch == data.epoch
      && frame.query == data.query
      && frame.seq < data.seq
    -> Ok(data)
    "reset"
      if frame.source == data.source
      && frame.epoch == data.epoch
      && frame.query == data.query
      && frame.seq == data.seq
    -> Ok(Data(..data, now: int.max(data.now, frame.now)))
    "reset" ->
      Ok(Data(
        frame.source,
        frame.epoch,
        frame.query,
        frame.seq,
        dict.from_list(
          list.map(frame.upserts, fn(s) { #(#(s.trace_id, s.id), s) }),
        ),
        set.from_list(frame.added),
        frame.coverage,
        frame.now,
      ))
    "delta"
      if frame.epoch != data.epoch
      || frame.query != data.query
      && frame.base_query != data.query
      || frame.source != data.source
    -> Error("reset required")
    "delta" if frame.query != data.query && frame.base != data.seq ->
      Error("query baseline changed")
    "delta" if frame.query == data.query && frame.seq < data.seq -> Ok(data)
    "delta" if frame.query == data.query && frame.seq == data.seq ->
      Ok(Data(..data, now: int.max(data.now, frame.now)))
    "delta" if frame.base > data.seq -> Error("stream gap")
    "delta" -> {
      let current =
        list.fold(frame.removed, data.spans, fn(acc, k) { dict.delete(acc, k) })
      let current =
        list.fold(frame.upserts, current, fn(acc, s) {
          dict.insert(acc, #(s.trace_id, s.id), s)
        })
      let members = list.fold(frame.dropped, data.members, set.delete)
      let members = list.fold(frame.added, members, set.insert)
      Ok(Data(
        frame.source,
        frame.epoch,
        frame.query,
        frame.seq,
        current,
        members,
        frame.coverage,
        frame.now,
      ))
    }
    _ -> Error("unknown span batch")
  }
}

pub fn all(data: Data) -> List(Span) {
  dict.values(data.spans)
  |> list.filter(fn(s) { set.contains(data.members, s.trace_id) })
}

pub fn parse(raw: String) -> Result(Frame, String) {
  json.parse(raw, frame_decoder())
  |> result.map_error(fn(_) { "Invalid span batch" })
}

fn frame_decoder() -> d.Decoder(Frame) {
  use version <- d.field("schema_version", d.int)
  use source <- d.field("source", d.string)
  use epoch <- d.field("epoch", d.string)
  use query <- d.field("query", d.string)
  use base_query <- d.field("base_query", d.string)
  use kind <- d.field("kind", d.string)
  use base <- d.field("base_seq", d.int)
  use seq <- d.field("seq", d.int)
  use now <- d.field("now_ms", d.int)
  use upserts <- d.field("upserts", d.list(span_decoder()))
  use removed <- d.field("removed", d.list(key_decoder()))
  use added <- d.field("members_added", d.list(d.string))
  use dropped <- d.field("members_removed", d.list(d.string))
  use coverage <- d.field("coverage", coverage_decoder())
  case
    version == 2
    && seq > 0
    && base >= 0
    && { seq >= base || base_query != "" }
    && list.contains(["reset", "delta"], kind)
  {
    True ->
      d.success(Frame(
        source,
        epoch,
        query,
        base_query,
        kind,
        base,
        seq,
        now,
        upserts,
        removed,
        added,
        dropped,
        coverage,
      ))
    False ->
      d.failure(
        Frame("", "", "", "", "", 0, 0, 0, [], [], [], [], coverage),
        "span feed v2",
      )
  }
}

fn key_decoder() -> d.Decoder(#(String, String)) {
  use trace <- d.field("trace_id", d.string)
  use id <- d.field("id", d.string)
  d.success(#(trace, id))
}

fn coverage_decoder() -> d.Decoder(Coverage) {
  use from_ms <- d.field("from_ms", d.int)
  use to_ms <- d.field("to_ms", d.int)
  use rolling <- d.field("rolling", d.bool)
  use source_limited <- d.field("source_limited", d.bool)
  use source_limit <- d.field("source_limit", d.int)
  use has_more <- d.field("has_more", d.bool)
  use truncated <- d.field("truncated", d.bool)
  use available_traces <- d.field("available_traces", d.int)
  use loaded_traces <- d.field("loaded_traces", d.int)
  d.success(Coverage(
    from_ms,
    to_ms,
    rolling,
    source_limited,
    source_limit,
    has_more,
    truncated,
    available_traces,
    loaded_traces,
  ))
}

pub fn span_decoder() -> d.Decoder(Span) {
  use id <- d.field("id", d.string)
  use trace_id <- d.field("trace_id", d.string)
  use parent_id <- d.field("parent_id", d.string)
  use name <- d.field("name", d.string)
  use start <- d.field("start_ms", d.float)
  use end <- d.field("end_ms", d.float)
  use start_time <- d.field("start_time", d.string)
  use end_time <- d.field("end_time", d.string)
  use kind <- d.field("kind", d.int)
  use failed <- d.field("failed", d.bool)
  use attrs <- d.field("attrs", d.list(attr_decoder()))
  use events <- d.field("events", d.list(event_decoder()))
  use errors <- d.field("errors", d.list(error_decoder()))
  use links <- d.field("links", d.list(link_decoder()))
  d.success(Span(
    id,
    trace_id,
    parent_id,
    name,
    start,
    end,
    start_time,
    end_time,
    kind,
    failed,
    attrs,
    events,
    errors,
    links,
  ))
}

fn attr_decoder() -> d.Decoder(Attr) {
  use key <- d.field("key", d.string)
  use kind <- d.field("kind", d.string)
  use value <- d.field("value", d.string)
  use group <- d.field("group", d.list(attr_decoder()))
  d.success(Attr(key, kind, value, group))
}

fn event_decoder() -> d.Decoder(Event) {
  use name <- d.field("name", d.string)
  use time <- d.field("time_ms", d.float)
  use attrs <- d.field("attrs", d.list(attr_decoder()))
  d.success(Event(name, time, attrs))
}

fn error_decoder() -> d.Decoder(Failure) {
  use message <- d.field("message", d.string)
  use attrs <- d.field("attrs", d.list(attr_decoder()))
  d.success(Failure(message, attrs))
}

fn link_decoder() -> d.Decoder(Link) {
  use trace_id <- d.field("trace_id", d.string)
  use span_id <- d.field("span_id", d.string)
  use attrs <- d.field("attrs", d.list(attr_decoder()))
  d.success(Link(trace_id, span_id, attrs))
}

pub fn encode(items: List(Span)) -> String {
  json.array(items, fn(s) {
    json.object([
      #("id", json.string(s.id)),
      #("trace_id", json.string(s.trace_id)),
      #("parent_id", json.string(s.parent_id)),
      #("name", json.string(s.name)),
      #("start_ms", json.float(s.start)),
      #("end_ms", json.float(s.end)),
      #("start_time", json.string(s.start_time)),
      #("end_time", json.string(s.end_time)),
      #("kind", json.int(s.kind)),
      #("failed", json.bool(s.failed)),
      #("attrs", json.array(s.attrs, encode_attr)),
      #(
        "events",
        json.array(s.events, fn(e) {
          json.object([
            #("name", json.string(e.name)),
            #("time_ms", json.float(e.time)),
            #("attrs", json.array(e.attrs, encode_attr)),
          ])
        }),
      ),
      #(
        "errors",
        json.array(s.errors, fn(e) {
          json.object([
            #("message", json.string(e.message)),
            #("attrs", json.array(e.attrs, encode_attr)),
          ])
        }),
      ),
      #(
        "links",
        json.array(s.links, fn(l) {
          json.object([
            #("trace_id", json.string(l.trace_id)),
            #("span_id", json.string(l.span_id)),
            #("attrs", json.array(l.attrs, encode_attr)),
          ])
        }),
      ),
    ])
  })
  |> json.to_string
}

fn encode_attr(a: Attr) -> json.Json {
  json.object([
    #("key", json.string(a.key)),
    #("kind", json.string(a.kind)),
    #("value", json.string(a.value)),
    #("group", json.array(a.group, encode_attr)),
  ])
}
