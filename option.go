package phos

import (
	"encoding/json"
	"log/slog"
)

type SpanKind int

const (
	Internal SpanKind = iota
	Server
	Client
	Producer
	Consumer
)

func (k SpanKind) String() string {
	switch k {
	case Server:
		return "server"
	case Client:
		return "client"
	case Producer:
		return "producer"
	case Consumer:
		return "consumer"
	default:
		return "internal"
	}
}

func (k SpanKind) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.String())
}

type link struct {
	traceID string
	spanID  string
	attrs   []slog.Attr
}

type spanConfig struct {
	attrs []slog.Attr
	kind  SpanKind
	links []link
}

type SpanOption func(*spanConfig)

func WithAttrs(attrs ...slog.Attr) SpanOption {
	cloned := cloneAttrs(attrs)
	return func(cfg *spanConfig) {
		cfg.attrs = append(cfg.attrs, cloned...)
	}
}

func WithKind(kind SpanKind) SpanOption {
	return func(cfg *spanConfig) {
		cfg.kind = kind
	}
}

func WithLink(traceID, spanID string, attrs ...slog.Attr) SpanOption {
	cloned := cloneAttrs(attrs)
	return func(cfg *spanConfig) {
		cfg.links = append(cfg.links, link{
			traceID: traceID,
			spanID:  spanID,
			attrs:   cloned,
		})
	}
}

func newSpanConfig(opts []SpanOption) spanConfig {
	cfg := spanConfig{kind: Internal}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

func cloneLinks(links []link) []link {
	if len(links) == 0 {
		return nil
	}
	cloned := make([]link, len(links))
	copy(cloned, links)
	for i := range cloned {
		cloned[i].attrs = cloneAttrs(cloned[i].attrs)
	}
	return cloned
}

func snapshotLinks(links []link) []SnapshotLink {
	if len(links) == 0 {
		return nil
	}
	out := make([]SnapshotLink, len(links))
	for i, existing := range links {
		out[i] = SnapshotLink{
			TraceID: existing.traceID,
			SpanID:  existing.spanID,
			Attrs:   cloneAttrs(existing.attrs),
		}
	}
	return out
}

func cloneSnapshotLinks(links []SnapshotLink) []SnapshotLink {
	if len(links) == 0 {
		return nil
	}
	cloned := make([]SnapshotLink, len(links))
	copy(cloned, links)
	for i := range cloned {
		cloned[i].Attrs = cloneAttrs(cloned[i].Attrs)
	}
	return cloned
}
