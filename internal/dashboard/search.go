package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"github.com/google/uuid"
	"github.com/johan-st/phos"
)

type searchJob struct {
	result                           *storedResults
	disposed                         bool
	serviceCounts, operationCounts   facetCounts
	mu                               sync.Mutex
	id, key                          string
	refresh                          string
	bytes                            int
	query                            searchQuery
	stamp                            sourceStamp
	summaries                        []traceSummary
	aggregates                       searchAggregates
	coverage                         searchCoverage
	seq                              uint64
	used, started                    time.Time
	cancel                           context.CancelFunc
	services, operations, attributes map[string]bool
}
type searchService struct {
	mu       sync.Mutex
	source   querySource
	identity string
	jobs     map[string]*searchJob
	current  map[string]string
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	feeds    map[string]*searchReplay
	slots    chan struct{}
}

func newSearchService(source querySource, identity string) *searchService {
	ctx, cancel := context.WithCancel(context.Background())
	return &searchService{source: source, identity: identity, jobs: map[string]*searchJob{}, current: map[string]string{}, ctx: ctx, cancel: cancel, feeds: map[string]*searchReplay{}, slots: make(chan struct{}, 2)}
}
func (s *searchService) close() error {
	s.mu.Lock()
	s.cancel()
	for _, j := range s.jobs {
		j.dispose()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return s.source.Close()
}
func searchKey(q searchQuery) string {
	if q.Window > 0 {
		q.From = 0
		q.To = 0
	}
	b, _ := json.Marshal(q)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (s *searchService) job(raw string) (*searchJob, int, error) {
	q, err := parseSearchQuery(raw, time.Now())
	if err != nil {
		return nil, 0, err
	}
	key := searchKey(q)
	v, _ := url.ParseQuery(raw)
	offset := 0
	token := ""
	if cursor := v.Get("cursor"); cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil {
			return nil, 0, errors.New("invalid cursor")
		}
		parts := strings.Split(string(b), ":")
		if len(parts) != 2 {
			return nil, 0, errors.New("invalid cursor")
		}
		token = parts[0]
		offset, e = strconv.Atoi(parts[1])
		if e != nil || offset < 0 || offset%100 != 0 {
			return nil, 0, errors.New("invalid cursor")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, 0, errors.New("dashboard stopping")
	}
	now := time.Now()
	for id, j := range s.jobs {
		if now.Sub(j.used) > 2*time.Minute {
			j.dispose()
			delete(s.jobs, id)
			if s.current[j.key] == id {
				delete(s.current, j.key)
			}
		}
	}
	if token != "" {
		j := s.jobs[token]
		if j == nil || j.key != key {
			return nil, 0, errSourceChanged
		}
		j.used = now
		return j, offset, nil
	}
	if id := s.current[key]; id != "" {
		j := s.jobs[id]
		if j != nil {
			j.mu.Lock()
			running := j.coverage.Running
			j.mu.Unlock()
			reuse := running || (q.Window == 0 && v.Get("refresh") == j.refresh) || (q.Window > 0 && now.Sub(j.started) < 5*time.Second)
			if !running && q.Window == 0 {
				if store, ok := s.source.(*dashboardStore); ok {
					ctx, cancel := context.WithTimeout(s.ctx, time.Second)
					latest, err := store.Stamp(ctx)
					cancel()
					j.mu.Lock()
					previous := j.stamp
					j.mu.Unlock()
					if err == nil && latest != previous {
						reuse = false
					}
				}
			}
			if reuse {
				j.used = now
				return j, 0, nil
			}
		}
	}
	if len(s.jobs) >= 16 {
		var oldest *searchJob
		for _, j := range s.jobs {
			if oldest == nil || j.used.Before(oldest.used) {
				oldest = j
			}
		}
		oldest.dispose()
		delete(s.jobs, oldest.id)
		if s.current[oldest.key] == oldest.id {
			delete(s.current, oldest.key)
		}
	}
	timeout := 5 * time.Second
	if _, ok := s.source.(*dashboardStore); ok {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	j := &searchJob{id: uuid.NewString(), key: key, query: q, started: now, used: now, cancel: cancel, seq: 1, summaries: []traceSummary{}, services: map[string]bool{}, operations: map[string]bool{}, attributes: map[string]bool{}, coverage: searchCoverage{From: q.From, To: q.To, Rolling: q.Window > 0, Running: true}, aggregates: searchAggregates{Buckets: []timeBucket{}, Latency: []latencyBucket{}}}
	j.refresh = v.Get("refresh")
	s.jobs[j.id] = j
	s.current[key] = j.id
	s.wg.Add(1)
	go func() { defer s.wg.Done(); defer cancel(); s.run(ctx, j) }()
	return j, 0, nil
}
func spanNames(s phos.Snapshot) (string, string) {
	service, operation, ok := strings.Cut(s.Name, ":")
	if !ok {
		operation = s.Name
		service = ""
	}
	for _, a := range s.Attrs {
		if a.Key == "service.name" {
			service = a.Value.String()
		}
		if a.Key == "operation" {
			operation = a.Value.String()
		}
	}
	return service, operation
}
func flattenAttrs(attrs []slog.Attr, prefix string, out map[string]string) {
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			flattenAttrs(v.Group(), prefix+a.Key+".", out)
		} else {
			out[prefix+a.Key] = v.String()
		}
	}
}
func matchSpan(s phos.Snapshot, q searchQuery) bool {
	n := s.TimeStart.UnixMilli()
	if n < q.From || n >= q.To {
		return false
	}
	svc, op := spanNames(s)
	if q.Service != "" && !strings.EqualFold(svc, q.Service) || q.Operation != "" && op != q.Operation {
		return false
	}
	attrs := map[string]string{}
	flattenAttrs(s.Attrs, "", attrs)
	for k, v := range q.Attributes {
		if got, ok := attrs[k]; !ok || got != v {
			return false
		}
	}
	return feedMatches(feedGroup{TraceID: s.TraceID, Spans: []phos.Snapshot{s}}, feedFilters{Query: q.Text})
}
func summarize(spans []phos.Snapshot) traceSummary {
	g := feedGroups(spans)[0]
	t := traceSummary{TraceID: g.TraceID, Name: g.Name, Start: wireMillis(g.StartedAt), End: wireMillis(g.EndedAt), Duration: float64(g.EndedAt.Sub(g.StartedAt)) / float64(time.Millisecond), Spans: len(g.Spans), RootStatus: "unknown"}
	ids := map[string]bool{}
	for _, p := range g.Spans {
		ids[p.ID] = true
	}
	rootFound := false
	hasRoot := false
	for _, p := range g.Spans {
		hasRoot = hasRoot || p.ParentID == ""
	}
	for _, p := range g.Spans {
		if feedFailed(p) {
			t.Errors++
		}
		t.Events += len(p.Events)
		if p.ParentID != "" && !ids[p.ParentID] {
			t.Incomplete = true
		}
		if !rootFound && (p.ParentID == "" || !hasRoot && !ids[p.ParentID]) {
			rootFound = true
			t.Service, t.Operation = spanNames(p)
			if p.ParentID == "" {
				for _, a := range p.Attrs {
					switch a.Key {
					case "http.status_code", "http.response.status_code", "status_code":
						t.RootStatus = "HTTP " + a.Value.String()
					case "otel.status_code":
						if t.RootStatus == "unknown" {
							t.RootStatus = a.Value.String()
						}
					}
				}
				if feedFailed(p) {
					t.RootStatus = "error"
				}
			}
		}
	}
	if !rootFound {
		t.Incomplete = true
	}
	return t
}
func (s *searchService) run(ctx context.Context, j *searchJob) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		j.mu.Lock()
		j.coverage.Running = false
		j.coverage.Reason = "Search capacity/deadline reached"
		j.mu.Unlock()
		return
	}
	if store, ok := s.source.(*dashboardStore); ok {
		store.runQuery(ctx, j)
		return
	}
	w, err := s.source.Stamp(ctx)
	j.mu.Lock()
	j.stamp = w
	j.mu.Unlock()
	hist := hdr.New(1, 3600000000000, 3)
	buckets, step := overviewBuckets(j.query.From, j.query.To)
	stats := make([]bucketAccumulator, len(buckets))
	j.mu.Lock()
	j.aggregates.Buckets = buckets
	j.aggregates.BucketVersion = 1
	j.aggregates.BucketWidth = step
	j.aggregates.DurationBounds = durationBounds
	j.mu.Unlock()
	if err == nil {
		var limited bool
		limited, err = s.source.Visit(ctx, j.query, w, func(spans []phos.Snapshot) error {
			if e := ctx.Err(); e != nil {
				return e
			}
			groups := feedGroups(spans)
			if len(groups) == 0 {
				return nil
			}
			spans = groups[0].Spans
			t := summarize(spans)
			j.mu.Lock()
			defer j.mu.Unlock()
			j.coverage.Scanned++
			if j.query.TraceID != "" && t.TraceID != j.query.TraceID {
				return nil
			}
			if t.Duration < float64(j.query.MinDuration) || j.query.MaxDuration > 0 && t.Duration > float64(j.query.MaxDuration) || j.query.Errors == "with" && t.Errors == 0 || j.query.Errors == "without" && t.Errors > 0 {
				return nil
			}
			matched := []phos.Snapshot{}
			for _, p := range spans {
				if matchSpan(p, j.query) {
					matched = append(matched, p)
				}
			}
			if len(matched) == 0 {
				return nil
			}
			encoded, _ := json.Marshal(t)
			if len(j.summaries) >= searchMaxSummaries || j.bytes+len(encoded) > 4*1024*1024 {
				return errSourceLimit
			}
			j.bytes += len(encoded)
			j.summaries = append(j.summaries, t)
			j.aggregates.Traces++
			if t.Errors > 0 {
				j.aggregates.ErrorTraces++
			}
			for _, p := range matched {
				j.aggregates.Spans++
				failed := feedFailed(p)
				if failed {
					j.aggregates.Errors++
				}
				i := int(p.TimeStart.UnixMilli()/step - j.query.From/step)
				j.aggregates.Buckets[i].addSpan(p)
				stats[i].addSpan(p)
				if !p.TimeEnd.IsZero() && !p.TimeEnd.Before(p.TimeStart) {
					d := max(int64(1), p.TimeEnd.Sub(p.TimeStart).Microseconds())
					_ = hist.RecordValue(min(d, 3600000000000))
				}
				svc, op := spanNames(p)
				j.serviceCounts.add(svc)
				j.operationCounts.add(op)
				if len(j.services) < 1000 && len(svc) <= 256 {
					j.services[svc] = true
				}
				if len(j.operations) < 1000 && len(op) <= 256 {
					j.operations[op] = true
				}
				attrs := map[string]string{}
				flattenAttrs(p.Attrs, "", attrs)
				for k := range attrs {
					if len(j.attributes) < 1000 && len(k) <= 256 {
						j.attributes[k] = true
					}
				}
			}
			j.seq++
			return nil
		})
		j.mu.Lock()
		j.coverage.SourceLimited = limited
		j.mu.Unlock()
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.coverage.Running = false
	j.coverage.Complete = err == nil && !j.coverage.SourceLimited
	if err != nil {
		j.coverage.Reason = err.Error()
	} else if j.coverage.SourceLimited {
		j.coverage.Reason = "Source search is bounded; upstream sampling and retention are unknown"
	}
	j.aggregates.P50 = float64(hist.ValueAtQuantile(50)) / 1000
	finishBucketStats(j.aggregates.Buckets, stats)
	j.aggregates.P95 = float64(hist.ValueAtQuantile(95)) / 1000
	// Fixed logarithmic display bins, independent of the number of stored spans.
	for bound := int64(100); bound <= 10000000000000; bound *= 10 {
		count := int64(0)
		for _, b := range hist.Distribution() {
			if b.To <= bound && b.To > bound/10 {
				count += b.Count
			}
		}
		if bound == 100 {
			for _, b := range hist.Distribution() {
				if b.To <= 10 {
					count += b.Count
				}
			}
		}
		j.aggregates.Latency = append(j.aggregates.Latency, latencyBucket{float64(bound) / 1000, count})
	}
	sort.Slice(j.summaries, func(i, k int) bool {
		a, b := j.summaries[i], j.summaries[k]
		switch j.query.Sort {
		case "slowest":
			if a.Duration != b.Duration {
				return a.Duration > b.Duration
			}
		case "errors":
			if a.Errors != b.Errors {
				return a.Errors > b.Errors
			}
		default:
			if a.Start != b.Start {
				return a.Start > b.Start
			}
		}
		return a.TraceID > b.TraceID
	})
	j.seq++
}
func (s *searchService) frame(j *searchJob, offset int) searchFrame {
	j.mu.Lock()
	defer j.mu.Unlock()
	total := len(j.summaries)
	if j.result != nil {
		total = j.result.count
	}
	offset = min(offset, total)
	end := min(offset+searchPageSize, total)
	items := []traceSummary{}
	if j.result != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
		var err error
		items, err = j.result.page(ctx, offset)
		cancel()
		if err != nil {
			j.coverage.Complete = false
			j.coverage.Reason = "Read local result page: " + err.Error()
		}
	} else {
		items = append(items, j.summaries[offset:end]...)
	}
	order := make([]string, len(items))
	for i, t := range items {
		order[i] = t.TraceID
	}
	cursor := ""
	if end < total && !j.coverage.Running {
		cursor = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", j.id, end)))
	}
	agg := j.aggregates
	if j.result == nil {
		agg.Services = j.serviceCounts.result()
		agg.Operations = j.operationCounts.result()
	}
	for _, bucket := range agg.Buckets {
		agg.UnknownDuration += bucket.UnknownDuration
		for _, count := range bucket.DurationCounts {
			agg.HeatmapMax = max(agg.HeatmapMax, count)
		}
	}
	agg.Buckets = append([]timeBucket{}, agg.Buckets...)
	agg.Latency = append([]latencyBucket{}, agg.Latency...)
	return searchFrame{Schema: 3, Source: s.identity, Epoch: j.stamp.Epoch, Query: fmt.Sprintf("%s:%d", j.id, offset), Kind: "reset", Seq: j.seq, Now: time.Now().UnixMilli(), Summaries: items, Removed: []string{}, Order: order, Aggregates: agg, Coverage: j.coverage, Health: s.source.Health(), Cursor: cursor, Page: offset/100 + 1, Pages: max(1, (total+99)/100)}
}
func queryResponse(id string, value any) responseMessage {
	b, err := json.Marshal(value)
	if err != nil {
		return errorResponse(id, 500, "encode dashboard data")
	}
	r := textResponse(id, 200, "application/json", string(b))
	if len(b) > feedCompressedBytes {
		b = gzipFeed(b)
		r.BodyBase64 = base64.StdEncoding.EncodeToString(b)
		r.Headers["content-encoding"] = "gzip"
	}
	if len(r.BodyBase64) > 900000 {
		return errorResponse(id, 413, "dashboard response exceeds wire budget")
	}
	r.Headers["cache-control"] = "no-store"
	return r
}
func (a *App) handleSearch(req requestMessage) responseMessage {
	j, offset, err := a.search.job(req.Query)
	if err != nil {
		if errors.Is(err, errSourceChanged) {
			return errorResponse(req.ID, 409, err.Error())
		}
		return errorResponse(req.ID, 400, err.Error())
	}
	j.mu.Lock()
	w := j.stamp
	j.mu.Unlock()
	if w.Epoch != "" {
		ctx, cancel := context.WithTimeout(a.baseTraceContext(req), time.Second)
		defer cancel()
		got, e := a.search.source.Stamp(ctx)
		if e != nil {
			return errorResponse(req.ID, 503, e.Error())
		}
		if got.Epoch != w.Epoch || got.MaxSeq < w.MaxSeq {
			j.dispose()
			a.search.mu.Lock()
			delete(a.search.current, j.key)
			delete(a.search.jobs, j.id)
			a.search.mu.Unlock()
			return errorResponse(req.ID, 409, errSourceChanged.Error())
		}
	}
	return queryResponse(req.ID, a.search.replay(a.search.frame(j, offset), j.query, req.Query))
}
func (a *App) handleMetadata(req requestMessage) responseMessage {
	m := sourceMetadata{Schema: 3, Source: a.sourceID, Health: a.search.source.Health(), Services: []string{}, Operations: []string{}, Attributes: []string{}, Historical: true}
	sets := []map[string]bool{{}, {}, {}}
	a.search.mu.Lock()
	for _, j := range a.search.jobs {
		j.mu.Lock()
		for i, values := range []map[string]bool{j.services, j.operations, j.attributes} {
			for v := range values {
				if v != "" && len(sets[i]) < 1000 {
					sets[i][v] = true
				}
			}
		}
		j.mu.Unlock()
	}
	a.search.mu.Unlock()
	for i, out := range []*[]string{&m.Services, &m.Operations, &m.Attributes} {
		for v := range sets[i] {
			*out = append(*out, v)
		}
		sort.Strings(*out)
	}
	if source, ok := a.search.source.(interface {
		Services(context.Context) ([]string, error)
	}); ok {
		ctx, cancel := context.WithTimeout(a.baseTraceContext(req), 3*time.Second)
		defer cancel()
		if services, err := source.Services(ctx); err == nil {
			m.Services = []string{}
			for _, service := range services {
				if len(service) <= 256 && len(m.Services) < 1000 {
					m.Services = append(m.Services, service)
				}
			}
		}
		m.Health = a.search.source.Health()
	}
	return queryResponse(req.ID, m)
}

type detailCursor struct {
	Trace     string
	Epoch     string
	Watermark int64
	Offset    int
	Revision  string
}

func (a *App) handleTrace(req requestMessage) responseMessage {
	v, _ := url.ParseQuery(req.Query)
	id := v.Get("trace_id")
	if id == "" {
		return errorResponse(req.ID, 400, "trace_id required")
	}
	ctx, cancel := context.WithTimeout(a.baseTraceContext(req), 5*time.Second)
	defer cancel()
	w, err := a.search.source.Stamp(ctx)
	if err != nil {
		return errorResponse(req.ID, 503, err.Error())
	}
	offset := 0
	previous := ""
	if raw := v.Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		var c detailCursor
		if e != nil || json.Unmarshal(b, &c) != nil || c.Trace != id || c.Offset < 0 {
			return errorResponse(req.ID, 400, "invalid trace cursor")
		}
		if c.Epoch != w.Epoch || c.Watermark > w.MaxSeq {
			return errorResponse(req.ID, 409, errSourceChanged.Error())
		}
		w.MaxSeq = c.Watermark
		offset = c.Offset
		previous = c.Revision
	}
	spans, err := a.search.source.Trace(ctx, id, w)
	reason := ""
	if err != nil {
		if !errors.Is(err, errSourceLimit) {
			return errorResponse(req.ID, 503, err.Error())
		}
		reason = err.Error()
	}
	if len(spans) == 0 {
		return errorResponse(req.ID, 404, "trace not available in source history")
	}
	groups := feedGroups(spans)
	spans = groups[0].Spans
	wire := make([]wireSpan, 0, len(spans))
	for _, p := range spans {
		wire = append(wire, toWireSpan(p))
	}
	b, _ := json.Marshal(wire)
	revision := fmt.Sprintf("%x", sha256.Sum256(b))
	if previous != "" && revision != previous {
		return errorResponse(req.ID, 409, "trace revision changed; reload")
	}
	if offset > len(wire) {
		return errorResponse(req.ID, 400, "invalid trace offset")
	}
	chunk := traceChunk{Schema: 3, Source: a.sourceID, Epoch: w.Epoch, TraceID: id, Revision: revision, Spans: []wireSpan{}, Reason: reason}
	chunk.Summary = summarize(spans)
	used := 0
	next := offset
	for next < len(wire) {
		b, _ := json.Marshal(wire[next])
		if len(b) > feedCompressedBytes {
			chunk.Reason = "A span exceeds the message budget"
			break
		}
		if used+len(b) > feedCompressedBytes {
			break
		}
		used += len(b)
		chunk.Spans = append(chunk.Spans, wire[next])
		next++
	}
	if next < len(wire) && next > offset {
		b, _ := json.Marshal(detailCursor{id, w.Epoch, w.MaxSeq, next, revision})
		chunk.Cursor = base64.RawURLEncoding.EncodeToString(b)
	}
	chunk.Complete = next == len(wire) && chunk.Reason == ""
	return queryResponse(req.ID, chunk)
}
