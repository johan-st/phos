import dashboard/prefetch as p
import dashboard/search
import dashboard/search_cache as cache
import dashboard/spans
import dashboard/state as s
import gleam/dict
import gleam/list
import gleam/option.{None, Some}
import gleeunit/should

fn model() -> s.Model {
  let #(model, _) =
    s.init(
      "/dashboard?trace_id=selected",
      "{\"schema_version\":3,\"source\":\"s\"}",
      dict.new(),
      1440,
    )
  let frame =
    search.Frame(
      ..search.empty().frame,
      source: "s",
      epoch: "e",
      seq: 1,
      order: ["selected", "a", "b", "c"],
    )
  s.Model(
    ..model,
    source: "s",
    detail_id: "selected",
    detail_loading: False,
    search: search.Data(..search.empty(), frame: frame),
    prefetch: p.offer(p.empty(), frame.order, False),
  )
}

pub fn cost_policy_is_unit_agnostic_and_uses_lru_order_test() {
  let entries = [
    cache.Entry("new", "", "", 999_999, 4, 20, False),
    cache.Entry("old", "", "", 1, 4, 10, False),
  ]
  cache.victims(entries, 3, 10, "insert") |> should.equal(Ok(["old"]))
  cache.victims(entries, 6, 10, "new") |> should.equal(Ok([]))
  cache.victims(entries, 0, 8, "insert") |> should.equal(Ok([]))
  cache.victims(entries, -1, 10, "insert")
  |> should.equal(Error(
    "Cache cost exceeds available capacity; unpin traces to make space",
  ))
}

pub fn visible_then_hover_priority_and_no_eviction_refetch_loop_test() {
  let queue = p.offer(p.empty(), ["a", "b", "c"], False) |> p.visible(["c"])
  let #(queue, picked) = p.next(queue, "")
  picked |> should.equal(Some(p.Candidate("c", False)))
  let queue = p.offer(queue, ["b"], True) |> p.offer(["a", "b", "c"], False)
  let #(queue, picked) = p.next(p.finish(queue), "")
  picked |> should.equal(Some(p.Candidate("b", True)))
  let #(queue, _) = p.next(p.finish(queue), "")
  let queue = p.finish(queue) |> p.offer(["a", "b", "c"], False)
  p.next(queue, "").1 |> should.equal(None)
  // A fresh user hover may recheck a previously attempted/evicted item.
  p.next(p.offer(queue, ["c"], True), "").1
  |> should.equal(Some(p.Candidate("c", True)))
}

pub fn cache_hit_avoids_network_and_hover_deduplicates_active_work_test() {
  let #(active, commands) = s.update(model(), s.HoverTrace("b"))
  commands
  |> list.contains(s.PeekTrace("s", "b", True, 0, active.prefetch.token))
  |> should.be_true
  let #(same, commands) = s.update(active, s.HoverTrace("b"))
  same.prefetch |> should.equal(active.prefetch)
  commands |> should.equal([])
  let #(next, commands) =
    s.update(active, s.PrefetchLoaded(0, active.prefetch.token, Ok("[]")))
  commands
  |> list.any(fn(c) {
    case c {
      s.FetchPrefetch(_, _, _, _) -> True
      _ -> False
    }
  })
  |> should.be_false
  next.detail_id |> should.equal("selected")
  next.data |> should.equal(active.data)
}

@external(javascript, "./fixture_ffi.mjs", "spans")
fn raw_spans() -> String

fn detail(cursor: String, complete: Bool) -> search.Detail {
  let assert Ok(raw) = spans.parse(raw_spans())
  let assert [span, ..] = raw.upserts
  let summary =
    search.Trace(
      "a",
      "op",
      "svc",
      "op",
      1.0,
      2.0,
      1.0,
      2,
      0,
      0,
      "unknown",
      False,
    )
  search.Detail(
    "s",
    "e",
    "a",
    "revision",
    summary,
    [spans.Span(..span, trace_id: "a")],
    cursor,
    complete,
    "",
  )
}

pub fn prefetch_chunks_cache_only_complete_consistent_trace_without_selecting_it_test() {
  let #(active, _) = s.update(model(), s.HoverTrace("a"))
  let token = active.prefetch.token
  let #(active, commands) = s.update(active, s.PrefetchLoaded(0, token, Ok("")))
  commands |> should.equal([s.FetchPrefetch("a", "", 0, token)])
  let #(part, commands) =
    s.update(active, s.PrefetchDetail(0, token, Ok(detail("next", False))))
  commands |> should.equal([s.FetchPrefetch("a", "next", 0, token)])
  part.data |> should.equal(active.data)
  let #(done, commands) =
    s.update(part, s.PrefetchDetail(0, token, Ok(detail("", True))))
  let assert [s.StorePrefetch(source, id, raw, count, cost, _, _)] = commands
  source |> should.equal("s")
  id |> should.equal("a")
  count |> should.equal(2)
  cost |> should.equal(cache.byte_length(raw))
  done.detail_id |> should.equal("selected")
  done.data |> should.equal(active.data)
  let #(_, commands) =
    s.update(
      part,
      s.PrefetchDetail(
        0,
        token,
        Ok(search.Detail(..detail("", True), revision: "other")),
      ),
    )
  commands
  |> list.any(fn(c) {
    case c {
      s.StorePrefetch(_, _, _, _, _, _, _) -> True
      _ -> False
    }
  })
  |> should.be_false
}

pub fn navigation_cancels_speculation_and_rejects_old_callbacks_test() {
  let #(active, _) = s.update(model(), s.HoverTrace("a"))
  let #(selected, commands) =
    s.update(active, s.Navigate("/dashboard?trace_id=b"))
  commands |> list.contains(s.CancelPrefetch) |> should.be_true
  let #(same, commands) =
    s.update(
      selected,
      s.PrefetchDetail(
        active.generation,
        active.prefetch.token,
        Ok(detail("", True)),
      ),
    )
  same |> should.equal(selected)
  commands |> should.equal([])
  selected.detail_id |> should.equal("b")
}

pub fn incomplete_wrong_source_and_repeated_cursor_never_enter_cache_test() {
  let #(active, _) = s.update(model(), s.HoverTrace("a"))
  let #(part, _) =
    s.update(
      active,
      s.PrefetchDetail(0, active.prefetch.token, Ok(detail("next", False))),
    )
  [
    detail("", False),
    search.Detail(..detail("", True), source: "other"),
    detail("next", False),
  ]
  |> list.each(fn(chunk) {
    let #(_, commands) =
      s.update(part, s.PrefetchDetail(0, part.prefetch.token, Ok(chunk)))
    commands
    |> list.any(fn(c) {
      case c {
        s.StorePrefetch(_, _, _, _, _, _, _) -> True
        _ -> False
      }
    })
    |> should.be_false
  })
}
