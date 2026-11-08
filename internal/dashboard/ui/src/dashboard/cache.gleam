@external(javascript, "./cache_ffi.mjs", "load")
pub fn load(
  source: String,
  route: String,
  max_age: Int,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./cache_ffi.mjs", "store")
pub fn store(
  route: String,
  raw: String,
  success: fn(String) -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./cache_ffi.mjs", "evict")
pub fn evict(
  keys: List(String),
  success: fn() -> Nil,
  failure: fn(String) -> Nil,
) -> Nil

@external(javascript, "./cache_ffi.mjs", "clear")
pub fn clear(success: fn() -> Nil, failure: fn(String) -> Nil) -> Nil
