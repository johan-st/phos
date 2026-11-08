package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type GCPImportConfig struct {
	Database, Project, Filter string
	From, To                  time.Time
}

type GCPImportReport struct {
	From         int64  `json:"from_ms"`
	To           int64  `json:"to_ms"`
	Pages        int    `json:"pages"`
	Observations int    `json:"span_observations"`
	NextPage     string `json:"next_page_token,omitempty"`
	Complete     bool   `json:"complete"`
}

func ImportGCP(ctx context.Context, cfg GCPImportConfig) (GCPImportReport, error) {
	report := GCPImportReport{From: cfg.From.UnixMilli(), To: cfg.To.UnixMilli()}
	if cfg.Database == "" || report.From < 0 || report.To <= report.From || report.To > 9223372036854 {
		return report, errors.New("database and valid absolute from/to range required")
	}
	source, err := newCloudTraceSource(ctx, cfg.Project, cfg.Filter)
	if err != nil {
		return report, err
	}
	defer source.Close()
	store, err := openDashboardStore(cfg.Database)
	if err != nil {
		return report, err
	}
	defer store.Close()
	return store.importGCP(ctx, source, report)
}

func (s *dashboardStore) gcpCheckpoint(ctx context.Context, key string) (*GCPImportReport, error) {
	raw, err := s.checkpoint(ctx, key)
	if err != nil || raw == "" {
		return nil, err
	}
	var report GCPImportReport
	if err = json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *dashboardStore) importGCP(ctx context.Context, source *cloudTraceSource, report GCPImportReport) (GCPImportReport, error) {
	if err := s.bindOrigin(ctx, source.origin(), "Google Cloud Trace import; stored observations only"); err != nil {
		return report, err
	}
	key := fmt.Sprintf("gcp:import:%s:%d:%d", source.identity(), report.From, report.To)
	previous, err := s.gcpCheckpoint(ctx, key)
	if err != nil {
		return report, err
	}
	if previous != nil {
		report = *previous
	}
	for pages := 0; !report.Complete && pages < 4096; pages++ {
		report, err = s.importGCPPage(ctx, source, report, key)
		if err != nil {
			return report, err
		}
	}
	if !report.Complete {
		return report, errors.New("GCP import page budget reached; rerun the same command to resume")
	}
	return report, nil
}

// Each page is replayable. Data commits in bounded batches; only the final
// transaction advances the page cursor. Failure replays unchanged spans safely.
func (s *dashboardStore) importGCPPage(ctx context.Context, source *cloudTraceSource, report GCPImportReport, key string) (GCPImportReport, error) {
	if report.Complete {
		return report, nil
	}
	raw, _ := json.Marshal(report)
	if err := s.ingest(ctx, nil, key, string(raw)); err != nil {
		return report, err
	}
	spans, next, err := source.page(ctx, time.UnixMilli(report.From), time.UnixMilli(report.To), report.NextPage)
	var status cloudTraceHTTPError
	if report.NextPage != "" && errors.As(err, &status) && status.code == 400 {
		// Pagination tokens may no longer be accepted after downtime. Restart
		// this fixed window once; committed observations deduplicate on replay.
		report.NextPage, report.Pages, report.Observations = "", 0, 0
		raw, _ = json.Marshal(report)
		if err = s.ingest(ctx, nil, key, string(raw)); err != nil {
			return report, err
		}
		spans, next, err = source.page(ctx, time.UnixMilli(report.From), time.UnixMilli(report.To), "")
	}
	if err != nil {
		return report, err
	}
	for start := 0; start < len(spans); start += 256 {
		if err = s.ingest(ctx, spans[start:min(start+256, len(spans))], "", ""); err != nil {
			return report, err
		}
	}
	updated := report
	updated.Pages++
	updated.Observations += len(spans)
	updated.NextPage = next
	updated.Complete = next == ""
	raw, _ = json.Marshal(updated)
	if err = s.ingest(ctx, nil, key, string(raw)); err != nil {
		return report, err
	}
	notice := fmt.Sprintf("Google Cloud Trace project %s; %d pages, %d span observations; range %s – %s; complete=%t. Sampling and upstream retention apply", source.project, updated.Pages, updated.Observations, time.UnixMilli(updated.From).UTC().Format(time.RFC3339), time.UnixMilli(updated.To).UTC().Format(time.RFC3339), updated.Complete)
	if err = s.bindOrigin(ctx, source.origin(), notice); err != nil {
		return updated, err
	}
	return updated, nil
}
