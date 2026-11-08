package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/johan-st/phos"
)

const (
	defaultBasePath = "/dashboard"
	defaultLivePath = "/dashboard/live"
)

type Config struct {
	Logger        *slog.Logger
	DemoJaegerURL string
	StoreDB       string
	TraceDB       string
	JaegerURL     string
	GCPProject    string
	GCPFilter     string
	TraceExporter phos.Exporter
}

// App serves the embedded dashboard and owns its queries and live subscriptions.
type App struct {
	logger        *slog.Logger
	traceExporter phos.Exporter
	sendMessage   func(any) error
	streamsMu     sync.Mutex
	streams       map[string]*dashboardStream
	subscriptions map[string]*httpSubscription
	streamsWG     sync.WaitGroup
	stopping      bool
	sourceID      string
	search        *searchService
}

type dashboardStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func New(cfg Config) (*App, error) {
	if cfg.GCPFilter != "" && cfg.GCPProject == "" {
		return nil, errors.New("GCP filter requires GCP project")
	}
	if cfg.GCPProject != "" {
		if cfg.StoreDB == "" || cfg.TraceDB != "" || cfg.DemoJaegerURL != "" || cfg.JaegerURL != "" {
			return nil, errors.New("GCP import requires StoreDB and no other live input")
		}
		if err := validateGCPProject(cfg.GCPProject); err != nil {
			return nil, err
		}
	}
	if cfg.DemoJaegerURL != "" && (cfg.StoreDB == "" || cfg.TraceDB != "" || cfg.JaegerURL != "") {
		return nil, errors.New("live demo import requires StoreDB and no other live input")
	}
	var source querySource
	var err error
	if cfg.StoreDB != "" {
		var store *dashboardStore
		store, err = openDashboardStore(cfg.StoreDB)
		if err == nil && cfg.TraceDB != "" {
			err = store.followNative(cfg.TraceDB)
		}
		if err == nil && cfg.DemoJaegerURL != "" {
			err = store.followDemo(cfg.DemoJaegerURL)
		}
		if err == nil && cfg.GCPProject != "" {
			err = store.followGCP(cfg.GCPProject, cfg.GCPFilter)
		}
		if err == nil && cfg.TraceDB == "" && cfg.DemoJaegerURL == "" && cfg.GCPProject == "" {
			notice := "Dashboard archive; no live input configured"
			if previous := store.Health().Notice; previous != "" {
				notice += "; saved source notice: " + previous
			}
			store.setNotice(notice)
		}
		if err != nil && store != nil {
			_ = store.Close()
		}
		source = store
	} else if strings.TrimSpace(cfg.JaegerURL) != "" {
		source, err = newJaegerQuerySource(cfg.JaegerURL)
	} else {
		source, err = newSQLiteQuerySource(cfg.TraceDB)
	}
	if err != nil {
		return nil, err
	}
	identity := cfg.TraceDB
	if cfg.StoreDB != "" {
		identity = cfg.StoreDB
	} else if cfg.JaegerURL != "" {
		identity = cfg.JaegerURL
	}
	app := &App{
		logger:        defaultLogger(cfg.Logger),
		traceExporter: cfg.TraceExporter,
		streams:       make(map[string]*dashboardStream),
		subscriptions: make(map[string]*httpSubscription),
		sourceID:      fmt.Sprintf("%x", sha256.Sum256([]byte(identity))),
	}
	app.search = newSearchService(source, app.sourceID)
	app.sendMessage = app.publishHTTP
	return app, nil
}

func (a *App) Close() error {
	if a == nil {
		return nil
	}
	a.StopStreams()
	return a.search.close()
}

func (a *App) StopStreams() {
	if a == nil {
		return
	}
	a.streamsMu.Lock()
	a.stopping = true
	for _, stream := range a.streams {
		stream.cancel()
	}
	a.streamsMu.Unlock()
	a.streamsWG.Wait()
}

func (a *App) handleRequest(req requestMessage) responseMessage {
	switch {
	case req.Method == http.MethodGet && (req.Path == defaultBasePath+"/search" || req.Path == defaultBasePath+"/spans"):
		return a.handleSearch(req)
	case req.Method == http.MethodGet && req.Path == defaultBasePath+"/trace":
		return a.handleTrace(req)
	case req.Method == http.MethodGet && req.Path == defaultBasePath+"/metadata":
		return a.handleMetadata(req)
	case req.Method == http.MethodGet && (req.Path == defaultBasePath+"/stream" || req.Path == defaultLivePath):
		return a.handleSearchStream(req)
	case req.Method == http.MethodGet && strings.HasPrefix(req.Path, defaultBasePath+"/assets/"):
		return assetResponse(req)
	case req.Method == http.MethodGet && req.Path == defaultLivePath+"/close":
		a.streamsMu.Lock()
		if stream := a.streams[parseConnectionID(req.Query)]; stream != nil {
			stream.cancel()
		}
		a.streamsMu.Unlock()
		return textResponse(req.ID, http.StatusNoContent, "text/plain", "")
	case req.Method == http.MethodGet && req.Path == defaultBasePath:
		return a.handleDashboard(req)
	default:
		return errorResponse(req.ID, http.StatusNotFound, "dashboard route not found")
	}
}

func (a *App) handleDashboard(req requestMessage) responseMessage {
	_, span := phos.NewSpan(a.traceContext(a.baseTraceContext(req)), "phos-dashboard:http_request",
		phos.WithAttrs(slog.String("http.route", defaultBasePath), slog.String("method", req.Method)))
	defer span.End()
	page, err := a.renderShell(req.Query)
	if err != nil {
		span.Fail(err)
		span.Attrs(slog.Int("status_code", http.StatusBadRequest))
		return errorResponse(req.ID, http.StatusBadRequest, err.Error())
	}
	span.Attrs(slog.Int("status_code", http.StatusOK))
	return textResponse(req.ID, http.StatusOK, "text/html; charset=utf-8", page)
}

func (a *App) traceContext(ctx context.Context) context.Context {
	if a.traceExporter != nil {
		return phos.WithExporter(ctx, a.traceExporter)
	}
	return ctx
}

func defaultLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (a *App) baseTraceContext(req requestMessage) context.Context {
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return phos.ExtractTraceContext(ctx, phos.MapCarrier(req.Headers))
}

func parseConnectionID(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(values.Get("connection_id"))
}

func textResponse(id string, status int, contentType string, body string) responseMessage {
	return responseMessage{
		ID: id, Status: status,
		Headers:    map[string]string{"content-type": contentType},
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)),
	}
}

func errorResponse(id string, status int, message string) responseMessage {
	payload, _ := json.Marshal(map[string]string{"error": message})
	return textResponse(id, status, "application/json", string(payload))
}
