// IndexedDB effects only. Eviction choices come from cache_policy.gleam.
// Data and the corresponding query checkpoint commit in the SAME transaction.
const DB_NAME = "spore-dashboard-spans-v2";
let opening;
const request = req => new Promise((resolve, reject) => {
  req.onsuccess = () => resolve(req.result);
  req.onerror = () => reject(req.error);
});
const completion = tx => new Promise((resolve, reject) => {
  tx.oncomplete = resolve;
  tx.onabort = tx.onerror = () => reject(tx.error || new Error("Cache transaction aborted"));
});
async function database() {
  if (!opening) opening = new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      const spans = db.createObjectStore("spans", { keyPath: "key" });
      spans.createIndex("source", "source");
      spans.createIndex("trace", "trace_key");
      db.createObjectStore("queries", { keyPath: "key" }).createIndex("source", "source");
      db.createObjectStore("sources", { keyPath: "source" });
    };
    req.onsuccess = () => {
      const db = req.result;
      db.onversionchange = () => { db.close(); opening = undefined; };
      resolve(db);
    };
    req.onerror = () => { opening = undefined; reject(req.error); };
    req.onblocked = () => { opening = undefined; reject(new Error("Cache upgrade blocked by another tab")); };
  });
  return opening;
}
const spanKey = (source, trace, id) => JSON.stringify([source, trace, id]);
const traceKey = (source, trace) => JSON.stringify([source, trace]);
const queryKey = (source, route) => JSON.stringify([source, route]);
const cacheError = () => "Browser cache unavailable; using memory only";

export async function read_cache(source, route, maxAge) {
  const db = await database();
  const tx = db.transaction(["spans", "queries"], "readwrite");
  const done = completion(tx);
  done.catch(() => {});
  const queries = tx.objectStore("queries");
  const record = await request(queries.get(queryKey(source, route)));
  if (!record || Date.now() - record.used > maxAge) { await done; return ""; }
  const store = tx.objectStore("spans");
  const rows = await Promise.all(record.keys.map(key => request(store.get(key))));
  if (rows.some(row => !row || row.epoch !== record.frame.epoch)) { await done; return ""; }
  const now = Date.now();
  record.used = now;
  queries.put(record);
  for (const row of rows) store.put({ ...row, used: now });
  await done;
  return JSON.stringify({ ...record.frame, kind: "reset", base_seq: 0,
    upserts: rows.map(row => row.span), removed: [],
    members_added: record.members, members_removed: [] });
}

export async function write_cache(route, raw) {
  const frame = JSON.parse(raw);
  const db = await database();
  const tx = db.transaction(["spans", "queries", "sources"], "readwrite");
  const done = completion(tx);
  // Install a rejection handler immediately, including errors before await done.
  done.catch(() => {});
  try {
    const store = tx.objectStore("spans");
    const queries = tx.objectStore("queries");
    const sources = tx.objectStore("sources");
    const source = await request(sources.get(frame.source));
    if (source && source.epoch !== frame.epoch && frame.now_ms < source.changed_at) { await done; return inventory(db); }
    if (source && source.epoch !== frame.epoch) {
      const oldSpans = await request(store.index("source").getAllKeys(frame.source));
      const oldQueries = await request(queries.index("source").getAllKeys(frame.source));
      for (const key of oldSpans) store.delete(key);
      for (const key of oldQueries) queries.delete(key);
    }
    sources.put({ source: frame.source, epoch: frame.epoch,
      changed_at: source?.epoch === frame.epoch ? source.changed_at : frame.now_ms });
    const key = queryKey(frame.source, route);
    let previous = await request(queries.get(key));
    if (previous?.frame.epoch === frame.epoch && previous.frame.query === frame.query && previous.frame.seq > frame.seq) { await done; return inventory(db); }
    if (frame.kind === "delta" && frame.base_query && previous?.frame.query !== frame.query) {
      const candidates = await request(queries.index("source").getAll(frame.source));
      previous = candidates.find(q => q.frame.epoch === frame.epoch && q.frame.query === frame.base_query && q.frame.seq === frame.base_seq);
    }
    if (frame.kind !== "reset" && (!previous || previous.frame.epoch !== frame.epoch || (previous.frame.query !== frame.query && previous.frame.query !== frame.base_query) || previous.frame.seq < frame.base_seq)) {
      await done;
      return inventory(db); // Never persist a checkpoint without its baseline.
    }
    const members = new Set(frame.kind === "reset" ? [] : previous.members);
    const keys = new Set(frame.kind === "reset" ? [] : previous.keys);
    for (const trace of frame.members_removed) members.delete(trace);
    for (const trace of frame.members_added) members.add(trace);
    for (const item of frame.removed) keys.delete(spanKey(frame.source, item.trace_id, item.id));
    const now = Date.now();
    const encoder = new TextEncoder();
    for (const span of frame.upserts) {
      const key = spanKey(frame.source, span.trace_id, span.id);
      keys.add(key);
      const old = await request(store.get(key));
      if (old?.epoch === frame.epoch && old.version > frame.version) continue;
      store.put({ key, source: frame.source, trace_id: span.trace_id,
        trace_key: traceKey(frame.source, span.trace_id), epoch: frame.epoch,
        version: frame.version, span, bytes: encoder.encode(JSON.stringify(span)).length, used: now });
    }
    const retained = [...keys].filter(key => members.has(JSON.parse(key)[1]));
    queries.put({ key, source: frame.source, used: now, keys: retained, members: [...members],
      frame: { ...frame, upserts: [], removed: [], members_added: [], members_removed: [] } });
    // Query metadata is disposable too, including queries with no spans.
    const records = await request(queries.getAll());
    records.sort((a, b) => b.used - a.used);
    for (const [index, record] of records.entries()) {
      if (index >= 64 || now - record.used > 3600000) queries.delete(record.key);
    }
    await done;
    return inventory(db);
  } catch (error) { try { tx.abort(); } catch {} await done.catch(() => {}); throw error; }
}

async function inventory(db) {
  const tx = db.transaction("spans", "readonly");
  const done = completion(tx);
  done.catch(() => {});
  const rows = await request(tx.objectStore("spans").getAll());
  await done;
  const traces = new Map();
  for (const row of rows) {
    const trace = traces.get(row.trace_key) || { key: row.trace_key, trace_id: row.trace_id, count: 0, bytes: 0, used: 0 };
    trace.count++; trace.bytes += row.bytes; trace.used = Math.max(trace.used, row.used);
    traces.set(row.trace_key, trace);
  }
  return JSON.stringify([...traces.values()]);
}

export async function delete_traces(keys) {
  const db = await database();
  const tx = db.transaction(["spans", "queries"], "readwrite");
  const done = completion(tx);
  done.catch(() => {});
  const store = tx.objectStore("spans");
  const victims = new Set(keys);
  for (const key of victims) {
    const rows = await request(store.index("trace").getAllKeys(key));
    for (const row of rows) store.delete(row);
  }
  const queries = tx.objectStore("queries");
  for (const query of await request(queries.getAll())) {
    // Invalidating coverage is mandatory when cached query data is evicted.
    if (query.members.some(id => victims.has(traceKey(query.source, id))) || Date.now() - query.used > 3600000) queries.delete(query.key);
  }
  await done;
}
export async function clear_cache() {
  const db = await database();
  const tx = db.transaction(["spans", "queries", "sources"], "readwrite");
  const done = completion(tx);
  for (const name of ["spans", "queries", "sources"]) tx.objectStore(name).clear();
  await done;
}
export const load = (source, route, maxAge, success, failure) => { read_cache(source, route, maxAge).then(success).catch(() => failure(cacheError())); };
export const store = (route, raw, success, failure) => { write_cache(route, raw).then(success).catch(() => failure(cacheError())); };
export const evict = (keys, success, failure) => { delete_traces(Array.from(keys)).then(success).catch(() => failure(cacheError())); };
export const clear = (success, failure) => { clear_cache().then(success).catch(() => failure(cacheError())); };
