package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const demoPollInterval = 30 * time.Second
const demoOverlap = 15 * time.Minute

// Demo following belongs to ingestion, independent of browser connections.
func (s *dashboardStore) followDemo(endpoint string) error {
	source, err := newJaegerQuerySource(endpoint)
	if err != nil {
		return err
	}
	if err = s.bindOrigin(context.Background(), "demo:"+originID(source.base.String()), "Live Jaeger demo import starting; upstream coverage is best-effort"); err != nil {
		source.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer source.Close()
		for {
			caughtUp, err := s.syncDemo(ctx, source, time.Now())
			if ctx.Err() != nil {
				return
			}
			s.record(err)
			if err == nil && !caughtUp {
				continue
			}
			timer := time.NewTimer(demoPollInterval)
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

func (s *dashboardStore) syncDemo(ctx context.Context, source *jaegerQuerySource, now time.Time) (bool, error) {
	// Reuse one checkpoint rather than accumulating one entry per polling window.
	key := "demo:live:" + originID(source.base.String())
	saved, err := s.checkpoint(ctx, key)
	if err != nil {
		return false, err
	}
	now = now.Truncate(time.Millisecond)
	from, to := now.Add(-demoOverlap), now
	if saved != "" {
		var previous DemoImportReport
		if err = json.Unmarshal([]byte(saved), &previous); err != nil {
			return false, err
		}
		if len(previous.Pending) > 0 {
			from, to = time.UnixMilli(previous.From), time.UnixMilli(previous.To)
		} else {
			last := time.UnixMilli(previous.To)
			if !now.After(last) {
				return true, nil
			}
			from = last.Add(-demoOverlap)
			// Catch up after downtime in bounded steps, preserving the missing period.
			if end := last.Add(demoOverlap); end.Before(to) {
				to = end
			}
		}
	}
	backfilling := to.Before(now.Add(-demoOverlap))
	if backfilling {
		latest, err := s.syncDemoRecent(ctx, source, now)
		if err != nil {
			return false, err
		}
		// Keep the live window fresh while an old archive catches up. A stalled
		// history request yields to the next live poll without losing its queue.
		historyCtx, cancel := context.WithTimeout(ctx, demoPollInterval)
		defer cancel()
		_, err = s.importDemo(historyCtx, source, DemoImportConfig{From: from, To: to}, key)
		s.setNotice(fmt.Sprintf("Live Jaeger demo import; polls every 30s with 15m overlap; recent window imported through %s; coverage best-effort, %d saturated intervals in latest live import. Historical catch-up window %s – %s; stored history retained", time.UnixMilli(latest.To).UTC().Format(time.RFC3339), latest.Unresolved, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)))
		if errors.Is(err, context.DeadlineExceeded) && historyCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return false, nil
		}
		return false, err
	}
	report, err := s.importDemo(ctx, source, DemoImportConfig{From: from, To: to}, key)
	if err != nil {
		return false, err
	}
	s.setNotice(fmt.Sprintf("Live Jaeger demo import; polls every 30s with 15m overlap; imported through %s; coverage best-effort, %d saturated intervals in latest import. Stored history retained", to.UTC().Format(time.RFC3339), report.Unresolved))
	return !to.Before(now), nil
}

func (s *dashboardStore) syncDemoRecent(ctx context.Context, source *jaegerQuerySource, now time.Time) (DemoImportReport, error) {
	// History keeps its original checkpoint. This bounded checkpoint follows
	// recent observations, even when the archive is days behind after downtime.
	key := "demo:recent:" + originID(source.base.String())
	saved, err := s.checkpoint(ctx, key)
	if err != nil {
		return DemoImportReport{}, err
	}
	from, to := now.Add(-demoOverlap), now
	if saved != "" {
		var previous DemoImportReport
		if err = json.Unmarshal([]byte(saved), &previous); err != nil {
			return DemoImportReport{}, err
		}
		last := time.UnixMilli(previous.To)
		if len(previous.Pending) == 0 && now.Sub(last) < demoPollInterval {
			return previous, nil
		}
		if len(previous.Pending) > 0 && last.After(from) {
			from, to = time.UnixMilli(previous.From), last
		}
	}
	return s.importDemo(ctx, source, DemoImportConfig{From: from, To: to}, key)
}
