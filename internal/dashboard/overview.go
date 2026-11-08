package dashboard

import (
	"sort"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/johan-st/phos"
)

// Versioned, additive span statistics. Duration bins are (previous, upper],
// except the first includes zero and the last is unbounded. Unknown durations
// are counted separately, never presented as zero-latency observations.
const durationBinCount = 20

var durationBounds = [durationBinCount]float64{0.1, 0.2, 0.5, 1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 30000, 60000, 300000, 0}

func (b *timeBucket) addSpan(p phos.Snapshot) {
	b.Spans++
	if feedFailed(p) {
		b.Errors++
	}
	if p.TimeEnd.IsZero() || p.TimeEnd.Before(p.TimeStart) {
		b.UnknownDuration++
		return
	}
	duration := float64(p.TimeEnd.Sub(p.TimeStart)) / float64(time.Millisecond)
	for i, bound := range durationBounds {
		if bound == 0 || duration <= bound {
			b.DurationCounts[i]++
			return
		}
	}
}

// UTC epoch alignment makes full buckets reusable across neighboring queries.
// Only edge buckets are clipped to [from,to); they must never be persisted as
// complete materialised buckets. Pick a stable resolution with at most 60 cells.
func overviewBuckets(from, to int64) ([]timeBucket, int64) {
	steps := []int64{1, 5, 10, 50, 100, 500, 1000, 5000, 10000, 15000, 30000, 60000, 120000, 300000, 600000, 900000, 1800000, 3600000, 7200000, 21600000, 43200000, 86400000}
	step := steps[len(steps)-1]
	for _, candidate := range steps {
		step = candidate
		if (to-1)/step-from/step+1 <= 60 {
			break
		}
	}
	for (to-1)/step-from/step+1 > 60 {
		step *= 2
	}
	buckets := []timeBucket{}
	for start := from / step * step; start < to; start += step {
		buckets = append(buckets, timeBucket{From: max(from, start), To: min(to, start+step)})
	}
	return buckets, step
}

type facetCounts struct {
	counts map[string]int
	other  int
}

func (f *facetCounts) add(value string) {
	f.addCount(value, 1)
}
func (f *facetCounts) addCount(value string, n int) {
	if n <= 0 {
		return
	}
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	if value == "" || len(value) > 256 || len(f.counts) >= 1000 && f.counts[value] == 0 {
		f.other += n
		return
	}
	f.counts[value] += n
}

// Query-local summaries reuse the persisted sparse duration histogram. Only
// clipped edges and queries with span/trace predicates need raw observations.
type bucketAccumulator struct {
	histogram            map[int64]int64
	services, operations facetCounts
}

func (b *bucketAccumulator) addSpan(p phos.Snapshot) {
	svc, op := spanNames(p)
	b.addFacets(svc, op, 1)
	if !p.TimeEnd.IsZero() && !p.TimeEnd.Before(p.TimeStart) {
		b.addDuration(histogramValue(p), 1)
	}
}
func (b *bucketAccumulator) addFacets(svc, op string, n int) {
	b.services.addCount(svc, n)
	b.operations.addCount(op, n)
}
func (b *bucketAccumulator) addDuration(v, n int64) {
	if b.histogram == nil {
		b.histogram = map[int64]int64{}
	}
	b.histogram[v] += n
}
func (b *bucketAccumulator) result(hist *hdr.Histogram) *bucketStats {
	hist.Reset()
	for v, n := range b.histogram {
		_ = hist.RecordValues(v, n)
	}
	return &bucketStats{
		Services: b.services.result(), Operations: b.operations.result(),
		Samples: hist.TotalCount(),
		P50:     float64(hist.ValueAtQuantile(50)) / 1000,
		P95:     float64(hist.ValueAtQuantile(95)) / 1000,
		P99:     float64(hist.ValueAtQuantile(99)) / 1000,
	}
}
func finishBucketStats(buckets []timeBucket, stats []bucketAccumulator) {
	hist := newHistogram()
	for i := range buckets {
		buckets[i].Stats = stats[i].result(hist)
	}
}
func (f facetCounts) result() searchFacet {
	out := searchFacet{Values: []facetValue{}, Other: f.other}
	for value, count := range f.counts {
		out.Values = append(out.Values, facetValue{value, count})
	}
	sort.Slice(out.Values, func(i, j int) bool {
		if out.Values[i].Count != out.Values[j].Count {
			return out.Values[i].Count > out.Values[j].Count
		}
		return out.Values[i].Value < out.Values[j].Value
	})
	for _, value := range out.Values[min(8, len(out.Values)):] {
		out.Other += value.Count
	}
	out.Values = out.Values[:min(8, len(out.Values))]
	return out
}
