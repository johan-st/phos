package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johan-st/phos"
)

func httpFixture(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	app, err := New(Config{StoreDB: filepath.Join(t.TempDir(), "dashboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	store := app.search.source.(*dashboardStore)
	now := time.Now().Add(-time.Second)
	if err := store.ingest(context.Background(), []phos.Snapshot{
		{ID: "root", TraceID: "test-trace", Name: "checkout:request", TimeStart: now, TimeEnd: now.Add(10 * time.Millisecond), Attrs: []slog.Attr{slog.Int("http.status_code", 200)}},
		{ID: "child", TraceID: "test-trace", ParentID: "root", Name: "db:query", TimeStart: now.Add(time.Millisecond), TimeEnd: now.Add(2 * time.Millisecond), Failed: true, Events: []phos.SnapshotEvent{{Name: "query.started", Time: now}}},
	}, "fixture", "done"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app)
	t.Cleanup(func() { _ = app.Close(); server.Close() })
	return app, server
}

func TestHTTPDashboardRoutes(t *testing.T) {
	_, server := httpFixture(t)
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, test := range []struct {
		path                  string
		status                int
		contentType, contains string
	}{
		{"/?trace_id=test-trace", 307, "text/html", "/dashboard?trace_id=test-trace"},
		{"/healthz", 200, "text/plain", "ok"},
		{"/dashboard", 200, "text/html", "dashboard-bootstrap"},
		{"/dashboard/assets/dashboard.js", 200, "text/javascript", ""},
		{"/dashboard/assets/dashboard.css", 200, "text/css", "app-dashboard"},
		{"/dashboard/metadata", 200, "application/json", `"schema_version":3`},
		{"/dashboard/trace?trace_id=test-trace", 200, "application/json", `"root_status":"HTTP 200"`},
		{"/dashboard/trace?trace_id=missing", 404, "application/json", "trace not available"},
		{"/dashboard/trace", 400, "application/json", "trace_id required"},
		{"/dashboard?from=invalid", 400, "application/json", "invalid range start"},
		{"/dashboard/search?window=1", 400, "application/json", "invalid live window"},
		{"/dashboard/assets/../app.go", 404, "application/json", "not found"},
		{"/missing", 404, "application/json", "not found"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response, err := client.Get(server.URL + test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.status || !strings.HasPrefix(response.Header.Get("Content-Type"), test.contentType) || !strings.Contains(string(body), test.contains) {
				t.Fatalf("status=%d type=%s body=%.200s", response.StatusCode, response.Header.Get("Content-Type"), body)
			}
		})
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/dashboard", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("POST status=%d", response.StatusCode)
	}
}

func TestHTTPArchiveNoticeAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard.db")
	store, err := openDashboardStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.bindOrigin(context.Background(), "native:fixture", "Native spans; waiting for ingestion"); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	app, err := New(Config{StoreDB: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	response := httptest.NewRecorder()
	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard/metadata", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Dashboard archive; no live input configured") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "saved source notice: Native spans; waiting for ingestion") {
		t.Fatalf("missing saved source notice: %s", response.Body.String())
	}
}

func TestHTTPStreamingAndDisconnect(t *testing.T) {
	app, server := httpFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/dashboard/stream?connection_id=client", nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected SSE response: %+v", response)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	event, sawConnection, sawData := "", false, false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			sawConnection = sawConnection || event == "connection"
		}
		if event == "search-batch" && strings.HasPrefix(line, "data: ") {
			var frame searchFrame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Coverage.Complete && len(frame.Summaries) == 1 {
				if frame.Summaries[0].TraceID != "test-trace" || frame.Summaries[0].Errors != 1 || frame.Summaries[0].RootStatus != "HTTP 200" {
					t.Fatalf("unexpected summary: %+v", frame.Summaries)
				}
				sawData = true
				break
			}
		}
	}
	if !sawConnection || !sawData {
		t.Fatalf("connection=%t data=%t err=%v", sawConnection, sawData, scanner.Err())
	}
	cancel()
	response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		app.streamsMu.Lock()
		remaining := len(app.streams) + len(app.subscriptions)
		app.streamsMu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("disconnected HTTP client retained a stream")
}

func TestHTTPExplicitStreamCloseAndShutdown(t *testing.T) {
	app, server := httpFixture(t)
	for _, shutdown := range []bool{false, true} {
		response, err := server.Client().Get(server.URL + "/dashboard/stream?connection_id=close-test")
		if err != nil {
			t.Fatal(err)
		}
		if shutdown {
			app.StopStreams()
		} else {
			closed, err := server.Client().Get(server.URL + "/dashboard/live/close?connection_id=close-test")
			if err != nil {
				t.Fatal(err)
			}
			closed.Body.Close()
			if closed.StatusCode != http.StatusNoContent {
				t.Fatalf("close status=%d", closed.StatusCode)
			}
		}
		finished := make(chan error, 1)
		go func() { _, err := io.Copy(io.Discard, response.Body); response.Body.Close(); finished <- err }()
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("stream did not close")
		}
	}
	response, err := server.Client().Get(server.URL + "/dashboard/stream")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stream accepted after shutdown: %d", response.StatusCode)
	}
}
