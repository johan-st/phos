import gleam/dict
import gleam/int
import gleam/list
import gleam/result
import gleam/string
import gleam/uri

pub fn params(route: String) -> List(#(String, String)) {
  let text =
    string.split_once(route, "?")
    |> result.map(fn(p) { p.1 })
    |> result.unwrap("")
  uri.parse_query(text) |> result.unwrap([])
}

pub fn get(route: String, key: String) -> String {
  dict.get(dict.from_list(params(route)), key) |> result.unwrap("")
}

pub fn number(route: String, key: String, fallback: Int) -> Int {
  get(route, key) |> int.parse |> result.unwrap(fallback)
}

pub fn build(values: List(#(String, String))) -> String {
  "/dashboard?" <> uri.query_to_string(list.filter(values, fn(p) { p.1 != "" }))
}

pub fn set(route: String, pairs: List(#(String, String))) -> String {
  let keys = list.map(pairs, fn(p) { p.0 })
  list.append(
    list.filter(params(route), fn(p) { !list.contains(keys, p.0) }),
    pairs,
  )
  |> build
}

pub fn feed(route: String) -> String {
  let selected =
    params(route)
    |> list.filter(fn(p) {
      list.contains(
        [
          "q",
          "service",
          "min_dur",
          "failed_only",
          "window",
          "from",
          "to",
          "limit",
          "exact_trace",
          "include_trace",
        ],
        p.0,
      )
    })
  let selected = case get(route, "window") == "" && get(route, "from") == "" {
    True -> [#("window", "900000"), ..selected]
    False -> selected
  }
  let selected = case get(route, "limit") == "" {
    True -> [#("limit", "100"), ..selected]
    False -> selected
  }
  selected |> list.sort(fn(a, b) { string.compare(a.0, b.0) }) |> build
}

pub fn checkpoint(route: String, epoch: String, seq: Int) -> String {
  set(feed(route), [#("epoch", epoch), #("after", int.to_string(seq))])
}
