package dashboard

import (
	"bytes"
	"compress/gzip"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/johan-st/phos"
)

const feedMaxSpans = 25000
const feedCompressedBytes = 256 * 1024

type wireAttr struct {
	Key   string     `json:"key"`
	Kind  string     `json:"kind"`
	Value string     `json:"value"`
	Group []wireAttr `json:"group"`
}
type wireEvent struct {
	Name  string     `json:"name"`
	Time  float64    `json:"time_ms"`
	Attrs []wireAttr `json:"attrs"`
}
type wireError struct {
	Message string     `json:"message"`
	Attrs   []wireAttr `json:"attrs"`
}
type wireLink struct {
	TraceID string     `json:"trace_id"`
	SpanID  string     `json:"span_id"`
	Attrs   []wireAttr `json:"attrs"`
}
type wireSpan struct {
	ID        string      `json:"id"`
	TraceID   string      `json:"trace_id"`
	ParentID  string      `json:"parent_id"`
	Name      string      `json:"name"`
	Start     float64     `json:"start_ms"`
	End       float64     `json:"end_ms"`
	StartTime string      `json:"start_time"`
	EndTime   string      `json:"end_time"`
	Kind      int         `json:"kind"`
	Failed    bool        `json:"failed"`
	Attrs     []wireAttr  `json:"attrs"`
	Events    []wireEvent `json:"events"`
	Errors    []wireError `json:"errors"`
	Links     []wireLink  `json:"links"`
}

func toWireAttrs(attrs []slog.Attr) []wireAttr {
	out := make([]wireAttr, 0, len(attrs))
	for _, a := range attrs {
		v := a.Value.Resolve()
		w := wireAttr{Key: a.Key, Kind: strings.ToLower(v.Kind().String()), Value: v.String(), Group: []wireAttr{}}
		switch v.Kind() {
		case slog.KindInt64:
			w.Value = strconv.FormatInt(v.Int64(), 10)
		case slog.KindUint64:
			w.Value = strconv.FormatUint(v.Uint64(), 10)
		case slog.KindDuration:
			w.Value = strconv.FormatInt(int64(v.Duration()), 10)
		case slog.KindTime:
			w.Value = v.Time().Format(time.RFC3339Nano)
		case slog.KindGroup:
			w.Value = ""
			w.Group = toWireAttrs(v.Group())
		case slog.KindAny:
			w.Kind = "string"
		case slog.KindFloat64:
			if math.IsNaN(v.Float64()) || math.IsInf(v.Float64(), 0) {
				w.Kind = "string"
			}
		}
		out = append(out, w)
	}
	return out
}
func wireMillis(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixMilli()) + float64(t.Nanosecond()%1000000)/1e6
}
func toWireSpan(s phos.Snapshot) wireSpan {
	trace := s.TraceID
	if trace == "" {
		trace = s.ID
	}
	w := wireSpan{ID: s.ID, TraceID: trace, ParentID: s.ParentID, Name: s.Name, Start: wireMillis(s.TimeStart), End: wireMillis(s.TimeEnd), StartTime: s.TimeStart.Format(time.RFC3339Nano), EndTime: s.TimeEnd.Format(time.RFC3339Nano), Kind: int(s.Kind), Failed: feedFailed(s), Attrs: toWireAttrs(s.Attrs), Events: []wireEvent{}, Errors: []wireError{}, Links: []wireLink{}}
	for _, e := range s.Events {
		w.Events = append(w.Events, wireEvent{e.Name, wireMillis(feedEventTime(e.Time, s.TimeEnd)), toWireAttrs(e.Attrs)})
	}
	for _, e := range s.Errors {
		w.Errors = append(w.Errors, wireError{feedError(e.Err), toWireAttrs(e.Attrs)})
	}
	for _, l := range s.Links {
		w.Links = append(w.Links, wireLink{l.TraceID, l.SpanID, toWireAttrs(l.Attrs)})
	}
	return w
}

func gzipFeed(body []byte) []byte {
	var out bytes.Buffer
	w, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	_, _ = w.Write(body)
	_ = w.Close()
	return out.Bytes()
}
