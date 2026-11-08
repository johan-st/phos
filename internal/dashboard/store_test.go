package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/johan-st/phos"
)

func storedApp(t *testing.T) (*App, *dashboardStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dashboard.db")
	app, err := New(Config{StoreDB: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app, app.search.source.(*dashboardStore), path
}
func storeBatch(t *testing.T, s *dashboardStore, items []phos.Snapshot) {
	t.Helper()
	for i := 0; i < len(items); i += 256 {
		if err := s.ingest(context.Background(), items[i:min(i+256, len(items))], "", ""); err != nil {
			t.Fatal(err)
		}
	}
}
func TestDashboardStoreBeyondLimitsRestartAndStablePages(t *testing.T) {
	app, store, path := storedApp(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	items := make([]phos.Snapshot, 10025)
	for i := range items {
		id := fmt.Sprintf("trace-%05d", i)
		items[i] = phos.Snapshot{ID: "span", TraceID: id, Name: "svc:request", TimeStart: base.Add(time.Duration(i) * time.Millisecond), TimeEnd: base.Add(time.Duration(i+1) * time.Millisecond)}
	}
	storeBatch(t, store, items)
	raw := fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(2*time.Minute).UnixMilli())
	f := searchDone(t, app, raw)
	if !f.Coverage.Complete || f.Aggregates.Spans != len(items) || f.Pages != 101 {
		t.Fatalf("large search: %+v %+v", f.Coverage, f.Aggregates)
	}
	w, _ := store.Stamp(context.Background())
	storeBatch(t, store, items[:2])
	again, _ := store.Stamp(context.Background())
	if again != w {
		t.Fatal("replay created a revision")
	}
	changed := items[0]
	changed.TimeEnd = changed.TimeStart.Add(time.Hour)
	storeBatch(t, store, []phos.Snapshot{changed})
	old, err := store.Trace(context.Background(), changed.TraceID, w)
	if err != nil || old[0].TimeEnd != items[0].TimeEnd {
		t.Fatalf("as-of detail: %v %v", old, err)
	}
	seen := map[string]bool{}
	for {
		for _, row := range f.Summaries {
			if seen[row.TraceID] {
				t.Fatal("duplicate page row")
			}
			seen[row.TraceID] = true
		}
		if f.Cursor == "" {
			break
		}
		f = searchDone(t, app, raw+"&cursor="+url.QueryEscape(f.Cursor))
		if f.Aggregates.Spans != len(items) {
			t.Fatal("page changed population")
		}
	}
	if len(seen) != len(items) {
		t.Fatalf("only %d rows", len(seen))
	}
	app.Close()
	reopened, err := New(Config{StoreDB: path})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f = searchDone(t, reopened, raw+"&sort=slowest")
	if !f.Coverage.Complete || f.Aggregates.Spans != len(items) || f.Summaries[0].TraceID != changed.TraceID {
		t.Fatalf("restart: %+v", f.Coverage)
	}
}
func TestDashboardBucketsMatchRawQueriesAndRevisions(t *testing.T) {
	app, store, _ := storedApp(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	rng := rand.New(rand.NewSource(42))
	items := []phos.Snapshot{}
	for i := 0; i < 240; i++ {
		start := base.Add(time.Duration(rng.Intn(360000)) * time.Millisecond)
		p := phos.Snapshot{ID: fmt.Sprint(i), TraceID: fmt.Sprint(i / 3), Name: []string{"svc:work", "worker:load", "K:read"}[i%3], TimeStart: start, TimeEnd: start.Add(time.Duration(rng.Intn(2000000)) * time.Microsecond), Failed: i%7 == 0, Attrs: []slog.Attr{slog.String("all", "yes")}}
		if i%13 == 0 {
			p.TimeEnd = time.Time{}
		}
		items = append(items, p)
	}
	storeBatch(t, store, items)
	reference, writer := searchFixture(t, items)
	for iteration := 0; iteration < 2; iteration++ {
		for _, filter := range []string{"", "&service=svc", "&service=k", "&operation=load", "&attr=all=yes", "&errors=with", "&min_dur=100", "&exact_trace=2"} {
			for _, bounds := range [][2]int64{{0, 360000}, {123, 299999}, {60500, 61200}} {
				raw := fmt.Sprintf("from=%d&to=%d%s&refresh=%d", base.UnixMilli()+bounds[0], base.UnixMilli()+bounds[1], filter, iteration)
				actual, expected := searchDone(t, app, raw), searchDone(t, reference, raw)
				if !actual.Coverage.Complete || !reflect.DeepEqual(actual.Aggregates, expected.Aggregates) {
					t.Fatalf("query %s\nactual %+v\nexpected %+v\ncoverage %+v", raw, actual.Aggregates, expected.Aggregates, actual.Coverage)
				}
			}
		}
		// Same batch contains repeated revisions and a move between bucket/dimension.
		p := items[0]
		p.TimeStart = base.Add(2 * time.Minute)
		p.TimeEnd = p.TimeStart.Add(time.Millisecond)
		p.Name = "worker:load"
		next := p
		next.TimeStart = base.Add(time.Minute)
		next.TimeEnd = next.TimeStart.Add(time.Second)
		next.Failed = true
		storeBatch(t, store, []phos.Snapshot{p, next, next})
		writer.Append(p)
		writer.Append(next)
	}
}
func TestDashboardProjectionAvoidsWholeBucketPayloadScan(t *testing.T) {
	app, store, _ := storedApp(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	storeBatch(t, store, []phos.Snapshot{{ID: "a", TraceID: "a", Name: "svc:a", TimeStart: base.Add(time.Second), TimeEnd: base.Add(2 * time.Second)}})
	// An interior payload is deliberately unreadable: overview must use stored
	// summaries + buckets. A partial edge must actually attempt to decode it.
	if _, err := store.db.Exec(`UPDATE dashboard_spans SET raw='invalid'`); err != nil {
		t.Fatal(err)
	}
	raw := fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(2*time.Minute).UnixMilli())
	f := searchDone(t, app, raw)
	if !f.Coverage.Complete || f.Aggregates.Spans != 1 {
		t.Fatalf("full bucket scanned raw: %+v", f.Coverage)
	}
	if stats := f.Aggregates.Buckets[0].Stats; stats == nil || stats.Samples != 1 || stats.Services.Values[0].Value != "svc" {
		t.Fatalf("column statistics did not use materialised buckets: %+v", stats)
	}
	f = searchDone(t, app, fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(10*time.Second).UnixMilli()))
	if f.Coverage.Complete {
		t.Fatal("edge failed to scan raw data")
	}
}
func TestDashboardStoreAtomicCheckpointAndOldBuckets(t *testing.T) {
	_, store, _ := storedApp(t)
	ctx := context.Background()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	p := phos.Snapshot{ID: "a", TraceID: "a", Name: "svc:a", TimeStart: base, TimeEnd: base.Add(time.Millisecond)}
	if err := store.ingest(ctx, []phos.Snapshot{p, {ID: ""}}, "native", "bad"); err == nil {
		t.Fatal("invalid batch accepted")
	}
	w, _ := store.Stamp(ctx)
	cp, _ := store.checkpoint(ctx, "native")
	if w.MaxSeq != 0 || cp != "" {
		t.Fatal("failed transaction published checkpoint/data")
	}
	storeBatch(t, store, []phos.Snapshot{p})
	w, _ = store.Stamp(ctx)
	p.TimeEnd = base.Add(time.Hour)
	storeBatch(t, store, []phos.Snapshot{p})
	q, _ := parseSearchQuery(fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(2*time.Minute).UnixMilli()), time.Now())
	r, e := newStoredResults(ctx, store.resultsDir)
	if e != nil {
		t.Fatal(e)
	}
	defer r.close()
	agg := searchAggregates{}
	agg.Buckets, agg.BucketWidth = overviewBuckets(q.From, q.To)
	hist := newHistogram()
	if e = store.projectedOverview(ctx, q, w, r, &agg, hist); e != nil {
		t.Fatal(e)
	}
	if hist.ValueAtQuantile(95) != 1000 || agg.Spans != 1 {
		t.Fatalf("old projection changed: %d %+v", hist.ValueAtQuantile(95), agg)
	}
}
func TestDashboardNativeResumeAndSourceIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.db")
	writer, err := newNativeTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	p := phos.Snapshot{ID: "a", TraceID: "a", TimeStart: time.Now(), TimeEnd: time.Now()}
	writer.Append(p)
	if _, err = openDashboardStore(path); err == nil {
		t.Fatal("accepted agent-owned database")
	}
	_, store, _ := storedApp(t)
	source, err := newSQLiteQuerySource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		caught, e := store.syncNative(ctx, source)
		if e != nil || !caught {
			t.Fatalf("sync: %v %v", caught, e)
		}
	}
	w, _ := store.Stamp(ctx)
	if w.MaxSeq != 1 {
		t.Fatal("resume duplicated data")
	}
	writer.Clear()
	p.ID = "b"
	p.TraceID = "b"
	writer.Append(p)
	if _, err = store.syncNative(ctx, source); err != nil {
		t.Fatal(err)
	}
	now, _ := store.Stamp(ctx)
	old, err := store.Trace(ctx, "a", now)
	if err != nil || len(old) != 1 {
		t.Fatal("source reset removed stored history")
	}
	current, err := store.Trace(ctx, "b", now)
	if err != nil || len(current) != 1 {
		t.Fatal("source reset did not catch up")
	}
}
func TestDashboardDemoImportOfflineAndResume(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_min"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_max"))
		spans := []map[string]any{}
		for i := 0; i < 401; i++ {
			start := base.Add(time.Duration(i) * time.Millisecond)
			if start.Before(from) || !start.Before(to) {
				continue
			}
			spans = append(spans, map[string]any{"traceId": fmt.Sprintf("%032x", i+1), "spanId": fmt.Sprintf("%016x", i+1), "name": "demo", "startTimeUnixNano": fmt.Sprint(start.UnixNano()), "endTimeUnixNano": fmt.Sprint(start.Add(time.Millisecond).UnixNano())})
			if len(spans) == 200 {
				break
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": spans}}}}})
	}))
	path := filepath.Join(t.TempDir(), "demo.db")
	cfg := DemoImportConfig{Database: path, URL: server.URL, From: base, To: base.Add(time.Second)}
	report, err := ImportDemo(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if requests <= 1 || !report.BestEffort || len(report.Pending) != 0 {
		t.Fatalf("import: %+v", report)
	}
	n := requests
	server.Close()
	if _, err = ImportDemo(context.Background(), cfg); err != nil || requests != n {
		t.Fatalf("completed import attempted upstream: %v", err)
	}
	app, err := New(Config{StoreDB: path})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	f := searchDone(t, app, fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(time.Second).UnixMilli()))
	if !f.Coverage.Complete || f.Aggregates.Spans != 401 || !strings.Contains(f.Health.Notice, "Demo") {
		t.Fatalf("offline: %+v %+v", f.Coverage, f.Aggregates)
	}
}

func TestDashboardNativeFollowerWithoutBrowser(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.db")
	writer, err := newNativeTestStore(input)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	base := time.Now().Add(-time.Minute)
	p := phos.Snapshot{ID: "a", TraceID: "a", Name: "svc:a", TimeStart: base, TimeEnd: base.Add(time.Millisecond)}
	writer.Append(p)
	app, err := New(Config{TraceDB: input, StoreDB: filepath.Join(dir, "dashboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	store := app.search.source.(*dashboardStore)
	wait := func(seq int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			w, e := store.Stamp(context.Background())
			if e == nil && w.MaxSeq >= seq {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("native follower did not commit", store.Health())
	}
	wait(1) // No search or browser interaction has occurred.
	raw := fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(time.Minute).UnixMilli())
	first := searchDone(t, app, raw)
	p.ID = "b"
	p.TraceID = "b"
	writer.Append(p)
	wait(2)
	next := searchDone(t, app, raw)
	if first.Aggregates.Spans != 1 || next.Aggregates.Spans != 2 {
		t.Fatal("fixed query did not refresh after ingestion")
	}
}
func TestDashboardHistogramEncodingMatchesHDR(t *testing.T) {
	rng := rand.New(rand.NewSource(18))
	hist := newHistogram()
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10000; i++ {
		d := int64(rng.Intn(3600000000)) * 1000
		p := phos.Snapshot{TimeStart: base, TimeEnd: base.Add(time.Duration(d) * time.Microsecond)}
		if !hist.ValuesAreEquivalent(min(max(d, 1), 3600000000000), histogramValue(p)) {
			t.Fatalf("histogram encoding differs at %d", d)
		}
	}
}
