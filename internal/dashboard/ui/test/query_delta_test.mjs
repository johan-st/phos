import "fake-indexeddb/auto";
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";
import { test } from "node:test";
import { read_cache, write_cache, clear_cache } from "../src/dashboard/cache_ffi.mjs";

test("filter transition commits a new checkpoint with unchanged baseline spans", async () => {
  await clear_cache();
  const baseline = JSON.parse(readFileSync(new URL("./fixtures/spans.json", import.meta.url)));
  await write_cache("all", JSON.stringify({ ...baseline, seq: 20 }));
  const filtered = { ...baseline, kind: "delta", query: "only-errors", base_query: baseline.query,
    base_seq: 20, seq: 1, upserts: [], members_added: [], members_removed: ["trace-2"],
    removed: [{ trace_id: "trace-2", id: "orphan" }] };
  await write_cache("errors", JSON.stringify(filtered));
  const cached = JSON.parse(await read_cache(baseline.source, "errors", 3600000));
  assert.equal(cached.query, "only-errors");
  assert.equal(cached.seq, 1);
  assert.equal(cached.upserts.length, 2);
  assert.ok(cached.upserts.every(s => s.trace_id === "trace-1"));
});
