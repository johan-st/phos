import dashboard/format
import dashboard/protocol
import gleam/int
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/string
import lustre/attribute as a
import lustre/element.{type Element, text}
import lustre/element/html as h

// Shared stationary-pointer delay for every dashboard hint and preview.
pub const delay_ms = 100

pub fn hint(value: String) -> a.Attribute(msg) {
  a.attribute("data-tooltip", value)
}

pub fn span_preview(row: protocol.Row) -> String {
  string.join(
    [
      row.name,
      "Span: " <> row.span_id,
      case row.kind {
        "event" -> "Event at: " <> row.time_label
        _ -> "Duration: " <> format.duration_label(row.duration_label)
      },
      "Trace offset: " <> format.duration_label(row.start_label),
      case row.kind {
        "event" -> int.to_string(row.attr_count) <> " attributes"
        _ ->
          case row.failed {
            True -> "Error recorded"
            False -> "No recorded error"
          }
          <> " · "
          <> int.to_string(row.event_count)
          <> " events"
          <> " · "
          <> int.to_string(row.error_count)
          <> " errors"
          <> " · "
          <> int.to_string(row.attr_count)
          <> " attributes"
      },
    ],
    "\n",
  )
}

pub fn trace_preview(trace: protocol.Trace) -> String {
  string.join(
    [
      trace.name,
      "Trace: " <> trace.trace_id,
      "Started: " <> trace.started_at_label,
      "Duration: " <> format.duration_label(trace.duration_label),
      int.to_string(trace.span_count)
        <> " spans · "
        <> int.to_string(trace.event_count)
        <> " events · "
        <> int.to_string(trace.error_count)
        <> " errors",
    ],
    "\n",
  )
}

pub type Model {
  Model(value: String, x: Int, y: Int, above: Bool, visible: Bool, token: Int)
}

pub type Msg {
  Hover(String, Int, Int, Bool, Bool)
  Settle(Int)
  Leave
}

pub fn empty() -> Model {
  Model("", 0, 0, False, False, 0)
}

pub fn hide(model: Model) -> Model {
  case model.value {
    "" -> model
    _ -> Model(..model, value: "", visible: False, token: model.token + 1)
  }
}

// Only the most recent stationary position may reveal a tooltip.
pub fn update(model: Model, msg: Msg) -> #(Model, Option(Int)) {
  case msg {
    Leave -> #(hide(model), None)
    Hover(value, x, y, above, False)
      if value == model.value
      && x == model.x
      && y == model.y
      && above == model.above
    -> #(model, None)
    Hover(value, x, y, above, focused) -> {
      let next = Model(value, x, y, above, focused, model.token + 1)
      #(next, case focused {
        True -> None
        False -> Some(next.token)
      })
    }
    Settle(token) if token == model.token && model.value != "" -> #(
      Model(..model, visible: True),
      None,
    )
    Settle(_) -> #(model, None)
  }
}

pub fn render(model: Model) -> Element(Msg) {
  case model.visible {
    False -> text("")
    True ->
      h.div(
        [
          a.class("dashboard-tooltip"),
          a.role("tooltip"),
          a.style([
            #(
              "left",
              "min("
                <> int.to_string(model.x)
                <> "px, max(8px, calc(100vw - 336px)))",
            ),
            #("top", int.to_string(model.y) <> "px"),
            #("transform", case model.above {
              True -> "translateY(calc(-100% - 12px))"
              False -> "translateY(12px)"
            }),
          ]),
        ],
        case string.split(model.value, "\n") {
          [title, ..lines] if lines != [] -> [
            h.strong([a.class("tooltip-heading")], [text(title)]),
            ..list.map(lines, fn(line) { h.div([], [text(line)]) })
          ]
          _ -> [text(model.value)]
        },
      )
  }
}
