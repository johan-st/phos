package phos

import "testing"

func TestGeneratedTraceAndSpanIDsAlwaysWellFormed(t *testing.T) {
	for range 200 {
		tid := generateTraceID()
		sid := generateSpanID()
		if len(tid) != 32 || !isLowerHex(tid, 32) || isAllZeroHex(tid) {
			t.Fatalf("trace id %q: want 32 non-zero lowercase hex chars", tid)
		}
		if len(sid) != 16 || !isLowerHex(sid, 16) || isAllZeroHex(sid) {
			t.Fatalf("span id %q: want 16 non-zero lowercase hex chars", sid)
		}
	}
}
