package dashboard

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/johan-st/phos"
	"github.com/johan-st/phos/internal/tracejsonl"
)

func searchFixture(t *testing.T, items []phos.Snapshot) (*App, *nativeTestStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "traces.db")
	writer, e := newNativeTestStore(path)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range items {
		writer.Append(p)
	}
	app, e := New(Config{TraceDB: path})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = app.Close(); _ = writer.Close() })
	return app, writer
}
func searchDone(t *testing.T, app *App, raw string) searchFrame {
	t.Helper()
	j, offset, e := app.search.job(raw)
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		f := app.search.frame(j, offset)
		if !f.Coverage.Running {
			return f
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("query did not finish")
	return searchFrame{}
}
func TestHistoricalSearchMatchingAndContext(t *testing.T) {
	base := time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC)
	items := []phos.Snapshot{
		{ID: "root", TraceID: "a", Name: "frontend:request", TimeStart: base.Add(-time.Hour), TimeEnd: base.Add(time.Second), Attrs: []slog.Attr{slog.Int("http.status_code", 200)}},
		{ID: "child", TraceID: "a", ParentID: "root", Name: "cart:load", TimeStart: base, TimeEnd: base.Add(time.Millisecond), Failed: true, Attrs: []slog.Attr{slog.String("tenant", "a")}},
		{ID: "other", TraceID: "b", Name: "cart:load", TimeStart: base, TimeEnd: base.Add(2 * time.Millisecond), Attrs: []slog.Attr{slog.String("tenant", "b")}},
		{ID: "at-end", TraceID: "end", Name: "cart:load", TimeStart: base.Add(48 * time.Hour), TimeEnd: base.Add(48*time.Hour + time.Second)},
	}
	app, _ := searchFixture(t, items)
	raw := fmt.Sprintf("from=%d&to=%d&service=cart&operation=load&attr=tenant=a", base.UnixMilli(), base.Add(48*time.Hour).UnixMilli())
	f := searchDone(t, app, raw)
	if !f.Coverage.Complete || f.Aggregates.Traces != 1 || f.Aggregates.Spans != 1 || f.Aggregates.Errors != 1 {
		t.Fatalf("bad coverage/aggregates: %+v", f)
	}
	if f.Summaries[0].RootStatus != "HTTP 200" || f.Summaries[0].Spans != 2 || f.Summaries[0].Errors != 1 {
		t.Fatalf("context/outcome: %+v", f.Summaries)
	}
	if got := searchDone(t, app, strings.Replace(raw, "service=cart", "service=frontend", 1)); got.Aggregates.Traces != 0 {
		t.Fatal("predicates matched different spans")
	}
	if got := searchDone(t, app, raw+"&errors=without"); got.Aggregates.Traces != 0 {
		t.Fatal("child errors incorrectly called successful")
	}
	w, e := app.search.source.Stamp(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	full, e := app.search.source.Trace(context.Background(), "a", w)
	if e != nil || len(full) != 2 {
		t.Fatalf("full context: %d %v", len(full), e)
	}
}
func TestSearchPaginationWatermarkAndDedup(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	items := []phos.Snapshot{}
	for i := 0; i < 205; i++ {
		id := fmt.Sprintf("trace-%03d", i)
		items = append(items, phos.Snapshot{ID: id, TraceID: id, Name: "svc:request", TimeStart: base, TimeEnd: base.Add(time.Duration(i) * time.Millisecond)})
	}
	app, writer := searchFixture(t, items)
	writer.Append(items[0])
	raw := fmt.Sprintf("from=%d&to=%d&sort=slowest", base.Add(-time.Hour).UnixMilli(), base.Add(time.Hour).UnixMilli())
	first := searchDone(t, app, raw)
	if len(first.Summaries) != 100 || first.Aggregates.Traces != 205 || first.Aggregates.Spans != 205 || first.Summaries[0].TraceID != "trace-204" {
		t.Fatalf("first page: %+v", first)
	}
	second := searchDone(t, app, raw+"&cursor="+url.QueryEscape(first.Cursor))
	if second.Aggregates.Traces != 205 || second.Summaries[0].TraceID != "trace-104" {
		t.Fatal("page changed aggregates or order")
	}
	writer.Append(phos.Snapshot{ID: "later", TraceID: "later", TimeStart: base, TimeEnd: base.Add(time.Hour)})
	if got := searchDone(t, app, raw+"&cursor="+url.QueryEscape(first.Cursor)); got.Aggregates.Traces != 205 {
		t.Fatal("watermark admitted new records")
	}
	writer.Clear()
	response := app.handleSearch(requestMessage{ID: "reset", Query: raw + "&cursor=" + url.QueryEscape(first.Cursor)})
	if response.Status != 409 {
		t.Fatalf("clear failed to invalidate cursor: %d", response.Status)
	}
}
func TestTraceChunksAndSourceReplacement(t *testing.T) {
	base := time.Now()
	items := []phos.Snapshot{}
	for i := 0; i < 400; i++ {
		items = append(items, phos.Snapshot{ID: fmt.Sprint(i), TraceID: "big", Name: "svc:child", TimeStart: base, TimeEnd: base.Add(time.Second), Attrs: []slog.Attr{slog.String("payload", strings.Repeat("x", 1500))}})
	}
	app, _ := searchFixture(t, items)
	cursor := ""
	revision := ""
	count := 0
	for attempts := 0; attempts < 20; attempts++ {
		response := app.handleTrace(requestMessage{ID: "trace", Query: "trace_id=big&cursor=" + url.QueryEscape(cursor)})
		if response.Status != 200 {
			t.Fatalf("status %d", response.Status)
		}
		body, e := base64.StdEncoding.DecodeString(response.BodyBase64)
		if e != nil {
			t.Fatal(e)
		}
		if response.Headers["content-encoding"] != "" {
			t.Fatal("chunk unexpectedly compressed")
		}
		var chunk traceChunk
		if e = json.Unmarshal(body, &chunk); e != nil {
			t.Fatal(e)
		}
		if len(response.BodyBase64) > 1048576 {
			t.Fatal("wire limit exceeded")
		}
		if revision != "" && revision != chunk.Revision {
			t.Fatal("revision changed")
		}
		revision = chunk.Revision
		count += len(chunk.Spans)
		cursor = chunk.Cursor
		if cursor == "" {
			if !chunk.Complete {
				t.Fatal(chunk.Reason)
			}
			break
		}
	}
	if count != 400 {
		t.Fatalf("lost chunks: %d", count)
	}
}
func TestJaegerV3NormalizationAndLimits(t *testing.T) {
	var params url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params = r.URL.Query()
		if r.URL.Path == "/api/v3/services" {
			io.WriteString(w, `{"services":["frontend","checkout"]}`)
			return
		}
		io.WriteString(w, `{"result":{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"frontend"}}]},"scopeSpans":[{"spans":[{"traceId":"abcdef","spanId":"1234","name":"request","kind":"SPAN_KIND_SERVER","startTimeUnixNano":"1700000000000000001","endTimeUnixNano":"1700000000001000001","attributes":[{"key":"http.status_code","value":{"intValue":"200"}},{"key":"big","value":{"intValue":"9223372036854775807"}}],"status":{"code":"STATUS_CODE_OK"},"links":[{"traceId":"cdef","spanId":"5678"}]},{"traceId":"abcdef","spanId":"5678","parentSpanId":"1234","name":"child","kind":3,"startTimeUnixNano":"1700000000000000002","endTimeUnixNano":"1700000000001000002","status":{"code":2,"message":"failure"},"events":[{"name":"exception","timeUnixNano":"1700000000000000003","attributes":[{"key":"exception.message","value":{"stringValue":"failed"}}]}]}]}]}]}}`)
	}))
	defer server.Close()
	s, e := newJaegerQuerySource(server.URL + "/api/traces?operation=GET%20%2Fdispatch&limit=40")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	q := searchQuery{From: 1699999999000, To: 1700000001000, Service: "frontend", Attributes: map[string]string{}}
	var spans []phos.Snapshot
	limited, e := s.Visit(context.Background(), q, sourceStamp{}, func(p []phos.Snapshot) error { spans = p; return nil })
	if e != nil || !limited || len(spans) != 2 {
		t.Fatalf("read: %v %v %d", limited, e, len(spans))
	}
	if params.Get("operation") != "" || params.Get("query.operation_name") != "" {
		t.Fatal("inherited hidden operation filter")
	}
	root := spans[0]
	if root.Kind != phos.Server || root.Failed || len(root.Links) != 1 || root.TimeStart.Nanosecond() != 1 {
		t.Fatalf("normalization: %+v", root)
	}
	attrs := map[string]string{}
	flattenAttrs(root.Attrs, "", attrs)
	if attrs["big"] != "9223372036854775807" {
		t.Fatal("integer precision lost")
	}
	if !spans[1].Failed || len(spans[1].Events) != 1 || len(spans[1].Errors) != 1 {
		t.Fatal("failure details lost")
	}
	server.Close()
	if _, e = s.Trace(context.Background(), "abcdef", sourceStamp{}); e == nil || s.Health().Error == "" {
		t.Fatal("source failure hidden")
	}
}
func TestSearchReplayAndCancellation(t *testing.T) {
	base := time.Now().Add(-time.Second)
	app, _ := searchFixture(t, []phos.Snapshot{{ID: "x", TraceID: "x", Name: "svc:x", TimeStart: base, TimeEnd: base.Add(time.Millisecond)}})
	q, _ := parseSearchQuery("", time.Now())
	first := searchDone(t, app, "")
	first = app.search.replay(first, q, "")
	next := first
	next.Summaries = append([]traceSummary{}, first.Summaries...)
	next.Summaries[0].Errors = 1
	next.Query = "ignored"
	next.Seq = 999
	delta := app.search.replay(next, q, fmt.Sprintf("epoch=%s&query_token=%s&after=%d", first.Epoch, first.Query, first.Seq))
	if delta.Kind != "delta" || len(delta.Summaries) != 1 || delta.Base != first.Seq {
		t.Fatalf("delta: %+v", delta)
	}
	for i := 0; i < 3; i++ {
		pending := first
		pending.Epoch = ""
		pending.Summaries = nil
		pending.Order = nil
		pending.Aggregates = searchAggregates{}
		pending.Coverage.Running = true
		got := app.search.replay(pending, q, "")
		if len(got.Summaries) != 1 || got.Aggregates.Spans != 1 {
			t.Fatal("pending refresh discarded completed results")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := app.search.source.Visit(ctx, q, sourceStamp{}, func([]phos.Snapshot) error { return nil })
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation: %v", e)
	}
}

func TestRollingBucketGridReplacesPreviousWindow(t *testing.T) {
	frame := searchFrame{Coverage: searchCoverage{From: 1000000, To: 1900000}}
	for i := 0; i < 60; i++ {
		frame.Aggregates.Buckets = append(frame.Aggregates.Buckets, timeBucket{From: 1000000 + int64(i)*15000, To: 1000000 + int64(i+1)*15000, Spans: 1})
	}
	for tick := 0; tick < 180; tick++ {
		next := frame
		next.Coverage.From += 5000
		next.Coverage.To += 5000
		next.Aggregates.Buckets = append([]timeBucket{}, frame.Aggregates.Buckets...)
		for i := range next.Aggregates.Buckets {
			next.Aggregates.Buckets[i].From += 5000
			next.Aggregates.Buckets[i].To += 5000
		}
		delta := summaryDiff(frame, next)
		if delta.BucketDelta || len(delta.Aggregates.Buckets) != 60 {
			t.Fatalf("rolling partition must replace, not accumulate, at tick %d: delta=%v count=%d", tick, delta.BucketDelta, len(delta.Aggregates.Buckets))
		}
		frame = next
	}
	unchanged := summaryDiff(frame, frame)
	if !unchanged.BucketDelta || len(unchanged.Aggregates.Buckets) != 0 {
		t.Fatal("unchanged partition should omit unchanged buckets")
	}
}

func TestSelectedTraceOnlyStreamsChangedSpans(t *testing.T) {
	base := time.Now()
	items := []phos.Snapshot{{ID: "a", TraceID: "t", Name: "svc:a", TimeStart: base, TimeEnd: base.Add(time.Second)}, {ID: "b", TraceID: "t", ParentID: "a", Name: "svc:b", TimeStart: base, TimeEnd: base.Add(time.Millisecond)}}
	first, hashes := selectedTraceChange(items, nil, "")
	if len(first.Upserts) != 0 || first.Base != "" {
		t.Fatal("initial checkpoint repeated full trace")
	}
	same, _ := selectedTraceChange(items, hashes, first.Revision)
	if same.Revision != first.Revision || len(same.Upserts) != 0 {
		t.Fatal("unchanged spans streamed")
	}
	items[1].Failed = true
	change, hashes := selectedTraceChange(items, hashes, first.Revision)
	if change.Revision == first.Revision || len(change.Upserts) != 1 || change.Upserts[0].ID != "b" {
		t.Fatal("delta did not isolate changed span")
	}
	removed, _ := selectedTraceChange(items[:1], hashes, change.Revision)
	if len(removed.Removed) != 1 || removed.Removed[0] != "b" {
		t.Fatal("removed span not identified")
	}
	app, _ := searchFixture(t, items)
	response := app.handleTrace(requestMessage{ID: "trace", Query: "trace_id=t"})
	b, _ := base64.StdEncoding.DecodeString(response.BodyBase64)
	var detail traceChunk
	if json.Unmarshal(b, &detail) != nil || detail.Revision != change.Revision {
		t.Fatal("HTTP and SSE detail revisions differ")
	}
}

func TestHistoricalRevisionsStatusAndReplacement(t *testing.T) {
	base := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	app, writer := searchFixture(t, []phos.Snapshot{
		{ID: "orphan", TraceID: "trace", ParentID: "missing", Name: "svc:early", TimeStart: base, TimeEnd: base.Add(time.Millisecond)},
		{ID: "root", TraceID: "trace", Name: "svc:root", TimeStart: base.Add(time.Second), TimeEnd: base.Add(2 * time.Second), Attrs: []slog.Attr{slog.Int("http.status_code", 200), slog.String("otel.status_code", "OK")}},
		{ID: "solo", Name: "svc:solo", TimeStart: base, TimeEnd: base.Add(time.Millisecond)},
	})
	raw := fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(time.Hour).UnixMilli())
	f := searchDone(t, app, raw)
	if f.Aggregates.Spans != 3 || len(f.Aggregates.Buckets) != 60 {
		t.Fatalf("initial aggregate: %+v", f.Aggregates)
	}
	for _, p := range f.Summaries {
		if p.TraceID == "trace" && (p.RootStatus != "HTTP 200" || !p.Incomplete) {
			t.Fatalf("root semantics: %+v", p)
		}
	}
	writer.Append(phos.Snapshot{ID: "root", TraceID: "trace", Name: "svc:root", TimeStart: base.Add(time.Second), TimeEnd: base.Add(2 * time.Second), Failed: true})
	newer := searchDone(t, app, raw+"&refresh=new")
	if newer.Aggregates.Spans != 3 || newer.Aggregates.Errors != 1 {
		t.Fatal("late revision double counted or refresh ignored")
	}
	w, _ := app.search.source.Stamp(context.Background())
	path := app.search.source.(*sqliteQuerySource).path
	writer.Close()
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	replacement, err := newNativeTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	replacement.Append(phos.Snapshot{ID: "new", TraceID: "new", TimeStart: base, TimeEnd: base.Add(time.Millisecond)})
	got, err := app.search.source.Stamp(context.Background())
	if err != nil || got.Epoch == w.Epoch {
		t.Fatal("replacement did not invalidate epoch", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = app.search.source.Trace(ctx, "new", got)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("detail query ignored cancellation", err)
	}
}
func TestSQLiteQuery100000Spans(t *testing.T) {
	if testing.Short() {
		t.Skip("large historical fixture")
	}
	path := filepath.Join(t.TempDir(), "history.db")
	writer, e := newNativeTestStore(path)
	if e != nil {
		t.Fatal(e)
	}
	writer.Close()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	stmt, e := tx.Prepare(`INSERT INTO spans_v2(id,trace_id,start_time_unix_nano,snapshot_json) VALUES(?,?,?,?)`)
	if e != nil {
		t.Fatal(e)
	}
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 100000; i++ {
		id := fmt.Sprint(i)
		trace := fmt.Sprint(i / 100)
		p := phos.Snapshot{ID: id, TraceID: trace, Name: "svc:operation", TimeStart: base.Add(time.Duration(i) * time.Millisecond), TimeEnd: base.Add(time.Duration(i+1) * time.Millisecond)}
		b, _ := tracejsonl.Marshal(p)
		if _, e = stmt.Exec(id, trace, p.TimeStart.UnixNano(), string(b)); e != nil {
			t.Fatal(e)
		}
	}
	stmt.Close()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	source, e := newSQLiteQuerySource(path)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w, e := source.Stamp(ctx)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	_, e = source.Visit(ctx, searchQuery{From: base.UnixMilli(), To: base.Add(time.Hour).UnixMilli()}, w, func(spans []phos.Snapshot) error { count += len(spans); return nil })
	if e != nil || count != 100000 {
		t.Fatalf("large read: %d %v", count, e)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > before.HeapAlloc+64*1024*1024 {
		t.Fatal("dashboard retained historical span payloads")
	}
	rows, e := db.Query(`EXPLAIN QUERY PLAN SELECT DISTINCT trace_id FROM spans_v2 INDEXED BY idx_spans_v2_start_time WHERE start_time_unix_nano>=? AND start_time_unix_nano<? AND seq<=?`, base.UnixNano(), base.Add(time.Hour).UnixNano(), w.MaxSeq)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	used := false
	for rows.Next() {
		var a, b, c int
		var detail string
		rows.Scan(&a, &b, &c, &detail)
		used = used || strings.Contains(detail, "idx_spans_v2_start_time")
	}
	if !used {
		t.Fatal("timestamp index not used")
	}
}
