package main

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	env := map[string]string{"PHOS_ADDR": "127.0.0.1:8000", "PHOS_STORE_DB": "archive.db", "PHOS_GCP_PROJECT": "my-project"}
	getenv := func(key string) string { return env[key] }
	addr, cfg, err := parseConfig([]string{"serve", "--addr", "127.0.0.1:9094", "--gcp-filter", "+service.name:checkout"}, getenv, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:9094" || cfg.StoreDB != "archive.db" || cfg.GCPProject != "my-project" || cfg.GCPFilter != "+service.name:checkout" {
		t.Fatalf("addr=%q cfg=%+v", addr, cfg)
	}
	for _, args := range [][]string{{"unknown"}, {"--unknown"}, {"--db="}, {"--addr="}} {
		if _, _, err := parseConfig(args, getenv, io.Discard); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
}

func TestHelpDoesNotStartService(t *testing.T) {
	var output strings.Builder
	if err := run(context.Background(), []string{"--help"}, func(string) string { return "" }, io.Discard, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "-trace-db") || !strings.Contains(output.String(), "127.0.0.1:9094") {
		t.Fatalf("missing help: %s", output.String())
	}
}

type startedOutput struct{ line chan string }

func (w startedOutput) Write(data []byte) (int, error) {
	w.line <- string(data)
	return len(data), nil
}

func TestRunServesAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := startedOutput{line: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--db", filepath.Join(t.TempDir(), "dashboard.db")},
			func(string) string { return "" }, output, io.Discard)
	}()
	var address string
	select {
	case line := <-output.line:
		address = strings.TrimSpace(strings.TrimPrefix(line, "Phos dashboard: "))
	case err := <-done:
		t.Fatalf("service exited during startup: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("service did not start")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: time.Second, Transport: transport}
	response, err := client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("dashboard status=%d", response.StatusCode)
	}
	stream, err := client.Get(strings.TrimSuffix(address, "/dashboard") + "/dashboard/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: status=%d type=%q", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service did not shut down with an active SSE client")
	}
}
