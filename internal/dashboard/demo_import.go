package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/johan-st/phos"
)

// DemoImportConfig is used by the explicit import command, never by dashboard
// search or hover. Original timestamps are retained for historical exploration.
type DemoImportConfig struct {
	Database, URL, Service string
	From, To               time.Time
}
type DemoImportReport struct {
	From         int64      `json:"from_ms"`
	To           int64      `json:"to_ms"`
	Requests     int        `json:"requests"`
	Observations int        `json:"span_observations"`
	Unresolved   int        `json:"unresolved_intervals"`
	BestEffort   bool       `json:"best_effort"`
	Pending      [][2]int64 `json:"pending_intervals"`
}

func ImportDemo(ctx context.Context, cfg DemoImportConfig) (DemoImportReport, error) {
	report := DemoImportReport{From: cfg.From.UnixMilli(), To: cfg.To.UnixMilli(), BestEffort: true}
	if cfg.Database == "" || report.From < 0 || report.To <= report.From || report.To > 9223372036854 {
		return report, errors.New("database and valid absolute from/to range required")
	}
	source, err := newJaegerQuerySource(cfg.URL)
	if err != nil {
		return report, err
	}
	defer source.Close()
	store, err := openDashboardStore(cfg.Database)
	if err != nil {
		return report, err
	}
	defer store.Close()
	if err = store.bindOrigin(ctx, "demo:"+originID(source.base.String()), "Demo dataset imported from Jaeger; upstream coverage is best-effort"); err != nil {
		return report, err
	}
	key := fmt.Sprintf("demo:%s:%d:%d", originID(source.base.String()+"\n"+cfg.Service), report.From, report.To)
	return store.importDemo(ctx, source, cfg, key)
}

// Both one-shot and live imports share adaptive splitting and transactional replay.
func (store *dashboardStore) importDemo(ctx context.Context, source *jaegerQuerySource, cfg DemoImportConfig, key string) (DemoImportReport, error) {
	report := DemoImportReport{From: cfg.From.UnixMilli(), To: cfg.To.UnixMilli(), BestEffort: true}
	saved, err := store.checkpoint(ctx, key)
	if err != nil {
		return report, err
	}
	if saved != "" {
		var previous DemoImportReport
		if err = json.Unmarshal([]byte(saved), &previous); err != nil {
			return report, err
		}
		if previous.From == report.From && previous.To == report.To {
			report = previous
		} else {
			saved = ""
		}
	}
	if saved == "" {
		report.Pending = [][2]int64{{report.From, report.To}}
		raw, _ := json.Marshal(report)
		if err = store.ingest(ctx, nil, key, string(raw)); err != nil {
			return report, err
		}
	}
	// A retry resumes the saved queue; a completed import is idempotent. Request
	// budgets apply per invocation so a large interrupted import can be resumed.
	for requests := 0; len(report.Pending) > 0 && requests < 4096; requests++ {
		window := report.Pending[0]
		params := url.Values{"query.start_time_min": {time.UnixMilli(window[0]).UTC().Format(time.RFC3339Nano)}, "query.start_time_max": {time.UnixMilli(window[1]).UTC().Format(time.RFC3339Nano)}, "query.search_depth": {"200"}}
		if cfg.Service != "" {
			params.Set("query.service_name", cfg.Service)
		}
		var spans []phos.Snapshot
		for attempt := 0; attempt < 3; attempt++ {
			spans, err = source.read(ctx, "/api/v3/traces", params)
			report.Requests++
			if err == nil || errors.Is(err, errSourceLimit) || ctx.Err() != nil {
				break
			}
			timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return report, ctx.Err()
			case <-timer.C:
			}
		}
		if err != nil && !errors.Is(err, errSourceLimit) {
			return report, err
		}
		saturated := errors.Is(err, errSourceLimit) || len(feedGroups(spans)) >= 200
		remaining := append([][2]int64{}, report.Pending[1:]...)
		if saturated && window[1]-window[0] > 1 {
			mid := window[0] + (window[1]-window[0])/2
			report.Pending = append([][2]int64{{window[0], mid}, {mid, window[1]}}, remaining...)
			// Only import complete decoded responses. Retrying an uncommitted split is safe.
			raw, _ := json.Marshal(report)
			if err = store.ingest(ctx, nil, key, string(raw)); err != nil {
				return report, err
			}
			continue
		}
		if err != nil {
			return report, fmt.Errorf("demo window %v could not be decoded completely: %w", window, err)
		}
		if saturated {
			report.Unresolved++
		}
		// Bound each storage transaction. Only advance the window checkpoint after
		// every batch committed; interruption replays earlier batches without duplicates.
		for start := 0; start < len(spans); start += 256 {
			if err = store.ingest(ctx, spans[start:min(start+256, len(spans))], "", ""); err != nil {
				return report, err
			}
		}
		report.Observations += len(spans)
		report.Pending = remaining
		raw, _ := json.Marshal(report)
		if err = store.ingest(ctx, nil, key, string(raw)); err != nil {
			return report, err
		}
	}
	if len(report.Pending) > 0 {
		return report, errors.New("demo import request budget reached; rerun the same command to resume")
	}
	notice := fmt.Sprintf("Demo dataset; Jaeger coverage best-effort, %d saturated intervals. Imported range %s – %s", report.Unresolved, cfg.From.UTC().Format(time.RFC3339), cfg.To.UTC().Format(time.RFC3339))
	err = store.bindOrigin(ctx, "demo:"+originID(source.base.String()), notice)
	return report, err
}
