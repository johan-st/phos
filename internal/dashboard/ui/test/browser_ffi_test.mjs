import test from "node:test";
import assert from "node:assert/strict";
import { gzipSync } from "node:zlib";
import * as browser from "../src/dashboard/browser_ffi.mjs";

test("large SSE batches use lossless native gzip transport decoding", async () => {
  const body = JSON.stringify({ spans: "å λ 🚀".repeat(100000) });
  const compressed = gzipSync(body).toString("base64");
  assert.equal(await browser.decode_batch(compressed), body);
  await assert.rejects(() => browser.decode_batch("not gzip"));
});

test("stream replacement closes its predecessor and waits for the agent connection frame", async () => {
  globalThis.location = { origin: "http://localhost:9091" };
  const sources = [];
  const requests = [];
  globalThis.fetch = async (url, options) => { requests.push({ url, options }); return { ok: true, text: async () => "{}" }; };
  globalThis.EventSource = class {
    handlers = {};
    constructor(url) { this.url = url; sources.push(this); }
    addEventListener(name, handler) { this.handlers[name] = handler; }
    close() { this.closed = true; }
  };
  let ready = 0;
  let payload;
  browser.open_stream("/dashboard?q=a%26b", value => { payload = value; }, () => { ready++; }, () => {});
  assert.equal(ready, 0);
  sources[0].handlers.connection();
  assert.equal(ready, 1);
  sources[0].handlers["span-batch"]({ data: "snapshot" });
  assert.equal(payload, "snapshot");
  assert.match(sources[0].url, /q=a%26b/);
  browser.open_stream("/dashboard?trace_id=next", () => {}, () => {}, () => {});
  assert.equal(sources[0].closed, true);
  assert.equal(requests.length, 1);
  assert.match(requests[0].url, /^\/dashboard\/live\/close\?connection_id=/);
  browser.close_stream();
  assert.equal(sources[1].closed, true);
});

test("fetch errors reach update and replacement aborts pending requests", async () => {
  globalThis.location = { origin: "http://localhost:9091" };
  const signals = [];
  globalThis.fetch = async (_, options) => {
    signals.push(options.signal);
    return { ok: false, status: 503 };
  };
  const error = await new Promise(resolve => browser.fetch_snapshot("/dashboard", () => assert.fail(), resolve));
  assert.match(error, /503/);
  const second = new Promise(resolve => browser.fetch_snapshot("/dashboard?q=new", () => assert.fail(), resolve));
  assert.equal(signals[0].aborted, true);
  await second;
  browser.close_stream();
  assert.equal(signals[1].aborted, true);
});

test("pointer subscriptions are installed only for a resize and fully removed", () => {
  const listeners = new Map();
  globalThis.window = {
    addEventListener: (name, callback) => listeners.set(name, callback),
    removeEventListener: name => listeners.delete(name),
  };
  let x;
  let released = 0;
  browser.watch_pointer(value => { x = value; }, () => { released++; });
  listeners.get("mousemove")({ clientX: 100.4 });
  assert.equal(x, 100);
  listeners.get("mouseup")();
  assert.equal(released, 1);
  assert.equal(listeners.size, 0);
});

test("prefetch cancellation never aborts selected trace requests", () => {
  globalThis.location = { origin: "http://localhost:9091" };
  const signals = [];
  globalThis.fetch = (_, options) => {
    signals.push(options.signal);
    return new Promise(() => {});
  };
  browser.fetch_data("/dashboard/trace", "/dashboard?trace_id=selected", "trace", () => {}, () => {});
  browser.fetch_data("/dashboard/trace", "/dashboard?trace_id=potential", "prefetch", () => {}, () => {});
  browser.cancel_prefetch();
  assert.equal(signals[0].aborted, false);
  assert.equal(signals[1].aborted, true);
  browser.close_stream();
  assert.equal(signals[0].aborted, true);
});

test("tooltip browser events use the nearest hint and dismiss on exit, scroll and Escape", () => {
  const listeners = new Map();
  globalThis.window = {
    addEventListener: (name, callback) => listeners.set(name, callback),
    removeEventListener: name => listeners.delete(name),
  };
  globalThis.innerHeight = 800;
  const received = [];
  let left = 0;
  const hint = {
    getAttribute: () => "event @ +12.345 ms",
    getBoundingClientRect: () => ({ left: 40, top: 690, bottom: 710 }),
  };
  const child = { closest: selector => selector === "[data-tooltip]" ? hint : undefined };
  browser.listen_tooltips((...args) => received.push(args), () => { left++; });
  listeners.get("pointermove")({ target: child, clientX: 52.4, clientY: 700.6, pointerType: "mouse", buttons: 0 });
  assert.deepEqual(received.pop(), ["event @ +12.345 ms", 52, 701, true, false]);
  listeners.get("focusin")({ target: child });
  assert.deepEqual(received.pop(), ["event @ +12.345 ms", 40, 690, true, true]);
  listeners.get("pointerout")({ target: child, relatedTarget: child });
  assert.equal(left, 0);
  listeners.get("pointerout")({ target: child, relatedTarget: null });
  listeners.get("scroll")();
  listeners.get("keydown")({ key: "Escape" });
  assert.equal(left, 3);
  listeners.get("pointermove")({ target: child, pointerType: "touch" });
  assert.equal(received.length, 0);
  assert.equal(left, 4);
  const previous = listeners.get("pointermove");
  browser.listen_tooltips(() => {}, () => {});
  assert.notEqual(listeners.get("pointermove"), previous);
  assert.equal(listeners.size, 9);
});
