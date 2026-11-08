import dashboard/state as s
import dashboard/tooltip as t
import gleam/dict
import gleam/option.{None, Some}
import gleeunit/should

pub fn tooltip_waits_for_latest_stationary_position_test() {
  let #(model, _) = s.init("/dashboard", "", dict.new(), 1440)
  let #(first, commands) =
    s.update(model, s.Tooltip(t.Hover("123 ms", 20, 30, False, False)))
  first.tooltip.visible |> should.be_false
  commands
  |> should.equal([s.After(100, s.Tooltip(t.Settle(first.tooltip.token)))])
  let #(moved, _) =
    s.update(first, s.Tooltip(t.Hover("123 ms", 21, 30, False, False)))
  let #(stale, _) = s.update(moved, s.Tooltip(t.Settle(first.tooltip.token)))
  stale.tooltip.visible |> should.be_false
  let #(settled, _) = s.update(stale, s.Tooltip(t.Settle(moved.tooltip.token)))
  settled.tooltip.visible |> should.be_true
  settled.route |> should.equal(model.route)
  let #(same, timer) =
    t.update(settled.tooltip, t.Hover("123 ms", 21, 30, False, False))
  same |> should.equal(settled.tooltip)
  timer |> should.equal(None)
  let #(moving, timer) = t.update(same, t.Hover("123 ms", 22, 30, False, False))
  moving.visible |> should.be_false
  timer |> should.equal(Some(moving.token))
}

pub fn leaving_and_escape_cancel_pending_tooltips_test() {
  let #(model, _) = s.init("/dashboard", "", dict.new(), 1440)
  let #(pending, _) =
    s.update(model, s.Tooltip(t.Hover("event", 20, 30, False, False)))
  let #(left, _) = s.update(pending, s.Tooltip(t.Leave))
  let #(late, _) = s.update(left, s.Tooltip(t.Settle(pending.tooltip.token)))
  late.tooltip.visible |> should.be_false
  let #(escaped, _) = s.update(pending, s.KeyPressed("Escape", False))
  let #(late, _) = s.update(escaped, s.Tooltip(t.Settle(pending.tooltip.token)))
  late.tooltip.visible |> should.be_false
}

pub fn focus_is_immediate_and_navigation_dismisses_test() {
  let #(model, _) = s.init("/dashboard", "", dict.new(), 1440)
  let #(focused, commands) =
    s.update(model, s.Tooltip(t.Hover("event", 20, 30, True, True)))
  focused.tooltip.visible |> should.be_true
  commands |> should.equal([])
  let #(navigated, _) =
    s.update(focused, s.Navigate("/dashboard?trace_id=other"))
  navigated.tooltip.visible |> should.be_false
}
