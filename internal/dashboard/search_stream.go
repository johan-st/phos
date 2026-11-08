package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/johan-st/phos"
)

type searchReplay struct {
	full      searchFrame
	completed *searchFrame
	journal   []searchFrame
	used      time.Time
	bytes     int
}

type traceChange struct {
	Schema   int          `json:"schema_version"`
	Source   string       `json:"source"`
	Epoch    string       `json:"epoch"`
	TraceID  string       `json:"trace_id"`
	Base     string       `json:"base_revision"`
	Revision string       `json:"revision"`
	Summary  traceSummary `json:"summary"`
	Upserts  []wireSpan   `json:"upserts"`
	Removed  []string     `json:"removed"`
}

// Keep only hashes between polls; unchanged spans never cross the browser wire.
func selectedTraceChange(items []phos.Snapshot, previous map[string][32]byte, base string) (traceChange, map[string][32]byte) {
	out := traceChange{Schema: 3, Base: base, Upserts: []wireSpan{}, Removed: []string{}}
	next := map[string][32]byte{}
	groups := feedGroups(items)
	if len(groups) == 0 {
		return out, next
	}
	items = groups[0].Spans
	out.Summary = summarize(items)
	out.TraceID = out.Summary.TraceID
	wire := make([]wireSpan, 0, len(items))
	for _, p := range items {
		span := toWireSpan(p)
		wire = append(wire, span)
		b, _ := json.Marshal(span)
		hash := sha256.Sum256(b)
		next[p.ID] = hash
		if previous != nil && previous[p.ID] != hash {
			out.Upserts = append(out.Upserts, span)
		}
	}
	for id := range previous {
		if _, ok := next[id]; !ok {
			out.Removed = append(out.Removed, id)
		}
	}
	b, _ := json.Marshal(wire)
	out.Revision = fmt.Sprintf("%x", sha256.Sum256(b))
	return out, next
}

func summaryDiff(old, next searchFrame) searchFrame {
	delta := next
	delta.Kind = "delta"
	delta.Base = old.Seq
	delta.Summaries = []traceSummary{}
	delta.Removed = []string{}
	delta.BucketDelta = true
	delta.Aggregates.Buckets = []timeBucket{}
	priorBuckets := map[int64]timeBucket{}
	for _, b := range old.Aggregates.Buckets {
		priorBuckets[b.From] = b
	}
	for _, b := range next.Aggregates.Buckets {
		if !reflect.DeepEqual(priorBuckets[b.From], b) {
			delta.Aggregates.Buckets = append(delta.Aggregates.Buckets, b)
		}
	}
	// Bucket keys belong to a time partition. Moving a rolling window creates
	// a new partition; merging it with overlapping old keys grows without bound.
	if old.Coverage.From != next.Coverage.From || old.Coverage.To != next.Coverage.To {
		delta.BucketDelta = false
		delta.Aggregates.Buckets = next.Aggregates.Buckets
	}
	prior := map[string]traceSummary{}
	for _, t := range old.Summaries {
		prior[t.TraceID] = t
	}
	for _, t := range next.Summaries {
		if prev, ok := prior[t.TraceID]; !ok || prev != t {
			delta.Summaries = append(delta.Summaries, t)
		}
		delete(prior, t.TraceID)
	}
	for id := range prior {
		delta.Removed = append(delta.Removed, id)
	}
	return delta
}
func (s *searchService) replay(next searchFrame, q searchQuery, raw string) searchFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%d", searchKey(q), next.Page)
	if q.Window == 0 {
		key += ":" + next.Query
	}
	state := s.feeds[key]
	if state != nil && next.Epoch == "" && next.Coverage.Running {
		next.Epoch = state.full.Epoch
	}
	if state == nil || state.full.Epoch != next.Epoch {
		state = &searchReplay{full: next}
		state.full.Query = uuid.NewString()
		state.full.Seq = 0
		s.feeds[key] = state
	}
	state.used = time.Now()
	next.Query = state.full.Query
	next.Seq = state.full.Seq
	next.Now = state.full.Now
	// Keep the last completed live result visible while its replacement runs.
	if q.Window > 0 && next.Coverage.Running && state.completed != nil {
		next.Summaries = state.completed.Summaries
		next.Order = state.completed.Order
		next.Aggregates = state.completed.Aggregates
		next.Coverage = state.completed.Coverage
		next.Coverage.Running = true
		next.Cursor = state.completed.Cursor
		next.Pages = state.completed.Pages
	} else if !next.Coverage.Running {
		completed := next
		state.completed = &completed
	}
	if next.Health.Error == state.full.Health.Error && next.Health.LastSuccess-state.full.Health.LastSuccess < 5000 {
		next.Health.LastSuccess = state.full.Health.LastSuccess
	}
	if state.full.Seq == 0 || !reflect.DeepEqual(next, state.full) {
		next.Seq++
		next.Now = time.Now().UnixMilli()
		delta := summaryDiff(state.full, next)
		state.full = next
		b, _ := json.Marshal(delta)
		state.bytes += len(b)
		state.journal = append(state.journal, delta)
		for len(state.journal) > 128 || state.bytes > 4*1024*1024 {
			b, _ := json.Marshal(state.journal[0])
			state.bytes -= len(b)
			state.journal = state.journal[1:]
		}
	}
	for len(s.feeds) > 16 {
		oldest := ""
		for k, v := range s.feeds {
			if k != key && (oldest == "" || v.used.Before(s.feeds[oldest].used)) {
				oldest = k
			}
		}
		delete(s.feeds, oldest)
	}
	v, _ := url.ParseQuery(raw)
	after, _ := strconv.ParseUint(v.Get("after"), 10, 64)
	if v.Get("epoch") != state.full.Epoch || v.Get("query_token") != state.full.Query || after == 0 || after > state.full.Seq || (len(state.journal) > 0 && after < state.journal[0].Base) {
		return state.full
	}
	if after == state.full.Seq {
		out := state.full
		out.Kind = "delta"
		out.Base = after
		out.Summaries = []traceSummary{}
		out.Removed = []string{}
		out.BucketDelta = true
		out.Aggregates.Buckets = []timeBucket{}
		return out
	}
	if len(state.journal) > 0 && after == state.journal[len(state.journal)-1].Base {
		return state.journal[len(state.journal)-1]
	}
	// Replay collapsed summary changes; final order and aggregates replace the
	// previous values atomically. Repeated upserts are idempotent in the client.
	out := state.full
	out.Kind = "delta"
	out.Base = after
	out.Summaries = []traceSummary{}
	out.Removed = []string{}
	upserts := map[string]traceSummary{}
	removed := map[string]bool{}
	for _, d := range state.journal {
		if d.Seq <= after {
			continue
		}
		for _, id := range d.Removed {
			delete(upserts, id)
			removed[id] = true
		}
		for _, t := range d.Summaries {
			upserts[t.TraceID] = t
			delete(removed, t.TraceID)
		}
	}
	for _, t := range upserts {
		out.Summaries = append(out.Summaries, t)
	}
	for id := range removed {
		out.Removed = append(out.Removed, id)
	}
	return out
}
func (a *App) handleSearchStream(req requestMessage) responseMessage {
	if _, err := parseSearchQuery(req.Query, time.Now()); err != nil {
		return errorResponse(req.ID, 400, err.Error())
	}
	id := parseConnectionID(req.Query)
	if id == "" {
		id = uuid.NewString()
	}
	traceCtx, span := phos.NewSpan(a.traceContext(a.baseTraceContext(req)), "dashboard-workload:live_connect", phos.WithAttrs(slog.String("connection.id", id)))
	span.Event("dashboard_workload.live_connected")
	ctx, cancel := context.WithTimeout(traceCtx, 45*time.Second)
	owner := &dashboardStream{cancel: cancel, done: make(chan struct{})}
	a.streamsMu.Lock()
	if a.stopping || len(a.streams) >= 128 {
		a.streamsMu.Unlock()
		cancel()
		span.End()
		return errorResponse(req.ID, 503, "dashboard streams unavailable")
	}
	if old := a.streams[id]; old != nil {
		old.cancel()
	}
	a.streams[id] = owner
	a.streamsWG.Add(1)
	a.streamsMu.Unlock()
	go func() {
		defer a.streamsWG.Done()
		defer close(owner.done)
		defer cancel()
		defer func() {
			span.Event("dashboard_workload.live_disconnected")
			span.End()
			a.streamsMu.Lock()
			if a.streams[id] == owner {
				delete(a.streams, id)
			}
			a.streamsMu.Unlock()
		}()
		params, _ := url.ParseQuery(req.Query)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		lastDetail := ""
		var detailHashes map[string][32]byte
		detailAt := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				j, offset, err := a.search.job(params.Encode())
				if err != nil {
					return
				}
				frame := a.search.replay(a.search.frame(j, offset), j.query, params.Encode())
				after, _ := strconv.ParseUint(params.Get("after"), 10, 64)
				if frame.Kind == "reset" || frame.Seq != after {
					if !a.publishSearch(id, "search-batch", frame) {
						return
					}
					params.Set("epoch", frame.Epoch)
					params.Set("query_token", frame.Query)
					params.Set("after", strconv.FormatUint(frame.Seq, 10))
				}
				if selected := params.Get("trace_id"); selected != "" && j.query.Window > 0 && time.Since(detailAt) >= 5*time.Second {
					detailAt = time.Now()
					w, e := a.search.source.Stamp(ctx)
					if e == nil {
						spans, e := a.search.source.Trace(ctx, selected, w)
						if e == nil {
							change, hashes := selectedTraceChange(spans, detailHashes, lastDetail)
							change.Source, change.Epoch = a.sourceID, w.Epoch
							if change.Revision != lastDetail {
								lastDetail, detailHashes = change.Revision, hashes
								b, _ := json.Marshal(change)
								if len(b) > feedCompressedBytes {
									if !a.publishSearch(id, "trace-changed", map[string]string{"trace_id": selected}) {
										return
									}
								} else if !a.publishSearch(id, "trace-delta", change) {
									return
								}
							}
						}
					}
				}
			}
		}
	}()
	return responseMessage{ID: req.ID, Kind: "http.response", Status: http.StatusOK, Stream: &responseStream{Protocol: "sse", ConnectionID: id}}
}
func (a *App) publishSearch(id, event string, value any) bool {
	b, err := json.Marshal(value)
	if err != nil {
		return false
	}
	if len(b) > feedCompressedBytes {
		b = []byte(base64.StdEncoding.EncodeToString(gzipFeed(b)))
		event += "-gzip"
	}
	if len(b) > 700000 {
		return false
	}
	return a.sendMessage != nil && a.sendMessage(sseEventMessage{Kind: "sse.event", ConnectionID: id, Event: event, DataBase64: base64.StdEncoding.EncodeToString(b)}) == nil
}
