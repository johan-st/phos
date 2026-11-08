import gleam/float
import gleam/int
import gleam/result
import gleam/string

// Native date/number formatting only; no mutable state or browser effects.
@external(javascript, "./format_ffi.mjs", "fixed")
pub fn fixed(value: Float, places: Int) -> String

@external(javascript, "./format_ffi.mjs", "timestamp")
pub fn timestamp(milliseconds: Float) -> String

@external(javascript, "./format_ffi.mjs", "parse_time")
pub fn parse_time(value: String) -> Int

pub fn duration(ms: Float) -> String {
  let ms = float.max(ms, 0.0)
  let #(value, unit, limit, next_unit) = case ms {
    0.0 -> #(0.0, "ms", 1000.0, "s")
    n if n <. 0.001 -> #(n *. 1_000_000.0, "ns", 1000.0, "µs")
    n if n <. 1.0 -> #(n *. 1000.0, "µs", 1000.0, "ms")
    n if n <. 1000.0 -> #(n, "ms", 1000.0, "s")
    n if n <. 60_000.0 -> #(n /. 1000.0, "s", 60.0, "min")
    n if n <. 3_600_000.0 -> #(n /. 60_000.0, "min", 60.0, "h")
    n if n <. 86_400_000.0 -> #(n /. 3_600_000.0, "h", 24.0, "d")
    n -> #(n /. 86_400_000.0, "d", 0.0, "")
  }
  let #(rounded, label) = significant(value)
  case limit >. 0.0 && rounded >=. limit {
    True -> "1 " <> next_unit
    False -> label <> " " <> unit
  }
}

// Three significant digits, without padding trailing decimal zeroes.
fn significant(value: Float) -> #(Float, String) {
  case value {
    0.0 -> #(0.0, "0")
    _ -> {
      let places = 3 - decimal_digits(value)
      let scale = float.power(10.0, int.to_float(places)) |> result.unwrap(1.0)
      let rounded = int.to_float(float.round(value *. scale)) /. scale
      #(rounded, trim_decimal(fixed(rounded, int.max(0, places))))
    }
  }
}

fn decimal_digits(value: Float) -> Int {
  case value {
    n if n >=. 10.0 -> 1 + decimal_digits(n /. 10.0)
    n if n <. 1.0 -> decimal_digits(n *. 10.0) - 1
    _ -> 1
  }
}

// Convert duration/offset labels at the rendering boundary; other text is unchanged.
pub fn duration_label(value: String) -> String {
  let relative = string.starts_with(value, "+")
  let number = case relative {
    True -> string.drop_start(value, 1)
    False -> value
  }
  let raw = string.drop_end(number, 2)
  let parsed = case float.parse(raw) {
    Ok(ms) -> Ok(ms)
    Error(_) -> int.parse(raw) |> result.map(int.to_float)
  }
  case string.ends_with(number, "ms"), parsed {
    True, Ok(ms) ->
      case relative {
        True -> "+"
        False -> ""
      }
      <> duration(ms)
    _, _ -> value
  }
}

@external(javascript, "./format_ffi.mjs", "input_time")
pub fn input_time(ms: Int) -> String

@external(javascript, "./format_ffi.mjs", "timezone")
pub fn timezone() -> String

@external(javascript, "./format_ffi.mjs", "parse_zone")
pub fn parse_zone(value: String, utc: Bool) -> Int

@external(javascript, "./format_ffi.mjs", "input_zone")
pub fn input_zone(ms: Int, utc: Bool) -> String

// Preserve raw milliseconds in the presentation model; round only when rendering.
pub fn exact_duration(ms: Float) -> String {
  float.to_string(float.max(ms, 0.0)) <> "ms"
}

fn trim_decimal(value: String) -> String {
  case string.contains(value, ".") {
    False -> value
    True ->
      case string.ends_with(value, "0") {
        True -> trim_decimal(string.drop_end(value, 1))
        False ->
          case string.ends_with(value, ".") {
            True -> string.drop_end(value, 1)
            False -> value
          }
      }
  }
}

pub fn relative(ms: Float) -> String {
  "+" <> exact_duration(ms)
}

pub fn percent(part: Float, total: Float) -> Float {
  case total >. 0.0 {
    True -> float.clamp(part /. total *. 100.0, 0.0, 100.0)
    False -> 0.0
  }
}

pub fn short(value: String) -> String {
  string.slice(value, 0, 12)
}

pub fn age(start: Float, now: Int) -> String {
  let seconds =
    int.max(0, float.truncate({ int.to_float(now) -. start } /. 1000.0))
  case seconds {
    n if n < 60 -> int.to_string(n) <> "s ago"
    n if n < 3600 -> int.to_string(n / 60) <> "m ago"
    n if n < 86_400 -> int.to_string(n / 3600) <> "h ago"
    n -> int.to_string(n / 86_400) <> "d ago"
  }
}
