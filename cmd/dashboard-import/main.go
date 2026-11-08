package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/johan-st/phos/internal/dashboard"
)

func main() {
	database := flag.String("db", "", "dashboard-owned SQLite database")
	endpoint := flag.String("jaeger", "", "Jaeger demo API URL (default: public demo when no GCP project)")
	project := flag.String("gcp-project", "", "Google Cloud Trace project (uses Application Default Credentials)")
	filter := flag.String("gcp-filter", "", "optional Cloud Trace filter expression")
	service := flag.String("service", "", "optional service filter")
	from := flag.String("from", "", "inclusive start (RFC3339)")
	to := flag.String("to", "", "exclusive end (RFC3339)")
	flag.Parse()
	if (*project != "" && (*endpoint != "" || *service != "")) || (*project == "" && *filter != "") {
		fmt.Fprintln(os.Stderr, "--gcp-project/--gcp-filter cannot be combined with --jaeger/--service; --gcp-filter requires --gcp-project")
		os.Exit(1)
	}
	start, err := time.Parse(time.RFC3339Nano, *from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --from:", err)
		os.Exit(1)
	}
	end, err := time.Parse(time.RFC3339Nano, *to)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid --to:", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var report any
	if *project != "" {
		report, err = dashboard.ImportGCP(ctx, dashboard.GCPImportConfig{Database: *database, Project: *project, Filter: *filter, From: start, To: end})
	} else {
		if *endpoint == "" {
			*endpoint = "https://jaeger.demo.jaegertracing.io"
		}
		report, err = dashboard.ImportDemo(ctx, dashboard.DemoImportConfig{Database: *database, URL: *endpoint, Service: *service, From: start, To: end})
	}
	json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Dashboard range: /dashboard?from=%d&to=%d\n", start.UnixMilli(), end.UnixMilli())
}
