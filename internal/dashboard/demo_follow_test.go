package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type demoRoundTripper func(*http.Request) (*http.Response, error)

func (f demoRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDemoFollowingFetchesRecentTracesBeforeBacklog(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-11 * 24 * time.Hour)
	_, store, _ := storedApp(t)
	source, err := newJaegerQuerySource("https://jaeger.example")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	key := "demo:live:" + originID(source.base.String())
	previous := DemoImportReport{From: old.Add(-demoOverlap).UnixMilli(), To: old.UnixMilli(), Pending: [][2]int64{{old.Add(-time.Minute).UnixMilli(), old.UnixMilli()}}}
	raw, _ := json.Marshal(previous)
	if err = store.ingest(ctx, nil, key, string(raw)); err != nil {
		t.Fatal(err)
	}
	var windows [][2]time.Time
	source.client.Transport = demoRoundTripper(func(r *http.Request) (*http.Response, error) {
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_min"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_max"))
		windows = append(windows, [2]time.Time{from, to})
		start := now.Add(-time.Minute)
		body := `{"resourceSpans":[]}`
		if !start.Before(from) && start.Before(to) {
			body = fmt.Sprintf(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"%032x","spanId":"%016x","name":"recent","startTimeUnixNano":"%d","endTimeUnixNano":"%d"}]}]}]}`, 1, 1, start.UnixNano(), start.Add(time.Millisecond).UnixNano())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	caughtUp, err := store.syncDemo(ctx, source, now)
	if err != nil || caughtUp {
		t.Fatalf("sync: %v %v", caughtUp, err)
	}
	if len(windows) != 2 || windows[0] != [2]time.Time{now.Add(-demoOverlap), now} || windows[1] != [2]time.Time{old.Add(-time.Minute), old} {
		t.Fatalf("recent traces did not take priority over saved backlog: %v", windows)
	}
	stamp, _ := store.Stamp(ctx)
	spans, err := store.Trace(ctx, fmt.Sprintf("%032x", 1), stamp)
	if err != nil || len(spans) != 1 || spans[0].Name != "recent" {
		t.Fatalf("recent trace not stored: %v %v", spans, err)
	}
	if _, err = store.syncDemo(ctx, source, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(windows) != 3 || !windows[2][1].Before(now.Add(-demoOverlap)) {
		t.Fatalf("recent import was not throttled while history advanced: %v", windows)
	}
	if _, err = store.syncDemo(ctx, source, now.Add(demoPollInterval)); err != nil {
		t.Fatal(err)
	}
	if len(windows) != 5 || windows[3][1] != now.Add(demoPollInterval) {
		t.Fatalf("live window was not refreshed during catch-up: %v", windows)
	}
	if !strings.Contains(store.Health().Notice, "Historical catch-up") || !strings.Contains(store.Health().Notice, now.Add(demoPollInterval).Format(time.RFC3339)) {
		t.Fatalf("notice did not distinguish live and historical progress: %+v", store.Health())
	}
}

func TestDemoRecentResumesPendingWindowUnlessStale(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Minute, 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			ctx := context.Background()
			_, store, _ := storedApp(t)
			source, err := newJaegerQuerySource("https://jaeger.example")
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			to := now.Add(-age)
			previous := DemoImportReport{From: to.Add(-demoOverlap).UnixMilli(), To: to.UnixMilli(), Pending: [][2]int64{{to.Add(-time.Minute).UnixMilli(), to.UnixMilli()}}}
			raw, _ := json.Marshal(previous)
			key := "demo:recent:" + originID(source.base.String())
			if err = store.ingest(ctx, nil, key, string(raw)); err != nil {
				t.Fatal(err)
			}
			var windows [][2]time.Time
			source.client.Transport = demoRoundTripper(func(r *http.Request) (*http.Response, error) {
				from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_min"))
				end, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_max"))
				windows = append(windows, [2]time.Time{from, end})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"resourceSpans":[]}`)), Header: make(http.Header)}, nil
			})
			if _, err = store.syncDemoRecent(ctx, source, now); err != nil {
				t.Fatal(err)
			}
			want := [2]time.Time{to.Add(-time.Minute), to}
			if age > demoOverlap {
				want = [2]time.Time{now.Add(-demoOverlap), now}
			}
			if len(windows) != 1 || windows[0] != want {
				t.Fatalf("recent retry window: got %v, want %v", windows, want)
			}
		})
	}
}

func TestDemoFollowingYieldsBlockedBackfillToLiveRefresh(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-11 * 24 * time.Hour)
	_, store, _ := storedApp(t)
	source, err := newJaegerQuerySource("https://jaeger.example")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	source.client.Timeout = 0 // Exercise the history deadline, not the HTTP timeout.
	key := "demo:live:" + originID(source.base.String())
	previous := DemoImportReport{From: old.Add(-demoOverlap).UnixMilli(), To: old.UnixMilli()}
	raw, _ := json.Marshal(previous)
	if err = store.ingest(ctx, nil, key, string(raw)); err != nil {
		t.Fatal(err)
	}
	var ends []time.Time
	source.client.Transport = demoRoundTripper(func(r *http.Request) (*http.Response, error) {
		end, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_max"))
		ends = append(ends, end)
		if len(ends) == 2 {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"resourceSpans":[]}`)), Header: make(http.Header)}, nil
	})
	// Bound the whole test in case a history pass stops yielding.
	bounded, cancel := context.WithTimeout(ctx, demoPollInterval+5*time.Second)
	defer cancel()
	if caughtUp, err := store.syncDemo(bounded, source, now); err != nil || caughtUp {
		t.Fatalf("backfill did not yield: %v %v", caughtUp, err)
	}
	saved, err := store.checkpoint(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	var pending DemoImportReport
	if err = json.Unmarshal([]byte(saved), &pending); err != nil || len(pending.Pending) != 1 {
		t.Fatalf("history queue was lost: %+v %v", pending, err)
	}
	if _, err = store.syncDemo(ctx, source, now.Add(demoPollInterval+time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(ends) != 4 || ends[2] != now.Add(demoPollInterval+time.Second) || ends[3] != old.Add(demoOverlap) {
		t.Fatalf("live refresh or history resume missing: %v", ends)
	}
}

func TestDemoFollowingOverlapRevisionsRestartAndCatchUp(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	phase := 0
	var windows [][2]time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_min"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("query.start_time_max"))
		windows = append(windows, [2]time.Time{from, to})
		spans := []map[string]any{}
		for i := 0; i <= phase; i++ {
			start := now.Add(-10*time.Minute + time.Duration(i)*time.Second)
			if start.Before(from) || !start.Before(to) {
				continue
			}
			spans = append(spans, map[string]any{"traceId": fmt.Sprintf("%032x", i+1), "spanId": fmt.Sprintf("%016x", i+1), "name": "demo", "startTimeUnixNano": fmt.Sprint(start.UnixNano()), "endTimeUnixNano": fmt.Sprint(start.Add(time.Duration(phase+1) * time.Millisecond).UnixNano())})
		}
		json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": spans}}}}})
	}))
	defer server.Close()
	source, err := newJaegerQuerySource(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(t.TempDir(), "demo.db")
	store, err := openDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { store.Close() }()
	sync := func(at time.Time, caughtUp bool) {
		t.Helper()
		got, err := store.syncDemo(ctx, source, at)
		if err != nil || got != caughtUp {
			t.Fatalf("sync: %v %v", got, err)
		}
	}
	sync(now, true)
	phase = 1 // A late span and an updated end time inside the overlap.
	sync(now.Add(30*time.Second), true)
	if windows[1][0] != now.Add(-demoOverlap) {
		t.Fatalf("no overlap: %v", windows)
	}
	stamp, _ := store.Stamp(ctx)
	spans, err := store.Trace(ctx, fmt.Sprintf("%032x", 1), stamp)
	if err != nil || len(spans) != 1 || spans[0].TimeEnd.Sub(spans[0].TimeStart) != 2*time.Millisecond {
		t.Fatalf("revision missing: %v %v", spans, err)
	}
	sync(now.Add(time.Minute), true)
	replayed, _ := store.Stamp(ctx)
	if replayed != stamp {
		t.Fatal("overlap duplicated unchanged spans")
	}
	var current, keys int
	store.db.QueryRow(`SELECT count(*) FROM dashboard_spans WHERE hi IS NULL`).Scan(&current)
	store.db.QueryRow(`SELECT count(*) FROM dashboard_checkpoints WHERE name LIKE 'demo:live:%'`).Scan(&keys)
	if current != 2 || keys != 1 {
		t.Fatalf("spans/checkpoints: %d/%d", current, keys)
	}
	store.Close()
	store, err = openDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sync(now.Add(time.Hour), false)
	last := windows[len(windows)-1]
	if last[0] != now.Add(time.Minute-demoOverlap) || last[1] != now.Add(time.Minute+demoOverlap) {
		t.Fatalf("restart skipped history: %v", last)
	}
	if !strings.Contains(store.Health().Notice, "Live Jaeger") {
		t.Fatal(store.Health())
	}
}

func TestDemoFollowingResumesFailedWindow(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	fail := true
	var starts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		starts = append(starts, r.URL.Query().Get("query.start_time_min"))
		if fail {
			cancel()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"resourceSpans":[]}`)
	}))
	defer server.Close()
	_, store, _ := storedApp(t)
	source, _ := newJaegerQuerySource(server.URL)
	defer source.Close()
	if _, err := store.syncDemo(ctx, source, now); err == nil {
		t.Fatal("expected cancellation")
	}
	fail = false
	caughtUp, err := store.syncDemo(context.Background(), source, now.Add(time.Minute))
	if err != nil || caughtUp || len(starts) != 2 || starts[0] != starts[1] {
		t.Fatalf("did not resume failed range: %v %v %v", caughtUp, err, starts)
	}
}

func TestDemoFollowerRunsWithoutBrowserAndStops(t *testing.T) {
	requested := make(chan struct{})
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	app, err := New(Config{StoreDB: filepath.Join(t.TempDir(), "demo.db"), DemoJaegerURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("no background import")
	}
	if err = app.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel request")
	}
	if _, err = New(Config{DemoJaegerURL: server.URL}); err == nil {
		t.Fatal("live import accepted without archive")
	}
	if _, err = New(Config{StoreDB: "unused", TraceDB: "native", DemoJaegerURL: server.URL}); err == nil {
		t.Fatal("mixed native and demo inputs")
	}
}
