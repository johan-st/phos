// Storage effects only. Eviction decisions are supplied by Gleam.
const DB_NAME = "spore-dashboard-search-v3";
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
    const req = indexedDB.open(DB_NAME, 3);
    req.onupgradeneeded = () => {
      const store = req.result.objectStoreNames.contains("entries")
        ? req.transaction.objectStore("entries")
        : req.result.createObjectStore("entries", { keyPath: "key" });
      if (!store.indexNames.contains("source")) store.createIndex("source", "source");
      if (!req.result.objectStoreNames.contains("metadata")) {
        const metadata = req.result.createObjectStore("metadata", { keyPath: "key" });
        metadata.createIndex("used", "used");
        const cursor = store.openCursor();
        cursor.onsuccess = () => {
          if (!cursor.result) return;
          metadata.put(metadataOf(cursor.result.value));
          cursor.result.continue();
        };
      }
    };
    req.onsuccess = () => {
      const db = req.result;
      db.onversionchange = () => { db.close(); opening = undefined; };
      resolve(db);
    };
    req.onerror = req.onblocked = () => { opening = undefined; reject(new Error("Cache unavailable")); };
  });
  return opening;
}
export const key = (source, kind, id) => JSON.stringify([source, kind, id]);
export const byte_length = value => new TextEncoder().encode(value).length;
const costOf = row => row.cost ?? row.bytes;
const metadataOf = ({ raw, bytes, ...metadata }) => ({ ...metadata, cost: metadata.cost ?? bytes });
const inventory = rows => JSON.stringify(rows.map(({ source, ...row }) => metadataOf(row)));
const nextUse = async store => {
  const latest = await request(store.index("used").openKeyCursor(null, "prev"));
  return Math.max(Date.now(), (latest?.key || 0) + 1);
};
export async function read_entry(source, kind, id, touch = true) {
  const db = await database();
  const tx = db.transaction(["entries", "metadata"], "readwrite");
  const done = completion(tx); done.catch(() => {});
  const store = tx.objectStore("entries");
  const metadata = tx.objectStore("metadata");
  const row = await request(store.get(key(source, kind, id)));
  if (!row) { await done; return ""; }
  if (touch) {
    const updated = { ...row, used: await nextUse(metadata) };
    store.put(updated);
    metadata.put(metadataOf(updated));
  }
  await done;
  return row.raw;
}
export async function write_entry(source, kind, id, raw, count, cost, decide) {
  if (!Number.isSafeInteger(cost) || cost < 0) {
    throw new Error("Cache insertion requires a non-negative integer cost");
  }
  const db = await database();
  const tx = db.transaction(["entries", "metadata"], "readwrite");
  const done = completion(tx); done.catch(() => {});
  try {
    const store = tx.objectStore("entries");
    const metadata = tx.objectStore("metadata");
    const rows = await request(metadata.getAll());
    const recordKey = key(source, kind, id);
    const decision = JSON.parse(decide(inventory(rows), cost, recordKey));
    if (decision.error) throw new Error(decision.error);
    const victims = new Set(decision.victims);
    const old = rows.find(row => row.key === recordKey);
    const retained = rows.filter(row => row.key !== recordKey && (!victims.has(row.key) || row.pinned));
    // Defensive storage guard; the policy chooses victims from this same snapshot.
    if (retained.reduce((n, row) => n + costOf(row), cost) > 50 * 1024 * 1024) {
      throw new Error("Cache capacity exceeded; unpin traces to make space");
    }
    const row = { key: recordKey, source, kind, id, raw, count, cost,
      used: await nextUse(metadata), pinned: old?.pinned || false };
    for (const entry of rows) if (victims.has(entry.key) && !entry.pinned) {
      store.delete(entry.key);
      metadata.delete(entry.key);
    }
    store.put(row);
    metadata.put(metadataOf(row));
    await done;
    return inventory([...retained, row]);
  } catch (error) { try { tx.abort(); } catch {} await done.catch(() => {}); throw error; }
}
export async function pin_entry(source, id, pinned) {
  const db = await database();
  const tx = db.transaction(["entries", "metadata"], "readwrite");
  const done = completion(tx); done.catch(() => {});
  const store = tx.objectStore("entries");
  const metadata = tx.objectStore("metadata");
  const row = await request(store.get(key(source, "trace", id)));
  if (!row) { await done; throw new Error("Load the complete trace before pinning"); }
  store.put({ ...row, pinned });
  metadata.put(metadataOf({ ...row, pinned }));
  const rows = await request(metadata.getAll());
  await done;
  return inventory(rows);
}
export async function clear_entries(source) {
  const db = await database();
  const tx = db.transaction(["entries", "metadata"], "readwrite");
  const done = completion(tx); done.catch(() => {});
  const store = tx.objectStore("entries");
  const metadata = tx.objectStore("metadata");
  for (const id of await request(store.index("source").getAllKeys(source))) {
    store.delete(id);
    metadata.delete(id);
  }
  const rows = await request(metadata.getAll());
  await done;
  return inventory(rows);
}
const fail = (callback, error) => callback(error?.message || "Browser cache unavailable; using memory only");
export async function read_inventory() {
  const db = await database();
  const tx = db.transaction("metadata", "readonly");
  return inventory(await request(tx.objectStore("metadata").getAll()));
}
export const read = (source, kind, id, success, status, failure) => {
  read_entry(source, kind, id).then(async raw => { status(await read_inventory()); success(raw); }).catch(e => fail(failure, e));
};
export const peek = (source, id, touch, success, status, failure) => {
  read_entry(source, "trace", id, touch).then(async raw => { status(await read_inventory()); success(raw); }).catch(e => fail(failure, e));
};
export const write = (source, kind, id, raw, count, cost, decide, success, failure) => { write_entry(source, kind, id, raw, count, cost, decide).then(success).catch(e => fail(failure, e)); };
export const pin = (source, id, pinned, success, failure) => { pin_entry(source, id, pinned).then(success).catch(e => fail(failure, e)); };
export const clear = (source, success, failure) => { clear_entries(source).then(success).catch(e => fail(failure, e)); };
