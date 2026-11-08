package dashboard

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const gcpPollInterval = 30 * time.Second
const gcpOverlap = 15 * time.Minute

func (s *dashboardStore) followGCP(project, filter string) error {
	ctx, cancel := context.WithCancel(context.Background())
	source, err := newCloudTraceSource(ctx, project, filter)
	if err != nil {
		cancel()
		return err
	}
	if err = s.bindOrigin(ctx, source.origin(), "Google Cloud Trace live import starting"); err != nil {
		source.Close()
		cancel()
		return err
	}
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer source.Close()
		for {
			caughtUp, err := s.syncGCP(ctx, source, time.Now())
			if ctx.Err() != nil {
				return
			}
			s.record(err)
			if err == nil && !caughtUp {
				continue
			}
			timer := time.NewTimer(gcpPollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	return nil
}

func (s *dashboardStore) syncGCP(ctx context.Context, source *cloudTraceSource, now time.Time) (bool, error) {
	now = now.Truncate(time.Millisecond)
	key := "gcp:live:" + source.identity()
	previous, err := s.gcpCheckpoint(ctx, key)
	if err != nil {
		return false, err
	}
	report := GCPImportReport{From: now.Add(-gcpOverlap).UnixMilli(), To: now.UnixMilli()}
	if previous != nil {
		if !previous.Complete {
			report = *previous
		} else {
			last := time.UnixMilli(previous.To)
			if !now.After(last) {
				return true, nil
			}
			report.From = last.Add(-gcpOverlap).UnixMilli()
			report.To = min(now.UnixMilli(), last.Add(gcpOverlap).UnixMilli())
		}
	}
	if report.To < now.Add(-gcpOverlap).UnixMilli() {
		// Refresh a recent page before each historical page. Neither an old
		// pagination cursor nor a slow backlog request can monopolize live work.
		latest, err := s.syncGCPRecent(ctx, source, now)
		if err != nil {
			return false, err
		}
		historyCtx, cancel := context.WithTimeout(ctx, gcpPollInterval)
		defer cancel()
		_, err = s.importGCPPage(historyCtx, source, report, key)
		s.setNotice(fmt.Sprintf("Google Cloud Trace live project %s; recent window through %s (complete=%t); historical catch-up through %s. Stored history retained; sampling and upstream retention apply", source.project, time.UnixMilli(latest.To).UTC().Format(time.RFC3339), latest.Complete, time.UnixMilli(report.To).UTC().Format(time.RFC3339)))
		if errors.Is(err, context.DeadlineExceeded) && historyCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return false, nil
		}
		return false, err
	}
	report, err = s.importGCPPage(ctx, source, report, key)
	// A multi-page window retains its original end while pages are read. Pause
	// after completing a recent window instead of continuously resweeping it
	// merely because the clock advanced between page requests.
	return report.Complete && report.To >= now.Add(-gcpOverlap).UnixMilli(), err
}

func (s *dashboardStore) syncGCPRecent(ctx context.Context, source *cloudTraceSource, now time.Time) (GCPImportReport, error) {
	key := "gcp:recent:" + source.identity()
	previous, err := s.gcpCheckpoint(ctx, key)
	if err != nil {
		return GCPImportReport{}, err
	}
	report := GCPImportReport{From: now.Add(-gcpOverlap).UnixMilli(), To: now.UnixMilli()}
	if previous != nil {
		last := time.UnixMilli(previous.To)
		if previous.Complete && now.Sub(last) < gcpPollInterval {
			return *previous, nil
		}
		if !previous.Complete && last.After(now.Add(-gcpOverlap)) {
			report = *previous
		}
	}
	return s.importGCPPage(ctx, source, report, key)
}
