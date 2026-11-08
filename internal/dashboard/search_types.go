package dashboard

// Schema 3 separates dataset queries from selected-trace detail. These are data
// contracts: labels, layout, navigation and interaction stay in Gleam.
import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/johan-st/phos"
)

const searchPageSize = 100
const searchMaxSummaries = 10000
const searchMaxTraceBytes = 50 * 1024 * 1024

var errSourceLimit = errors.New("source candidate or trace-size limit reached")
var errSourceChanged = errors.New("source changed; refresh the query")

type searchQuery struct {
	From, To, Window                                int64
	Text, Service, Operation, TraceID, Errors, Sort string
	MinDuration, MaxDuration                        int64
	Attributes                                      map[string]string
}

func parseSearchQuery(raw string, now time.Time) (searchQuery, error) {
	v, err := url.ParseQuery(raw)
	q := searchQuery{Window: 900000, Sort: "latest", Attributes: map[string]string{}}
	if err != nil {
		return q, err
	}
	if s := v.Get("window"); s != "" {
		q.Window, err = strconv.ParseInt(s, 10, 64)
		if err != nil || q.Window < 60000 || q.Window > 365*24*60*60*1000 {
			return q, errors.New("invalid live window")
		}
	}
	q.To = now.UnixMilli()
	q.From = q.To - q.Window
	if v.Get("from") != "" || v.Get("to") != "" {
		q.From, err = strconv.ParseInt(v.Get("from"), 10, 64)
		if err != nil {
			return q, errors.New("invalid range start")
		}
		q.To, err = strconv.ParseInt(v.Get("to"), 10, 64)
		// SQLite timestamps are signed nanoseconds; validate before converting.
		if err != nil || q.From < 0 || q.To <= q.From || q.To > 9223372036854 {
			return q, errors.New("invalid range end")
		}
		q.Window = 0
	}
	q.Text = strings.TrimSpace(v.Get("q"))
	q.Service = v.Get("service")
	q.Operation = v.Get("operation")
	q.TraceID = v.Get("exact_trace")
	q.Errors = v.Get("errors")
	if q.Errors == "" && v.Get("failed_only") != "" {
		q.Errors = "with"
	}
	if q.Errors != "" && q.Errors != "all" && q.Errors != "with" && q.Errors != "without" {
		return q, errors.New("invalid error filter")
	}
	for key, target := range map[string]*int64{"min_dur": &q.MinDuration, "max_dur": &q.MaxDuration} {
		if s := v.Get(key); s != "" {
			*target, err = strconv.ParseInt(s, 10, 64)
			if err != nil || *target < 0 {
				return q, errors.New("invalid trace duration")
			}
		}
	}
	if q.MaxDuration > 0 && q.MaxDuration < q.MinDuration {
		return q, errors.New("maximum trace duration precedes minimum")
	}
	if s := v.Get("sort"); s != "" {
		if s != "latest" && s != "slowest" && s != "errors" {
			return q, errors.New("invalid sort")
		}
		q.Sort = s
	}
	for _, item := range v["attr"] {
		key, value, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return q, errors.New("attributes must be key=value")
		}
		q.Attributes[key] = value
	}
	if len(raw) > 16384 || len(q.Attributes) > 16 {
		return q, errors.New("query is too large")
	}
	return q, nil
}

type sourceStamp struct {
	Epoch  string
	MaxSeq int64
}
type sourceHealth struct {
	Kind           string `json:"kind"`
	Notice         string `json:"notice,omitempty"`
	LastSuccess    int64  `json:"last_success_ms"`
	Error          string `json:"error"`
	RetentionKnown bool   `json:"retention_known"`
}
type sourceMetadata struct {
	Schema     int          `json:"schema_version"`
	Source     string       `json:"source"`
	Health     sourceHealth `json:"health"`
	Services   []string     `json:"services"`
	Operations []string     `json:"operations"`
	Attributes []string     `json:"attributes"`
	Historical bool         `json:"historical"`
}
type querySource interface {
	Stamp(context.Context) (sourceStamp, error)
	Visit(context.Context, searchQuery, sourceStamp, func([]phos.Snapshot) error) (bool, error)
	Trace(context.Context, string, sourceStamp) ([]phos.Snapshot, error)
	Health() sourceHealth
	Close() error
}

type traceSummary struct {
	TraceID    string  `json:"trace_id"`
	Name       string  `json:"name"`
	Service    string  `json:"service"`
	Operation  string  `json:"operation"`
	Start      float64 `json:"start_ms"`
	End        float64 `json:"end_ms"`
	Duration   float64 `json:"duration_ms"`
	Spans      int     `json:"span_count"`
	Events     int     `json:"event_count"`
	Errors     int     `json:"error_count"`
	RootStatus string  `json:"root_status"`
	Incomplete bool    `json:"incomplete"`
}
type timeBucket struct {
	From            int64                 `json:"from_ms"`
	To              int64                 `json:"to_ms"`
	Spans           int                   `json:"spans"`
	Errors          int                   `json:"errors"`
	DurationCounts  [durationBinCount]int `json:"duration_counts"`
	UnknownDuration int                   `json:"unknown_duration"`
	Stats           *bucketStats          `json:"stats,omitempty"`
}
type bucketStats struct {
	Services   searchFacet `json:"services"`
	Operations searchFacet `json:"operations"`
	Samples    int64       `json:"samples"`
	P50        float64     `json:"p50_ms"`
	P95        float64     `json:"p95_ms"`
	P99        float64     `json:"p99_ms"`
}
type latencyBucket struct {
	To    float64 `json:"to_ms"`
	Count int64   `json:"count"`
}
type facetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}
type searchFacet struct {
	Values []facetValue `json:"values"`
	Other  int          `json:"other"`
}
type searchAggregates struct {
	BucketVersion   int                       `json:"bucket_version"`
	BucketWidth     int64                     `json:"bucket_width_ms"`
	DurationBounds  [durationBinCount]float64 `json:"duration_bounds_ms"`
	HeatmapMax      int                       `json:"heatmap_max"`
	UnknownDuration int                       `json:"unknown_duration"`
	Services        searchFacet               `json:"services"`
	Operations      searchFacet               `json:"operations"`
	Traces          int                       `json:"traces"`
	Spans           int                       `json:"spans"`
	Errors          int                       `json:"errors"`
	ErrorTraces     int                       `json:"error_traces"`
	P50             float64                   `json:"p50_ms"`
	P95             float64                   `json:"p95_ms"`
	Buckets         []timeBucket              `json:"buckets"`
	Latency         []latencyBucket           `json:"latency"`
}
type searchCoverage struct {
	From          int64  `json:"from_ms"`
	To            int64  `json:"to_ms"`
	Rolling       bool   `json:"rolling"`
	Complete      bool   `json:"complete"`
	Reason        string `json:"reason"`
	Scanned       int    `json:"scanned_traces"`
	Running       bool   `json:"running"`
	SourceLimited bool   `json:"source_limited"`
}
type searchFrame struct {
	Schema      int              `json:"schema_version"`
	Source      string           `json:"source"`
	Epoch       string           `json:"epoch"`
	Query       string           `json:"query"`
	Kind        string           `json:"kind"`
	Base        uint64           `json:"base_seq"`
	Seq         uint64           `json:"seq"`
	Now         int64            `json:"now_ms"`
	Summaries   []traceSummary   `json:"summaries"`
	Removed     []string         `json:"removed"`
	Order       []string         `json:"order"`
	Aggregates  searchAggregates `json:"aggregates"`
	BucketDelta bool             `json:"bucket_delta"`
	Coverage    searchCoverage   `json:"coverage"`
	Health      sourceHealth     `json:"health"`
	Cursor      string           `json:"cursor"`
	Page        int              `json:"page"`
	Pages       int              `json:"pages"`
}
type traceChunk struct {
	Schema   int          `json:"schema_version"`
	Source   string       `json:"source"`
	Epoch    string       `json:"epoch"`
	TraceID  string       `json:"trace_id"`
	Revision string       `json:"revision"`
	Summary  traceSummary `json:"summary"`
	Spans    []wireSpan   `json:"spans"`
	Cursor   string       `json:"cursor"`
	Complete bool         `json:"complete"`
	Reason   string       `json:"reason"`
}
