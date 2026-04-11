// Package phos provides lightweight request-scoped tracing with optional export,
// W3C Trace Context propagation, and CLI-oriented trace rendering.
//
// Spans are stored in context: use [Start] (or [InSpan] / [InSpanE]) and call
// [Span.End] exactly once per span; [Span.End] is idempotent. While a span is
// open, [Span.Attrs], [Span.Event], [Span.Error], and [Span.Fail] are safe for
// concurrent use; after [Span.End], further mutations are ignored.
//
// Use [WithExporter] to attach an [Exporter] to a context (e.g. per request).
// Use [SetExporter] for a process-wide default when no context exporter is set.
// [SetExporter] uses an internal read-write lock so it is safe to call concurrently
// with [Start] and span completion.
//
// Propagation helpers [InjectTraceContext] and [ExtractTraceContext] work with
// any [Carrier] implementation, such as [HTTPHeaderCarrier] or [MapCarrier].
package phos
