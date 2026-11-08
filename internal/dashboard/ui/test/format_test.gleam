import dashboard/duration_view
import dashboard/format
import gleam/list
import gleam/string
import gleeunit/should
import lustre/element

pub fn readable_duration_units_test() {
  format.duration(485_674.3) |> should.equal("8.09 min")
  format.duration(0.0) |> should.equal("0 ms")
  format.duration(-10.0) |> should.equal("0 ms")
  format.duration(0.0005) |> should.equal("500 ns")
  format.duration(0.125) |> should.equal("125 µs")
  format.duration(12.345) |> should.equal("12.3 ms")
  format.duration(1500.0) |> should.equal("1.5 s")
  format.duration(7_200_000.0) |> should.equal("2 h")
  format.duration(172_800_000.0) |> should.equal("2 d")
}

pub fn rounded_units_promote_at_boundaries_test() {
  format.duration(0.0009995) |> should.equal("1 µs")
  format.duration(0.9995) |> should.equal("1 ms")
  format.duration(999.5) |> should.equal("1 s")
  format.duration(59_950.0) |> should.equal("1 min")
  format.duration(3_597_000.0) |> should.equal("1 h")
  format.duration(86_220_000.0) |> should.equal("1 d")
  format.duration(999.4) |> should.equal("999 ms")
  format.duration(59_949.0) |> should.equal("59.9 s")
}

pub fn durations_render_rounded_without_exact_controls_test() {
  let raw = format.exact_duration(485_674.312345)
  raw |> should.equal("485674.312345ms")
  let html = duration_view.label("duration", raw) |> element.to_string
  html |> string.contains("8.09 min") |> should.be_true
  html |> should.equal("<span class=\"duration\">8.09 min</span>")
  let relative = duration_view.label("", "+0.125ms") |> element.to_string
  relative |> string.contains("+125 µs") |> should.be_true
  relative |> string.contains("+0.125 ms") |> should.be_false
  duration_view.label("name", "load items")
  |> element.to_string
  |> should.equal("<span class=\"name\">load items</span>")
}

pub fn timings_use_three_significant_digits_across_scales_test() {
  [
    #(0.000000123456, "0.123 ns"),
    #(0.0123456, "12.3 µs"),
    #(1.23456, "1.23 ms"),
    #(11.183, "11.2 ms"),
    #(99.99, "100 ms"),
    #(691.916015625, "692 ms"),
    #(15_040.747, "15 s"),
    #(12_345.678 *. 86_400_000.0, "12300 d"),
  ]
  |> list.each(fn(example) {
    format.duration(example.0) |> should.equal(example.1)
  })
  format.duration_label("+691.916015625ms") |> should.equal("+692 ms")
  format.duration_label("12345ms") |> should.equal("12.3 s")
  format.duration_label("+12345ms") |> should.equal("+12.3 s")
}
