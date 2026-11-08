// Cache policy is pure. The adapter executes transactions and enforces caps.
import gleam/dynamic/decode as d
import gleam/int
import gleam/json
import gleam/list
import gleam/result

pub type Entry {
  Entry(
    key: String,
    id: String,
    kind: String,
    count: Int,
    cost: Int,
    used: Int,
    pinned: Bool,
  )
}

pub fn parse(raw: String) -> Result(List(Entry), String) {
  let decoder = {
    use key <- d.field("key", d.string)
    use id <- d.field("id", d.string)
    use kind <- d.field("kind", d.string)
    use count <- d.field("count", d.int)
    use cost <- d.field("cost", d.int)
    use used <- d.field("used", d.int)
    use pinned <- d.field("pinned", d.bool)
    d.success(Entry(key, id, kind, count, cost, used, pinned))
  }
  json.parse(raw, d.list(decoder))
  |> result.map_error(fn(_) { "Cache inventory unavailable" })
}

pub const capacity = 52_428_800

// LRU admission, using the inventory read inside the write transaction.
// Pins remain explicit exceptions to eviction; their cost still counts.
pub fn victims(
  entries: List(Entry),
  cost: Int,
  limit: Int,
  replace: String,
) -> Result(List(String), String) {
  let retained = list.filter(entries, fn(e) { e.key != replace })
  let total = list.fold(retained, cost, fn(n, e) { n + e.cost })
  let sorted = list.sort(retained, fn(a, b) { int.compare(a.used, b.used) })
  let remaining =
    list.fold(sorted, #(total, []), fn(acc, e) {
      case acc.0 > limit && !e.pinned {
        True -> #(acc.0 - e.cost, [e.key, ..acc.1])
        False -> acc
      }
    })
  case cost < 0 || cost > limit || remaining.0 > limit {
    True ->
      Error("Cache cost exceeds available capacity; unpin traces to make space")
    False -> Ok(remaining.1)
  }
}

pub fn admission(raw: String, cost: Int, replace: String) -> String {
  let decision =
    parse(raw)
    |> result.try(fn(entries) { victims(entries, cost, capacity, replace) })
  case decision {
    Ok(keys) -> json.object([#("victims", json.array(keys, json.string))])
    Error(message) -> json.object([#("error", json.string(message))])
  }
  |> json.to_string
}

@external(javascript, "./search_cache_ffi.mjs", "key")
pub fn key(source: String, kind: String, id: String) -> String

@external(javascript, "./search_cache_ffi.mjs", "byte_length")
pub fn byte_length(value: String) -> Int

@external(javascript, "./search_cache_ffi.mjs", "read")
pub fn read(
  source: String,
  kind: String,
  id: String,
  success: fn(String) -> Nil,
  status: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

pub fn write(
  source: String,
  kind: String,
  id: String,
  raw: String,
  count: Int,
  cost: Int,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil {
  write_with_policy(
    source,
    kind,
    id,
    raw,
    count,
    cost,
    admission,
    success,
    failure,
  )
}

@external(javascript, "./search_cache_ffi.mjs", "write")
fn write_with_policy(
  source: String,
  kind: String,
  id: String,
  raw: String,
  count: Int,
  cost: Int,
  decide: fn(String, Int, String) -> String,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./search_cache_ffi.mjs", "peek")
pub fn peek(
  source: String,
  id: String,
  touch: Bool,
  success: fn(String) -> Nil,
  status: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./search_cache_ffi.mjs", "pin")
pub fn pin(
  source: String,
  id: String,
  pinned: Bool,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./search_cache_ffi.mjs", "clear")
pub fn clear(
  source: String,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil
