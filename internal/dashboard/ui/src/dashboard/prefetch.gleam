// One speculative transfer at a time. State and scheduling stay in Gleam.
import dashboard/spans
import gleam/list
import gleam/option.{type Option, None, Some}
import gleam/set.{type Set}

pub type Candidate {
  Candidate(id: String, urgent: Bool)
}

pub type Transfer {
  Transfer(
    id: String,
    revision: String,
    parts: List(spans.Span),
    cost: Int,
    cursors: Set(String),
  )
}

pub type State {
  State(
    queue: List(Candidate),
    tried: Set(String),
    active: Option(Transfer),
    token: Int,
  )
}

pub fn empty() -> State {
  State([], set.new(), None, 0)
}

pub fn reset(state: State) -> State {
  State(..empty(), token: state.token + 1)
}

pub fn offer(state: State, ids: List(String), urgent: Bool) -> State {
  let active = case state.active {
    Some(t) -> t.id
    None -> ""
  }
  let candidates =
    list.filter(ids, fn(id) {
      id != ""
      && id != active
      && { urgent || !set.contains(state.tried, id) }
      && { urgent || !list.any(state.queue, fn(c) { c.id == id }) }
    })
    |> list.unique
  let queued =
    list.filter(state.queue, fn(c) { !list.contains(candidates, c.id) })
  let added = list.map(candidates, fn(id) { Candidate(id, urgent) })
  State(..state, queue: case urgent {
    True -> list.append(added, queued)
    False -> list.append(queued, added)
  })
}

// Keep candidates bounded to the current result page. Newly arriving results
// can enter continuously, but evictions never cause a speculative fetch loop.
pub fn retain(state: State, ids: List(String)) -> State {
  State(
    ..state,
    queue: list.filter(state.queue, fn(c) { list.contains(ids, c.id) }),
    tried: set.intersection(state.tried, set.from_list(ids)),
  )
}

pub fn next(state: State, selected: String) -> #(State, Option(Candidate)) {
  case state.active {
    Some(_) -> #(state, None)
    None -> {
      let queue = list.filter(state.queue, fn(c) { c.id != selected })
      case queue {
        [] -> #(State(..state, queue: []), None)
        [c, ..rest] -> #(
          State(
            queue: rest,
            tried: set.insert(state.tried, c.id),
            token: state.token + 1,
            active: Some(Transfer(c.id, "", [], 0, set.new())),
          ),
          Some(c),
        )
      }
    }
  }
}

pub fn finish(state: State) -> State {
  State(..state, active: None)
}

pub fn visible(state: State, ids: List(String)) -> State {
  let #(visible, rest) =
    list.partition(state.queue, fn(c) { list.contains(ids, c.id) })
  State(..state, queue: list.append(visible, rest))
}
