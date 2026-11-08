package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/bits"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/johan-st/phos"
	"github.com/johan-st/phos/internal/tracejsonl"
)

const storedBucketWidth int64 = 1000

type bucketKey struct {
	Start              int64
	Service, Operation string
}
type storedBucket struct {
	Counts    timeBucket
	Histogram map[int64]int64
}

// Sparse HDR-equivalent counts (1us, three significant figures). Retaining the
// top 11 bits gives the same equivalence ranges as the query histogram, without
// serializing its zero-filled count array in every second/service/operation cell.
func histogramValue(p phos.Snapshot) int64 {
	d := min(max(int64(1), p.TimeEnd.Sub(p.TimeStart).Microseconds()), int64(3600000000000))
	shift := max(0, bits.Len64(uint64(d))-11)
	return d >> shift << shift
}
func newHistogram() *hdr.Histogram { return hdr.New(1, 3600000000000, 3) }
func updateBucket(ctx context.Context, tx *sql.Tx, changed map[bucketKey]*storedBucket, p phos.Snapshot, sign int) error {
	service, operation := spanNames(p)
	start := p.TimeStart.UnixMilli() / storedBucketWidth * storedBucketWidth
	k := bucketKey{start, service, operation}
	b := changed[k]
	if b == nil {
		b = &storedBucket{Histogram: map[int64]int64{}}
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT raw FROM dashboard_buckets WHERE start=? AND service=? AND operation=? AND hi IS NULL`, k.Start, k.Service, k.Operation).Scan(&raw)
		if err == nil {
			if err = json.Unmarshal(raw, b); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		changed[k] = b
	}
	var delta timeBucket
	delta.addSpan(p)
	b.Counts.Spans += sign * delta.Spans
	b.Counts.Errors += sign * delta.Errors
	b.Counts.UnknownDuration += sign * delta.UnknownDuration
	for i, n := range delta.DurationCounts {
		b.Counts.DurationCounts[i] += sign * n
	}
	if delta.UnknownDuration == 0 {
		v := histogramValue(p)
		b.Histogram[v] += int64(sign)
		if b.Histogram[v] == 0 {
			delete(b.Histogram, v)
		}
	}
	return nil
}
func addCounts(dst *timeBucket, src timeBucket) {
	dst.Spans += src.Spans
	dst.Errors += src.Errors
	dst.UnknownDuration += src.UnknownDuration
	for i, n := range src.DurationCounts {
		dst.DurationCounts[i] += n
	}
}
func finishHistogram(agg *searchAggregates, hist *hdr.Histogram) {
	agg.P50 = float64(hist.ValueAtQuantile(50)) / 1000
	agg.P95 = float64(hist.ValueAtQuantile(95)) / 1000
	distribution := hist.Distribution()
	for bound := int64(100); bound <= 10000000000000; bound *= 10 {
		var count int64
		for _, b := range distribution {
			if b.To <= bound && (b.To > bound/10 || bound == 100) {
				count += b.Count
			}
		}
		agg.Latency = append(agg.Latency, latencyBucket{float64(bound) / 1000, count})
	}
}
func (s *dashboardStore) projectedOverview(ctx context.Context, q searchQuery, w sourceStamp, r *storedResults, agg *searchAggregates, hist *hdr.Histogram) error {
	stats := make([]bucketAccumulator, len(agg.Buckets))
	for i := range agg.Buckets {
		display := &agg.Buckets[i]
		column := &stats[i]
		from := (display.From + storedBucketWidth - 1) / storedBucketWidth * storedBucketWidth
		to := display.To / storedBucketWidth * storedBucketWidth
		if from < to {
			rows, err := s.db.QueryContext(ctx, `SELECT service,operation,raw FROM dashboard_buckets WHERE start>=? AND start<? AND lo<=? AND (hi IS NULL OR hi>?) AND (?='' OR dashboard_equal_fold(service,?)) AND (?='' OR operation=?)`, from, to, w.MaxSeq, w.MaxSeq, q.Service, q.Service, q.Operation, q.Operation)
			if err != nil {
				return err
			}
			for rows.Next() {
				var svc, op string
				var raw []byte
				if err = rows.Scan(&svc, &op, &raw); err != nil {
					break
				}
				var b storedBucket
				if err = json.Unmarshal(raw, &b); err != nil {
					break
				}
				addCounts(display, b.Counts)
				column.addFacets(svc, op, b.Counts.Spans)
				for v, n := range b.Histogram {
					column.addDuration(v, n)
					if err = hist.RecordValues(v, n); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
				if err = r.facets(ctx, svc, op, b.Counts.Spans); err != nil {
					break
				}
			}
			e := rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if e != nil {
				return e
			}
			if err = s.rawOverview(ctx, q, w, display, display.From, from, r, hist, column); err != nil {
				return err
			}
			if err = s.rawOverview(ctx, q, w, display, to, display.To, r, hist, column); err != nil {
				return err
			}
		} else if err := s.rawOverview(ctx, q, w, display, display.From, display.To, r, hist, column); err != nil {
			return err
		}
		agg.Spans += display.Spans
		agg.Errors += display.Errors
	}
	finishBucketStats(agg.Buckets, stats)
	return nil
}
func (s *dashboardStore) rawOverview(ctx context.Context, q searchQuery, w sourceStamp, b *timeBucket, from, to int64, r *storedResults, hist *hdr.Histogram, column *bucketAccumulator) error {
	if from >= to {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT raw FROM dashboard_spans WHERE start>=? AND start<? AND lo<=? AND (hi IS NULL OR hi>?) AND (?='' OR dashboard_equal_fold(service,?)) AND (?='' OR operation=?)`, from, to, w.MaxSeq, w.MaxSeq, q.Service, q.Service, q.Operation, q.Operation)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		p, e := tracejsonl.Unmarshal(raw)
		if e != nil {
			return e
		}
		b.addSpan(p)
		column.addSpan(p)
		if !p.TimeEnd.IsZero() && !p.TimeEnd.Before(p.TimeStart) {
			if e = hist.RecordValue(min(max(int64(1), p.TimeEnd.Sub(p.TimeStart).Microseconds()), int64(3600000000000))); e != nil {
				return e
			}
		}
		svc, op := spanNames(p)
		if e = r.facets(ctx, svc, op, 1); e != nil {
			return e
		}
	}
	return rows.Err()
}
