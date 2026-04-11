package phos

import (
	"context"
	"testing"
)

func FuzzParseTraceParent(f *testing.F) {
	seeds := []string{
		validVersion00TraceParent,
		validUnsampledTraceParent,
		validFutureTraceParent,
		"",
		"00-00000000000000000000000000000000-0000000000000000-00",
		"zz-not-a-traceparent",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		got, err := ParseTraceParent(input)
		if err != nil {
			return
		}
		if !isLowerHex(got.Version, 2) || got.Version == "ff" {
			t.Fatalf("accepted invalid version %#v", got)
		}
		if !isLowerHex(got.TraceID, 32) || isAllZeroHex(got.TraceID) {
			t.Fatalf("accepted invalid trace id %#v", got)
		}
		if !isLowerHex(got.Parent, 16) || isAllZeroHex(got.Parent) {
			t.Fatalf("accepted invalid parent id %#v", got)
		}
		if !isLowerHex(got.Flags, 2) {
			t.Fatalf("accepted invalid flags %#v", got)
		}

		reparsed, reparsedErr := ParseTraceParent(got.String())
		if reparsedErr != nil {
			t.Fatalf("ParseTraceParent(String()) error = %v", reparsedErr)
		}
		if reparsed != got {
			t.Fatalf("reparsed = %#v, want %#v", reparsed, got)
		}
	})
}

func FuzzInjectExtractRoundTrip(f *testing.F) {
	f.Add(validVersion00TraceParent, "rojo=1,congo=2")
	f.Add(validUnsampledTraceParent, "")

	f.Fuzz(func(t *testing.T, traceParentValue, traceState string) {
		if _, err := ParseTraceParent(traceParentValue); err != nil {
			return
		}

		ctx := ExtractTraceContext(context.Background(), MapCarrier{
			TraceParentHeader: traceParentValue,
			TraceStateHeader:  traceState,
		})
		ctx, started := Start(ctx, "child")
		sp := started.(*span)

		carrier := MapCarrier{}
		InjectTraceContext(ctx, carrier)
		got, err := ParseTraceParent(carrier[TraceParentHeader])
		if err != nil {
			t.Fatalf("ParseTraceParent() error = %v", err)
		}
		if got.TraceID != sp.traceID {
			t.Fatalf("TraceID = %q, want %q", got.TraceID, sp.traceID)
		}
		if got.Parent != sp.id {
			t.Fatalf("Parent = %q, want %q", got.Parent, sp.id)
		}
		if got.Flags != outgoingTraceFlagsForValue(sp.traceFlags) {
			t.Fatalf("Flags = %q, want %q", got.Flags, outgoingTraceFlagsForValue(sp.traceFlags))
		}
		if traceState != "" && carrier[TraceStateHeader] != traceState {
			t.Fatalf("TraceState = %q, want %q", carrier[TraceStateHeader], traceState)
		}
	})
}

func TestGeneratedIDsHaveExpectedShape(t *testing.T) {
	for range 100 {
		ctx, started := Start(context.Background(), "root")
		sp := started.(*span)

		if !isLowerHex(sp.id, 16) || isAllZeroHex(sp.id) {
			t.Fatalf("span id = %q, want 16 lowercase hex chars and not all zero", sp.id)
		}
		if !isLowerHex(sp.traceID, 32) || isAllZeroHex(sp.traceID) {
			t.Fatalf("trace id = %q, want 32 lowercase hex chars and not all zero", sp.traceID)
		}

		carrier := MapCarrier{}
		InjectTraceContext(ctx, carrier)
		if _, err := ParseTraceParent(carrier[TraceParentHeader]); err != nil {
			t.Fatalf("ParseTraceParent() error = %v", err)
		}
	}
}
