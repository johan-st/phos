import "fake-indexeddb/auto";
import assert from "node:assert/strict";
import test from "node:test";
import * as cache from "../src/dashboard/search_cache_ffi.mjs";

// A separate test process starts with the actual old IndexedDB version.
test("upgrading a populated v1 cache preserves payload, cost, recency and pins", async () => {
  const db = await new Promise((resolve, reject) => {
    const request = indexedDB.open("spore-dashboard-search-v3", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("entries", { keyPath: "key" }).createIndex("source", "source");
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  const tx = db.transaction("entries", "readwrite");
  tx.objectStore("entries").put({ key: cache.key("source", "trace", "t"), source: "source", kind: "trace", id: "t", raw: "legacy", count: 42, bytes: 6, used: 123, pinned: true });
  await new Promise((resolve, reject) => { tx.oncomplete = resolve; tx.onerror = reject; });
  db.close();
  assert.deepEqual(JSON.parse(await cache.read_inventory()), [{ key: cache.key("source", "trace", "t"), kind: "trace", id: "t", count: 42, cost: 6, used: 123, pinned: true }]);
  assert.equal(await cache.read_entry("source", "trace", "t", false), "legacy");
  assert.equal(JSON.parse(await cache.read_inventory())[0].used, 123);
});
