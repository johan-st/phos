package phos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
)

func BenchmarkStartEndRoot(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, span := NewSpan(context.Background(), "root")
		span.End()
	}
}

func BenchmarkStartEndChild(b *testing.B) {
	ctx, parent := NewSpan(context.Background(), "parent")
	defer parent.End()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		childCtx, child := NewSpan(ctx, "child")
		_ = childCtx
		child.End()
	}
}

func BenchmarkInjectTraceContextSpan(b *testing.B) {
	ctx, span := NewSpan(context.Background(), "bench")
	defer span.End()
	carrier := MapCarrier{}
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		clear(carrier)
		InjectTraceContext(ctx, carrier)
	}
}

func BenchmarkInjectTraceContextExtracted(b *testing.B) {
	carrier := MapCarrier{}
	ctx := ExtractTraceContext(context.Background(), MapCarrier{
		TraceParentHeader: validVersion00TraceParent,
		TraceStateHeader:  "rojo=1,congo=2",
	})
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		clear(carrier)
		InjectTraceContext(ctx, carrier)
	}
}

func BenchmarkExtractTraceContextMapCarrier(b *testing.B) {
	carrier := MapCarrier{
		TraceParentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		TraceStateHeader:  "rojo=1,congo=2",
	}
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ExtractTraceContext(context.Background(), carrier)
	}
}

func BenchmarkExtractTraceContextHTTPHeaderCarrier(b *testing.B) {
	headers := http.Header{}
	headers.Set("TraceParent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	headers.Add("TraceState", "rojo=1")
	headers.Add("TraceState", "congo=2")
	carrier := HTTPHeaderCarrier{Header: headers}
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ExtractTraceContext(context.Background(), carrier)
	}
}

func BenchmarkSpanAttrs(b *testing.B) {
	cases := []struct {
		name  string
		count int
	}{
		{name: "3", count: 3},
		{name: "128", count: 128},
		{name: "512", count: 512},
	}

	for _, tc := range cases {
		attrs := benchmarkAttrs(tc.count)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				_, sp := NewSpan(context.Background(), "bench")
				s := sp
				s.Attrs(attrs...)
			}
		})
	}
}

func BenchmarkSpanEvent(b *testing.B) {
	attrs := []slog.Attr{
		slog.String("stage", "db"),
		slog.Int("rows", 3),
	}
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, sp := NewSpan(context.Background(), "bench")
		s := sp
		s.Event("query", attrs...)
	}
}

func BenchmarkSpanError(b *testing.B) {
	err := errors.New("boom")
	attrs := []slog.Attr{
		slog.String("op", "write"),
		slog.Int("attempt", 1),
	}
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, sp := NewSpan(context.Background(), "bench")
		s := sp
		s.Error(err, attrs...)
	}
}

func benchmarkAttrs(count int) []slog.Attr {
	attrs := make([]slog.Attr, 0, count)
	for i := 0; i < count; i++ {
		attrs = append(attrs, slog.String(fmt.Sprintf("attr_%03d", i), fmt.Sprintf("value_%03d", i)))
	}
	return attrs
}
