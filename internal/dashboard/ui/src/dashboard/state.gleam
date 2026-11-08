import dashboard/prefetch
import dashboard/tooltip

// All application decisions live here. No browser calls, mutable state, or DOM.
import dashboard/cache_policy
import dashboard/format
import dashboard/project
import dashboard/protocol
import dashboard/query
import dashboard/search
import dashboard/search_cache
import dashboard/spans
import gleam/dict.{type Dict}
import gleam/dynamic/decode
import gleam/int
import gleam/json
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/result
import gleam/set.{type Set}
import gleam/string
import gleam/uri

pub type Connection {
  Paused
  Connecting
  Live
  Reconnecting
}

pub type Content {
  Loading
  Available(protocol.Page)
  Unavailable(String)
}

pub type InspectorTab {
  Overview
  Errors
  Attributes
  Events
}

pub type Pane {
  TraceList
  Waterfall
  Inspector
}

pub type Menu {
  NoMenu
  FiltersMenu
  SortMenu
  CacheMenu
}

pub type Drag {
  Idle
  Dragging(pane: Pane, origin: Int, width: Int)
}

pub type Draft {
  Draft(query: String, service: String, min_duration: String, failed_only: Bool)
}

pub type Model {
  Model(
    route: String,
    generation: Int,
    content: Content,
    connection: Connection,
    notice: String,
    tooltip: tooltip.Model,
    column_hover: tooltip.Model,
    preview_column: String,
    draft: Draft,
    inspector_tab: InspectorTab,
    active_pane: Pane,
    overview_open: Bool,
    menu: Menu,
    closed_groups: Set(String),
    sidebar_open: Bool,
    sidebar_width: Int,
    inspector_width: Int,
    preferred_sidebar_width: Option(Int),
    preferred_inspector_width: Option(Int),
    viewport: Int,
    drag: Drag,
    data: spans.Data,
    index: project.Index,
    source: String,
    refreshing: Bool,
    cache_available: Bool,
    cache_notice: String,
    range_mode: String,
    range_from: String,
    range_to: String,
    v3: Bool,
    search: search.Data,
    extra: Dict(String, String),
    detail_id: String,
    detail_revision: String,
    detail_parts: List(spans.Span),
    detail_loading: Bool,
    detail_complete: Bool,
    detail_notice: String,
    detail_summary: Option(search.Trace),
    cache_entries: List(search_cache.Entry),
    prefetch: prefetch.State,
    saved: Dict(String, String),
    saved_name: String,
    utc: Bool,
    previous_cursors: List(String),
  )
}

pub type Msg {
  Tooltip(tooltip.Msg)
  ColumnHover(tooltip.Msg)
  ColumnHoverEnded
  VisibleTraces(List(String))
  HoverTrace(String)
  PrefetchLoaded(Int, Int, Result(String, String))
  PrefetchDetail(Int, Int, Result(search.Detail, String))
  PrefetchStored(Int, Int, Result(List(search_cache.Entry), String))
  TraceDelta(Int, search.Change)
  Navigate(String)
  LocationChanged(String)
  Connected(Int)
  Disconnected(Int)
  Renew(Int)
  Retry(Int)
  ToggleLive
  QueryChanged(String)
  ServiceChanged(String)
  DurationChanged(String)
  FailedChanged(Bool)
  ApplyFilters
  ToggleSidebar
  ToggleOverview
  SelectTab(InspectorTab)
  ToggleMenu(Menu)
  ToggleGroup(String)
  FocusPane(Pane)
  KeyPressed(String, Bool)
  ResizeStarted(Pane, Int)
  PointerMoved(Int)
  PointerReleased
  ResetWidth(Pane)
  ViewportChanged(Int)
  Batch(Int, String, Result(spans.Frame, String))
  CacheLoaded(Int, Result(String, String))
  CacheStored(Result(List(cache_policy.Trace), String))
  CacheCleared(Result(Nil, String))
  ClearCache
  RangeMode(String)
  RangeFrom(String)
  RangeTo(String)
  ApplyRange
  LoadMore
  Tick
  RetryFetch(Int)
  SearchBatch(Int, Result(search.Frame, String))
  SearchPoll(Int)
  SourceMetadata(Int, Result(search.Metadata, String))
  TraceDetail(Int, Result(search.Detail, String))
  TraceChanged(Int, String)
  ExtraChanged(String, String)
  RefreshSearch
  PreviousInterval
  NextInterval
  SelectInterval(Int, Int)
  ToggleUTC
  NextSearchPage
  PreviousSearchPage
  SavedName(String)
  SaveSearch
  OpenSaved(String)
  DeleteSaved
  PinTrace
  ExportTrace
  SearchCacheLoaded(Int, String, String, Result(String, String))
  SearchCacheStored(Result(List(search_cache.Entry), String))
}

// Descriptions only. The application's effect interpreter executes these.
pub type Command {
  PeekTrace(String, String, Bool, Int, Int)
  FetchPrefetch(String, String, Int, Int)
  StorePrefetch(String, String, String, Int, Int, Int, Int)
  CancelPrefetch
  OpenStream(String, Int)
  CloseStream
  Fetch(String, Int)
  PushHistory(String)
  Persist(String, String)
  Focus(String)
  RevealSelection(String)
  After(Int, Msg)
  WatchPointer
  UnwatchPointer
  LoadCache(String, String, Int)
  StoreCache(String, String)
  EvictCache(List(String))
  ClearCacheStore
  FetchSearch(String, Int)
  FetchMetadata(Int)
  FetchTrace(String, String, Int)
  OpenSearchStream(String, Int)
  ReadSearchCache(String, String, String, Int)
  WriteSearchCache(String, String, String, String, Int, Int)
  SetTracePin(String, String, Bool)
  ClearSearchCache(String)
  DownloadTrace(String, String)
}

pub fn init(
  route: String,
  bootstrap: String,
  prefs: Dict(String, String),
  viewport: Int,
) -> #(Model, List(Command)) {
  let v3 =
    json.parse(
      bootstrap,
      decode.field("schema_version", decode.int, decode.success),
    )
    == Ok(3)
  let paused =
    dict.get(prefs, "liveUpdatesPaused") == Ok("true")
    || { v3 && query.get(route, "from") != "" }
  let model =
    Model(
      route: route,
      generation: 0,
      content: Loading,
      connection: case paused {
        True -> Paused
        False -> Connecting
      },
      notice: "",
      tooltip: tooltip.empty(),
      column_hover: tooltip.empty(),
      preview_column: "",
      draft: draft_from_route(route),
      inspector_tab: tab_from_string(
        dict.get(prefs, "inspectorTab") |> result.unwrap("overview"),
      ),
      active_pane: case query.get(route, "trace_id") {
        "" -> TraceList
        _ ->
          case query.get(route, "span_id") {
            "" -> Waterfall
            _ -> Inspector
          }
      },
      overview_open: True,
      menu: NoMenu,
      closed_groups: set.from_list(["Infrastructure", "Runtime"]),
      sidebar_open: dict.get(prefs, "sidebarOpen") != Ok("false"),
      sidebar_width: clamp_sidebar(
        pref_int(prefs, "sidebarWidth", viewport * 24 / 100),
        viewport,
      ),
      inspector_width: clamp_inspector(
        pref_int(prefs, "inspectorWidth", viewport * 28 / 100),
        viewport,
      ),
      preferred_sidebar_width: preferred_width(prefs, "sidebarWidth"),
      preferred_inspector_width: preferred_width(prefs, "inspectorWidth"),
      viewport: viewport,
      drag: Idle,
      data: spans.empty(),
      index: project.empty_index(),
      source: cache_policy.source(bootstrap),
      refreshing: True,
      cache_available: True,
      cache_notice: "",
      range_mode: case query.get(route, "from") {
        "" -> query.get(query.feed(route), "window")
        _ -> "custom"
      },
      range_from: format.input_zone(
        query.number(route, "from", 0),
        dict.get(prefs, "timezoneUTC") == Ok("true"),
      ),
      range_to: format.input_zone(
        query.number(route, "to", 0),
        dict.get(prefs, "timezoneUTC") == Ok("true"),
      ),
      v3: v3,
      search: search.empty(),
      extra: dict.from_list(query.params(route)),
      detail_id: "",
      detail_revision: "",
      detail_parts: [],
      detail_loading: False,
      detail_complete: False,
      detail_notice: "",
      detail_summary: None,
      cache_entries: [],
      prefetch: prefetch.empty(),
      saved: parse_saved(
        dict.get(prefs, "savedSearches") |> result.unwrap("{}"),
      ),
      saved_name: "",
      utc: dict.get(prefs, "timezoneUTC") == Ok("true"),
      previous_cursors: [],
    )
  case v3 {
    True -> #(
      reproject(Model(..model, data: spans.Data(..model.data, seq: 1))),
      [
        ReadSearchCache(model.source, "query", search_route(route), 0),
        FetchMetadata(0),
        After(1000, Tick),
      ],
    )
    False -> #(model, [
      LoadCache(model.source, query.feed(route), 0),
      After(1000, Tick),
    ])
  }
}

pub fn update(model: Model, msg: Msg) -> #(Model, List(Command)) {
  case msg {
    ColumnHoverEnded -> #(
      Model(..model, column_hover: tooltip.hide(model.column_hover)),
      [],
    )
    ColumnHover(message) -> {
      let #(hover, timer) = tooltip.update(model.column_hover, message)
      // Keep the last settled column readable while crossing to the side panel.
      let preview = case message, hover.visible {
        tooltip.Leave, _ -> ""
        _, True -> hover.value
        _, False -> model.preview_column
      }
      #(
        Model(..model, column_hover: hover, preview_column: preview),
        case timer {
          Some(token) -> [
            After(tooltip.delay_ms, ColumnHover(tooltip.Settle(token))),
          ]
          None -> []
        },
      )
    }
    Tooltip(message) -> {
      let #(hint, timer) = tooltip.update(model.tooltip, message)
      #(Model(..model, tooltip: hint), case timer {
        Some(token) -> [After(tooltip.delay_ms, Tooltip(tooltip.Settle(token)))]
        None -> []
      })
    }
    _ -> update_application(model, msg)
  }
}

fn update_application(model: Model, msg: Msg) -> #(Model, List(Command)) {
  let #(next, commands) = update_message(model, msg)
  let next = case next.route != model.route || next.source != model.source {
    True ->
      Model(
        ..next,
        tooltip: tooltip.hide(next.tooltip),
        column_hover: tooltip.hide(next.column_hover),
        preview_column: "",
      )
    False -> next
  }
  case next.v3 {
    False -> #(next, commands)
    True -> {
      let reset =
        next.generation != model.generation
        || next.source != model.source
        || next.search.frame.epoch != model.search.frame.epoch
        || msg == ClearCache
      let queue = case reset {
        True -> prefetch.reset(next.prefetch)
        False -> next.prefetch
      }
      let queue = prefetch.retain(queue, next.search.frame.order)
      let queue =
        prefetch.offer(
          queue,
          list.filter(next.search.frame.order, fn(id) { id != next.detail_id }),
          False,
        )
      let next = Model(..next, prefetch: queue)
      let commands = case reset {
        True -> [CancelPrefetch, ..commands]
        False -> commands
      }
      case next.detail_loading || !next.cache_available || msg == ClearCache {
        True -> #(next, commands)
        False -> {
          let #(queue, candidate) = prefetch.next(queue, next.detail_id)
          let next = Model(..next, prefetch: queue)
          case candidate {
            None -> #(next, commands)
            Some(c) -> #(
              next,
              list.append(commands, [
                PeekTrace(
                  next.source,
                  c.id,
                  c.urgent,
                  next.generation,
                  queue.token,
                ),
              ]),
            )
          }
        }
      }
    }
  }
}

fn update_message(model: Model, msg: Msg) -> #(Model, List(Command)) {
  case msg {
    Tooltip(_) | ColumnHover(_) | ColumnHoverEnded -> #(model, [])
    VisibleTraces(ids) -> #(
      Model(..model, prefetch: prefetch.visible(model.prefetch, ids)),
      [],
    )
    HoverTrace(id) ->
      case
        id != model.detail_id && list.contains(model.search.frame.order, id)
      {
        True -> #(
          Model(..model, prefetch: prefetch.offer(model.prefetch, [id], True)),
          [],
        )
        False -> #(model, [])
      }
    PrefetchLoaded(generation, token, result) ->
      case valid_prefetch(model, generation, token) {
        False -> #(model, [])
        True ->
          case result {
            Error(_) -> #(
              Model(
                ..model,
                prefetch: prefetch.finish(model.prefetch),
                cache_available: False,
              ),
              [],
            )
            Ok(raw) ->
              case json.parse(raw, decode.list(spans.span_decoder())) {
                Ok(_) -> #(
                  Model(..model, prefetch: prefetch.finish(model.prefetch)),
                  [],
                )
                Error(_) -> {
                  let assert Some(active) = model.prefetch.active
                  #(model, [FetchPrefetch(active.id, "", generation, token)])
                }
              }
          }
      }
    PrefetchDetail(generation, token, detail) ->
      case valid_prefetch(model, generation, token) {
        False -> #(model, [])
        True -> receive_prefetch(model, detail)
      }
    PrefetchStored(generation, token, result) ->
      case valid_prefetch(model, generation, token) {
        False -> #(model, [])
        True ->
          case result {
            Ok(entries) -> #(
              Model(
                ..model,
                cache_entries: entries,
                prefetch: prefetch.finish(model.prefetch),
              ),
              [],
            )
            Error(_) -> #(
              Model(..model, prefetch: prefetch.finish(model.prefetch)),
              [],
            )
          }
      }
    TraceDelta(generation, change)
      if generation == model.generation
      && change.id == model.detail_id
      && !model.detail_loading
    -> {
      case change.revision == model.detail_revision {
        True -> #(model, [])
        False ->
          case
            change.source == model.source
            && change.epoch == model.data.epoch
            && change.base == model.detail_revision
            && model.detail_complete
          {
            False -> update(model, TraceChanged(generation, change.id))
            True -> {
              let items =
                list.fold(change.removed, model.data.spans, fn(acc, id) {
                  dict.delete(acc, #(change.id, id))
                })
              let items =
                list.fold(change.upserts, items, fn(acc, p) {
                  dict.insert(acc, #(p.trace_id, p.id), p)
                })
              let data = spans.Data(..model.data, spans: items)
              let next =
                reproject(
                  Model(
                    ..model,
                    data: data,
                    index: project.index(data),
                    detail_revision: change.revision,
                    detail_summary: Some(change.summary),
                  ),
                )
              cache_write(
                next,
                "trace",
                change.id,
                spans.encode(dict.values(items)),
                dict.size(items),
              )
            }
          }
      }
    }
    TraceDelta(_, _) -> #(model, [])
    SearchBatch(generation, _) if generation != model.generation -> #(model, [])
    SearchBatch(_, Error(message)) ->
      case string.contains(message, "(409)") {
        True ->
          navigate_search(
            model,
            query.set(model.route, [
              #("cursor", ""),
              #("refresh", int.to_string(model.data.now)),
            ]),
            False,
          )
        False -> #(Model(..model, notice: message, refreshing: False), [
          After(2000, SearchPoll(model.generation)),
        ])
      }
    SearchBatch(_, Ok(frame)) -> receive_search(model, frame)
    SearchPoll(generation) if generation == model.generation -> #(model, [
      FetchSearch(search_resume(model), generation),
    ])
    SearchPoll(_) -> #(model, [])
    SourceMetadata(generation, Ok(metadata)) if generation == model.generation -> #(
      reproject(
        Model(
          ..model,
          search: search.Data(
            ..model.search,
            services: metadata.services,
            operations: metadata.operations,
            attributes: metadata.attributes,
          ),
        ),
      ),
      [],
    )
    SourceMetadata(_, _) -> #(model, [])
    TraceDetail(generation, _) if generation != model.generation -> #(model, [])
    TraceDetail(_, Error(message)) -> #(
      Model(
        ..model,
        detail_loading: False,
        detail_notice: message,
        detail_parts: [],
      ),
      [],
    )
    TraceDetail(_, Ok(detail)) -> receive_detail(model, detail)
    TraceChanged(generation, id)
      if generation == model.generation
      && id == model.detail_id
      && !model.detail_loading
    -> #(
      Model(
        ..model,
        detail_loading: True,
        detail_parts: [],
        detail_revision: "",
      ),
      [FetchTrace(id, "", generation)],
    )
    TraceChanged(_, _) -> #(model, [])
    ExtraChanged(key, value) -> #(
      Model(..model, extra: dict.insert(model.extra, key, value)),
      [],
    )
    RefreshSearch -> {
      let #(next, commands) =
        navigate_search(
          model,
          query.set(model.route, [
            #("refresh", int.to_string(model.data.now)),
            #("cursor", ""),
          ]),
          False,
        )
      case next.detail_id == "" {
        True -> #(next, commands)
        False -> #(
          Model(
            ..next,
            detail_loading: True,
            detail_parts: [],
            detail_revision: "",
          ),
          list.append(commands, [
            FetchTrace(next.detail_id, "", next.generation),
          ]),
        )
      }
    }
    PreviousInterval -> shift_interval(model, -1)
    NextInterval -> shift_interval(model, 1)
    SelectInterval(from, to) ->
      navigate_search(
        model,
        query.set(model.route, [
          #("from", int.to_string(from)),
          #("to", int.to_string(to)),
          #("window", ""),
          #("cursor", ""),
        ]),
        True,
      )
    ToggleUTC -> {
      let utc = !model.utc
      let from = format.parse_zone(model.range_from, model.utc)
      let to = format.parse_zone(model.range_to, model.utc)
      #(
        Model(
          ..model,
          utc: utc,
          range_from: format.input_zone(from, utc),
          range_to: format.input_zone(to, utc),
        ),
        [Persist("timezoneUTC", bool_string(utc))],
      )
    }
    NextSearchPage -> {
      let cursor = query.get(model.route, "cursor")
      navigate_search(
        Model(..model, previous_cursors: [cursor, ..model.previous_cursors]),
        query.set(model.route, [#("cursor", model.search.frame.cursor)]),
        True,
      )
    }
    PreviousSearchPage -> {
      let cursor = list.first(model.previous_cursors) |> result.unwrap("")
      navigate_search(
        Model(..model, previous_cursors: list.drop(model.previous_cursors, 1)),
        query.set(model.route, [#("cursor", cursor)]),
        True,
      )
    }
    SavedName(name) -> #(Model(..model, saved_name: name), [])
    SaveSearch -> {
      let name = string.trim(model.saved_name)
      case name == "" {
        True -> #(Model(..model, notice: "Name the saved search"), [])
        False -> {
          let saved =
            dict.insert(
              model.saved,
              name,
              query.set(model.route, [#("cursor", ""), #("refresh", "")]),
            )
          #(Model(..model, saved: saved), [
            Persist("savedSearches", encode_saved(saved)),
          ])
        }
      }
    }
    OpenSaved(name) ->
      case dict.get(model.saved, name) {
        Ok(route) ->
          navigate_search(Model(..model, saved_name: name), route, True)
        Error(_) -> #(model, [])
      }
    DeleteSaved -> {
      let saved = dict.delete(model.saved, model.saved_name)
      #(Model(..model, saved: saved, saved_name: ""), [
        Persist("savedSearches", encode_saved(saved)),
      ])
    }
    PinTrace -> {
      case model.detail_complete && !model.detail_loading {
        True -> #(model, [
          SetTracePin(model.source, model.detail_id, !trace_pinned(model)),
        ])
        False -> #(
          Model(..model, cache_notice: "Load a complete trace before pinning"),
          [],
        )
      }
    }
    ExportTrace ->
      case model.detail_complete {
        True -> #(model, [
          DownloadTrace(model.detail_id, spans.encode(spans.all(model.data))),
        ])
        False -> #(
          Model(
            ..model,
            detail_notice: "Wait for a complete trace before exporting",
          ),
          [],
        )
      }
    SearchCacheLoaded(generation, _, _, _) if generation != model.generation -> #(
      model,
      [],
    )
    SearchCacheLoaded(_, kind, id, result) ->
      receive_cache(model, kind, id, result)
    SearchCacheStored(Ok(entries)) -> #(
      Model(
        ..model,
        cache_entries: entries,
        cache_notice: "",
        cache_available: True,
      ),
      [],
    )
    SearchCacheStored(Error(message)) -> #(
      Model(..model, cache_notice: message),
      [],
    )
    ClearCache if model.v3 -> #(Model(..model, cache_entries: []), [
      ClearSearchCache(model.source),
    ])
    ApplyFilters if model.v3 -> {
      let pairs =
        list.map(
          ["operation", "max_dur", "errors", "attr", "exact_trace"],
          fn(key) { #(key, dict.get(model.extra, key) |> result.unwrap("")) },
        )
      navigate_search(
        model,
        query.set(query.set(model.route, pairs), [
          #("q", model.draft.query),
          #("service", model.draft.service),
          #("min_dur", model.draft.min_duration),
          #("failed_only", ""),
          #("cursor", ""),
        ]),
        True,
      )
    }
    ApplyRange if model.v3 -> {
      let from = format.parse_zone(model.range_from, model.utc)
      let to = format.parse_zone(model.range_to, model.utc)
      case model.range_mode == "custom" && { from < 0 || to <= from } {
        True -> #(
          Model(..model, notice: "Choose a valid start and end time"),
          [],
        )
        False ->
          navigate_search(
            model,
            query.set(model.route, [
              #("from", case model.range_mode {
                "custom" -> int.to_string(from)
                _ -> ""
              }),
              #("to", case model.range_mode {
                "custom" -> int.to_string(to)
                _ -> ""
              }),
              #("window", case model.range_mode {
                "custom" -> ""
                other -> other
              }),
              #("cursor", ""),
              #("refresh", ""),
            ]),
            True,
          )
      }
    }
    ToggleLive if model.v3 -> {
      case model.connection {
        Paused -> {
          let #(next, _) =
            navigate_search(
              Model(..model, connection: Connecting),
              query.set(model.route, [
                #("from", ""),
                #("to", ""),
                #("window", "900000"),
                #("cursor", ""),
              ]),
              True,
            )
          #(next, [
            CloseStream,
            FetchSearch(search_resume(next), next.generation),
            PushHistory(next.route),
            Persist("liveUpdatesPaused", "false"),
          ])
        }
        _ -> #(
          Model(
            ..model,
            connection: Paused,
            refreshing: False,
            generation: model.generation + 1,
            detail_loading: False,
            detail_parts: [],
            detail_revision: "",
          ),
          [CloseStream, Persist("liveUpdatesPaused", "true")],
        )
      }
    }
    Connected(generation)
      if model.v3 && generation == model.generation && model.connection != Paused
    -> #(Model(..model, connection: Live), [
      FetchSearch(search_resume(model), generation),
      After(30_000, Renew(generation)),
    ])
    Renew(generation)
      if model.v3 && generation == model.generation && model.connection != Paused
    -> #(model, [OpenSearchStream(search_resume(model), generation)])
    Retry(generation)
      if model.v3
      && generation == model.generation
      && model.connection == Reconnecting
    -> #(Model(..model, connection: Connecting), [
      FetchSearch(search_resume(model), generation),
    ])
    Tick -> {
      let data = spans.Data(..model.data, now: model.data.now + 1000)
      let model = Model(..model, data: data)
      let content = case model.content {
        Available(page) ->
          Available(case model.v3 {
            True -> search.page(model.search, page, model.route, data.now)
            False -> project.clock(page, data, model.index)
          })
        other -> other
      }
      #(Model(..model, content: content), [After(1000, Tick)])
    }
    RetryFetch(generation)
      if generation == model.generation && model.notice != ""
    -> #(model, [Fetch(resume_route(model), generation)])
    RetryFetch(_) -> #(model, [])
    CacheLoaded(generation, _) if generation != model.generation -> #(model, [])
    CacheLoaded(_, result) -> {
      let model = case result {
        Ok(raw) ->
          case spans.parse(raw) {
            Ok(frame) ->
              case spans.apply(spans.empty(), frame) {
                Ok(data) ->
                  reproject(
                    Model(
                      ..model,
                      data: data,
                      index: project.index(data),
                      refreshing: True,
                    ),
                  )
                Error(_) -> model
              }
            Error(_) -> model
          }
        Error(message) ->
          Model(..model, cache_available: False, cache_notice: message)
      }
      #(model, [Fetch(resume_route(model), model.generation)])
    }
    Batch(generation, _, _) if generation != model.generation -> #(model, [])
    Batch(_, _, Error(message)) -> #(
      Model(
        ..model,
        notice: message,
        refreshing: False,
        content: case model.content {
          Available(_) -> model.content
          _ -> Unavailable(message <> " · retrying…")
        },
      ),
      [After(2000, RetryFetch(model.generation))],
    )
    Batch(_, raw, Ok(frame)) -> {
      case spans.apply(model.data, frame) {
        Error(_) -> #(model, [Fetch(resume_route(model), model.generation)])
        Ok(data) -> {
          let changed = data != model.data
          let next =
            Model(
              ..model,
              data: data,
              source: frame.source,
              refreshing: False,
              notice: "",
            )
          let next = case changed {
            True ->
              reproject(
                Model(
                  ..next,
                  index: project.update_index(model.index, data, frame),
                ),
              )
            False -> next
          }
          let start = case
            model.connection == Connecting
            && model.data.seq == 0
            || model.connection == Connecting
            && { model.refreshing || model.notice != "" }
          {
            True -> [OpenStream(resume_route(next), next.generation)]
            False -> []
          }
          let persist = case changed && model.cache_available {
            True -> [StoreCache(query.feed(model.route), raw)]
            False -> []
          }
          let reveal = case
            selection_key(model.content) != selection_key(next.content)
          {
            True -> [RevealSelection(pane_id(next.active_pane))]
            False -> []
          }
          let wanted = query.get(next.route, "trace_id")
          case
            wanted != ""
            && !set.contains(data.members, wanted)
            && query.get(next.route, "include_trace") != wanted
            && query.get(next.route, "exact_trace") == ""
          {
            True -> {
              let #(next, commands) =
                navigate_query(
                  next,
                  query.set(next.route, [#("include_trace", wanted)]),
                  [],
                )
              #(next, list.append(persist, commands))
            }
            False -> #(next, list.append(reveal, list.append(start, persist)))
          }
        }
      }
    }
    CacheStored(Ok(inventory)) -> {
      let victims =
        cache_policy.evict(
          inventory,
          model.data.now,
          query.get(model.route, "trace_id"),
        )
      #(model, case victims {
        [] -> []
        _ -> [EvictCache(victims)]
      })
    }
    CacheStored(Error(message)) -> #(
      Model(..model, cache_available: False, cache_notice: message),
      [],
    )
    ClearCache -> #(model, [ClearCacheStore])
    CacheCleared(Ok(_)) -> {
      let next =
        Model(
          ..model,
          generation: model.generation + 1,
          data: spans.empty(),
          cache_available: True,
          cache_notice: "Cache cleared",
          refreshing: True,
          connection: case model.connection {
            Paused -> Paused
            _ -> Connecting
          },
        )
      #(next, [CloseStream, Fetch(resume_route(next), next.generation)])
    }
    CacheCleared(Error(message)) -> #(
      Model(..model, cache_available: False, cache_notice: message),
      [],
    )
    RangeMode(mode) -> #(Model(..model, range_mode: mode), [])
    RangeFrom(value) -> #(Model(..model, range_from: value), [])
    RangeTo(value) -> #(Model(..model, range_to: value), [])
    ApplyRange -> {
      let from = format.parse_time(model.range_from)
      let to = format.parse_time(model.range_to)
      case
        model.range_mode == "custom"
        && { from < 0 || to <= from || to - from > 86_400_000 }
      {
        True -> #(
          Model(
            ..model,
            notice: "Choose a valid time range of at most 24 hours",
          ),
          [],
        )
        False ->
          navigate(
            model,
            query.set(model.route, [
              #("window", case model.range_mode {
                "custom" -> ""
                other -> other
              }),
              #("from", case model.range_mode {
                "custom" -> int.to_string(from)
                _ -> ""
              }),
              #("to", case model.range_mode {
                "custom" -> int.to_string(to)
                _ -> ""
              }),
              #("page", ""),
              #("detail_page", ""),
              #("limit", "100"),
              #("trace_id", ""),
              #("span_id", ""),
              #("exact_trace", ""),
              #("include_trace", ""),
            ]),
            True,
          )
      }
    }
    LoadMore ->
      navigate(
        model,
        query.set(model.route, [
          #(
            "limit",
            int.to_string(int.min(
              10_000,
              query.number(model.route, "limit", 100) + 100,
            )),
          ),
        ]),
        True,
      )
    Navigate(route) -> navigate(model, route, True)
    LocationChanged(route) -> navigate(model, route, False)
    Connected(generation)
      if generation == model.generation && model.connection != Paused
    -> #(Model(..model, connection: Live), [
      Fetch(resume_route(model), generation),
      After(30_000, Renew(generation)),
    ])
    Disconnected(generation)
      if generation == model.generation && model.connection != Paused
    -> {
      let generation = generation + 1
      #(Model(..model, connection: Reconnecting, generation: generation), [
        CloseStream,
        After(1000, Retry(generation)),
      ])
    }
    Renew(generation)
      if generation == model.generation && model.connection != Paused
    -> {
      let generation = generation + 1
      #(Model(..model, generation: generation, connection: Connecting), [
        OpenStream(resume_route(model), generation),
      ])
    }
    Retry(generation)
      if generation == model.generation && model.connection == Reconnecting
    -> #(Model(..model, connection: Connecting), [
      OpenStream(resume_route(model), generation),
    ])
    Connected(_) | Disconnected(_) | Renew(_) | Retry(_) -> #(model, [])
    ToggleLive -> {
      let generation = model.generation + 1
      case model.connection {
        Paused -> #(
          Model(..model, generation: generation, connection: Connecting),
          [
            Persist("liveUpdatesPaused", "false"),
            OpenStream(resume_route(model), generation),
          ],
        )
        _ -> #(Model(..model, generation: generation, connection: Paused), [
          CloseStream,
          Persist("liveUpdatesPaused", "true"),
        ])
      }
    }
    QueryChanged(value) -> #(
      Model(..model, draft: Draft(..model.draft, query: value)),
      [],
    )
    ServiceChanged(value) -> #(
      Model(..model, draft: Draft(..model.draft, service: value)),
      [],
    )
    DurationChanged(value) -> #(
      Model(..model, draft: Draft(..model.draft, min_duration: value)),
      [],
    )
    FailedChanged(value) -> #(
      Model(..model, draft: Draft(..model.draft, failed_only: value)),
      [],
    )
    ApplyFilters ->
      navigate(model, filter_route(model.route, model.draft), True)
    ToggleOverview -> #(
      Model(..model, overview_open: !model.overview_open, menu: NoMenu),
      [],
    )
    ToggleSidebar -> {
      let opened = !model.sidebar_open
      #(
        Model(..model, sidebar_open: opened, active_pane: case opened {
          False if model.active_pane == TraceList -> Waterfall
          _ -> model.active_pane
        }),
        [Persist("sidebarOpen", bool_string(opened))],
      )
    }
    SelectTab(tab) -> #(Model(..model, inspector_tab: tab), [
      Persist("inspectorTab", tab_name(tab)),
    ])
    ToggleMenu(menu) -> #(
      Model(..model, menu: case model.menu == menu {
        True -> NoMenu
        False -> menu
      }),
      [],
    )
    ToggleGroup(name) -> {
      let groups = case set.contains(model.closed_groups, name) {
        True -> set.delete(model.closed_groups, name)
        False -> set.insert(model.closed_groups, name)
      }
      #(Model(..model, closed_groups: groups), [])
    }
    FocusPane(pane) -> #(Model(..model, active_pane: pane), [])
    KeyPressed("Tab", shift) -> cycle_pane(model, shift)
    KeyPressed("j", _) | KeyPressed("ArrowDown", _) -> move_selection(model, 1)
    KeyPressed("k", _) | KeyPressed("ArrowUp", _) -> move_selection(model, -1)
    KeyPressed("Escape", _) -> #(
      Model(
        ..model,
        menu: NoMenu,
        drag: Idle,
        tooltip: tooltip.hide(model.tooltip),
        column_hover: tooltip.hide(model.column_hover),
        preview_column: "",
      ),
      [
        UnwatchPointer,
      ],
    )
    KeyPressed(_, _) -> #(model, [])
    ResizeStarted(pane, x) -> {
      let width = case pane {
        TraceList -> model.sidebar_width
        _ -> model.inspector_width
      }
      #(Model(..model, drag: Dragging(pane, x, width)), [WatchPointer])
    }
    PointerMoved(x) ->
      case model.drag {
        Idle -> #(model, [])
        Dragging(TraceList, origin, width) -> #(
          Model(
            ..model,
            sidebar_width: clamp_sidebar(width + x - origin, model.viewport),
          ),
          [],
        )
        Dragging(_, origin, width) -> {
          let width = clamp_inspector(width + origin - x, model.viewport)
          #(
            Model(..model, inspector_width: case width < 40 {
              True -> 0
              False -> width
            }),
            [],
          )
        }
      }
    PointerReleased ->
      case model.drag {
        Idle -> #(model, [])
        Dragging(pane, _, _) -> #(
          Model(
            ..model,
            drag: Idle,
            preferred_sidebar_width: case pane {
              TraceList -> Some(model.sidebar_width)
              _ -> model.preferred_sidebar_width
            },
            preferred_inspector_width: case pane {
              Inspector -> Some(model.inspector_width)
              _ -> model.preferred_inspector_width
            },
          ),
          [
            UnwatchPointer,
            Persist(
              case pane {
                TraceList -> "sidebarWidth"
                _ -> "inspectorWidth"
              },
              int.to_string(case pane {
                TraceList -> model.sidebar_width
                _ -> model.inspector_width
              }),
            ),
          ],
        )
      }
    ResetWidth(TraceList) -> {
      let width = clamp_sidebar(model.viewport * 24 / 100, model.viewport)
      #(
        Model(
          ..model,
          sidebar_width: width,
          preferred_sidebar_width: Some(width),
        ),
        [
          Persist("sidebarWidth", int.to_string(width)),
        ],
      )
    }
    ResetWidth(_) -> {
      let width = clamp_inspector(model.viewport * 28 / 100, model.viewport)
      #(
        Model(
          ..model,
          inspector_width: width,
          preferred_inspector_width: Some(width),
        ),
        [
          Persist("inspectorWidth", int.to_string(width)),
        ],
      )
    }
    ViewportChanged(width) -> #(
      Model(
        ..model,
        viewport: width,
        sidebar_width: clamp_sidebar(
          option.unwrap(model.preferred_sidebar_width, width * 24 / 100),
          width,
        ),
        inspector_width: clamp_inspector(
          option.unwrap(model.preferred_inspector_width, width * 28 / 100),
          width,
        ),
      ),
      [],
    )
  }
}

fn navigate(
  model: Model,
  route: String,
  push: Bool,
) -> #(Model, List(Command)) {
  // Display-only navigation owns neither the search nor the trace transfer.
  // This also handles Back/Forward between display modes without invalidating
  // callbacks, drafts, or the selected event.
  case model.v3 && trace_view_route(route) == trace_view_route(model.route) {
    True -> #(
      reproject(Model(..model, route: route)),
      case push && route != model.route {
        True -> [PushHistory(route)]
        False -> []
      },
    )
    False -> {
      let model = selection_pane(model, route)
      case model.v3 {
        True -> navigate_search(model, route, push)
        False -> navigate_legacy(model, route, push)
      }
    }
  }
}

fn trace_view_route(route: String) -> List(#(String, String)) {
  query.params(route)
  |> list.filter(fn(pair) { !list.contains(["events", "error_path"], pair.0) })
  |> list.sort(fn(a, b) { string.compare(a.0, b.0) })
}

fn selection_pane(model: Model, route: String) -> Model {
  let event = query.get(route, "event_ix")
  let changed =
    query.get(route, "span_id") != query.get(model.route, "span_id")
    || event != query.get(model.route, "event_ix")
  Model(
    ..model,
    active_pane: case True {
      _ if changed -> Inspector
      _ ->
        case
          query.get(route, "trace_id") != query.get(model.route, "trace_id")
        {
          True -> Waterfall
          False -> model.active_pane
        }
    },
    inspector_tab: case changed && event != "" {
      True -> Overview
      False -> model.inspector_tab
    },
  )
}

fn navigate_legacy(
  model: Model,
  route: String,
  push: Bool,
) -> #(Model, List(Command)) {
  let wanted = query.get(route, "trace_id")
  let route = case
    model.data.seq > 0
    && wanted != ""
    && !set.contains(model.data.members, wanted)
    && query.get(route, "include_trace") != wanted
  {
    True -> query.set(route, [#("include_trace", wanted)])
    False -> route
  }
  let same_query = query.feed(model.route) == query.feed(route)
  let history = case push {
    True -> [PushHistory(route)]
    False -> []
  }
  case same_query && model.data.seq > 0 {
    True -> #(
      reproject(
        Model(
          ..model,
          route: route,
          menu: NoMenu,
          draft: draft_from_route(route),
        ),
      ),
      history,
    )
    False -> navigate_query(model, route, history)
  }
}

fn navigate_query(
  model: Model,
  route: String,
  history: List(Command),
) -> #(Model, List(Command)) {
  let generation = model.generation + 1
  let next =
    Model(
      ..model,
      route: route,
      generation: generation,
      draft: draft_from_route(route),
      range_mode: case query.get(route, "from") {
        "" -> query.get(query.feed(route), "window")
        _ -> "custom"
      },
      range_from: format.input_time(query.number(route, "from", 0)),
      range_to: format.input_time(query.number(route, "to", 0)),
      menu: NoMenu,
      notice: "",
      refreshing: True,
      connection: case model.connection {
        Paused -> Paused
        _ -> Connecting
      },
    )
  let commands = [
    CloseStream,
    case model.data.seq > 0 {
      True -> Fetch(resume_route(next), generation)
      False -> LoadCache(model.source, query.feed(route), generation)
    },
  ]
  #(next, list.append(history, commands))
}

fn resume_route(model: Model) -> String {
  query.set(query.checkpoint(model.route, model.data.epoch, model.data.seq), [
    #("query_token", model.data.query),
  ])
}

fn reproject(model: Model) -> Model {
  case model.v3 {
    True ->
      Model(
        ..model,
        content: Available(search.page(
          model.search,
          project.page_index(model.data, model.route, model.index),
          model.route,
          model.data.now,
        )),
      )
    False -> reproject_legacy(model)
  }
}

fn reproject_legacy(model: Model) -> Model {
  case model.data.seq > 0 {
    True ->
      Model(
        ..model,
        content: Available(project.page_index(
          model.data,
          model.route,
          model.index,
        )),
      )
    False -> model
  }
}

pub fn route_params(route: String) -> List(#(String, String)) {
  let query =
    string.split_once(route, "?")
    |> result.map(fn(pair) { pair.1 })
    |> result.unwrap("")
  uri.parse_query(query) |> result.unwrap([])
}

fn draft_from_route(route: String) -> Draft {
  let params = dict.from_list(route_params(route))
  Draft(
    dict.get(params, "q") |> result.unwrap(""),
    dict.get(params, "service") |> result.unwrap(""),
    dict.get(params, "min_dur") |> result.unwrap(""),
    dict.get(params, "failed_only") |> result.unwrap("") != "",
  )
}

pub fn filter_route(route: String, draft: Draft) -> String {
  let retained =
    route_params(route)
    |> list.filter(fn(pair) {
      !list.contains(
        [
          "q",
          "service",
          "min_dur",
          "failed_only",
          "trace_id",
          "include_trace",
          "exact_trace",
          "span_id",
          "event_ix",
          "page",
          "detail_page",
        ],
        pair.0,
      )
    })
  let params =
    list.append(retained, [
      #("q", string.trim(draft.query)),
      #("service", draft.service),
      #("min_dur", draft.min_duration),
      #("failed_only", case draft.failed_only {
        True -> "1"
        False -> ""
      }),
    ])
    |> list.filter(fn(pair) { pair.1 != "" })
  "/dashboard?" <> uri.query_to_string(params)
}

pub fn paging_href(href: String, pane: Pane) -> String {
  let params =
    route_params(href)
    |> list.filter(fn(pair) {
      case pane {
        TraceList ->
          !list.contains(
            ["trace_id", "span_id", "event_ix", "detail_page"],
            pair.0,
          )
        _ -> !list.contains(["span_id", "event_ix"], pair.0)
      }
    })
  "/dashboard?" <> uri.query_to_string(params)
}

fn move_selection(model: Model, direction: Int) -> #(Model, List(Command)) {
  let items = case model.content {
    Available(page) ->
      case model.active_pane {
        TraceList ->
          list.map(page.traces, fn(trace) { #(trace.selected, trace.href) })
        Waterfall ->
          case page.selected_trace {
            None -> []
            Some(trace) ->
              trace.rows
              |> list.filter(fn(row) { !row.collapsed })
              |> list.map(fn(row) { #(row.selected, row.href) })
          }
        Inspector -> []
      }
    _ -> []
  }
  let index = index_where(items, fn(pair) { pair.0 })
  let next = int.clamp(index + direction, 0, int.max(0, list.length(items) - 1))
  case items |> list.drop(next) |> list.first {
    Ok(#(_, href)) if next != index -> navigate(model, href, True)
    _ -> #(model, [])
  }
}

fn cycle_pane(model: Model, backwards: Bool) -> #(Model, List(Command)) {
  let panes =
    list.append(
      case model.sidebar_open {
        True -> [TraceList]
        False -> []
      },
      list.append([Waterfall], case model.inspector_width > 0 {
        True -> [Inspector]
        False -> []
      }),
    )
  let index = int.max(0, index_where(panes, fn(p) { p == model.active_pane }))
  let direction = case backwards {
    True -> -1
    False -> 1
  }
  let next =
    panes
    |> list.drop(
      { index + direction + list.length(panes) } % list.length(panes),
    )
    |> list.first
    |> result.unwrap(Waterfall)
  #(Model(..model, active_pane: next), [Focus(pane_id(next))])
}

fn selection_key(content: Content) -> String {
  case content {
    Available(page) ->
      case page.selected_item {
        Some(item) ->
          item.trace_id
          <> ":"
          <> item.span_id
          <> ":"
          <> item.kind
          <> ":"
          <> item.time_label
        None -> ""
      }
    _ -> ""
  }
}

fn index_where(items: List(a), predicate: fn(a) -> Bool) -> Int {
  items
  |> list.index_map(fn(item, index) { #(index, predicate(item)) })
  |> list.find(fn(pair) { pair.1 })
  |> result.map(fn(pair) { pair.0 })
  |> result.unwrap(-1)
}

pub fn pane_id(pane: Pane) -> String {
  case pane {
    TraceList -> "trace-list"
    Waterfall -> "trace-detail"
    Inspector -> "span-detail"
  }
}

pub fn tab_name(tab: InspectorTab) -> String {
  case tab {
    Overview -> "overview"
    Errors -> "errors"
    Attributes -> "attributes"
    Events -> "events"
  }
}

fn tab_from_string(tab: String) -> InspectorTab {
  case tab {
    "errors" -> Errors
    "attributes" -> Attributes
    "events" -> Events
    _ -> Overview
  }
}

fn pref_int(prefs: Dict(String, String), key: String, fallback: Int) -> Int {
  dict.get(prefs, key) |> result.then(int.parse) |> result.unwrap(fallback)
}

fn preferred_width(prefs: Dict(String, String), key: String) -> Option(Int) {
  case dict.get(prefs, key) |> result.then(int.parse) {
    Ok(width) -> Some(width)
    Error(_) -> None
  }
}

pub fn clamp_sidebar(width: Int, viewport: Int) -> Int {
  int.clamp(width, 220, int.min(420, int.max(220, viewport * 45 / 100)))
}

pub fn clamp_inspector(width: Int, viewport: Int) -> Int {
  int.clamp(width, 0, viewport / 2)
}

pub fn bool_string(value: Bool) -> String {
  case value {
    True -> "true"
    False -> "false"
  }
}

pub fn search_route(route: String) -> String {
  query.params(route)
  |> list.filter(fn(p) {
    list.contains(
      [
        "q",
        "service",
        "operation",
        "min_dur",
        "max_dur",
        "errors",
        "failed_only",
        "attr",
        "exact_trace",
        "from",
        "to",
        "window",
        "sort",
        "cursor",
        "refresh",
      ],
      p.0,
    )
  })
  |> list.sort(fn(a, b) { string.compare(a.0, b.0) })
  |> query.build
}

fn search_resume(model: Model) -> String {
  query.set(search_route(model.route), [
    #("epoch", model.search.frame.epoch),
    #("query_token", model.search.frame.query),
    #("after", int.to_string(model.search.frame.seq)),
    #("trace_id", query.get(model.route, "trace_id")),
  ])
}

fn navigate_search(
  model: Model,
  route: String,
  push: Bool,
) -> #(Model, List(Command)) {
  let same = search_route(route) == search_route(model.route)
  let id = query.get(route, "trace_id")
  let selected = id == model.detail_id
  let generation = model.generation + 1
  let data = case selected {
    True -> model.data
    False -> spans.Data(..spans.empty(), now: model.data.now, seq: 1)
  }
  let next =
    Model(
      ..model,
      route: route,
      generation: generation,
      data: data,
      index: case selected {
        True -> model.index
        False -> project.empty_index()
      },
      search: case same {
        True -> model.search
        False ->
          search.Data(
            ..search.empty(),
            services: model.search.services,
            operations: model.search.operations,
            attributes: model.search.attributes,
          )
      },
      draft: draft_from_route(route),
      extra: dict.from_list(query.params(route)),
      menu: NoMenu,
      notice: "",
      refreshing: !same,
      detail_id: case selected {
        True -> model.detail_id
        False -> id
      },
      detail_loading: !selected && id != "",
      detail_complete: selected && model.detail_complete,
      detail_parts: [],
      detail_revision: case selected {
        True -> model.detail_revision
        False -> ""
      },
      detail_notice: "",
      range_mode: case query.get(route, "from") {
        "" ->
          case query.get(route, "window") {
            "" -> "900000"
            w -> w
          }
        _ -> "custom"
      },
      range_from: format.input_zone(query.number(route, "from", 0), model.utc),
      range_to: format.input_zone(query.number(route, "to", 0), model.utc),
      connection: case
        query.get(route, "from") != "" || model.connection == Paused
      {
        True -> Paused
        False -> Connecting
      },
    )
  let commands = [
    CloseStream,
    ..case push {
      True -> [PushHistory(route)]
      False -> []
    }
  ]
  let commands =
    list.append(commands, case same {
      True ->
        case next.connection {
          Paused -> []
          _ -> [OpenSearchStream(search_resume(next), generation)]
        }
      False -> [
        ReadSearchCache(model.source, "query", search_route(route), generation),
        FetchMetadata(generation),
      ]
    })
  let commands =
    list.append(commands, case !selected && id != "" {
      True -> [ReadSearchCache(model.source, "trace", id, generation)]
      False -> []
    })
  #(reproject(next), commands)
}

fn receive_search(
  model: Model,
  frame: search.Frame,
) -> #(Model, List(Command)) {
  case search.apply(model.search, frame) {
    Error(_) -> #(Model(..model, search: search.empty()), [
      FetchSearch(search_route(model.route), model.generation),
    ])
    Ok(data) -> {
      let wanted = case query.get(model.route, "trace_id") {
        "" -> list.first(frame.order) |> result.unwrap("")
        id -> id
      }
      let changed_source =
        model.search.frame.epoch != ""
        && model.search.frame.epoch != frame.epoch
      let load = wanted != "" && { wanted != model.detail_id || changed_source }
      let next =
        Model(
          ..model,
          search: data,
          source: frame.source,
          route: query.set(model.route, [#("trace_id", wanted)]),
          refreshing: frame.coverage.running,
          notice: "",
          data: spans.Data(..model.data, now: frame.now),
          detail_id: case load {
            True -> wanted
            False -> model.detail_id
          },
          detail_loading: load || model.detail_loading,
          detail_complete: !load && model.detail_complete,
        )
      let commands = case model.connection {
        Connecting -> [OpenSearchStream(search_resume(next), next.generation)]
        _ -> []
      }
      let commands =
        list.append(
          commands,
          case frame.coverage.running && model.connection != Live {
            True -> [After(300, SearchPoll(model.generation))]
            False -> []
          },
        )
      let commands =
        list.append(commands, case load {
          True -> [
            ReadSearchCache(frame.source, "trace", wanted, model.generation),
          ]
          False -> []
        })
      let commands =
        list.append(
          commands,
          case
            !frame.coverage.running
            && {
              model.search.frame.coverage.running
              || list.is_empty(model.search.services)
            }
          {
            True -> [FetchMetadata(model.generation)]
            False -> []
          },
        )
      let commands =
        list.append(
          commands,
          case !frame.coverage.running && frame.seq != model.search.frame.seq {
            True ->
              cache_write(
                next,
                "query",
                search_route(model.route),
                search.encode(data),
                0,
              ).1
            False -> []
          },
        )
      #(reproject(next), commands)
    }
  }
}

fn receive_detail(
  model: Model,
  detail: search.Detail,
) -> #(Model, List(Command)) {
  case
    detail.id != query.get(model.route, "trace_id")
    || detail.source != model.source
  {
    True -> #(model, [])
    False -> {
      case
        model.detail_revision != ""
        && model.detail_revision != detail.revision
        && !list.is_empty(model.detail_parts)
      {
        True -> #(
          Model(
            ..model,
            detail_parts: [],
            detail_revision: "",
            detail_loading: True,
          ),
          [FetchTrace(detail.id, "", model.generation)],
        )
        False -> {
          let parts = list.append(model.detail_parts, detail.spans)
          let next =
            Model(
              ..model,
              detail_id: detail.id,
              detail_summary: Some(detail.summary),
              detail_revision: detail.revision,
              detail_parts: parts,
              detail_loading: detail.cursor != "",
              detail_complete: detail.complete,
              detail_notice: detail.reason,
            )
          case detail.cursor != "" {
            True -> #(next, [
              FetchTrace(detail.id, detail.cursor, model.generation),
            ])
            False -> {
              let data =
                spans.Data(
                  ..spans.empty(),
                  source: detail.source,
                  epoch: detail.epoch,
                  seq: 1,
                  now: model.data.now,
                  members: set.from_list([detail.id]),
                  spans: dict.from_list(
                    list.map(parts, fn(p) { #(#(p.trace_id, p.id), p) }),
                  ),
                )
              let next =
                reproject(
                  Model(
                    ..next,
                    data: data,
                    index: project.index(data),
                    detail_parts: [],
                  ),
                )
              case detail.complete {
                True ->
                  cache_write(
                    next,
                    "trace",
                    detail.id,
                    spans.encode(parts),
                    list.length(parts),
                  )
                False -> #(next, [])
              }
            }
          }
        }
      }
    }
  }
}

fn receive_cache(
  model: Model,
  kind: String,
  id: String,
  cached: Result(String, String),
) -> #(Model, List(Command)) {
  let model = case cached {
    Error(message) -> Model(..model, cache_notice: message)
    _ -> model
  }
  let raw = cached |> result.unwrap("")
  case kind {
    "query" -> {
      let next = case search.parse(raw) {
        Ok(frame) ->
          case
            search.apply(
              search.Data(
                ..search.empty(),
                services: model.search.services,
                operations: model.search.operations,
                attributes: model.search.attributes,
              ),
              frame,
            )
          {
            Ok(data) ->
              reproject(Model(..model, search: data, refreshing: True))
            Error(_) -> model
          }
        Error(_) -> model
      }
      #(next, [FetchSearch(search_resume(next), next.generation)])
    }
    _ -> {
      let next = case json.parse(raw, decode.list(spans.span_decoder())) {
        Ok(items) -> {
          let data =
            spans.Data(
              ..spans.empty(),
              seq: 1,
              now: model.data.now,
              members: set.from_list([id]),
              spans: dict.from_list(
                list.map(items, fn(p) { #(#(p.trace_id, p.id), p) }),
              ),
            )
          reproject(
            Model(
              ..model,
              data: data,
              index: project.index(data),
              detail_notice: "Cached trace; refreshing",
            ),
          )
        }
        Error(_) -> model
      }
      #(
        Model(
          ..next,
          detail_parts: [],
          detail_revision: "",
          detail_loading: True,
        ),
        [FetchTrace(id, "", next.generation)],
      )
    }
  }
}

fn cache_write(
  model: Model,
  kind: String,
  id: String,
  raw: String,
  count: Int,
) -> #(Model, List(Command)) {
  #(model, [
    WriteSearchCache(
      model.source,
      kind,
      id,
      raw,
      count,
      search_cache.byte_length(raw),
    ),
  ])
}

pub fn trace_pinned(model: Model) -> Bool {
  list.any(model.cache_entries, fn(e) {
    e.key == search_cache.key(model.source, "trace", model.detail_id)
    && e.pinned
  })
}

fn shift_interval(model: Model, direction: Int) -> #(Model, List(Command)) {
  let range = model.search.frame.coverage
  let width = range.to - range.from
  navigate_search(
    model,
    query.set(model.route, [
      #("from", int.to_string(range.from + direction * width)),
      #("to", int.to_string(range.to + direction * width)),
      #("window", ""),
      #("cursor", ""),
    ]),
    True,
  )
}

fn parse_saved(raw: String) -> Dict(String, String) {
  json.parse(raw, decode.dict(decode.string, decode.string))
  |> result.unwrap(dict.new())
}

fn encode_saved(saved: Dict(String, String)) -> String {
  dict.to_list(saved)
  |> list.map(fn(p) { #(p.0, json.string(p.1)) })
  |> json.object
  |> json.to_string
}

fn valid_prefetch(model: Model, generation: Int, token: Int) -> Bool {
  generation == model.generation
  && token == model.prefetch.token
  && model.prefetch.active != None
}

fn receive_prefetch(
  model: Model,
  result: Result(search.Detail, String),
) -> #(Model, List(Command)) {
  let assert Some(active) = model.prefetch.active
  case result {
    Error(_) -> #(Model(..model, prefetch: prefetch.finish(model.prefetch)), [])
    Ok(detail) -> {
      let cost =
        active.cost + search_cache.byte_length(spans.encode(detail.spans))
      case
        detail.id != active.id
        || detail.source != model.source
        || detail.epoch != model.search.frame.epoch
        || { active.revision != "" && active.revision != detail.revision }
        || cost > search_cache.capacity
        || {
          detail.cursor != "" && set.contains(active.cursors, detail.cursor)
        }
      {
        True -> #(Model(..model, prefetch: prefetch.finish(model.prefetch)), [])
        False -> {
          let parts = list.append(active.parts, detail.spans)
          let active =
            prefetch.Transfer(
              ..active,
              revision: detail.revision,
              parts: parts,
              cost: cost,
              cursors: set.insert(active.cursors, detail.cursor),
            )
          let next =
            Model(
              ..model,
              prefetch: prefetch.State(..model.prefetch, active: Some(active)),
            )
          case detail.cursor {
            "" ->
              case detail.complete {
                False -> #(
                  Model(..next, prefetch: prefetch.finish(next.prefetch)),
                  [],
                )
                True -> {
                  let raw = spans.encode(parts)
                  #(next, [
                    StorePrefetch(
                      model.source,
                      detail.id,
                      raw,
                      list.length(parts),
                      search_cache.byte_length(raw),
                      model.generation,
                      model.prefetch.token,
                    ),
                  ])
                }
              }
            cursor -> #(next, [
              FetchPrefetch(
                detail.id,
                cursor,
                model.generation,
                model.prefetch.token,
              ),
            ])
          }
        }
      }
    }
  }
}
