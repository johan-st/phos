package phos

import (
	"context"
	"net/http"
	"testing"
)

const (
	validVersion00TraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	validUnsampledTraceParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	validFutureTraceParent    = "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09-extra"
)

func TestW3CTraceContextSpec(t *testing.T) {
	t.Run("3.2.2 traceparent", func(t *testing.T) {
		t.Run("string format", func(t *testing.T) {
			traceParent := TraceParent{
				Version: "00",
				TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
				Parent:  "00f067aa0ba902b7",
				Flags:   "01",
			}

			if got := traceParent.String(); got != validVersion00TraceParent {
				t.Fatalf("TraceParent.String() = %q, want %q", got, validVersion00TraceParent)
			}
		})

		acceptCases := []struct {
			name  string
			value string
			want  TraceParent
		}{
			{
				name:  "version 00",
				value: validVersion00TraceParent,
				want: TraceParent{
					Version: "00",
					TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
					Parent:  "00f067aa0ba902b7",
					Flags:   "01",
				},
			},
			{
				name:  "future version with extra data",
				value: validFutureTraceParent,
				want: TraceParent{
					Version: "01",
					TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
					Parent:  "00f067aa0ba902b7",
					Flags:   "09",
				},
			},
			{
				name:  "future version exact minimum length",
				value: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09",
				want: TraceParent{
					Version: "01",
					TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
					Parent:  "00f067aa0ba902b7",
					Flags:   "09",
				},
			},
			{
				name:  "trim surrounding whitespace",
				value: "  " + validVersion00TraceParent + " \t",
				want: TraceParent{
					Version: "00",
					TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
					Parent:  "00f067aa0ba902b7",
					Flags:   "01",
				},
			},
		}

		for _, tc := range acceptCases {
			t.Run("accepts/"+tc.name, func(t *testing.T) {
				got, err := ParseTraceParent(tc.value)
				if err != nil {
					t.Fatalf("ParseTraceParent() error = %v", err)
				}
				if got != tc.want {
					t.Fatalf("ParseTraceParent() = %#v, want %#v", got, tc.want)
				}
			})
		}

		rejectCases := []struct {
			name  string
			value string
		}{
			{name: "too short", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7"},
			{name: "invalid version chars", value: "0g-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
			{name: "forbidden version ff", value: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
			{name: "missing first dash", value: "004bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
			{name: "missing second dash", value: "00-4bf92f3577b34da6a3ce929d0e0e473600f067aa0ba902b7-01"},
			{name: "missing third dash", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b701"},
			{name: "version 00 with extra fields", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"},
			{name: "future version without separator after flags", value: "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-09extra"},
			{name: "trace id wrong length", value: "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01"},
			{name: "trace id uppercase", value: "00-4BF92F3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"},
			{name: "trace id all zeros", value: "00-00000000000000000000000000000000-00f067aa0ba902b7-01"},
			{name: "parent id wrong length", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b-01"},
			{name: "parent id uppercase", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00F067aa0ba902b7-01"},
			{name: "parent id all zeros", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"},
			{name: "flags wrong length", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1"},
			{name: "flags invalid chars", value: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g"},
		}

		for _, tc := range rejectCases {
			t.Run("rejects/"+tc.name, func(t *testing.T) {
				if _, err := ParseTraceParent(tc.value); err == nil {
					t.Fatalf("ParseTraceParent(%q) error = nil, want error", tc.value)
				}
			})
		}
	})

	t.Run("3.3 tracestate", func(t *testing.T) {
		extractCases := []struct {
			name    string
			carrier Carrier
			want    traceContext
		}{
			{
				name: "record invalid traceparent and ignore associated tracestate",
				carrier: MapCarrier{
					TraceParentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
					TraceStateHeader:  "rojo=1",
				},
				want: traceContext{
					diagnostics: []traceContextDiagnostic{{
						event:  "tracecontext.invalid_traceparent",
						header: TraceParentHeader,
						value:  "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
						reason: errInvalidParentID.Error(),
					}},
				},
			},
			{
				name: "ignore tracestate without traceparent",
				carrier: MapCarrier{
					TraceStateHeader: "rojo=1",
				},
				want: traceContext{},
			},
			{
				name: "invalid tracestate does not break traceparent",
				carrier: MapCarrier{
					TraceParentHeader: validVersion00TraceParent,
					TraceStateHeader:  "bad=\nvalue",
				},
				want: traceContext{
					traceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
					parentID:   "00f067aa0ba902b7",
					traceFlags: "01",
					diagnostics: []traceContextDiagnostic{{
						event:  "tracecontext.invalid_tracestate",
						header: TraceStateHeader,
						value:  "bad=\nvalue",
						reason: "tracestate value contains non-printable characters",
					}},
				},
			},
			{
				name: "mixed case headers",
				carrier: MapCarrier{
					"TraceParent": validVersion00TraceParent,
					"TraceState":  "rojo=1",
				},
				want: traceContext{
					traceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
					parentID:   "00f067aa0ba902b7",
					traceFlags: "01",
					traceState: "rojo=1",
				},
			},
		}

		for _, tc := range extractCases {
			t.Run("extracts/"+tc.name, func(t *testing.T) {
				ctx := ExtractTraceContext(context.Background(), tc.carrier)
				got := traceContextFromContext(ctx)
				assertTraceContext(t, got, tc.want)
			})
		}

		httpCases := []struct {
			name  string
			build func() http.Header
			want  string
		}{
			{
				name: "combines multiple tracestate headers in order",
				build: func() http.Header {
					headers := http.Header{}
					headers.Set("TraceParent", validVersion00TraceParent)
					headers.Add("TraceState", "rojo=1")
					headers.Add("TraceState", "congo=2")
					headers.Add("TraceState", "vendor=3")
					return headers
				},
				want: "rojo=1,congo=2,vendor=3",
			},
		}

		for _, tc := range httpCases {
			t.Run("http/"+tc.name, func(t *testing.T) {
				ctx := ExtractTraceContext(context.Background(), HTTPHeaderCarrier{Header: tc.build()})
				if got := traceContextFromContext(ctx).traceState; got != tc.want {
					t.Fatalf("traceState = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("3.2.2.5 trace-flags", func(t *testing.T) {
		flagCases := []struct {
			name  string
			flags string
			want  string
		}{
			{name: "sampled set", flags: "01", want: "01"},
			{name: "sampled clear", flags: "00", want: "00"},
			{name: "sampled preserved with future bits", flags: "09", want: "01"},
			{name: "future bits dropped when unsampled", flags: "08", want: "00"},
			{name: "invalid flags default unsampled", flags: "zz", want: "00"},
		}

		for _, tc := range flagCases {
			t.Run(tc.name, func(t *testing.T) {
				if got := outgoingTraceFlagsForValue(tc.flags); got != tc.want {
					t.Fatalf("outgoingTraceFlagsForValue(%q) = %q, want %q", tc.flags, got, tc.want)
				}
			})
		}
	})

	t.Run("3.4 mutation and propagation", func(t *testing.T) {
		propagationCases := []struct {
			name                string
			setup               func() (context.Context, string)
			wantVersion         string
			wantTraceID         string
			wantFlags           string
			wantTraceState      string
			wantLowercaseHeader bool
		}{
			{
				name: "preserve incoming trace id and tracestate",
				setup: func() (context.Context, string) {
					ctx := ExtractTraceContext(context.Background(), MapCarrier{
						TraceParentHeader: validVersion00TraceParent,
						TraceStateHeader:  "rojo=1,congo=2",
					})
					ctx, startedSpan := Start(ctx, "child")
					return ctx, startedSpan.(*span).id
				},
				wantVersion:         "00",
				wantTraceID:         "4bf92f3577b34da6a3ce929d0e0e4736",
				wantFlags:           "01",
				wantTraceState:      "rojo=1,congo=2",
				wantLowercaseHeader: true,
			},
			{
				name: "reencode future version as version 00",
				setup: func() (context.Context, string) {
					ctx := ExtractTraceContext(context.Background(), MapCarrier{
						TraceParentHeader: validFutureTraceParent,
						TraceStateHeader:  "rojo=1",
					})
					ctx, startedSpan := Start(ctx, "child")
					return ctx, startedSpan.(*span).id
				},
				wantVersion:         "00",
				wantTraceID:         "4bf92f3577b34da6a3ce929d0e0e4736",
				wantFlags:           "01",
				wantTraceState:      "rojo=1",
				wantLowercaseHeader: true,
			},
			{
				name: "new trace defaults unsampled",
				setup: func() (context.Context, string) {
					ctx, startedSpan := Start(context.Background(), "root")
					return ctx, startedSpan.(*span).id
				},
				wantVersion:         "00",
				wantTraceID:         "",
				wantFlags:           "00",
				wantTraceState:      "",
				wantLowercaseHeader: true,
			},
		}

		for _, tc := range propagationCases {
			t.Run("injects/"+tc.name, func(t *testing.T) {
				ctx, expectedParentID := tc.setup()
				carrier := MapCarrier{}
				InjectTraceContext(ctx, carrier)

				traceParent, err := ParseTraceParent(carrier[TraceParentHeader])
				if err != nil {
					t.Fatalf("ParseTraceParent() error = %v", err)
				}
				if traceParent.Version != tc.wantVersion {
					t.Fatalf("Version = %q, want %q", traceParent.Version, tc.wantVersion)
				}
				if tc.wantTraceID != "" && traceParent.TraceID != tc.wantTraceID {
					t.Fatalf("TraceID = %q, want %q", traceParent.TraceID, tc.wantTraceID)
				}
				if traceParent.Parent != expectedParentID {
					t.Fatalf("Parent = %q, want current span id %q", traceParent.Parent, expectedParentID)
				}
				if traceParent.Flags != tc.wantFlags {
					t.Fatalf("Flags = %q, want %q", traceParent.Flags, tc.wantFlags)
				}
				if got := carrier[TraceStateHeader]; got != tc.wantTraceState {
					t.Fatalf("TraceState = %q, want %q", got, tc.wantTraceState)
				}
				if tc.wantLowercaseHeader {
					if _, ok := carrier[TraceParentHeader]; !ok {
						t.Fatalf("carrier missing %q header", TraceParentHeader)
					}
					if _, ok := carrier["TraceParent"]; ok {
						t.Fatalf("carrier unexpectedly used non-lowercase traceparent header")
					}
				}
			})
		}

		startCases := []struct {
			name            string
			setup           func() (context.Context, *span)
			wantTraceID     string
			wantParentID    string
			wantTraceFlags  string
			wantTraceState  string
			wantOutgoingRef func(*span) string
		}{
			{
				name: "start with extracted trace context keeps remote trace data",
				setup: func() (context.Context, *span) {
					ctx := ExtractTraceContext(context.Background(), MapCarrier{
						TraceParentHeader: validUnsampledTraceParent,
						TraceStateHeader:  "rojo=1",
					})
					ctx, childSpan := Start(ctx, "child")
					return ctx, childSpan.(*span)
				},
				wantTraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
				wantParentID:   "00f067aa0ba902b7",
				wantTraceFlags: "00",
				wantTraceState: "rojo=1",
				wantOutgoingRef: func(s *span) string {
					return s.id
				},
			},
		}

		for _, tc := range startCases {
			t.Run("start/"+tc.name, func(t *testing.T) {
				ctx, span := tc.setup()
				if span.traceID != tc.wantTraceID {
					t.Fatalf("traceID = %q, want %q", span.traceID, tc.wantTraceID)
				}
				if span.parentID != tc.wantParentID {
					t.Fatalf("parentID = %q, want %q", span.parentID, tc.wantParentID)
				}
				if span.traceFlags != tc.wantTraceFlags {
					t.Fatalf("traceFlags = %q, want %q", span.traceFlags, tc.wantTraceFlags)
				}
				if span.traceState != tc.wantTraceState {
					t.Fatalf("traceState = %q, want %q", span.traceState, tc.wantTraceState)
				}

				outgoing := MapCarrier{}
				InjectTraceContext(ctx, outgoing)
				traceParent, err := ParseTraceParent(outgoing[TraceParentHeader])
				if err != nil {
					t.Fatalf("ParseTraceParent() error = %v", err)
				}
				if traceParent.Parent != tc.wantOutgoingRef(span) {
					t.Fatalf("Parent = %q, want %q", traceParent.Parent, tc.wantOutgoingRef(span))
				}
				if traceParent.Flags != tc.wantTraceFlags {
					t.Fatalf("Flags = %q, want %q", traceParent.Flags, tc.wantTraceFlags)
				}
			})
		}
	})
}

func TestExtractTraceContextInvalidInputLeavesContextUntouched(t *testing.T) {
	type ctxKey string

	base := context.WithValue(context.Background(), ctxKey("keep"), "value")
	got := ExtractTraceContext(base, MapCarrier{
		TraceParentHeader: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		TraceStateHeader:  "rojo=1",
	})

	if got.Value(ctxKey("keep")) != "value" {
		t.Fatalf("context value lost after invalid extract")
	}
	traceCtx := traceContextFromContext(got)
	if traceCtx.traceID != "" || traceCtx.parentID != "" || traceCtx.traceFlags != "" || traceCtx.traceState != "" {
		t.Fatalf("trace context = %#v, want only diagnostic metadata", traceCtx)
	}
	if len(traceCtx.diagnostics) != 1 {
		t.Fatalf("len(diagnostics) = %d, want 1", len(traceCtx.diagnostics))
	}
	if traceCtx.diagnostics[0].event != "tracecontext.invalid_traceparent" {
		t.Fatalf("diagnostic event = %q, want %q", traceCtx.diagnostics[0].event, "tracecontext.invalid_traceparent")
	}
}

func TestExtractTraceContextMissingTraceParentIgnoresTraceState(t *testing.T) {
	ctx := ExtractTraceContext(context.Background(), HTTPHeaderCarrier{
		Header: http.Header{
			"Tracestate": []string{"rojo=1", "congo=2"},
		},
	})

	assertTraceContext(t, traceContextFromContext(ctx), traceContext{})
}

func TestInjectExtractStartRoundTripHTTPHeaderCarrier(t *testing.T) {
	rootCtx, root := Start(context.Background(), "root")
	rootSpan := root.(*span)

	headers := http.Header{}
	InjectTraceContext(rootCtx, HTTPHeaderCarrier{Header: headers})

	extracted := ExtractTraceContext(context.Background(), HTTPHeaderCarrier{Header: headers})
	childCtx, child := Start(extracted, "child")
	childSpan := child.(*span)

	if childSpan.traceID != rootSpan.traceID {
		t.Fatalf("child traceID = %q, want %q", childSpan.traceID, rootSpan.traceID)
	}
	if childSpan.parentID != rootSpan.id {
		t.Fatalf("child parentID = %q, want %q from extracted remote parent", childSpan.parentID, rootSpan.id)
	}

	outgoing := http.Header{}
	InjectTraceContext(childCtx, HTTPHeaderCarrier{Header: outgoing})
	traceParent, err := ParseTraceParent(outgoing.Get("Traceparent"))
	if err != nil {
		t.Fatalf("ParseTraceParent() error = %v", err)
	}
	if traceParent.TraceID != rootSpan.traceID {
		t.Fatalf("outgoing trace id = %q, want %q", traceParent.TraceID, rootSpan.traceID)
	}
	if traceParent.Parent != childSpan.id {
		t.Fatalf("outgoing parent = %q, want %q", traceParent.Parent, childSpan.id)
	}
}

func TestInjectExtractStartRoundTripMapCarrier(t *testing.T) {
	rootCtx, root := Start(context.Background(), "root")
	rootSpan := root.(*span)

	outgoing := MapCarrier{}
	InjectTraceContext(rootCtx, outgoing)

	extracted := ExtractTraceContext(context.Background(), outgoing)
	_, child := Start(extracted, "child")
	childSpan := child.(*span)

	if childSpan.traceID != rootSpan.traceID {
		t.Fatalf("child traceID = %q, want %q", childSpan.traceID, rootSpan.traceID)
	}
	if childSpan.parentID != rootSpan.id {
		t.Fatalf("child parentID = %q, want %q from extracted remote parent", childSpan.parentID, rootSpan.id)
	}
}

func TestInjectWithoutActiveSpanCreatesValidOutgoingHeader(t *testing.T) {
	carrier := MapCarrier{}
	InjectTraceContext(context.Background(), carrier)

	traceParent, err := ParseTraceParent(carrier[TraceParentHeader])
	if err != nil {
		t.Fatalf("ParseTraceParent() error = %v", err)
	}
	if traceParent.TraceID == "" || traceParent.Parent == "" {
		t.Fatalf("traceparent = %#v, want generated trace and parent ids", traceParent)
	}
	if traceParent.Flags != "00" {
		t.Fatalf("trace flags = %q, want %q", traceParent.Flags, "00")
	}
}

func assertTraceContext(t *testing.T, got, want traceContext) {
	t.Helper()

	if got.traceID != want.traceID {
		t.Fatalf("traceID = %q, want %q", got.traceID, want.traceID)
	}
	if got.parentID != want.parentID {
		t.Fatalf("parentID = %q, want %q", got.parentID, want.parentID)
	}
	if got.traceFlags != want.traceFlags {
		t.Fatalf("traceFlags = %q, want %q", got.traceFlags, want.traceFlags)
	}
	if got.traceState != want.traceState {
		t.Fatalf("traceState = %q, want %q", got.traceState, want.traceState)
	}
	if len(got.diagnostics) != len(want.diagnostics) {
		t.Fatalf("len(diagnostics) = %d, want %d", len(got.diagnostics), len(want.diagnostics))
	}
	for i := range want.diagnostics {
		if got.diagnostics[i] != want.diagnostics[i] {
			t.Fatalf("diagnostics[%d] = %#v, want %#v", i, got.diagnostics[i], want.diagnostics[i])
		}
	}
}
