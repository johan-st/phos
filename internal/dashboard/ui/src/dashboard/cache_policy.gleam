// Cache policy stays pure; the IndexedDB adapter only executes transactions.
import gleam/dynamic/decode as d
import gleam/int
import gleam/json
import gleam/list
import gleam/order
import gleam/result

pub type Trace {
  Trace(key: String, trace_id: String, count: Int, bytes: Int, used: Int)
}

pub fn source(raw: String) -> String {
  let decoder = {
    use source <- d.field("source", d.string)
    d.success(source)
  }
  json.parse(raw, decoder) |> result.unwrap("")
}

pub fn inventory(raw: String) -> Result(List(Trace), String) {
  let decoder = {
    use key <- d.field("key", d.string)
    use id <- d.field("trace_id", d.string)
    use count <- d.field("count", d.int)
    use bytes <- d.field("bytes", d.int)
    use used <- d.field("used", d.int)
    d.success(Trace(key, id, count, bytes, used))
  }
  json.parse(raw, d.list(decoder))
  |> result.map_error(fn(_) { "Cache metadata unavailable; using memory" })
}

pub fn evict(traces: List(Trace), now: Int, selected: String) -> List(String) {
  let count = list.fold(traces, 0, fn(n, t) { n + t.count })
  let bytes = list.fold(traces, 0, fn(n, t) { n + t.bytes })
  let ordered =
    list.sort(traces, fn(a, b) {
      case a.trace_id == selected, b.trace_id == selected {
        True, False -> order.Gt
        False, True -> order.Lt
        _, _ -> int.compare(a.used, b.used)
      }
    })
  let result =
    list.fold(ordered, #(count, bytes, []), fn(acc, t) {
      case
        acc.0 > 25_000 || acc.1 > 50 * 1024 * 1024 || now - t.used > 3_600_000
      {
        True -> #(acc.0 - t.count, acc.1 - t.bytes, [t.key, ..acc.2])
        False -> acc
      }
    })
  result.2
}
