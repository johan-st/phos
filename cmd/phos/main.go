package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johan-st/phos/internal/dashboard"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) (runErr error) {
	addr, cfg, err := parseConfig(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg.Logger = slog.New(slog.NewTextHandler(stderr, nil))
	app, err := dashboard.New(cfg)
	if err != nil {
		return fmt.Errorf("start dashboard: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, app.Close()) }()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	_, _ = fmt.Fprintf(stdout, "Phos dashboard: http://%s/dashboard\n", listener.Addr())
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		app.StopStreams()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown dashboard: %w", err)
		}
		return nil
	}
}

func parseConfig(args []string, getenv func(string) string, stderr io.Writer) (string, dashboard.Config, error) {
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("phos [serve]", flag.ContinueOnError)
	fs.SetOutput(stderr)
	defaultValue := func(key, fallback string) string {
		if value := getenv(key); value != "" {
			return value
		}
		return fallback
	}
	addr := fs.String("addr", defaultValue("PHOS_ADDR", "127.0.0.1:9094"), "HTTP listen address (PHOS_ADDR)")
	db := fs.String("db", defaultValue("PHOS_STORE_DB", ".phos/dashboard.db"), "persistent dashboard SQLite archive (PHOS_STORE_DB)")
	traceDB := fs.String("trace-db", getenv("PHOS_TRACE_DB"), "follow a native spans_v2 SQLite database read-only (PHOS_TRACE_DB)")
	jaeger := fs.String("jaeger-url", getenv("PHOS_JAEGER_URL"), "continuously import a Jaeger API into the archive (PHOS_JAEGER_URL)")
	project := fs.String("gcp-project", getenv("PHOS_GCP_PROJECT"), "continuously import a Google Cloud Trace project using ADC (PHOS_GCP_PROJECT)")
	filter := fs.String("gcp-filter", getenv("PHOS_GCP_FILTER"), "optional Cloud Trace filter (PHOS_GCP_FILTER)")
	if err := fs.Parse(args); err != nil {
		return "", dashboard.Config{}, err
	}
	if fs.NArg() != 0 {
		return "", dashboard.Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	if *addr == "" || *db == "" {
		return "", dashboard.Config{}, errors.New("--addr and --db must not be empty")
	}
	cfg := dashboard.Config{StoreDB: *db, TraceDB: *traceDB, DemoJaegerURL: *jaeger, GCPProject: *project, GCPFilter: *filter}
	return *addr, cfg, nil
}
