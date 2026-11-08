import "fake-indexeddb/auto";
import { IDBObjectStore } from "fake-indexeddb";
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { test, beforeEach } from "node:test";
import { read_cache, write_cache, clear_cache, delete_traces } from "../src/dashboard/cache_ffi.mjs";

const fixture = JSON.parse(readFileSync(new URL("./fixtures/spans.json", import.meta.url)));
const route = "/dashboard?limit=100&window=900000";
const write = frame => write_cache(route, JSON.stringify(frame));
const read = async () => {
  const raw = await read_cache(fixture.source, route, 3600000);
  return raw ? JSON.parse(raw) : null;
};
beforeEach(clear_cache);

test("reset and delta persist data with their checkpoint, surviving reopen reads", async () => {
  const inventory = JSON.parse(await write(fixture));
  assert.equal(inventory.reduce((n, t) => n + t.count, 0), 3);
  assert.equal((await read()).upserts.length, 3);
  const changed = { ...fixture.upserts[0], name: "changed" };
  await write({ ...fixture, kind: "delta", base_seq: 1, seq: 2, version: fixture.version + 1,
    upserts: [changed], members_added: [] });
  const cached = await read();
  assert.equal(cached.seq, 2);
  assert.equal(cached.upserts.find(s => s.id === changed.id).name, "changed");
  await write(fixture); // delayed reset must not move the checkpoint backwards
  assert.equal((await read()).seq, 2);
});

test("a replay gap or missing baseline never advances the checkpoint", async () => {
  const gap = { ...fixture, kind: "delta", base_seq: 8, seq: 9, upserts: [], members_added: [] };
  await write(gap);
  assert.equal(await read(), null);
  await write(fixture);
  await write(gap);
  assert.equal((await read()).seq, 1);
});

test("whole-trace eviction invalidates coverage, but not other source caches", async () => {
  await write(fixture);
  await write_cache(route, JSON.stringify({ ...fixture, source: "other" }));
  await delete_traces([JSON.stringify([fixture.source, "trace-1"])]);
  assert.equal(await read(), null);
  assert.ok(await read_cache("other", route, 3600000));
});

test("restart resets source caches and an old epoch cannot return", async () => {
  await write(fixture);
  await write_cache("other-query", JSON.stringify(fixture));
  await write({ ...fixture, epoch: "restarted", now_ms: fixture.now_ms + 1000, upserts: [], members_added: [] });
  assert.equal(await read_cache(fixture.source, "other-query", 3600000), "");
  assert.equal((await read()).epoch, "restarted");
  await write(fixture);
  assert.equal((await read()).epoch, "restarted");
});

test("removals, expiry and clear never claim missing data is complete", async () => {
  await write(fixture);
  await write({ ...fixture, kind: "delta", base_seq: 1, seq: 2, upserts: [], members_added: [],
    removed: fixture.upserts.filter(s => s.trace_id === "trace-1").map(s => ({ trace_id: s.trace_id, id: s.id })),
    members_removed: ["trace-1"] });
  assert.equal((await read()).upserts.length, 1);
  assert.equal(await read_cache(fixture.source, route, -1), "");
  await clear_cache();
  assert.equal(await read(), null);
});

test("older queries cannot overwrite newer shared span records", async () => {
  const newer = { ...fixture, version: 10, upserts: fixture.upserts.map(s => ({ ...s, name: "newer" })) };
  await write(newer);
  await write_cache("other-query", JSON.stringify({ ...fixture, version: 1 }));
  assert.ok((await read()).upserts.every(s => s.name === "newer"));
});

test("empty queries cannot grow metadata without bound", async () => {
  for (let i = 0; i < 70; i++) {
    await write_cache(`query-${i}`, JSON.stringify({ ...fixture, upserts: [], members_added: [] }));
  }
  const db = await new Promise((resolve, reject) => {
    const request = indexedDB.open("spore-dashboard-spans-v2", 1);
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
  const count = await new Promise(resolve => {
    const request = db.transaction("queries").objectStore("queries").count();
    request.onsuccess = () => resolve(request.result);
  });
  db.close();
  assert.equal(count, 64);
});

test("a failed checkpoint transaction rolls back already queued span writes", async () => {
  await write(fixture);
  const original = IDBObjectStore.prototype.put;
  IDBObjectStore.prototype.put = function (...args) {
    if (this.name === "queries") throw new DOMException("full", "QuotaExceededError");
    return original.apply(this, args);
  };
  try {
    await assert.rejects(() => write({ ...fixture, seq: 2, version: fixture.version + 1,
      upserts: fixture.upserts.map(s => ({ ...s, name: "must roll back" })) }));
  } finally {
    IDBObjectStore.prototype.put = original;
  }
  const cached = await read();
  assert.equal(cached.seq, 1);
  assert.ok(cached.upserts.every(s => s.name !== "must roll back"));
});
