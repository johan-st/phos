// The browser boundary performs I/O only. It never selects, filters, or renders.
@external(javascript, "./browser_ffi.mjs", "flags")
pub fn flags() -> String

@external(javascript, "./browser_ffi.mjs", "fetch_data")
pub fn fetch_data(
  path: String,
  route: String,
  family: String,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./browser_ffi.mjs", "open_search_stream")
pub fn open_search_stream(
  route: String,
  batch: fn(String) -> Nil,
  changed: fn(String) -> Nil,
  ready: fn() -> Nil,
  lost: fn() -> Nil,
) -> Nil

@external(javascript, "./browser_ffi.mjs", "download_trace")
pub fn download_trace(id: String, raw: String) -> Nil

@external(javascript, "./browser_ffi.mjs", "open_stream")
pub fn open_stream(
  route: String,
  snapshot: fn(String) -> Nil,
  ready: fn() -> Nil,
  lost: fn() -> Nil,
) -> Nil

@external(javascript, "./browser_ffi.mjs", "close_stream")
pub fn close_stream() -> Nil

@external(javascript, "./browser_ffi.mjs", "fetch_snapshot")
pub fn fetch_snapshot(
  route: String,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./browser_ffi.mjs", "listen")
pub fn listen(
  location: fn(String) -> Nil,
  key: fn(String, Bool) -> Nil,
  resize: fn(Int) -> Nil,
) -> Nil

@external(javascript, "./browser_ffi.mjs", "watch_pointer")
pub fn watch_pointer(move: fn(Int) -> Nil, release: fn() -> Nil) -> Nil

@external(javascript, "./browser_ffi.mjs", "unwatch_pointer")
pub fn unwatch_pointer() -> Nil

@external(javascript, "./browser_ffi.mjs", "push_history")
pub fn push_history(route: String) -> Nil

@external(javascript, "./browser_ffi.mjs", "persist")
pub fn persist(key: String, value: String) -> Nil

@external(javascript, "./browser_ffi.mjs", "focus")
pub fn focus(id: String) -> Nil

@external(javascript, "./browser_ffi.mjs", "reveal_selection")
pub fn reveal_selection(id: String) -> Nil

@external(javascript, "./browser_ffi.mjs", "after")
pub fn after(milliseconds: Int, callback: fn() -> Nil) -> Nil

@external(javascript, "./browser_ffi.mjs", "watch_candidates")
pub fn watch_candidates(visible: fn(String) -> Nil) -> Nil

@external(javascript, "./browser_ffi.mjs", "cancel_prefetch")
pub fn cancel_prefetch() -> Nil

@external(javascript, "./browser_ffi.mjs", "listen_tooltips")
pub fn listen_tooltips(
  hover: fn(String, Int, Int, Bool, Bool) -> Nil,
  leave: fn() -> Nil,
) -> Nil
