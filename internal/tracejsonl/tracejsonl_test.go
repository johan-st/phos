package tracejsonl

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/johan-st/phos"
)

func TestSnapshotRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 11, 12, 13, time.UTC)
	want := phos.Snapshot{
		ID: "child", ParentID: "root", TraceID: "trace", Name: "request",
		TimeStart: now, TimeEnd: now.Add(time.Second), Kind: phos.Server, Failed: true,
		Attrs: []slog.Attr{
			slog.String("string", "hello"), slog.Int64("int", -5), slog.Uint64("uint", 1<<63),
			slog.Float64("float", 1.25), slog.Bool("bool", true), slog.Duration("duration", 2*time.Second),
			slog.Time("time", now), slog.Group("group", slog.String("nested", "value"), slog.Int("count", 3)),
		},
		Links:  []phos.SnapshotLink{{TraceID: "linked-trace", SpanID: "linked-span", Attrs: []slog.Attr{slog.Bool("follow", true)}}},
		Events: []phos.SnapshotEvent{{Time: now.Add(time.Millisecond), Name: "event", Attrs: []slog.Attr{slog.Duration("delay", time.Millisecond)}}},
		Errors: []phos.SnapshotError{{Err: errors.New("failed"), Attrs: []slog.Attr{slog.Int("attempt", 2)}}},
	}
	data, err := Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"schema_version":2`)) {
		t.Fatalf("missing schema version: %s", data)
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\ngot: %#v\nwant: %#v", got, want)
	}
}

func TestUnsupportedAnyBecomesString(t *testing.T) {
	data, err := Marshal(phos.Snapshot{Attrs: []slog.Attr{slog.Any("item", struct{ Name string }{"value"})}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attrs[0].Value.Kind() != slog.KindString || got.Attrs[0].Value.String() != "{value}" {
		t.Fatalf("attr = %#v", got.Attrs[0])
	}
}

func TestUnknownSchemaVersion(t *testing.T) {
	for _, data := range [][]byte{[]byte(`{"schema_version":3}`), []byte(`{"id":"legacy"}`)} {
		if _, err := Unmarshal(data); err == nil {
			t.Fatalf("Unmarshal(%s) accepted unknown schema", data)
		}
	}
}
