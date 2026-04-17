package phos

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestHTTPHeaderCarrierNilHeaderIsSafe(t *testing.T) {
	carrier := HTTPHeaderCarrier{}

	if got := carrier.Get(TraceParentHeader); got != "" {
		t.Fatalf("Get() = %q, want empty string", got)
	}

	carrier.Set(TraceParentHeader, validVersion00TraceParent)

	if keys := carrier.Keys(); keys != nil {
		t.Fatalf("Keys() = %#v, want nil", keys)
	}
}

func TestMapCarrierGetPrecedenceAndKeys(t *testing.T) {
	carrier := MapCarrier{
		TraceParentHeader: validVersion00TraceParent,
		"TraceParent":     "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
		TraceStateHeader:  "lowercase=1",
		"TraceState":      "mixed=1",
		"Z-Key":           "z",
		"A-Key":           "a",
	}

	traceParent := carrier.Get(TraceParentHeader)
	if traceParent != validVersion00TraceParent {
		t.Fatalf("Get(traceparent) = %q, want %q", traceParent, validVersion00TraceParent)
	}
	traceState := carrier.Get(TraceStateHeader)
	if traceState != "lowercase=1" {
		t.Fatalf("Get(tracestate) = %q, want %q", traceState, "lowercase=1")
	}

	wantKeys := []string{"A-Key", "TraceParent", "TraceState", "Z-Key", "traceparent", "tracestate"}
	if got := carrier.Keys(); !reflect.DeepEqual(got, wantKeys) {
		t.Fatalf("Keys() = %#v, want %#v", got, wantKeys)
	}
}

func TestMapCarrierGetFallsBackToCaseInsensitiveLookup(t *testing.T) {
	carrier := MapCarrier{
		"TRACEPARENT": validVersion00TraceParent,
		"TRACESTATE":  "rojo=1",
	}

	traceParent := carrier.Get(TraceParentHeader)
	if traceParent != validVersion00TraceParent {
		t.Fatalf("Get(traceparent) = %q, want %q", traceParent, validVersion00TraceParent)
	}
	traceState := carrier.Get(TraceStateHeader)
	if traceState != "rojo=1" {
		t.Fatalf("Get(tracestate) = %q, want %q", traceState, "rojo=1")
	}
}

func TestHTTPHeaderCarrierGetTraceStateValues(t *testing.T) {
	headers := http.Header{}
	headers.Add("TraceState", "rojo=1")
	headers.Add("TraceState", "congo=2")

	carrier := HTTPHeaderCarrier{Header: headers}
	got := carrier.Get(TraceStateHeader)
	if got != "rojo=1,congo=2" {
		t.Fatalf("Get(tracestate) = %q, want %q", got, "rojo=1,congo=2")
	}
}

func TestHTTPHeaderCarrierInjectUsesRealHTTPHeader(t *testing.T) {
	headers := http.Header{}
	ctx := ExtractTraceContext(context.Background(), MapCarrier{
		TraceParentHeader: validVersion00TraceParent,
		TraceStateHeader:  "rojo=1,congo=2",
	})
	ctx, started := NewSpan(ctx, "http-child")
	span := started

	InjectTraceContext(ctx, HTTPHeaderCarrier{Header: headers})

	gotParent := headers.Get("Traceparent")
	if gotParent == "" {
		t.Fatalf("Traceparent header missing")
	}
	traceParent, err := ParseTraceParent(gotParent)
	if err != nil {
		t.Fatalf("ParseTraceParent() error = %v", err)
	}
	if traceParent.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q, want remote trace id", traceParent.TraceID)
	}
	if traceParent.Parent != span.spanIDValue() {
		t.Fatalf("parent id = %q, want %q", traceParent.Parent, span.spanIDValue())
	}
	if traceParent.Flags != "01" {
		t.Fatalf("flags = %q, want 01", traceParent.Flags)
	}
	if got := headers.Values("Tracestate"); len(got) != 1 || got[0] != "rojo=1,congo=2" {
		t.Fatalf("Tracestate values = %#v, want [\"rojo=1,congo=2\"]", got)
	}
}

func TestInjectAndExtractIgnoreNilCarrier(t *testing.T) {
	ctx, started := NewSpan(context.Background(), "root")
	span := started

	InjectTraceContext(ctx, nil)

	extracted := ExtractTraceContext(ctx, nil)
	extractedSpan := SpanFromContext(extracted)
	if extractedSpan == nil {
		t.Fatalf("SpanFromContext(extracted) = nil, want existing span")
	}
	if extractedSpan.spanIDValue() != span.spanIDValue() {
		t.Fatalf("extracted span id = %q, want %q", extractedSpan.spanIDValue(), span.spanIDValue())
	}
}

func TestJoinComma(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "empty", values: nil, want: ""},
		{name: "single", values: []string{"rojo=1"}, want: "rojo=1"},
		{name: "multiple", values: []string{"rojo=1", "congo=2", "vendor=3"}, want: "rojo=1,congo=2,vendor=3"},
		{name: "embedded commas", values: []string{"a=1,2", "b=3"}, want: "a=1,2,b=3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinComma(tc.values); got != tc.want {
				t.Fatalf("joinComma(%#v) = %q, want %q", tc.values, got, tc.want)
			}
		})
	}
}

func TestTextprotoCanonicalMIMEHeaderKey(t *testing.T) {
	if got := textprotoCanonicalMIMEHeaderKey(TraceParentHeader); got != "Traceparent" {
		t.Fatalf("traceparent canonical key = %q, want %q", got, "Traceparent")
	}
	if got := textprotoCanonicalMIMEHeaderKey(TraceStateHeader); got != "Tracestate" {
		t.Fatalf("tracestate canonical key = %q, want %q", got, "Tracestate")
	}
	if got := textprotoCanonicalMIMEHeaderKey("x-custom-header"); got != "X-Custom-Header" {
		t.Fatalf("custom canonical key = %q, want %q", got, "X-Custom-Header")
	}
}
