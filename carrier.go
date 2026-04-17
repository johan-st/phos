package phos

import (
	"net/http"
	"sort"
	"strings"
)

// https://www.w3.org/TR/trace-context

type Carrier interface {
	Get(key string) string
	Set(key string, value string)
	Keys() []string
}

type HTTPHeaderCarrier struct {
	Header http.Header
}

func (c HTTPHeaderCarrier) Get(key string) string {
	if c.Header == nil {
		return ""
	}
	if strings.EqualFold(key, TraceStateHeader) {
		values := c.Header[textprotoCanonicalMIMEHeaderKey(key)]
		if len(values) == 0 {
			return ""
		}
		if len(values) == 1 {
			return values[0]
		}
		return joinComma(values)
	}
	return c.Header.Get(key)
}

func (c HTTPHeaderCarrier) Set(key string, value string) {
	if c.Header == nil {
		return
	}
	c.Header.Set(key, value)
}

func (c HTTPHeaderCarrier) Keys() []string {
	if c.Header == nil {
		return nil
	}
	keys := make([]string, 0, len(c.Header))
	for k := range c.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type MapCarrier map[string]string

func (c MapCarrier) Get(key string) string {
	if v, ok := c[key]; ok {
		return v
	}
	switch key {
	case TraceParentHeader:
		if v, ok := c["TraceParent"]; ok {
			return v
		}
	case TraceStateHeader:
		if v, ok := c["TraceState"]; ok {
			return v
		}
	}
	for existingKey, existingValue := range c {
		if strings.EqualFold(existingKey, key) {
			return existingValue
		}
	}
	return ""
}

func (c MapCarrier) Set(key string, value string) {
	c[key] = value
}

func (c MapCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func joinComma(values []string) string {
	if len(values) == 0 {
		return ""
	}
	total := len(values) - 1
	for _, value := range values {
		total += len(value)
	}

	var b strings.Builder
	b.Grow(total)
	for i, value := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(value)
	}
	return b.String()
}

func textprotoCanonicalMIMEHeaderKey(key string) string {
	switch {
	case strings.EqualFold(key, TraceParentHeader):
		return "Traceparent"
	case strings.EqualFold(key, TraceStateHeader):
		return "Tracestate"
	default:
		return http.CanonicalHeaderKey(key)
	}
}
