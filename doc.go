// Package phos provides lightweight request-scoped tracing with optional export,
// W3C Trace Context propagation, and CLI-oriented trace rendering.
//
// Spans are stored in context: use [NewSpan] and call [Span.End] exactly once
// per span; [Span.End] is idempotent. While a span is open, [Span.Attrs],
// [Span.Event], and [Span.Error] are safe for concurrent use; after [Span.End],
// further mutations are ignored. [Span.Fail] records a terminal error and ends
// the span.
//
// Use [WithExporter] to attach an [Exporter] to a context (e.g. per request).
// Use [SetExporter] for a process-wide default when no context exporter is set.
// [SetExporter] uses an internal read-write lock so it is safe to call concurrently
// with [NewSpan] and span completion.
//
// Once a span ends it is no longer returned by [SpanFromContext]. Helper calls
// such as [Attrs], [Event], [Error], and [Fail] therefore become no-ops when a
// context only carries an ended span.
//
// Call [DrainAndClose] to start shutdown admission control. While draining, new
// root spans return no-op spans but child spans on still-open local parents are
// still allowed. Call [WaitForClosed] before process exit to wait until all open
// local span trees have closed.
//
// Propagation helpers [InjectTraceContext] and [ExtractTraceContext] work with
// any [Carrier] implementation, such as [HTTPHeaderCarrier] or [MapCarrier].
package phos
