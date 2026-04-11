package phos

import (
	"net/http"
	"sort"
	"strings"
)

// https://www.w3.org/TR/trace-context

type Carrier interface {
	Get(key string) (string, bool)
	Set(key string, value string)
	Keys() []string
}

type HTTPHeaderCarrier struct {
	Header http.Header
}

func (c HTTPHeaderCarrier) Get(key string) (string, bool) {
	if c.Header == nil {
		return "", false
	}
	if strings.EqualFold(key, TraceStateHeader) {
		values := c.Header[textprotoCanonicalMIMEHeaderKey(key)]
		if len(values) == 0 {
			return "", false
		}
		if len(values) == 1 {
			return values[0], true
		}
		return joinComma(values), true
	}
	v := c.Header.Get(key)
	return v, v != ""
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

func (c MapCarrier) Get(key string) (string, bool) {
	v, ok := c[key]
	if ok {
		return v, true
	}
	switch key {
	case TraceParentHeader:
		v, ok = c["TraceParent"]
		if ok {
			return v, true
		}
	case TraceStateHeader:
		v, ok = c["TraceState"]
		if ok {
			return v, true
		}
	}
	for existingKey, existingValue := range c {
		if strings.EqualFold(existingKey, key) {
			return existingValue, true
		}
	}
	return "", false
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
