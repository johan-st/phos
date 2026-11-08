package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/johan-st/phos"
)

type jaegerQuerySource struct {
	mu         sync.Mutex
	base       *url.URL
	client     *http.Client
	epoch      string
	health     sourceHealth
	services   []string
	servicesAt time.Time
}

func newJaegerQuerySource(raw string) (*jaegerQuerySource, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid Jaeger URL")
	}
	// Accept the existing configured /api/traces URL, but never carry its
	// hidden operation/limit predicates into the dashboard's own query contract.
	u.Path = strings.TrimSuffix(strings.Split(u.Path, "/api/")[0], "/")
	u.RawQuery = ""
	u.Fragment = ""
	return &jaegerQuerySource{base: u, client: &http.Client{Timeout: 5 * time.Second}, epoch: uuid.NewString(), health: sourceHealth{Kind: "jaeger"}}, nil
}
func (s *jaegerQuerySource) Close() error         { s.client.CloseIdleConnections(); return nil }
func (s *jaegerQuerySource) Health() sourceHealth { s.mu.Lock(); defer s.mu.Unlock(); return s.health }
func (s *jaegerQuerySource) record(e error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e != nil {
		s.health.Error = e.Error()
	} else {
		s.health.Error = ""
		s.health.LastSuccess = time.Now().UnixMilli()
	}
}
func (s *jaegerQuerySource) Stamp(context.Context) (sourceStamp, error) {
	return sourceStamp{Epoch: s.epoch}, nil
}
func (s *jaegerQuerySource) get(ctx context.Context, path string, params url.Values, consume func(*json.Decoder) error) (err error) {
	defer func() { s.record(err) }()
	u := *s.base
	u.Path += path
	u.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("upstream Jaeger returned HTTP %d", response.StatusCode)
	}
	reader := &io.LimitedReader{R: response.Body, N: searchMaxTraceBytes + 1}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	err = consume(decoder)
	if reader.N <= 0 {
		return errSourceLimit
	}
	return err
}
func (s *jaegerQuerySource) Services(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	if time.Since(s.servicesAt) < time.Minute {
		out := append([]string{}, s.services...)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	var body struct {
		Services []string `json:"services"`
	}
	err := s.get(ctx, "/api/v3/services", nil, func(d *json.Decoder) error { return d.Decode(&body) })
	if len(body.Services) > 1000 {
		body.Services = body.Services[:1000]
	}
	if err == nil {
		s.mu.Lock()
		s.services = body.Services
		s.servicesAt = time.Now()
		s.mu.Unlock()
	}
	return body.Services, err
}
func (s *jaegerQuerySource) Visit(ctx context.Context, q searchQuery, w sourceStamp, visit func([]phos.Snapshot) error) (bool, error) {
	p := url.Values{"query.start_time_min": {time.UnixMilli(q.From).UTC().Format(time.RFC3339Nano)}, "query.start_time_max": {time.UnixMilli(q.To).UTC().Format(time.RFC3339Nano)}, "query.search_depth": {"200"}}
	if q.Service != "" {
		p.Set("query.service_name", q.Service)
	}
	if q.Operation != "" {
		p.Set("query.operation_name", q.Operation)
	}
	if len(q.Attributes) > 0 {
		b, _ := json.Marshal(q.Attributes)
		p.Set("query.attributes", string(b))
	}
	if q.TraceID != "" {
		spans, err := s.Trace(ctx, q.TraceID, w)
		if err != nil {
			return true, err
		}
		return true, visit(spans)
	}
	spans, err := s.read(ctx, "/api/v3/traces", p)
	if err != nil {
		return true, err
	}
	for i, g := range feedGroups(spans) {
		if i >= 200 {
			return true, errSourceLimit
		}
		if err = ctx.Err(); err != nil {
			return true, err
		}
		if err = visit(g.Spans); err != nil {
			return true, err
		}
	}
	// FindTraces has no guaranteed ordering/exhaustive pagination. A short
	// response cannot establish that every retained matching trace was searched.
	return true, nil
}
func (s *jaegerQuerySource) Trace(ctx context.Context, id string, _ sourceStamp) ([]phos.Snapshot, error) {
	if _, err := hex.DecodeString(id); err != nil {
		return nil, errors.New("invalid Jaeger trace ID")
	}
	return s.read(ctx, "/api/v3/traces/"+id, nil)
}

type otelValue struct {
	String *string         `json:"stringValue"`
	Bool   *bool           `json:"boolValue"`
	Int    json.RawMessage `json:"intValue"`
	Double *float64        `json:"doubleValue"`
	Bytes  *string         `json:"bytesValue"`
	Array  *struct {
		Values []otelValue `json:"values"`
	} `json:"arrayValue"`
	Map *struct {
		Values []otelAttr `json:"values"`
	} `json:"kvlistValue"`
}
type otelAttr struct {
	Key   string    `json:"key"`
	Value otelValue `json:"value"`
}

func (v otelValue) value() slog.Value {
	switch {
	case v.String != nil:
		return slog.StringValue(*v.String)
	case v.Bool != nil:
		return slog.BoolValue(*v.Bool)
	case len(v.Int) > 0:
		n, e := strconv.ParseInt(strings.Trim(string(v.Int), "\""), 10, 64)
		if e == nil {
			return slog.Int64Value(n)
		}
		return slog.StringValue(strings.Trim(string(v.Int), "\""))
	case v.Double != nil:
		return slog.Float64Value(*v.Double)
	case v.Bytes != nil:
		return slog.StringValue(*v.Bytes)
	case v.Map != nil:
		return slog.GroupValue(otelAttrs(v.Map.Values)...)
	case v.Array != nil:
		items := make([]slog.Attr, len(v.Array.Values))
		for i, item := range v.Array.Values {
			items[i] = slog.Attr{Key: strconv.Itoa(i), Value: item.value()}
		}
		return slog.GroupValue(items...)
	default:
		return slog.StringValue("")
	}
}
func otelAttrs(in []otelAttr) []slog.Attr {
	out := make([]slog.Attr, len(in))
	for i, a := range in {
		out[i] = slog.Attr{Key: a.Key, Value: a.Value.value()}
	}
	return out
}

type otelSpan struct {
	TraceID string          `json:"traceId"`
	ID      string          `json:"spanId"`
	Parent  string          `json:"parentSpanId"`
	Name    string          `json:"name"`
	Start   string          `json:"startTimeUnixNano"`
	End     string          `json:"endTimeUnixNano"`
	Kind    json.RawMessage `json:"kind"`
	Attrs   []otelAttr      `json:"attributes"`
	Status  struct {
		Code    json.RawMessage `json:"code"`
		Message string          `json:"message"`
	} `json:"status"`
	Events []struct {
		Name  string     `json:"name"`
		Time  string     `json:"timeUnixNano"`
		Attrs []otelAttr `json:"attributes"`
	} `json:"events"`
	Links []struct {
		Trace string     `json:"traceId"`
		Span  string     `json:"spanId"`
		Attrs []otelAttr `json:"attributes"`
	} `json:"links"`
}
type otelData struct {
	Resources []struct {
		Resource struct {
			Attrs []otelAttr `json:"attributes"`
		} `json:"resource"`
		Scopes []struct {
			Spans []otelSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

func otelID(raw string) string {
	if raw == "" || strings.Trim(raw, "0") == "" {
		return ""
	}
	if _, e := hex.DecodeString(raw); e == nil {
		return strings.ToLower(raw)
	}
	if b, e := base64.StdEncoding.DecodeString(raw); e == nil {
		return hex.EncodeToString(b)
	}
	return raw
}
func otelTime(raw string) time.Time {
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
func otelKind(raw json.RawMessage) phos.SpanKind {
	switch strings.Trim(string(raw), "\"") {
	case "2", "SPAN_KIND_SERVER":
		return phos.Server
	case "3", "SPAN_KIND_CLIENT":
		return phos.Client
	case "4", "SPAN_KIND_PRODUCER":
		return phos.Producer
	case "5", "SPAN_KIND_CONSUMER":
		return phos.Consumer
	default:
		return phos.Internal
	}
}
func (s *jaegerQuerySource) read(ctx context.Context, path string, params url.Values) (out []phos.Snapshot, err error) {
	err = s.get(ctx, path, params, func(d *json.Decoder) error {
		for {
			var body struct {
				Result *otelData `json:"result"`
				otelData
				Error json.RawMessage `json:"error"`
			}
			e := d.Decode(&body)
			if e == io.EOF {
				return nil
			}
			if e != nil {
				return e
			}
			if len(body.Error) > 0 {
				return errors.New("upstream Jaeger query failed")
			}
			data := body.otelData
			if body.Result != nil {
				data = *body.Result
			}
			for _, r := range data.Resources {
				for _, scope := range r.Scopes {
					for _, p := range scope.Spans {
						if len(out) >= feedMaxSpans {
							return errSourceLimit
						}
						attrs := append(otelAttrs(r.Resource.Attrs), otelAttrs(p.Attrs)...)
						status := strings.Trim(string(p.Status.Code), "\"")
						failed := status == "2" || status == "STATUS_CODE_ERROR"
						for _, a := range attrs {
							if a.Key == "error" && a.Value.Kind() == slog.KindBool && a.Value.Bool() {
								failed = true
							}
						}
						span := phos.Snapshot{ID: otelID(p.ID), TraceID: otelID(p.TraceID), ParentID: otelID(p.Parent), Name: p.Name, TimeStart: otelTime(p.Start), TimeEnd: otelTime(p.End), Kind: otelKind(p.Kind), Failed: failed, Attrs: attrs}
						if status != "" && status != "0" && status != "STATUS_CODE_UNSET" {
							label := "OK"
							if failed {
								label = "ERROR"
							}
							span.Attrs = append(span.Attrs, slog.String("otel.status_code", label))
						}
						svc, _ := spanNames(span)
						if svc != "" {
							span.Name = svc + ":" + p.Name
						}
						span.Attrs = append(span.Attrs, slog.String("operation", p.Name))
						for _, e := range p.Events {
							span.Events = append(span.Events, phos.SnapshotEvent{Name: e.Name, Time: otelTime(e.Time), Attrs: otelAttrs(e.Attrs)})
						}
						for _, l := range p.Links {
							span.Links = append(span.Links, phos.SnapshotLink{TraceID: otelID(l.Trace), SpanID: otelID(l.Span), Attrs: otelAttrs(l.Attrs)})
						}
						if p.Status.Message != "" && failed {
							span.Errors = append(span.Errors, phos.SnapshotError{Err: errors.New(p.Status.Message)})
						}
						out = append(out, span)
					}
				}
			}
		}
	})
	return out, err
}
