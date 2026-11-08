// Mutable values here are browser I/O handles, never application/UI state.
let stream;
let request;
let unlisten;
let stopPointer;
const requests = new Map();

export function flags() {
  const prefs = {};
  for (const key of ["liveUpdatesPaused", "sidebarOpen", "sidebarWidth", "inspectorWidth", "inspectorTab", "savedSearches", "timezoneUTC"]) {
    try {
      const value = localStorage.getItem("dashboard." + key);
      if (value !== null) prefs[key] = value;
    } catch { /* Storage may be disabled; the model supplies defaults. */ }
  }
  return JSON.stringify({
    route: location.pathname + location.search,
    bootstrap: document.getElementById("dashboard-bootstrap")?.textContent || "",
    viewport: innerWidth,
    prefs,
  });
}

export function close_stream() {
  for (const controller of requests.values()) controller.abort();
  requests.clear();
  request?.abort();
  request = undefined;
  if (!stream) return;
  const previous = stream;
  stream = undefined;
  previous.source.close();
  // GET is required by the existing dashboard workload route contract.
  fetch("/dashboard/live/close?connection_id=" + encodeURIComponent(previous.id),
    { cache: "no-store", keepalive: true }).catch(() => {});
}

export function fetch_data(path, route, family, success, failure) {
  requests.get(family)?.abort();
  const controller = new AbortController();
  requests.set(family, controller);
  const url = new URL(route, location.origin);
  url.pathname = path;
  fetch(url.pathname + url.search, { cache: "no-store", signal: controller.signal })
    .then(async response => {
      const body = await response.text();
      if (!response.ok) throw new Error("Dashboard request failed (" + response.status + "): " + body.slice(0, 180));
      return body;
    })
    .then(success)
    .catch(error => { if (error.name !== "AbortError") failure(String(error.message)); });
}

export function open_search_stream(route, batch, changed, ready, lost) {
  // Replacing a subscription does not abort independent detail/metadata reads.
  if (stream) {
    const previous = stream;
    previous.source.close();
    fetch("/dashboard/live/close?connection_id=" + encodeURIComponent(previous.id), { cache: "no-store", keepalive: true }).catch(() => {});
  }
  const id = crypto.randomUUID();
  const url = new URL(route, location.origin);
  url.pathname = "/dashboard/stream";
  url.searchParams.set("connection_id", id);
  const source = new EventSource(url.pathname + url.search);
  stream = { source, id };
  source.addEventListener("search-batch", event => batch(event.data));
  source.addEventListener("search-batch-gzip", event => decode_batch(event.data).then(batch).catch(lost));
  source.addEventListener("trace-changed", event => changed(event.data));
  source.addEventListener("trace-delta", event => changed(event.data));
  source.addEventListener("connection", ready);
  source.addEventListener("error", lost);
}

export function download_trace(id, raw) {
  const url = URL.createObjectURL(new Blob([raw], { type: "application/json" }));
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = "trace-" + id.replace(/[^a-zA-Z0-9_-]/g, "_") + ".json";
  anchor.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function open_stream(route, snapshot, ready, lost) {
  close_stream();
  const id = crypto.randomUUID();
  const url = new URL(route, location.origin);
  url.pathname = "/dashboard/stream";
  url.searchParams.set("connection_id", id);
  const source = new EventSource(url.pathname + url.search);
  stream = { source, id };
  source.addEventListener("span-batch", event => snapshot(event.data));
  source.addEventListener("span-batch-gzip", event => {
    // Transport decoding only; ordering/generation checks remain in Elm update.
    decode_batch(event.data).then(snapshot).catch(lost);
  });
  // This frame is sent after the agent has registered its SSE connection.
  // Fetching now also recovers an initial snapshot published before registration.
  source.addEventListener("connection", ready);
  source.addEventListener("error", lost);
}

export async function decode_batch(encoded) {
  const bytes = Uint8Array.from(atob(encoded), char => char.charCodeAt(0));
  return new Response(new Blob([bytes]).stream().pipeThrough(new DecompressionStream("gzip"))).text();
}

export function fetch_snapshot(route, success, failure) {
  request?.abort();
  const controller = new AbortController();
  request = controller;
  const url = new URL(route, location.origin);
  url.pathname = "/dashboard/spans";
  fetch(url.pathname + url.search, { cache: "no-store", signal: controller.signal })
    .then(response => {
      if (!response.ok) throw new Error("Dashboard request failed (" + response.status + ")");
      return response.text();
    })
    .then(success)
    .catch(error => {
      if (error.name !== "AbortError") failure(String(error.message));
    });
}

export function listen(onLocation, onKey, onResize) {
  unlisten?.();
  const handlers = {
    popstate: () => onLocation(location.pathname + location.search),
    resize: () => onResize(innerWidth),
    pagehide: () => close_stream(),
    keydown: event => {
      const target = event.target;
      if (event.ctrlKey || event.metaKey || event.altKey || target?.isContentEditable ||
          target?.closest("input, select, textarea")) return;
      // Preserve native Tab traversal through controls. Pane shortcuts apply
      // when the pane itself is focused, never trap focus in form controls.
      if (event.key === "Tab" && target?.closest("a, button, summary, [role='button']")) return;
      if (["Tab", "j", "k", "ArrowUp", "ArrowDown", "Escape"].includes(event.key)) {
        event.preventDefault();
        onKey(event.key, event.shiftKey);
      }
    },
  };
  for (const [name, handler] of Object.entries(handlers)) window.addEventListener(name, handler);
  unlisten = () => {
    for (const [name, handler] of Object.entries(handlers)) window.removeEventListener(name, handler);
  };
}

let stopTooltips;
export function listen_tooltips(hover, leave) {
  stopTooltips?.();
  const target = node => node?.closest?.("[data-tooltip]");
  const show = (event, focused) => {
    const node = target(event.target);
    if (!node || event.pointerType === "touch" || event.buttons > 0) {
      leave();
      return;
    }
    const rect = focused ? node.getBoundingClientRect() : undefined;
    const above = (rect ? rect.bottom : event.clientY) > innerHeight / 2;
    const x = rect ? rect.left : event.clientX;
    const y = rect ? (above ? rect.top : rect.bottom) : event.clientY;
    hover(node.getAttribute("data-tooltip"), Math.max(8, Math.round(x)),
      Math.round(y), above, focused);
  };
  const handlers = {
    pointermove: event => show(event, false),
    pointerout: event => {
      if (target(event.target) !== target(event.relatedTarget)) leave();
    },
    focusin: event => show(event, true),
    focusout: leave,
    pointerdown: leave,
    scroll: leave,
    resize: leave,
    blur: leave,
    keydown: event => { if (event.key === "Escape") leave(); },
  };
  for (const [name, handler] of Object.entries(handlers)) window.addEventListener(name, handler, true);
  stopTooltips = () => {
    for (const [name, handler] of Object.entries(handlers)) window.removeEventListener(name, handler, true);
  };
}

export function watch_pointer(onMove, onRelease) {
  unwatch_pointer();
  const move = event => onMove(Math.round(event.clientX));
  const release = () => { unwatch_pointer(); onRelease(); };
  window.addEventListener("mousemove", move);
  window.addEventListener("mouseup", release);
  window.addEventListener("blur", release);
  stopPointer = () => {
    window.removeEventListener("mousemove", move);
    window.removeEventListener("mouseup", release);
    window.removeEventListener("blur", release);
  };
}

export function unwatch_pointer() {
  stopPointer?.();
  stopPointer = undefined;
}

export function push_history(route) {
  history.pushState(null, "", route);
}

export function persist(key, value) {
  try { localStorage.setItem("dashboard." + key, value); } catch {}
}

export function focus(id) {
  requestAnimationFrame(() => document.getElementById(id)?.focus({ preventScroll: true }));
}

export function reveal_selection(id) {
  requestAnimationFrame(() => {
    document.getElementById(id)?.querySelector(".selected")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  });
}

export function after(milliseconds, callback) {
  setTimeout(callback, milliseconds);
}

export function cancel_prefetch() {
  requests.get("prefetch")?.abort();
  requests.delete("prefetch");
}

let stopCandidates;
export function watch_candidates(onVisible) {
  stopCandidates?.();
  if (typeof IntersectionObserver === "undefined") return;
  const root = document.getElementById("app");
  if (!root) return;
  const observed = new Set();
  const visible = new Set();
  let pending;
  let last = "";
  const publish = () => {
    cancelAnimationFrame(pending);
    pending = requestAnimationFrame(() => {
      const ids = [...root.querySelectorAll("a[data-trace-id]")]
        .filter(node => visible.has(node)).map(node => node.dataset.traceId);
      const raw = JSON.stringify(ids);
      if (raw !== last) { last = raw; onVisible(raw); }
    });
  };
  const intersection = new IntersectionObserver(entries => {
    for (const entry of entries) {
      if (entry.isIntersecting) visible.add(entry.target);
      else visible.delete(entry.target);
    }
    publish();
  });
  const reconcile = () => {
    for (const node of observed) if (!root.contains(node)) {
      intersection.unobserve(node); observed.delete(node); visible.delete(node);
    }
    for (const node of root.querySelectorAll("a[data-trace-id]")) if (!observed.has(node)) {
      observed.add(node); intersection.observe(node);
    }
    publish();
  };
  const mutations = new MutationObserver(reconcile);
  mutations.observe(root, { childList: true, subtree: true });
  reconcile();
  stopCandidates = () => {
    mutations.disconnect(); intersection.disconnect(); cancelAnimationFrame(pending);
    window.removeEventListener("pagehide", stopCandidates);
  };
  window.addEventListener("pagehide", stopCandidates);
}
