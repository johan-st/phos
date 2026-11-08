package dashboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/johan-st/phos"
	"golang.org/x/oauth2"
)

type gcpTransport func(*http.Request) (*http.Response, error)

func (f gcpTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gcpResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func gcpFixture(start time.Time, name string) cloudTrace {
	return cloudTrace{Project: "test-project", ID: "abcdef0123456789abcdef0123456789", Spans: []cloudTraceSpan{
		{ID: "18446744073709551615", Name: name, Kind: "RPC_SERVER", Start: start.UTC().Format(time.RFC3339Nano), End: start.Add(time.Millisecond).UTC().Format(time.RFC3339Nano), Labels: map[string]string{"service.name": "checkout", "/http/status_code": "200", "custom": "retained"}},
		{ID: "2", Parent: "18446744073709551615", Name: "database", Kind: "RPC_CLIENT", Start: start.Add(time.Microsecond).UTC().Format(time.RFC3339Nano), End: start.Add(2 * time.Millisecond).UTC().Format(time.RFC3339Nano), Labels: map[string]string{"error": "false", "/error/message": "query failed"}},
	}}
}

func gcpBody(t *testing.T, traces []cloudTrace, next string) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Traces []cloudTrace `json:"traces"`
		Next   string       `json:"nextPageToken,omitempty"`
	}{traces, next})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestGCPDecodingPreservesIdentityTimingLabelsAndErrorValues(t *testing.T) {
	start := time.Date(2026, 10, 8, 12, 0, 0, 123456789, time.UTC)
	trace := gcpFixture(start, "GET /checkout")
	trace.ID = strings.ToUpper(trace.ID)
	spans, err := decodeCloudTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	root, child := spans[0], spans[1]
	if root.TraceID != strings.ToLower(trace.ID) || root.ID != "ffffffffffffffff" || root.ParentID != "" || child.ID != "0000000000000002" || child.ParentID != root.ID {
		t.Fatalf("identity: %+v", spans)
	}
	if root.TimeStart != start || root.TimeEnd != start.Add(time.Millisecond) || root.Kind != phos.Server || child.Kind != phos.Client {
		t.Fatalf("timing/kind: %+v", spans)
	}
	if root.Name != "checkout:GET /checkout" || root.Failed || !child.Failed || len(child.Errors) != 1 || child.Errors[0].Err.Error() != "query failed" {
		t.Fatalf("names/errors: %+v", spans)
	}
	attrs := map[string]string{}
	flattenAttrs(root.Attrs, "", attrs)
	if attrs["custom"] != "retained" || attrs["/http/status_code"] != "200" || attrs["http.status_code"] != "200" || attrs["gcp.project_id"] != trace.Project {
		t.Fatalf("attributes: %v", attrs)
	}
	for _, value := range []string{"false", "", "true", "TRUE"} {
		trace.Spans = []cloudTraceSpan{trace.Spans[0]}
		trace.Spans[0].Labels = map[string]string{"error": value}
		items, err := decodeCloudTrace(trace)
		if err != nil || items[0].Failed != strings.EqualFold(value, "true") {
			t.Fatalf("error=%q: %v %v", value, items, err)
		}
	}
}

func TestGCPRejectsMalformedRecords(t *testing.T) {
	for _, mutate := range []func(*cloudTrace){
		func(p *cloudTrace) { p.ID = "00000000000000000000000000000000" },
		func(p *cloudTrace) { p.ID = "xyz" },
		func(p *cloudTrace) { p.Spans[0].ID = "0" },
		func(p *cloudTrace) { p.Spans[0].ID = "18446744073709551616" },
		func(p *cloudTrace) { p.Spans[0].Parent = "invalid" },
		func(p *cloudTrace) { p.Spans[0].Start = "invalid" },
		func(p *cloudTrace) { p.Spans[0].End = "2024-01-01T00:00:00Z" },
	} {
		trace := gcpFixture(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC), "request")
		mutate(&trace)
		if _, err := decodeCloudTrace(trace); err == nil {
			t.Fatalf("accepted malformed record: %+v", trace)
		}
	}
}

func TestGCPPageUsesAuthenticatedCompleteViewAndInclusiveEnd(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Minute)
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	client.Transport.(*oauth2.Transport).Base = gcpTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.Method != "GET" || r.URL.Host != "cloudtrace.googleapis.com" || r.URL.Path != "/v1/projects/test-project/traces" {
			t.Fatalf("request: %s", r.URL)
		}
		if q.Get("view") != "COMPLETE" || q.Get("pageToken") != "next /+" || q.Get("filter") != "+service.name:checkout" || q.Get("startTime") != from.Format(time.RFC3339Nano) || q.Get("endTime") != to.Add(-time.Nanosecond).Format(time.RFC3339Nano) {
			t.Fatalf("query: %v", q)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("X-Goog-User-Project") != "quota-project" {
			t.Fatal("authentication/quota headers missing")
		}
		// A short/empty page with a token must not be interpreted as complete.
		return gcpResponse(200, `{"traces":[],"nextPageToken":"another"}`), nil
	})
	source := &cloudTraceSource{project: "test-project", filter: "+service.name:checkout", quotaProject: "quota-project", client: client}
	defer source.Close()
	spans, next, err := source.page(ctx, from, to, "next /+")
	if err != nil || len(spans) != 0 || next != "another" {
		t.Fatalf("page: %v %q %v", spans, next, err)
	}
}

func TestGCPImportResumesPagesAfterRestartAndDeduplicatesRevisions(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 10, 8, 12, 0, 0, 123456789, time.UTC)
	path := filepath.Join(t.TempDir(), "gcp.db")
	store, err := openDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source := &cloudTraceSource{project: "test-project", client: &http.Client{}}
	defer source.Close()
	var tokens []string
	broken := true
	source.client.Transport = gcpTransport(func(r *http.Request) (*http.Response, error) {
		token := r.URL.Query().Get("pageToken")
		tokens = append(tokens, token)
		if token == "" {
			return gcpResponse(200, gcpBody(t, []cloudTrace{gcpFixture(start, "initial")}, "page-two")), nil
		}
		if broken {
			return gcpResponse(403, `{}`), nil
		}
		return gcpResponse(200, gcpBody(t, []cloudTrace{gcpFixture(start, "changed")}, "")), nil
	})
	report := GCPImportReport{From: start.Add(-time.Minute).UnixMilli(), To: start.Add(time.Minute).UnixMilli()}
	partial, err := store.importGCP(ctx, source, report)
	if err == nil || partial.Complete || partial.Pages != 1 || partial.NextPage != "page-two" {
		t.Fatalf("partial: %+v %v", partial, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broken = false
	finished, err := store.importGCP(ctx, source, report)
	if err != nil || !finished.Complete || finished.Pages != 2 || !reflect.DeepEqual(tokens, []string{"", "page-two", "page-two"}) {
		t.Fatalf("resume: %+v %v %v", finished, err, tokens)
	}
	stamp, _ := store.Stamp(ctx)
	spans, err := store.Trace(ctx, "abcdef0123456789abcdef0123456789", stamp)
	if err != nil || len(spans) != 2 || spans[1].Name != "checkout:changed" {
		t.Fatalf("retained: %+v %v", spans, err)
	}
	_, err = store.importGCP(ctx, source, report)
	if err != nil || len(tokens) != 3 {
		t.Fatalf("completed import fetched again: %v %v", tokens, err)
	}
	// A new overlapping window refreshes observations, but identical labels do
	// not create new revisions even when map iteration order differs.
	source.client.Transport = gcpTransport(func(r *http.Request) (*http.Response, error) {
		return gcpResponse(200, gcpBody(t, []cloudTrace{gcpFixture(start, "changed")}, "")), nil
	})
	_, err = store.importGCP(ctx, source, GCPImportReport{From: report.From, To: report.To + 1000})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.Stamp(ctx)
	if after != stamp {
		t.Fatalf("unchanged poll created revisions: %v %v", stamp, after)
	}
	if !strings.Contains(store.Health().Notice, "Google Cloud Trace") {
		t.Fatal("GCP provenance missing")
	}
}

func TestGCPPageFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	ctx := context.Background()
	for _, body := range []string{`{"traces":[`, `{"traces":[{"traceId":"bad"}]}`, `{"nextPageToken":"same"}`, `{"traces":[{"projectId":"another","traceId":"abcdef0123456789abcdef0123456789"}]}`} {
		_, store, _ := storedApp(t)
		calls := 0
		source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(r *http.Request) (*http.Response, error) { calls++; return gcpResponse(200, body), nil })}}
		report := GCPImportReport{From: 1000, To: 2000, NextPage: "same", Pages: 2, Observations: 10}
		_, err := store.importGCPPage(ctx, source, report, "test")
		if err == nil || calls != 1 {
			t.Fatalf("invalid page accepted/retried: %s %v %d", body, err, calls)
		}
		saved, err := store.gcpCheckpoint(ctx, "test")
		if err != nil || *saved != report {
			t.Fatalf("checkpoint advanced: %+v %v", saved, err)
		}
		stamp, _ := store.Stamp(ctx)
		if stamp.MaxSeq != 0 {
			t.Fatal("bad page committed data")
		}
	}
}

func TestGCPImportRefusesDifferentArchiveOrigin(t *testing.T) {
	ctx := context.Background()
	_, store, _ := storedApp(t)
	if err := store.bindOrigin(ctx, "demo:existing", "demo"); err != nil {
		t.Fatal(err)
	}
	source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("request before origin validation")
		return nil, nil
	})}}
	if _, err := store.importGCP(ctx, source, GCPImportReport{From: 1000, To: 2000}); err == nil {
		t.Fatal("GCP mixed into demo archive")
	}
}

func TestGCPLivePrioritizesRecentPagesAndResumesBacklog(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	_, store, _ := storedApp(t)
	source := &cloudTraceSource{project: "test-project", client: &http.Client{}}
	key := "gcp:live:" + source.identity()
	previous := GCPImportReport{From: old.Add(-gcpOverlap).UnixMilli(), To: old.UnixMilli(), NextPage: "history-page", Pages: 4}
	raw, _ := json.Marshal(previous)
	if err := store.ingest(ctx, nil, key, string(raw)); err != nil {
		t.Fatal(err)
	}
	var windows []string
	source.client.Transport = gcpTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		windows = append(windows, q.Get("startTime")+"|"+q.Get("pageToken"))
		if q.Get("pageToken") == "history-page" {
			return gcpResponse(200, gcpBody(t, nil, "more-history")), nil
		}
		if q.Get("pageToken") == "more-history" {
			return gcpResponse(200, gcpBody(t, nil, "")), nil
		}
		return gcpResponse(200, gcpBody(t, []cloudTrace{gcpFixture(now.Add(-time.Minute), "live")}, "")), nil
	})
	if done, err := store.syncGCP(ctx, source, now); done || err != nil {
		t.Fatalf("sync: %v %v", done, err)
	}
	expected := []string{now.Add(-gcpOverlap).Format(time.RFC3339) + "|", old.Add(-gcpOverlap).Format(time.RFC3339) + "|history-page"}
	if !reflect.DeepEqual(windows, expected) {
		t.Fatalf("live priority/pending page: %v", windows)
	}
	if _, err := store.syncGCP(ctx, source, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(windows) != 3 || !strings.HasSuffix(windows[2], "|more-history") {
		t.Fatalf("recent import not throttled/history not resumed: %v", windows)
	}
	if _, err := store.syncGCP(ctx, source, now.Add(gcpPollInterval)); err != nil {
		t.Fatal(err)
	}
	if len(windows) != 5 || windows[3] != now.Add(gcpPollInterval-gcpOverlap).Format(time.RFC3339)+"|" {
		t.Fatalf("recent polling stopped during backlog: %v", windows)
	}
	stamp, _ := store.Stamp(ctx)
	items, err := store.Trace(ctx, "abcdef0123456789abcdef0123456789", stamp)
	if err != nil || len(items) != 2 {
		t.Fatalf("live data missing: %+v %v", items, err)
	}
}

func TestGCPLiveRecentPendingResumesAndStaleWindowRefreshes(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Minute, 24 * time.Hour} {
		ctx := context.Background()
		_, store, _ := storedApp(t)
		source := &cloudTraceSource{project: "test-project", client: &http.Client{}}
		key := "gcp:recent:" + source.identity()
		previous := GCPImportReport{From: now.Add(-age - gcpOverlap).UnixMilli(), To: now.Add(-age).UnixMilli(), NextPage: "pending"}
		raw, _ := json.Marshal(previous)
		if err := store.ingest(ctx, nil, key, string(raw)); err != nil {
			t.Fatal(err)
		}
		source.client.Transport = gcpTransport(func(r *http.Request) (*http.Response, error) {
			want := "pending"
			if age > gcpOverlap {
				want = ""
			}
			if r.URL.Query().Get("pageToken") != want {
				t.Fatalf("age %s: wrong pending token", age)
			}
			return gcpResponse(200, `{}`), nil
		})
		report, err := store.syncGCPRecent(ctx, source, now)
		wantTo := previous.To
		if age > gcpOverlap {
			wantTo = now.UnixMilli()
		}
		if err != nil || report.To != wantTo {
			t.Fatalf("age %s: %+v %v", age, report, err)
		}
	}
}

func TestGCPRetriesTransientHTTPAndCancels(t *testing.T) {
	ctx := context.Background()
	calls := 0
	source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return gcpResponse(503, `{}`), nil
		}
		return gcpResponse(200, `{}`), nil
	})}}
	if _, _, err := source.page(ctx, time.Unix(1, 0), time.Unix(2, 0), ""); err != nil || calls != 2 {
		t.Fatalf("retry: %d %v", calls, err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	source.client.Transport = gcpTransport(func(*http.Request) (*http.Response, error) { cancel(); return gcpResponse(429, `{}`), nil })
	if _, _, err := source.page(cancelCtx, time.Unix(1, 0), time.Unix(2, 0), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestGCPConfigurationRejectsMixedSourcesBeforeAuthentication(t *testing.T) {
	for _, cfg := range []Config{{GCPProject: "test-project"}, {GCPProject: "test-project", StoreDB: "unused", TraceDB: "native"}, {GCPProject: "test-project", StoreDB: "unused", DemoJaegerURL: "https://demo"}, {GCPProject: "test-project", StoreDB: "unused", JaegerURL: "https://demo"}, {GCPFilter: "filter"}, {GCPProject: "../project", StoreDB: "unused"}} {
		if app, err := New(cfg); err == nil {
			app.Close()
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}

func TestGCPSourceUsesADCAndQuotaProjectOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"fixture","client_secret":"fixture","refresh_token":"fixture","quota_project_id":"adc-quota"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")
	source, err := newCloudTraceSource(context.Background(), "test-project", "filter")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.quotaProject != "adc-quota" || source.project != "test-project" || source.filter != "filter" || source.client.Timeout != 0 {
		t.Fatalf("ADC configuration: %+v", source)
	}
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "override-quota")
	overridden, err := newCloudTraceSource(context.Background(), "test-project", "")
	if err != nil {
		t.Fatal(err)
	}
	defer overridden.Close()
	if overridden.quotaProject != "override-quota" {
		t.Fatal("quota project override ignored")
	}
}

type gcpContextBody struct {
	ctx    context.Context
	closed bool
}

func (b *gcpContextBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *gcpContextBody) Close() error { b.closed = true; return nil }

func TestGCPRequestCancellationUsesContexts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"fixture","client_secret":"fixture","refresh_token":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
	source, err := newCloudTraceSource(context.Background(), "test-project", "")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	transport := source.client.Transport.(*oauth2.Transport)
	transport.Source = oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"})

	for _, tc := range []struct {
		name    string
		body    bool
		timeout time.Duration
		cancel  bool
	}{
		{name: "headers", timeout: 10 * time.Second},
		{name: "body", body: true, timeout: 10 * time.Second},
		{name: "caller deadline", timeout: time.Second},
		{name: "caller cancel", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousOutput := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previousOutput)
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if tc.timeout > 0 && tc.timeout < 10*time.Second {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, tc.timeout)
					defer stop()
				}
				start := time.Now()
				var authenticated bool
				var deadline time.Time
				var body *gcpContextBody
				transport.Base = gcpTransport(func(r *http.Request) (*http.Response, error) {
					authenticated = r.Header.Get("Authorization") == "Bearer test-token"
					deadline, _ = r.Context().Deadline()
					if tc.cancel {
						cancel()
					}
					if tc.body {
						body = &gcpContextBody{ctx: r.Context()}
						resp := gcpResponse(http.StatusOK, "")
						resp.Body = body
						return resp, nil
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				})
				_, _, err := source.readPage(ctx, time.Unix(1, 0), time.Unix(2, 0), "")
				synctest.Wait()
				wantErr := context.DeadlineExceeded
				if tc.cancel {
					wantErr = context.Canceled
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("cancellation: got %v, want %v", err, wantErr)
				}
				if !authenticated {
					t.Error("request lost its OAuth2 authorization header")
				}
				limit := tc.timeout
				if tc.cancel {
					limit = 10 * time.Second
				}
				if !deadline.Equal(start.Add(limit)) || time.Since(start) != tc.timeout {
					t.Errorf("deadline/elapsed: %v / %v; want %v / %v", deadline, time.Since(start), start.Add(limit), tc.timeout)
				}
				if body != nil && !body.closed {
					t.Error("timed-out response body was not closed")
				}
				if source.client.Timeout != 0 || strings.Contains(logs.String(), "Transport.CancelRequest") {
					t.Errorf("legacy cancellation timeout=%v, logs=%q", source.client.Timeout, logs.String())
				}
			})
		})
	}
}

func TestGCPArchiveServesSearchAndDetailOffline(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	app, store, _ := storedApp(t)
	source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(*http.Request) (*http.Response, error) {
		return gcpResponse(200, gcpBody(t, []cloudTrace{gcpFixture(start, "request")}, "")), nil
	})}}
	if _, err := store.importGCP(ctx, source, GCPImportReport{From: start.Add(-time.Minute).UnixMilli(), To: start.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	source.Close()
	source.client.Transport = gcpTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("dashboard fetched upstream data")
		return nil, nil
	})
	frame := searchDone(t, app, fmt.Sprintf("from=%d&to=%d&service=checkout&errors=with", start.Add(-time.Minute).UnixMilli(), start.Add(time.Minute).UnixMilli()))
	if !frame.Coverage.Complete || len(frame.Summaries) != 1 || frame.Aggregates.Spans != 1 || frame.Summaries[0].Errors != 1 || frame.Summaries[0].RootStatus != "HTTP 200" {
		t.Fatalf("GCP search: %+v", frame)
	}
	response := app.handleRequest(requestMessage{ID: "detail", Method: http.MethodGet, Path: "/dashboard/trace", Query: "trace_id=abcdef0123456789abcdef0123456789"})
	raw, err := base64.StdEncoding.DecodeString(response.BodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	var detail traceChunk
	if err = json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusOK || len(detail.Spans) != 2 {
		t.Fatalf("offline detail: %+v", response)
	}
}

func TestGCPPageReplaysCommittedPrefixAfterStorageFailure(t *testing.T) {
	ctx := context.Background()
	_, store, _ := storedApp(t)
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	trace := gcpFixture(start, "request")
	template := trace.Spans[0]
	trace.Spans = nil
	for i := 1; i <= 300; i++ {
		span := template
		span.ID = fmt.Sprint(i)
		trace.Spans = append(trace.Spans, span)
	}
	// Force failure in the second batch after the first 256 spans committed.
	if _, err := store.db.Exec(`CREATE TRIGGER fail_gcp BEFORE INSERT ON dashboard_spans WHEN NEW.id='000000000000012c' BEGIN SELECT RAISE(FAIL,'fixture'); END`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return gcpResponse(200, gcpBody(t, []cloudTrace{trace}, "")), nil
	})}}
	report := GCPImportReport{From: start.Add(-time.Minute).UnixMilli(), To: start.Add(time.Minute).UnixMilli()}
	if _, err := store.importGCP(ctx, source, report); err == nil {
		t.Fatal("storage failure ignored")
	}
	key := fmt.Sprintf("gcp:import:%s:%d:%d", source.identity(), report.From, report.To)
	saved, err := store.gcpCheckpoint(ctx, key)
	if err != nil || *saved != report {
		t.Fatalf("page advanced after failed batch: %+v %v", saved, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM dashboard_spans`).Scan(&count); err != nil || count != 256 {
		t.Fatalf("committed prefix: %d %v", count, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_gcp`); err != nil {
		t.Fatal(err)
	}
	finished, err := store.importGCP(ctx, source, report)
	if err != nil || !finished.Complete || finished.Pages != 1 || finished.Observations != 300 || calls != 2 {
		t.Fatalf("replay: %+v %d %v", finished, calls, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM dashboard_spans`).Scan(&count); err != nil || count != 300 {
		t.Fatalf("replay duplicated revisions: %d %v", count, err)
	}
}

func TestGCPExpiredPageTokenRestartsFixedWindow(t *testing.T) {
	ctx := context.Background()
	_, store, _ := storedApp(t)
	start := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	report := GCPImportReport{From: start.Add(-time.Minute).UnixMilli(), To: start.Add(time.Minute).UnixMilli(), Pages: 5, Observations: 20, NextPage: "expired"}
	var tokens []string
	source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(r *http.Request) (*http.Response, error) {
		token := r.URL.Query().Get("pageToken")
		tokens = append(tokens, token)
		if token != "" {
			return gcpResponse(400, `{}`), nil
		}
		if r.URL.Query().Get("startTime") != time.UnixMilli(report.From).UTC().Format(time.RFC3339Nano) {
			t.Fatal("expired token changed window")
		}
		return gcpResponse(200, gcpBody(t, []cloudTrace{gcpFixture(start, "request")}, "")), nil
	})}}
	finished, err := store.importGCPPage(ctx, source, report, "resume")
	if err != nil || !finished.Complete || finished.Pages != 1 || finished.Observations != 2 || !reflect.DeepEqual(tokens, []string{"expired", ""}) {
		t.Fatalf("token recovery: %+v %v %v", finished, tokens, err)
	}
}

func TestGCPLiveMultiPageWindowCompletesWithoutContinuousResweep(t *testing.T) {
	ctx := context.Background()
	_, store, _ := storedApp(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var windows []string
	source := &cloudTraceSource{project: "test-project", client: &http.Client{Transport: gcpTransport(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		windows = append(windows, q.Get("endTime"))
		next := "page-two"
		if q.Get("pageToken") != "" {
			next = ""
		}
		return gcpResponse(200, gcpBody(t, nil, next)), nil
	})}}
	if complete, err := store.syncGCP(ctx, source, now); err != nil || complete {
		t.Fatalf("first page: %t %v", complete, err)
	}
	if complete, err := store.syncGCP(ctx, source, now.Add(time.Second)); err != nil || !complete {
		t.Fatalf("finished recent window did not yield to poll interval: %t %v", complete, err)
	}
	if len(windows) != 2 || windows[0] != windows[1] {
		t.Fatalf("pagination changed fixed window: %v", windows)
	}
	if complete, err := store.syncGCP(ctx, source, now.Add(gcpPollInterval)); err != nil || complete {
		t.Fatalf("next window first page: %t %v", complete, err)
	}
	if len(windows) != 3 || windows[2] == windows[1] {
		t.Fatalf("next poll did not refresh current window: %v", windows)
	}
}
