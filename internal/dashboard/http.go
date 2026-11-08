package dashboard

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// These private envelopes preserve the dashboard query contract while keeping
// the HTTP transport independent of Spore, its agent and NATS.
type requestMessage struct {
	Context context.Context
	ID      string
	Kind    string
	Method  string
	Path    string
	Query   string
	Headers map[string]string
}

type responseMessage struct {
	ID         string
	Kind       string
	Status     int
	Headers    map[string]string
	BodyBase64 string
	Stream     *responseStream
}

type responseStream struct {
	Protocol     string
	ConnectionID string
}

type sseEventMessage struct {
	Kind         string
	ConnectionID string
	Event        string
	DataBase64   string
}

type httpSubscription struct {
	ctx    context.Context
	events chan sseEventMessage
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		target := defaultBasePath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	req := requestMessage{Context: r.Context(), Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Headers: make(map[string]string)}
	for key := range r.Header {
		req.Headers[strings.ToLower(key)] = r.Header.Get(key)
	}
	if r.Method == http.MethodGet && (r.URL.Path == defaultBasePath+"/stream" || r.URL.Path == defaultLivePath) {
		a.serveStream(w, r, req)
		return
	}
	writeHTTPResponse(w, a.handleRequest(req))
}

func writeHTTPResponse(w http.ResponseWriter, response responseMessage) {
	body, err := base64.StdEncoding.DecodeString(response.BodyBase64)
	if err != nil {
		http.Error(w, "invalid dashboard response", http.StatusInternalServerError)
		return
	}
	for key, value := range response.Headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(response.Status)
	_, _ = w.Write(body)
}

func (a *App) serveStream(w http.ResponseWriter, r *http.Request, req requestMessage) {
	_, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	params, err := url.ParseQuery(req.Query)
	if err != nil {
		writeHTTPResponse(w, errorResponse("", http.StatusBadRequest, err.Error()))
		return
	}
	id := params.Get("connection_id")
	if id == "" {
		id = uuid.NewString()
		params.Set("connection_id", id)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	req.Context, req.Query = ctx, params.Encode()
	subscription := &httpSubscription{ctx: ctx, events: make(chan sseEventMessage, 8)}
	a.streamsMu.Lock()
	if a.stopping || len(a.subscriptions) >= 128 || a.subscriptions[id] != nil {
		a.streamsMu.Unlock()
		writeHTTPResponse(w, errorResponse("", http.StatusServiceUnavailable, "dashboard streams unavailable"))
		return
	}
	a.subscriptions[id] = subscription
	a.streamsMu.Unlock()
	defer func() {
		a.streamsMu.Lock()
		delete(a.subscriptions, id)
		a.streamsMu.Unlock()
	}()
	response := a.handleSearchStream(req)
	if response.Stream == nil {
		writeHTTPResponse(w, response)
		return
	}
	a.streamsMu.Lock()
	owner := a.streams[id]
	a.streamsMu.Unlock()
	if owner == nil {
		writeHTTPResponse(w, errorResponse("", http.StatusServiceUnavailable, "dashboard stream closed"))
		return
	}
	defer owner.cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	write := func(event, data string) error {
		// A client that stops reading cannot retain a stream through shutdown.
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write("connection", "{}"); err != nil {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-owner.done:
			return
		case <-keepalive.C:
			if err := write("keepalive", "{}"); err != nil {
				return
			}
		case event := <-subscription.events:
			data, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil || write(event.Event, string(data)) != nil {
				return
			}
		}
	}
}

func (a *App) publishHTTP(value any) error {
	event, ok := value.(sseEventMessage)
	if !ok {
		return fmt.Errorf("unsupported dashboard event %T", value)
	}
	a.streamsMu.Lock()
	subscription := a.subscriptions[event.ConnectionID]
	a.streamsMu.Unlock()
	if subscription == nil {
		return errors.New("dashboard subscription closed")
	}
	select {
	case <-subscription.ctx.Done():
		return subscription.ctx.Err()
	case subscription.events <- event:
		return nil
	default:
		// Reconnection replays the retained checkpoint after a slow client.
		return errors.New("dashboard subscriber is too slow")
	}
}
