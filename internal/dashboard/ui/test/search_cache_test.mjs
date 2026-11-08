import "fake-indexeddb/auto";
import { IDBObjectStore } from "fake-indexeddb";
import assert from "node:assert/strict";
import { test, beforeEach } from "node:test";
import * as cache from "../src/dashboard/search_cache_ffi.mjs";
import { input_zone, parse_zone } from "../src/dashboard/format_ffi.mjs";

beforeEach(async () => { await cache.clear_entries("a"); await cache.clear_entries("b"); });

// Exercise the same pure admission function used by the app, against fresh
// IndexedDB transaction inventories (not a duplicate eviction implementation).
const { admission } = await import("../build/dev/javascript/dashboard/dashboard/search_cache.mjs");
const CAP = 50 * 1024 * 1024;
const put = (source, kind, id, raw, count, cost) => cache.write_entry(source, kind, id, raw, count, cost, admission);

test("LRU uses arbitrary declared cost, touches reads, and peeks preserve recency", async () => {
  await put("a", "trace", "a", "tiny", 100000, CAP / 2);
  await put("a", "trace", "b", "also tiny", 100000, CAP / 2);
  await cache.read_entry("a", "trace", "a");
  await cache.read_entry("a", "trace", "b", false);
  await put("a", "trace", "c", "c", 100000, CAP / 2);
  assert.equal(await cache.read_entry("a", "trace", "b"), "");
  assert.equal(await cache.read_entry("a", "trace", "a"), "tiny");
  assert.equal(await cache.read_entry("a", "trace", "c"), "c");
  assert.equal(JSON.parse(await cache.read_inventory()).reduce((n, e) => n + e.cost, 0), CAP);
});

test("pins count toward capacity and failed admission preserves previous records", async () => {
  await put("a", "trace", "pin", "p", 1, CAP - 1);
  await cache.pin_entry("a", "pin", true);
  await put("b", "query", "q", "q", 0, 1);
  await assert.rejects(put("a", "trace", "overflow", "x", 1, 2));
  assert.equal(await cache.read_entry("b", "query", "q"), "q");
  assert.equal(await cache.read_entry("a", "trace", "pin"), "p");
  await assert.rejects(put("a", "trace", "pin", "new", 1, CAP + 1));
  assert.equal(await cache.read_entry("a", "trace", "pin"), "p");
  await cache.clear_entries("a");
  assert.equal(await cache.read_entry("b", "query", "q"), "q");
});

test("replacement accounts for old cost and quota failure rolls back evictions", async () => {
  await put("a", "trace", "t", "old", 1, CAP);
  await put("a", "trace", "t", "smaller", 1, 1);
  const original = IDBObjectStore.prototype.put;
  IDBObjectStore.prototype.put = () => { throw new DOMException("quota", "QuotaExceededError"); };
  try { await assert.rejects(put("a", "query", "q", "new", 0, CAP)); }
  finally { IDBObjectStore.prototype.put = original; }
  assert.equal(await cache.read_entry("a", "trace", "t"), "smaller");
  await assert.rejects(cache.pin_entry("a", "not-loaded", true));
});

test("concurrent admissions see committed LRU state and enforce the shared cap", async () => {
  await put("a", "trace", "old", "old", 1, CAP / 2);
  await Promise.all([
    put("a", "trace", "first", "first", 1, CAP / 2),
    put("b", "trace", "second", "second", 1, CAP / 2),
  ]);
  const rows = JSON.parse(await cache.read_inventory());
  assert.equal(rows.length, 2);
  assert.equal(rows.reduce((n, e) => n + e.cost, 0), CAP);
  assert.equal(await cache.read_entry("a", "trace", "old"), "");
});

test("insertion requires an explicit valid integer cost", async () => {
  for (const cost of [undefined, -1, 1.5, NaN, Infinity]) {
    await assert.rejects(put("a", "trace", "t", "payload", 1, cost));
  }
  await put("a", "trace", "free", "payload", 1, 0);
  assert.equal(await cache.read_entry("a", "trace", "free"), "payload");
});

test("LRU does not expire idle records; date controls still handle DST", async () => {
  const now = Date.now;
  Date.now = () => 1000;
  try {
    await put("a", "trace", "idle", "[]", 0, 2);
    Date.now = () => 7201001;
    assert.equal(await cache.read_entry("a", "trace", "idle"), "[]");
  } finally { Date.now = now; }
  const old = process.env.TZ;
  process.env.TZ = "Europe/Stockholm";
  try {
    assert.equal(parse_zone("2026-03-29T02:30", false), -1);
    const utc = parse_zone("2026-03-29T02:30", true);
    assert.equal(input_zone(utc, true), "2026-03-29T02:30");
    assert.equal(input_zone(utc, false), "2026-03-29T04:30");
    assert.equal(parse_zone("2026-10-25T02:30", false), Date.parse("2026-10-25T00:30:00Z"));
  } finally { if (old === undefined) delete process.env.TZ; else process.env.TZ = old; }
});

test("legacy byte-sized entries migrate to cost accounting without discarding data", async () => {
  const db = await new Promise((resolve, reject) => {
    const request = indexedDB.open("spore-dashboard-search-v3", 3);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  const tx = db.transaction("entries", "readwrite");
  tx.objectStore("entries").put({ key: cache.key("a", "trace", "legacy"), source: "a", kind: "trace", id: "legacy", raw: "old", count: 1, bytes: CAP, used: 1, pinned: false });
  await new Promise((resolve, reject) => { tx.oncomplete = resolve; tx.onerror = reject; });
  db.close();
  assert.equal(await cache.read_entry("a", "trace", "legacy"), "old");
  assert.equal(JSON.parse(await cache.read_inventory())[0].cost, CAP);
  await put("a", "trace", "new", "new", 1, 1);
  assert.equal(await cache.read_entry("a", "trace", "legacy"), "");
});
