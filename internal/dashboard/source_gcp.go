package dashboard

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/johan-st/phos"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const gcpRequestTimeout = 10 * time.Second

// Cloud Trace is an ingestion source. Dashboard queries only read the archive.
type cloudTraceSource struct {
	project, filter, quotaProject string
	client                        *http.Client
}

func validateGCPProject(project string) error {
	if project == "" {
		return errors.New("GCP project is required")
	}
	for _, c := range project {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:", c)) {
			return errors.New("invalid GCP project")
		}
	}
	return nil
}

func newCloudTraceSource(ctx context.Context, project, filter string) (*cloudTraceSource, error) {
	if err := validateGCPProject(project); err != nil {
		return nil, err
	}
	// Credential refresh uses its own HTTP client, so bound it as well as API reads.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: gcpRequestTimeout})
	credentials, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/trace.readonly")
	if err != nil {
		return nil, fmt.Errorf("authenticate Cloud Trace: configure Application Default Credentials with gcloud auth application-default login: %w", err)
	}
	var settings struct {
		QuotaProject string `json:"quota_project_id"`
	}
	if len(credentials.JSON) > 0 {
		if err := json.Unmarshal(credentials.JSON, &settings); err != nil {
			return nil, errors.New("invalid Google Application Default Credentials JSON")
		}
	}
	if override := os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT"); override != "" {
		settings.QuotaProject = override
	}
	client := oauth2.NewClient(ctx, credentials.TokenSource)
	// NewClient inherits the credential client's timeout. Use request contexts for
	// API reads to avoid net/http calling OAuth2's deprecated CancelRequest.
	client.Timeout = 0
	return &cloudTraceSource{project: project, filter: filter, quotaProject: settings.QuotaProject, client: client}, nil
}

func (s *cloudTraceSource) Close()           { s.client.CloseIdleConnections() }
func (s *cloudTraceSource) identity() string { return originID(s.project + "\n" + s.filter) }
func (s *cloudTraceSource) origin() string   { return "gcp:" + originID(s.project) }

type cloudTrace struct {
	Project string           `json:"projectId"`
	ID      string           `json:"traceId"`
	Spans   []cloudTraceSpan `json:"spans"`
}

type cloudTraceSpan struct {
	ID     string            `json:"spanId"`
	Parent string            `json:"parentSpanId"`
	Name   string            `json:"name"`
	Kind   string            `json:"kind"`
	Start  string            `json:"startTime"`
	End    string            `json:"endTime"`
	Labels map[string]string `json:"labels"`
}

type cloudTraceHTTPError struct{ code int }

func (e cloudTraceHTTPError) Error() string {
	switch e.code {
	case http.StatusUnauthorized:
		return "Cloud Trace HTTP 401: refresh Application Default Credentials"
	case http.StatusForbidden:
		return "Cloud Trace HTTP 403: check trace-read permissions, Cloud Trace API enablement, and quota project access"
	default:
		return fmt.Sprintf("upstream Cloud Trace returned HTTP %d", e.code)
	}
}

func (s *cloudTraceSource) page(ctx context.Context, from, to time.Time, token string) ([]phos.Snapshot, string, error) {
	var spans []phos.Snapshot
	var next string
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		spans, next, err = s.readPage(ctx, from, to, token)
		if err == nil || ctx.Err() != nil {
			break
		}
		var status cloudTraceHTTPError
		if errors.As(err, &status) && status.code != http.StatusTooManyRequests && status.code < 500 {
			break
		}
		// Decoder/validation errors are permanent; only HTTP/transport failures retry.
		var transport *url.Error
		if !errors.As(err, &status) && !errors.As(err, &transport) {
			break
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, "", ctx.Err()
		case <-timer.C:
		}
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	return spans, next, err
}

func (s *cloudTraceSource) readPage(ctx context.Context, from, to time.Time, token string) ([]phos.Snapshot, string, error) {
	ctx, cancel := context.WithTimeout(ctx, gcpRequestTimeout)
	defer cancel()

	params := url.Values{
		"view": {"COMPLETE"}, "pageSize": {"100"}, "orderBy": {"start desc"},
		"startTime": {from.UTC().Format(time.RFC3339Nano)},
		// Cloud Trace's end is inclusive; our import interval is [from,to).
		"endTime": {to.Add(-time.Nanosecond).UTC().Format(time.RFC3339Nano)},
	}
	if s.filter != "" {
		params.Set("filter", s.filter)
	}
	if token != "" {
		params.Set("pageToken", token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudtrace.googleapis.com/v1/projects/"+url.PathEscape(s.project)+"/traces?"+params.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	if s.quotaProject != "" {
		req.Header.Set("X-Goog-User-Project", s.quotaProject)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", cloudTraceHTTPError{resp.StatusCode}
	}
	// Refuse a partial page rather than advancing past records we did not decode.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, searchMaxTraceBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) > searchMaxTraceBytes {
		return nil, "", errSourceLimit
	}
	var body struct {
		Traces []cloudTrace `json:"traces"`
		Next   string       `json:"nextPageToken"`
	}
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, "", fmt.Errorf("decode Cloud Trace page: %w", err)
	}
	if body.Next != "" && body.Next == token {
		return nil, "", errors.New("upstream Cloud Trace returned a repeated page token")
	}
	spans := []phos.Snapshot{}
	for _, trace := range body.Traces {
		if trace.Project != "" && trace.Project != s.project {
			return nil, "", errors.New("upstream Cloud Trace response belongs to another project")
		}
		trace.Project = s.project
		decoded, err := decodeCloudTrace(trace)
		if err != nil {
			return nil, "", err
		}
		spans = append(spans, decoded...)
		if len(spans) > feedMaxSpans {
			return nil, "", errSourceLimit
		}
	}
	return spans, body.Next, nil
}

func cloudSpanID(raw string, parent bool) (string, error) {
	if parent && (raw == "" || raw == "0") {
		return "", nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return "", errors.New("invalid Cloud Trace span ID")
	}
	return fmt.Sprintf("%016x", n), nil
}

func decodeCloudTrace(trace cloudTrace) ([]phos.Snapshot, error) {
	b, err := hex.DecodeString(trace.ID)
	if err != nil || len(b) != 16 || strings.Trim(trace.ID, "0") == "" {
		return nil, errors.New("invalid Cloud Trace trace ID")
	}
	spans := make([]phos.Snapshot, 0, len(trace.Spans))
	for _, p := range trace.Spans {
		id, err := cloudSpanID(p.ID, false)
		if err != nil {
			return nil, err
		}
		parent, err := cloudSpanID(p.Parent, true)
		if err != nil {
			return nil, err
		}
		start, err := time.Parse(time.RFC3339Nano, p.Start)
		if err != nil || start.Before(time.Unix(0, 0)) || start.After(time.Unix(0, math.MaxInt64)) {
			return nil, errors.New("invalid Cloud Trace span start time")
		}
		end, err := time.Parse(time.RFC3339Nano, p.End)
		if err != nil || end.Before(start) || end.After(time.Unix(0, math.MaxInt64)) {
			return nil, errors.New("invalid Cloud Trace span end time")
		}
		kind := phos.Internal
		if p.Kind == "RPC_SERVER" {
			kind = phos.Server
		}
		if p.Kind == "RPC_CLIENT" {
			kind = phos.Client
		}
		span := phos.Snapshot{ID: id, TraceID: strings.ToLower(trace.ID), ParentID: parent, Name: p.Name, TimeStart: start.UTC(), TimeEnd: end.UTC(), Kind: kind}
		// Sorted labels keep unchanged observations byte-identical across polls.
		keys := make([]string, 0, len(p.Labels))
		for key := range p.Labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			span.Attrs = append(span.Attrs, slog.String(key, p.Labels[key]))
		}
		// Preserve canonical Google labels and expose their standard dashboard aliases.
		for _, alias := range [][2]string{{"/http/method", "http.method"}, {"/http/url", "http.url"}, {"/http/route", "http.route"}} {
			if value := p.Labels[alias[0]]; value != "" && p.Labels[alias[1]] == "" {
				span.Attrs = append(span.Attrs, slog.String(alias[1], value))
			}
		}
		if code, err := strconv.Atoi(p.Labels["/http/status_code"]); err == nil && code >= 100 && code <= 599 && p.Labels["http.status_code"] == "" && p.Labels["http.response.status_code"] == "" {
			span.Attrs = append(span.Attrs, slog.Int("http.status_code", code))
		}
		service := p.Labels["service.name"]
		if service == "" {
			service = p.Labels["/service/name"]
		}
		if service == "" {
			service = p.Labels["g.co/gae/app/module"]
		}
		if service != "" {
			span.Name = service + ":" + p.Name
			if p.Labels["service.name"] == "" {
				span.Attrs = append(span.Attrs, slog.String("service.name", service))
			}
		}
		span.Attrs = append(span.Attrs, slog.String("operation", p.Name), slog.String("gcp.project_id", trace.Project))
		span.Failed = strings.EqualFold(p.Labels["error"], "true") || strings.EqualFold(p.Labels["otel.status_code"], "ERROR") || p.Labels["/error/message"] != "" || p.Labels["/error/name"] != ""
		if message := p.Labels["/error/message"]; message != "" {
			span.Errors = append(span.Errors, phos.SnapshotError{Err: errors.New(message)})
		}
		spans = append(spans, span)
	}
	return spans, nil
}
