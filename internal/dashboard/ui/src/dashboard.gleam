import dashboard/browser
import dashboard/cache
import dashboard/cache_policy
import dashboard/query
import dashboard/search
import dashboard/search_cache
import dashboard/spans
import dashboard/state.{type Model, type Msg}
import dashboard/tooltip
import dashboard/view
import gleam/dict
import gleam/dynamic/decode
import gleam/json
import gleam/list
import lustre
import lustre/effect.{type Effect}

pub fn main() -> Nil {
  let app = lustre.application(init, update, view.render)
  let _ = lustre.start(app, "#app", browser.flags())
  Nil
}

fn init(raw: String) -> #(Model, Effect(Msg)) {
  let decoder = {
    use route <- decode.field("route", decode.string)
    use bootstrap <- decode.field("bootstrap", decode.string)
    use prefs <- decode.field(
      "prefs",
      decode.dict(decode.string, decode.string),
    )
    use viewport <- decode.field("viewport", decode.int)
    decode.success(#(route, bootstrap, prefs, viewport))
  }
  let #(route, bootstrap, prefs, viewport) = case json.parse(raw, decoder) {
    Ok(flags) -> flags
    Error(_) -> #("/dashboard", "", dict.new(), 1280)
  }
  let #(model, commands) = state.init(route, bootstrap, prefs, viewport)
  let listeners =
    effect.from(fn(dispatch) {
      browser.watch_candidates(fn(raw) {
        case json.parse(raw, decode.list(decode.string)) {
          Ok(ids) -> dispatch(state.VisibleTraces(ids))
          Error(_) -> Nil
        }
      })
      browser.listen_tooltips(
        fn(value, x, y, above, focused) {
          dispatch(state.Tooltip(tooltip.Hover(value, x, y, above, focused)))
        },
        fn() { dispatch(state.Tooltip(tooltip.Leave)) },
      )
      browser.listen(
        fn(route) { dispatch(state.LocationChanged(route)) },
        fn(key, shift) { dispatch(state.KeyPressed(key, shift)) },
        fn(width) { dispatch(state.ViewportChanged(width)) },
      )
    })
  #(model, effect.batch([listeners, perform(commands)]))
}

fn update(model: Model, msg: Msg) -> #(Model, Effect(Msg)) {
  let #(model, commands) = state.update(model, msg)
  #(model, perform(commands))
}

fn perform(commands: List(state.Command)) -> Effect(Msg) {
  commands
  |> list.map(fn(command) {
    effect.from(fn(dispatch) {
      case command {
        state.CancelPrefetch -> browser.cancel_prefetch()
        state.PeekTrace(source, id, touch, generation, token) ->
          search_cache.peek(
            source,
            id,
            touch,
            fn(raw) {
              dispatch(state.PrefetchLoaded(generation, token, Ok(raw)))
            },
            fn(raw) {
              dispatch(state.SearchCacheStored(search_cache.parse(raw)))
            },
            fn(message) {
              dispatch(state.PrefetchLoaded(generation, token, Error(message)))
            },
          )
        state.FetchPrefetch(id, cursor, generation, token) ->
          browser.fetch_data(
            "/dashboard/trace",
            query.build([#("trace_id", id), #("cursor", cursor)]),
            "prefetch",
            fn(raw) {
              dispatch(state.PrefetchDetail(
                generation,
                token,
                search.detail(raw),
              ))
            },
            fn(message) {
              dispatch(state.PrefetchDetail(generation, token, Error(message)))
            },
          )
        state.StorePrefetch(source, id, raw, count, cost, generation, token) ->
          search_cache.write(
            source,
            "trace",
            id,
            raw,
            count,
            cost,
            fn(raw) {
              dispatch(state.PrefetchStored(
                generation,
                token,
                search_cache.parse(raw),
              ))
            },
            fn(message) {
              dispatch(state.PrefetchStored(generation, token, Error(message)))
            },
          )
        state.FetchSearch(route, generation) ->
          browser.fetch_data(
            "/dashboard/search",
            route,
            "search",
            fn(raw) {
              dispatch(state.SearchBatch(generation, search.parse(raw)))
            },
            fn(message) {
              dispatch(state.SearchBatch(generation, Error(message)))
            },
          )
        state.FetchMetadata(generation) ->
          browser.fetch_data(
            "/dashboard/metadata",
            "/dashboard",
            "metadata",
            fn(raw) {
              dispatch(state.SourceMetadata(generation, search.metadata(raw)))
            },
            fn(message) {
              dispatch(state.SourceMetadata(generation, Error(message)))
            },
          )
        state.FetchTrace(id, cursor, generation) ->
          browser.fetch_data(
            "/dashboard/trace",
            query.build([#("trace_id", id), #("cursor", cursor)]),
            "trace",
            fn(raw) {
              dispatch(state.TraceDetail(generation, search.detail(raw)))
            },
            fn(message) {
              dispatch(state.TraceDetail(generation, Error(message)))
            },
          )
        state.OpenSearchStream(route, generation) ->
          browser.open_search_stream(
            route,
            fn(raw) {
              dispatch(state.SearchBatch(generation, search.parse(raw)))
            },
            fn(raw) {
              case search.change(raw) {
                Ok(change) -> dispatch(state.TraceDelta(generation, change))
                Error(_) ->
                  case
                    json.parse(
                      raw,
                      decode.field("trace_id", decode.string, decode.success),
                    )
                  {
                    Ok(id) -> dispatch(state.TraceChanged(generation, id))
                    Error(_) -> Nil
                  }
              }
            },
            fn() { dispatch(state.Connected(generation)) },
            fn() { dispatch(state.Disconnected(generation)) },
          )
        state.ReadSearchCache(source, kind, id, generation) ->
          search_cache.read(
            source,
            kind,
            id,
            fn(raw) {
              dispatch(state.SearchCacheLoaded(generation, kind, id, Ok(raw)))
            },
            fn(raw) {
              dispatch(state.SearchCacheStored(search_cache.parse(raw)))
            },
            fn(message) {
              dispatch(state.SearchCacheLoaded(
                generation,
                kind,
                id,
                Error(message),
              ))
            },
          )
        state.WriteSearchCache(source, kind, id, raw, count, cost) ->
          search_cache.write(
            source,
            kind,
            id,
            raw,
            count,
            cost,
            fn(raw) {
              dispatch(state.SearchCacheStored(search_cache.parse(raw)))
            },
            fn(message) { dispatch(state.SearchCacheStored(Error(message))) },
          )
        state.SetTracePin(source, id, pinned) ->
          search_cache.pin(
            source,
            id,
            pinned,
            fn(raw) {
              dispatch(state.SearchCacheStored(search_cache.parse(raw)))
            },
            fn(message) { dispatch(state.SearchCacheStored(Error(message))) },
          )
        state.ClearSearchCache(source) ->
          search_cache.clear(
            source,
            fn(raw) {
              dispatch(state.SearchCacheStored(search_cache.parse(raw)))
            },
            fn(message) { dispatch(state.SearchCacheStored(Error(message))) },
          )
        state.DownloadTrace(id, raw) -> browser.download_trace(id, raw)
        state.OpenStream(route, generation) ->
          browser.open_stream(
            route,
            fn(raw) { dispatch(state.Batch(generation, raw, spans.parse(raw))) },
            fn() { dispatch(state.Connected(generation)) },
            fn() { dispatch(state.Disconnected(generation)) },
          )
        state.CloseStream -> browser.close_stream()
        state.Fetch(route, generation) ->
          browser.fetch_snapshot(
            route,
            fn(raw) { dispatch(state.Batch(generation, raw, spans.parse(raw))) },
            fn(message) {
              dispatch(state.Batch(generation, "", Error(message)))
            },
          )
        state.PushHistory(route) -> browser.push_history(route)
        state.Persist(key, value) -> browser.persist(key, value)
        state.Focus(id) -> browser.focus(id)
        state.RevealSelection(id) -> browser.reveal_selection(id)
        state.After(milliseconds, msg) ->
          browser.after(milliseconds, fn() { dispatch(msg) })
        state.WatchPointer ->
          browser.watch_pointer(fn(x) { dispatch(state.PointerMoved(x)) }, fn() {
            dispatch(state.PointerReleased)
          })
        state.UnwatchPointer -> browser.unwatch_pointer()
        state.LoadCache(source, route, generation) ->
          cache.load(
            source,
            route,
            3_600_000,
            fn(raw) { dispatch(state.CacheLoaded(generation, Ok(raw))) },
            fn(message) {
              dispatch(state.CacheLoaded(generation, Error(message)))
            },
          )
        state.StoreCache(route, raw) ->
          cache.store(
            route,
            raw,
            fn(raw) { dispatch(state.CacheStored(cache_policy.inventory(raw))) },
            fn(message) { dispatch(state.CacheStored(Error(message))) },
          )
        state.EvictCache(keys) ->
          cache.evict(keys, fn() { Nil }, fn(message) {
            dispatch(state.CacheStored(Error(message)))
          })
        state.ClearCacheStore ->
          cache.clear(
            fn() { dispatch(state.CacheCleared(Ok(Nil))) },
            fn(message) { dispatch(state.CacheCleared(Error(message))) },
          )
      }
    })
  })
  |> effect.batch
}
