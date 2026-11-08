package dashboard

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/johan-st/phos"
)

func TestOverviewDurationBoundaries(t *testing.T) {
	base := time.Unix(1700000000, 0)
	var bucket timeBucket
	// Zero, exact boundary, just over, overflow, absent and invalid end.
	for _, duration := range []time.Duration{0, 100 * time.Microsecond, 101 * time.Microsecond, 6 * time.Minute} {
		bucket.addSpan(phos.Snapshot{TimeStart: base, TimeEnd: base.Add(duration)})
	}
	bucket.addSpan(phos.Snapshot{TimeStart: base, Failed: true})
	bucket.addSpan(phos.Snapshot{TimeStart: base, TimeEnd: base.Add(-time.Millisecond)})
	if bucket.Spans != 6 || bucket.Errors != 1 || bucket.UnknownDuration != 2 || bucket.DurationCounts[0] != 2 || bucket.DurationCounts[1] != 1 || bucket.DurationCounts[19] != 1 {
		t.Fatalf("duration bins lost or misclassified spans: %+v", bucket)
	}
}

func TestOverviewBucketsAlignAndClip(t *testing.T) {
	for _, interval := range [][2]int64{{0, 1}, {1234, 912345}, {3600000, 7200000}, {1, 9223372036854}} {
		from, to := interval[0], interval[1]
		buckets, step := overviewBuckets(from, to)
		if len(buckets) == 0 || len(buckets) > 60 || buckets[0].From != from || buckets[len(buckets)-1].To != to {
			t.Fatalf("invalid partition: %v", buckets)
		}
		for i, b := range buckets {
			if b.To <= b.From || b.To-b.From > step {
				t.Fatal("invalid bucket width")
			}
			if i > 0 && (b.From != buckets[i-1].To || b.From%step != 0) {
				t.Fatal("gap or unaligned interior boundary")
			}
		}
	}
}

func TestOverviewWholePopulationFiltersAndUpdates(t *testing.T) {
	base := time.UnixMilli(1700000000123)
	items := []phos.Snapshot{}
	for i := 0; i < 205; i++ {
		id := fmt.Sprint(i)
		items = append(items, phos.Snapshot{ID: id, TraceID: id, Name: "svc:work", TimeStart: base, TimeEnd: base.Add(time.Millisecond)})
	}
	// Outside [from,to), despite sharing the first/last aligned bucket.
	items = append(items, phos.Snapshot{ID: "before", TraceID: "before", Name: "svc:work", TimeStart: base.Add(-time.Millisecond), TimeEnd: base})
	items = append(items, phos.Snapshot{ID: "end", TraceID: "end", Name: "svc:work", TimeStart: base.Add(time.Minute), TimeEnd: base.Add(2 * time.Minute)})
	app, writer := searchFixture(t, items)
	// Latest span version moves between duration bins without double counting.
	changed := items[0]
	changed.TimeEnd = base.Add(time.Second)
	writer.Append(changed)
	raw := fmt.Sprintf("from=%d&to=%d", base.UnixMilli(), base.Add(time.Minute).UnixMilli())
	frame := searchDone(t, app, raw)
	if !frame.Coverage.Complete || len(frame.Summaries) != 100 || frame.Aggregates.Spans != 205 {
		t.Fatalf("population: %+v", frame.Aggregates)
	}
	counts := [durationBinCount]int{}
	for _, b := range frame.Aggregates.Buckets {
		for i, n := range b.DurationCounts {
			counts[i] += n
		}
	}
	if counts[3] != 204 || counts[12] != 1 || frame.Aggregates.HeatmapMax != 204 {
		t.Fatalf("deduplicated durations: %v", counts)
	}
	if !reflect.DeepEqual(frame.Aggregates.Services.Values, []facetValue{{"svc", 205}}) || !reflect.DeepEqual(frame.Aggregates.Operations.Values, []facetValue{{"work", 205}}) {
		t.Fatal("facets were page-limited")
	}
	filtered := searchDone(t, app, raw+"&operation=absent")
	if filtered.Aggregates.Spans != 0 || filtered.Aggregates.HeatmapMax != 0 {
		t.Fatal("heatmap ignored filter")
	}
	next := frame
	next.Seq++
	next.Aggregates.Buckets = append([]timeBucket{}, frame.Aggregates.Buckets...)
	next.Aggregates.Buckets[0].DurationCounts[3]--
	next.Aggregates.Buckets[0].DurationCounts[12]++
	delta := summaryDiff(frame, next)
	if !delta.BucketDelta || len(delta.Aggregates.Buckets) != 1 {
		t.Fatal("duration-only update was lost")
	}
}

func TestOverviewFacetsAreBoundedAndAccountForOmittedValues(t *testing.T) {
	var counts facetCounts
	for i := 0; i < 1100; i++ {
		counts.add(fmt.Sprint(i))
	}
	counts.add("0")
	counts.add("")
	facet := counts.result()
	total := facet.Other
	for _, v := range facet.Values {
		total += v.Count
	}
	if len(counts.counts) != 1000 || len(facet.Values) != 8 || total != 1102 || facet.Values[0] != (facetValue{"0", 2}) {
		t.Fatalf("facet bounds: %+v", facet)
	}
}

func TestOverviewColumnStatsCoverAllPagesAndClippedEdges(t *testing.T) {
	app, store, _ := storedApp(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	items := []phos.Snapshot{}
	for i := 0; i < 205; i++ {
		start := base.Add(200 * time.Millisecond)
		duration := time.Millisecond
		if i >= 195 {
			duration = 100 * time.Millisecond
		}
		items = append(items, phos.Snapshot{ID: fmt.Sprint(i), TraceID: fmt.Sprint(i), Name: "api:request", TimeStart: start, TimeEnd: start.Add(duration), Failed: i < 5})
	}
	items = append(items,
		phos.Snapshot{ID: "outside", TraceID: "outside", Name: "outside:ignored", TimeStart: base.Add(122 * time.Millisecond), TimeEnd: base.Add(time.Second)},
		phos.Snapshot{ID: "later", TraceID: "later", Name: "worker:load", TimeStart: base.Add(6 * time.Second), TimeEnd: base.Add(6020 * time.Millisecond)},
		phos.Snapshot{ID: "unknown", TraceID: "unknown", Name: "worker:pending", TimeStart: base.Add(6 * time.Second)},
	)
	storeBatch(t, store, items)
	raw := fmt.Sprintf("from=%d&to=%d", base.UnixMilli()+123, base.UnixMilli()+120123)
	frame := searchDone(t, app, raw)
	first, second := frame.Aggregates.Buckets[0], frame.Aggregates.Buckets[1]
	if !frame.Coverage.Complete || len(frame.Summaries) != 100 || first.To-first.From != 4877 || first.Spans != 205 || first.Errors != 5 {
		t.Fatalf("column population or width: %+v", first)
	}
	stats := first.Stats
	if stats == nil || stats.Samples != 205 || stats.P50 != 1 || stats.P95 != 1 || stats.P99 < 100 || stats.P99 > 100.1 || !reflect.DeepEqual(stats.Services.Values, []facetValue{{"api", 205}}) || !reflect.DeepEqual(stats.Operations.Values, []facetValue{{"request", 205}}) {
		t.Fatalf("first column stats: %+v", stats)
	}
	if second.Spans != 2 || second.UnknownDuration != 1 || second.Stats.Samples != 1 || second.Stats.P50 < 20 || second.Stats.P50 > 20.1 || len(second.Stats.Operations.Values) != 2 {
		t.Fatalf("second column stats: %+v %+v", second, second.Stats)
	}
	empty := frame.Aggregates.Buckets[2]
	if empty.Stats == nil || empty.Stats.Samples != 0 || len(empty.Stats.Services.Values) != 0 {
		t.Fatalf("empty column: %+v", empty)
	}
	filtered := searchDone(t, app, raw+"&service=worker&attr=absent=no")
	for _, b := range filtered.Aggregates.Buckets {
		if b.Spans != 0 || b.Stats == nil || b.Stats.Samples != 0 || len(b.Stats.Services.Values) != 0 {
			t.Fatalf("filtered column leaked spans: %+v", b)
		}
	}
	// Independently allocated but identical stats must not produce deltas.
	next := frame
	next.Seq++
	next.Aggregates.Buckets = append([]timeBucket{}, frame.Aggregates.Buckets...)
	copyStats := *stats
	next.Aggregates.Buckets[0].Stats = &copyStats
	if len(summaryDiff(frame, next).Aggregates.Buckets) != 0 {
		t.Fatal("identical statistics produced a delta")
	}
	copyStats.P99++
	if len(summaryDiff(frame, next).Aggregates.Buckets) != 1 {
		t.Fatal("percentile-only update was lost")
	}
}
