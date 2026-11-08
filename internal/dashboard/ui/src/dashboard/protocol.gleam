// Typed boundary for the workload's versioned JSON projections.
import gleam/dynamic/decode
import gleam/json
import gleam/option.{type Option, None, Some}

pub type Snapshot {
  Snapshot(revision: Int, page: Page)
}

pub fn parse(raw: String) -> Result(Snapshot, String) {
  let decoder = {
    use version <- decode.field("schema_version", decode.int)
    use revision <- decode.field("revision", decode.int)
    use page <- decode.field("page", page_decoder())
    decode.success(#(version, Snapshot(revision, page)))
  }
  case json.parse(raw, decoder) {
    Ok(#(1, snapshot)) -> Ok(snapshot)
    Ok(_) -> Error("Unsupported dashboard schema")
    Error(_) -> Error("Invalid dashboard update")
  }
}

fn nullable_list(of: decode.Decoder(a)) -> decode.Decoder(List(a)) {
  decode.optional(decode.list(of))
  |> decode.map(fn(value) {
    case value {
      Some(items) -> items
      None -> []
    }
  })
}

pub type Page {
  Page(
    summary: Summary,
    filters: Filters,
    trace_paging: Pagination,
    detail_paging: Pagination,
    traces: List(Trace),
    selected_trace: Option(TraceDetail),
    selected_item: Option(ItemDetail),
    stream_path: String,
  )
}

fn page_decoder() -> decode.Decoder(Page) {
  use summary <- decode.field("Summary", summary_decoder())
  use filters <- decode.field("Filters", filters_decoder())
  use trace_paging <- decode.field("TracePaging", pagination_decoder())
  use detail_paging <- decode.field("DetailPaging", pagination_decoder())
  use traces <- decode.field("Traces", nullable_list(trace_decoder()))
  use selected_trace <- decode.field(
    "SelectedTrace",
    decode.optional(trace_detail_decoder()),
  )
  use selected_item <- decode.field(
    "SelectedItem",
    decode.optional(item_detail_decoder()),
  )
  use stream_path <- decode.field("StreamPath", decode.string)
  decode.success(Page(
    summary:,
    filters:,
    trace_paging:,
    detail_paging:,
    traces:,
    selected_trace:,
    selected_item:,
    stream_path:,
  ))
}

pub type Summary {
  Summary(total_traces: Int, total_spans: Int, error_traces: Int)
}

fn summary_decoder() -> decode.Decoder(Summary) {
  use total_traces <- decode.field("TotalTraces", decode.int)
  use total_spans <- decode.field("TotalSpans", decode.int)
  use error_traces <- decode.field("ErrorTraces", decode.int)
  decode.success(Summary(total_traces:, total_spans:, error_traces:))
}

pub type Filters {
  Filters(
    query: String,
    failed_only: Bool,
    error_path_only: Bool,
    service_filter: String,
    min_duration_ms: Int,
    services: List(String),
    sort_by: String,
    events_mode: String,
    selected_trace_id: String,
    selected_span_id: String,
    selected_event_ix: Int,
    trace_page: Int,
    trace_page_size: Int,
    detail_page: Int,
    detail_page_size: Int,
    base_path: String,
    page_title: String,
    active_nav: String,
    nav_links: List(NavLink),
    error_path_href: String,
    full_trace_href: String,
    sort_latest_href: String,
    sort_slowest_href: String,
    sort_errors_href: String,
    rows_mode_href: String,
    markers_mode_href: String,
  )
}

fn filters_decoder() -> decode.Decoder(Filters) {
  use query <- decode.field("Query", decode.string)
  use failed_only <- decode.field("FailedOnly", decode.bool)
  use error_path_only <- decode.field("ErrorPathOnly", decode.bool)
  use service_filter <- decode.field("ServiceFilter", decode.string)
  use min_duration_ms <- decode.field("MinDurationMs", decode.int)
  use services <- decode.field("Services", nullable_list(decode.string))
  use sort_by <- decode.field("SortBy", decode.string)
  use events_mode <- decode.field("EventsMode", decode.string)
  use selected_trace_id <- decode.field("SelectedTraceID", decode.string)
  use selected_span_id <- decode.field("SelectedSpanID", decode.string)
  use selected_event_ix <- decode.field("SelectedEventIX", decode.int)
  use trace_page <- decode.field("TracePage", decode.int)
  use trace_page_size <- decode.field("TracePageSize", decode.int)
  use detail_page <- decode.field("DetailPage", decode.int)
  use detail_page_size <- decode.field("DetailPageSize", decode.int)
  use base_path <- decode.field("BasePath", decode.string)
  use page_title <- decode.field("PageTitle", decode.string)
  use active_nav <- decode.field("ActiveNav", decode.string)
  use nav_links <- decode.field("NavLinks", nullable_list(nav_link_decoder()))
  use error_path_href <- decode.field("ErrorPathHref", decode.string)
  use full_trace_href <- decode.field("FullTraceHref", decode.string)
  use sort_latest_href <- decode.field("SortLatestHref", decode.string)
  use sort_slowest_href <- decode.field("SortSlowestHref", decode.string)
  use sort_errors_href <- decode.field("SortErrorsHref", decode.string)
  use rows_mode_href <- decode.field("RowsModeHref", decode.string)
  use markers_mode_href <- decode.field("MarkersModeHref", decode.string)
  decode.success(Filters(
    query:,
    failed_only:,
    error_path_only:,
    service_filter:,
    min_duration_ms:,
    services:,
    sort_by:,
    events_mode:,
    selected_trace_id:,
    selected_span_id:,
    selected_event_ix:,
    trace_page:,
    trace_page_size:,
    detail_page:,
    detail_page_size:,
    base_path:,
    page_title:,
    active_nav:,
    nav_links:,
    error_path_href:,
    full_trace_href:,
    sort_latest_href:,
    sort_slowest_href:,
    sort_errors_href:,
    rows_mode_href:,
    markers_mode_href:,
  ))
}

pub type Pagination {
  Pagination(
    current_page: Int,
    page_size: Int,
    total_items: Int,
    total_pages: Int,
    start: Int,
    end: Int,
    has_prev: Bool,
    has_next: Bool,
    prev_href: String,
    next_href: String,
    page_links: List(PageLink),
  )
}

fn pagination_decoder() -> decode.Decoder(Pagination) {
  use current_page <- decode.field("CurrentPage", decode.int)
  use page_size <- decode.field("PageSize", decode.int)
  use total_items <- decode.field("TotalItems", decode.int)
  use total_pages <- decode.field("TotalPages", decode.int)
  use start <- decode.field("Start", decode.int)
  use end <- decode.field("End", decode.int)
  use has_prev <- decode.field("HasPrev", decode.bool)
  use has_next <- decode.field("HasNext", decode.bool)
  use prev_href <- decode.field("PrevHref", decode.string)
  use next_href <- decode.field("NextHref", decode.string)
  use page_links <- decode.field(
    "PageLinks",
    nullable_list(page_link_decoder()),
  )
  decode.success(Pagination(
    current_page:,
    page_size:,
    total_items:,
    total_pages:,
    start:,
    end:,
    has_prev:,
    has_next:,
    prev_href:,
    next_href:,
    page_links:,
  ))
}

pub type PageLink {
  PageLink(number: Int, href: String, active: Bool)
}

fn page_link_decoder() -> decode.Decoder(PageLink) {
  use number <- decode.field("Number", decode.int)
  use href <- decode.field("Href", decode.string)
  use active <- decode.field("Active", decode.bool)
  decode.success(PageLink(number:, href:, active:))
}

pub type Trace {
  Trace(
    trace_id: String,
    short_trace_id: String,
    name: String,
    service_name: String,
    operation_name: String,
    started_at_label: String,
    relative_time_label: String,
    duration_label: String,
    duration_severity: String,
    span_count: Int,
    event_count: Int,
    error_count: Int,
    failed: Bool,
    selected: Bool,
    href: String,
    live_href: String,
  )
}

fn trace_decoder() -> decode.Decoder(Trace) {
  use trace_id <- decode.field("TraceID", decode.string)
  use short_trace_id <- decode.field("ShortTraceID", decode.string)
  use name <- decode.field("Name", decode.string)
  use service_name <- decode.field("ServiceName", decode.string)
  use operation_name <- decode.field("OperationName", decode.string)
  use started_at_label <- decode.field("StartedAtLabel", decode.string)
  use relative_time_label <- decode.field("RelativeTimeLabel", decode.string)
  use duration_label <- decode.field("DurationLabel", decode.string)
  use duration_severity <- decode.field("DurationSeverity", decode.string)
  use span_count <- decode.field("SpanCount", decode.int)
  use event_count <- decode.field("EventCount", decode.int)
  use error_count <- decode.field("ErrorCount", decode.int)
  use failed <- decode.field("Failed", decode.bool)
  use selected <- decode.field("Selected", decode.bool)
  use href <- decode.field("Href", decode.string)
  use live_href <- decode.field("LiveHref", decode.string)
  decode.success(Trace(
    trace_id:,
    short_trace_id:,
    name:,
    service_name:,
    operation_name:,
    started_at_label:,
    relative_time_label:,
    duration_label:,
    duration_severity:,
    span_count:,
    event_count:,
    error_count:,
    failed:,
    selected:,
    href:,
    live_href:,
  ))
}

pub type TraceDetail {
  TraceDetail(
    trace_id: String,
    short_trace_id: String,
    name: String,
    started_at_label: String,
    duration_label: String,
    span_count: Int,
    event_count: Int,
    error_count: Int,
    has_errors: Bool,
    root_cause_label: String,
    rows: List(Row),
    axis_ticks: List(AxisTick),
  )
}

fn trace_detail_decoder() -> decode.Decoder(TraceDetail) {
  use trace_id <- decode.field("TraceID", decode.string)
  use short_trace_id <- decode.field("ShortTraceID", decode.string)
  use name <- decode.field("Name", decode.string)
  use started_at_label <- decode.field("StartedAtLabel", decode.string)
  use duration_label <- decode.field("DurationLabel", decode.string)
  use span_count <- decode.field("SpanCount", decode.int)
  use event_count <- decode.field("EventCount", decode.int)
  use error_count <- decode.field("ErrorCount", decode.int)
  use has_errors <- decode.field("HasErrors", decode.bool)
  use root_cause_label <- decode.field("RootCauseLabel", decode.string)
  use rows <- decode.field("Rows", nullable_list(row_decoder()))
  use axis_ticks <- decode.field(
    "AxisTicks",
    nullable_list(axis_tick_decoder()),
  )
  decode.success(TraceDetail(
    trace_id:,
    short_trace_id:,
    name:,
    started_at_label:,
    duration_label:,
    span_count:,
    event_count:,
    error_count:,
    has_errors:,
    root_cause_label:,
    rows:,
    axis_ticks:,
  ))
}

pub type AxisTick {
  AxisTick(left_pct: String, label: String)
}

fn axis_tick_decoder() -> decode.Decoder(AxisTick) {
  use left_pct <- decode.field("LeftPct", decode.string)
  use label <- decode.field("Label", decode.string)
  decode.success(AxisTick(left_pct:, label:))
}

pub type Row {
  Row(
    kind: String,
    span_id: String,
    event_ix: Int,
    short_span_id: String,
    name: String,
    tree_guides: List(TreeGuide),
    tree_failed: Bool,
    start_label: String,
    time_label: String,
    duration_label: String,
    left_pct: Float,
    width_pct: Float,
    failed: Bool,
    first_error: Bool,
    root: Bool,
    selected: Bool,
    collapsed: Bool,
    on_critical_path: Bool,
    pct_of_trace: String,
    attr_count: Int,
    event_count: Int,
    error_count: Int,
    event_markers: List(Marker),
    href: String,
    live_href: String,
  )
}

fn row_decoder() -> decode.Decoder(Row) {
  use kind <- decode.field("Kind", decode.string)
  use span_id <- decode.field("SpanID", decode.string)
  use event_ix <- decode.field("EventIX", decode.int)
  use short_span_id <- decode.field("ShortSpanID", decode.string)
  use name <- decode.field("Name", decode.string)
  use tree_guides <- decode.field(
    "TreeGuides",
    nullable_list(tree_guide_decoder()),
  )
  use tree_failed <- decode.field("TreeFailed", decode.bool)
  use start_label <- decode.field("StartLabel", decode.string)
  use time_label <- decode.field("TimeLabel", decode.string)
  use duration_label <- decode.field("DurationLabel", decode.string)
  use left_pct <- decode.field("LeftPct", decode.float)
  use width_pct <- decode.field("WidthPct", decode.float)
  use failed <- decode.field("Failed", decode.bool)
  use first_error <- decode.field("FirstError", decode.bool)
  use root <- decode.field("Root", decode.bool)
  use selected <- decode.field("Selected", decode.bool)
  use collapsed <- decode.field("Collapsed", decode.bool)
  use on_critical_path <- decode.field("OnCriticalPath", decode.bool)
  use pct_of_trace <- decode.field("PctOfTrace", decode.string)
  use attr_count <- decode.field("AttrCount", decode.int)
  use event_count <- decode.field("EventCount", decode.int)
  use error_count <- decode.field("ErrorCount", decode.int)
  use event_markers <- decode.field(
    "EventMarkers",
    nullable_list(marker_decoder()),
  )
  use href <- decode.field("Href", decode.string)
  use live_href <- decode.field("LiveHref", decode.string)
  decode.success(Row(
    kind:,
    span_id:,
    event_ix:,
    short_span_id:,
    name:,
    tree_guides:,
    tree_failed:,
    start_label:,
    time_label:,
    duration_label:,
    left_pct:,
    width_pct:,
    failed:,
    first_error:,
    root:,
    selected:,
    collapsed:,
    on_critical_path:,
    pct_of_trace:,
    attr_count:,
    event_count:,
    error_count:,
    event_markers:,
    href:,
    live_href:,
  ))
}

pub type Marker {
  Marker(left_pct: Float, name: String, time_label: String)
}

fn marker_decoder() -> decode.Decoder(Marker) {
  use left_pct <- decode.field("LeftPct", decode.float)
  use name <- decode.field("Name", decode.string)
  use time_label <- decode.field("TimeLabel", decode.string)
  decode.success(Marker(left_pct:, name:, time_label:))
}

pub type Ancestor {
  Ancestor(name: String, span_id: String, failed: Bool)
}

fn ancestor_decoder() -> decode.Decoder(Ancestor) {
  use name <- decode.field("Name", decode.string)
  use span_id <- decode.field("SpanID", decode.string)
  use failed <- decode.field("Failed", decode.bool)
  decode.success(Ancestor(name:, span_id:, failed:))
}

pub type ItemDetail {
  ItemDetail(
    kind: String,
    trace_id: String,
    span_id: String,
    short_span_id: String,
    parent_span_id: String,
    parent_name: String,
    name: String,
    service_name: String,
    operation_name: String,
    started_at_label: String,
    ended_at_label: String,
    time_label: String,
    duration_label: String,
    self_duration: String,
    pct_of_trace: String,
    child_count: Int,
    depth_in_tree: Int,
    failed: Bool,
    root: Bool,
    first_error: Bool,
    root_cause_summary: String,
    ancestry: List(Ancestor),
    overview: List(Attr),
    attr_groups: List(AttrGroup),
    event_count: Int,
    error_count: Int,
    events: List(Item),
    errors: List(Item),
  )
}

fn item_detail_decoder() -> decode.Decoder(ItemDetail) {
  use kind <- decode.field("Kind", decode.string)
  use trace_id <- decode.field("TraceID", decode.string)
  use span_id <- decode.field("SpanID", decode.string)
  use short_span_id <- decode.field("ShortSpanID", decode.string)
  use parent_span_id <- decode.field("ParentSpanID", decode.string)
  use parent_name <- decode.field("ParentName", decode.string)
  use name <- decode.field("Name", decode.string)
  use service_name <- decode.field("ServiceName", decode.string)
  use operation_name <- decode.field("OperationName", decode.string)
  use started_at_label <- decode.field("StartedAtLabel", decode.string)
  use ended_at_label <- decode.field("EndedAtLabel", decode.string)
  use time_label <- decode.field("TimeLabel", decode.string)
  use duration_label <- decode.field("DurationLabel", decode.string)
  use self_duration <- decode.field("SelfDuration", decode.string)
  use pct_of_trace <- decode.field("PctOfTrace", decode.string)
  use child_count <- decode.field("ChildCount", decode.int)
  use depth_in_tree <- decode.field("DepthInTree", decode.int)
  use failed <- decode.field("Failed", decode.bool)
  use root <- decode.field("Root", decode.bool)
  use first_error <- decode.field("FirstError", decode.bool)
  use root_cause_summary <- decode.field("RootCauseSummary", decode.string)
  use ancestry <- decode.field("Ancestry", nullable_list(ancestor_decoder()))
  use overview <- decode.field("Overview", nullable_list(attr_decoder()))
  use attr_groups <- decode.field(
    "AttrGroups",
    nullable_list(attr_group_decoder()),
  )
  use event_count <- decode.field("EventCount", decode.int)
  use error_count <- decode.field("ErrorCount", decode.int)
  use events <- decode.field("Events", nullable_list(item_decoder()))
  use errors <- decode.field("Errors", nullable_list(item_decoder()))
  decode.success(ItemDetail(
    kind:,
    trace_id:,
    span_id:,
    short_span_id:,
    parent_span_id:,
    parent_name:,
    name:,
    service_name:,
    operation_name:,
    started_at_label:,
    ended_at_label:,
    time_label:,
    duration_label:,
    self_duration:,
    pct_of_trace:,
    child_count:,
    depth_in_tree:,
    failed:,
    root:,
    first_error:,
    root_cause_summary:,
    ancestry:,
    overview:,
    attr_groups:,
    event_count:,
    error_count:,
    events:,
    errors:,
  ))
}

pub type AttrGroup {
  AttrGroup(name: String, attrs: List(Attr))
}

fn attr_group_decoder() -> decode.Decoder(AttrGroup) {
  use name <- decode.field("Name", decode.string)
  use attrs <- decode.field("Attrs", nullable_list(attr_decoder()))
  decode.success(AttrGroup(name:, attrs:))
}

pub type TreeGuide {
  TreeGuide(kind: String, failed: Bool)
}

fn tree_guide_decoder() -> decode.Decoder(TreeGuide) {
  use kind <- decode.field("Kind", decode.string)
  use failed <- decode.field("Failed", decode.bool)
  decode.success(TreeGuide(kind:, failed:))
}

pub type Attr {
  Attr(key: String, value: String)
}

fn attr_decoder() -> decode.Decoder(Attr) {
  use key <- decode.field("key", decode.string)
  use value <- decode.field("value", decode.string)
  decode.success(Attr(key:, value:))
}

pub type Item {
  Item(
    kind: String,
    time: String,
    name: String,
    error: String,
    level: String,
    msg: String,
    attrs: List(Attr),
  )
}

fn item_decoder() -> decode.Decoder(Item) {
  use kind <- decode.field("kind", decode.string)
  use time <- decode.field("time", decode.string)
  use name <- decode.optional_field("name", "", decode.string)
  use error <- decode.optional_field("error", "", decode.string)
  use level <- decode.optional_field("level", "", decode.string)
  use msg <- decode.optional_field("msg", "", decode.string)
  use attrs <- decode.optional_field("attrs", [], nullable_list(attr_decoder()))
  decode.success(Item(kind:, time:, name:, error:, level:, msg:, attrs:))
}

pub type NavLink {
  NavLink(href: String, label: String, key: String)
}

fn nav_link_decoder() -> decode.Decoder(NavLink) {
  use href <- decode.field("Href", decode.string)
  use label <- decode.field("Label", decode.string)
  use key <- decode.field("Key", decode.string)
  decode.success(NavLink(href:, label:, key:))
}
