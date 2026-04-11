package phos

import (
	"context"
	"fmt"
	"strings"
)

const (
	TraceParentHeader = "traceparent"
	TraceStateHeader  = "tracestate"
)

type TraceParent struct {
	Version string
	TraceID string
	Parent  string
	Flags   string
}

func (t TraceParent) String() string {
	if len(t.Version) != 2 || len(t.TraceID) != 32 || len(t.Parent) != 16 || len(t.Flags) != 2 {
		return strings.Join([]string{t.Version, t.TraceID, t.Parent, t.Flags}, "-")
	}

	var buf [55]byte
	copy(buf[0:2], t.Version)
	buf[2] = '-'
	copy(buf[3:35], t.TraceID)
	buf[35] = '-'
	copy(buf[36:52], t.Parent)
	buf[52] = '-'
	copy(buf[53:55], t.Flags)
	return string(buf[:])
}

func ParseTraceParent(v string) (TraceParent, error) {
	v = strings.TrimSpace(v)
	if len(v) < 55 {
		return TraceParent{}, errInvalidTraceparentLength
	}
	if !isLowerHex(v[:2], 2) {
		return TraceParent{}, errInvalidTraceparentVersion
	}
	if v[2] != '-' {
		return TraceParent{}, errInvalidTraceparentFormat
	}

	t := TraceParent{
		Version: v[:2],
		TraceID: v[3:35],
		Parent:  v[36:52],
		Flags:   v[53:55],
	}
	if t.Version == "ff" {
		return TraceParent{}, errInvalidTraceparentVersion
	}
	if v[35] != '-' || v[52] != '-' {
		return TraceParent{}, errInvalidTraceparentFormat
	}
	if t.Version == "00" && len(v) != 55 {
		return TraceParent{}, errInvalidTraceparentFormat
	}
	if len(v) > 55 && v[55] != '-' {
		return TraceParent{}, errInvalidTraceparentFormat
	}
	if !isLowerHex(t.TraceID, 32) || isAllZeroHex(t.TraceID) {
		return TraceParent{}, errInvalidTraceID
	}
	if !isLowerHex(t.Parent, 16) || isAllZeroHex(t.Parent) {
		return TraceParent{}, errInvalidParentID
	}
	if !isLowerHex(t.Flags, 2) {
		return TraceParent{}, errInvalidTraceFlags
	}
	return t, nil
}

func InjectTraceContext(ctx context.Context, carrier Carrier) {
	if carrier == nil {
		return
	}
	traceCtx := traceContextFromContext(ctx)
	traceID := traceIDFromContext(ctx)
	if traceID == "" {
		traceID = generateTraceID()
	}

	parentID := ""
	if s := spanFromContext(ctx); s != nil && s.spanIDValue() != "" {
		parentID = s.spanIDValue()
	} else {
		parentID = generateSpanID()
	}

	carrier.Set(TraceParentHeader, TraceParent{
		Version: "00",
		TraceID: traceID,
		Parent:  parentID,
		Flags:   outgoingTraceFlags(ctx),
	}.String())
	if traceState := traceStateFromContext(ctx); traceState != "" {
		carrier.Set(TraceStateHeader, traceState)
	} else if traceCtx.traceState != "" {
		carrier.Set(TraceStateHeader, traceCtx.traceState)
	}
}

func ExtractTraceContext(ctx context.Context, carrier Carrier) context.Context {
	if carrier == nil {
		return ctx
	}
	v, ok := carrier.Get(TraceParentHeader)
	if !ok {
		return ctx
	}
	t, err := ParseTraceParent(v)
	if err != nil {
		return context.WithValue(ctx, traceContextKey, traceContext{
			diagnostics: []traceContextDiagnostic{{
				event:  "tracecontext.invalid_traceparent",
				header: TraceParentHeader,
				value:  v,
				reason: err.Error(),
			}},
		})
	}
	traceCtx := traceContext{
		traceID:    t.TraceID,
		parentID:   t.Parent,
		traceFlags: outgoingTraceFlagsForValue(t.Flags),
	}
	if traceState, ok := carrier.Get(TraceStateHeader); ok {
		if err := validateTraceState(traceState); err != nil {
			traceCtx.diagnostics = append(traceCtx.diagnostics, traceContextDiagnostic{
				event:  "tracecontext.invalid_tracestate",
				header: TraceStateHeader,
				value:  traceState,
				reason: err.Error(),
			})
		} else {
			traceCtx.traceState = traceState
		}
	}
	return context.WithValue(ctx, traceContextKey, traceCtx)
}

func traceIDFromContext(ctx context.Context) string {
	if s := spanFromContext(ctx); s != nil {
		return s.traceIDValue()
	}
	if traceCtx := traceContextFromContext(ctx); traceCtx.traceID != "" {
		return traceCtx.traceID
	}
	return ""
}

func traceStateFromContext(ctx context.Context) string {
	if s := spanFromContext(ctx); s != nil {
		return s.traceStateValue()
	}
	return traceContextFromContext(ctx).traceState
}

func outgoingTraceFlags(ctx context.Context) string {
	if s := spanFromContext(ctx); s != nil {
		return outgoingTraceFlagsForValue(s.traceFlagsValue())
	}
	return outgoingTraceFlagsForValue(traceContextFromContext(ctx).traceFlags)
}

func outgoingTraceFlagsForValue(flags string) string {
	if !isLowerHex(flags, 2) {
		return "00"
	}
	if lowerHexNibble(flags[1])&0x01 == 0x01 {
		return "01"
	}
	return "00"
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if isLowerHexChar(s[i]) {
			continue
		}
		return false
	}
	return true
}

func isAllZeroHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

func isLowerHexChar(ch byte) bool {
	return (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')
}

func lowerHexNibble(ch byte) byte {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0'
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10
	default:
		return 0
	}
}

func validateTraceState(v string) error {
	if len(v) == 0 {
		return fmt.Errorf("empty tracestate")
	}
	if len(v) > 512 {
		return fmt.Errorf("tracestate exceeds 512 characters")
	}

	members := strings.Split(v, ",")
	if len(members) > 32 {
		return fmt.Errorf("tracestate exceeds 32 members")
	}

	for _, member := range members {
		member = strings.TrimSpace(member)
		if member == "" {
			return fmt.Errorf("tracestate contains an empty member")
		}

		key, value, ok := strings.Cut(member, "=")
		if !ok {
			return fmt.Errorf("tracestate member missing '='")
		}
		if err := validateTraceStateKey(key); err != nil {
			return err
		}
		if err := validateTraceStateValue(value); err != nil {
			return err
		}
	}

	return nil
}

func validateTraceStateKey(key string) error {
	if key == "" {
		return fmt.Errorf("tracestate key is empty")
	}
	if len(key) > 256 {
		return fmt.Errorf("tracestate key exceeds 256 characters")
	}

	tenant, system, hasTenant := strings.Cut(key, "@")
	if hasTenant {
		if tenant == "" || system == "" {
			return fmt.Errorf("tracestate key has an invalid tenant format")
		}
		if len(tenant) > 241 {
			return fmt.Errorf("tracestate tenant exceeds 241 characters")
		}
		if !isTraceStateToken(tenant) || !isTraceStateToken(system) {
			return fmt.Errorf("tracestate key contains invalid characters")
		}
		return nil
	}

	if !isTraceStateToken(key) {
		return fmt.Errorf("tracestate key contains invalid characters")
	}
	return nil
}

func validateTraceStateValue(value string) error {
	if len(value) == 0 {
		return fmt.Errorf("tracestate value is empty")
	}
	if len(value) > 256 {
		return fmt.Errorf("tracestate value exceeds 256 characters")
	}
	if value[len(value)-1] == ' ' {
		return fmt.Errorf("tracestate value has trailing whitespace")
	}
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if ch < 0x20 || ch > 0x7e {
			return fmt.Errorf("tracestate value contains non-printable characters")
		}
		if ch == ',' || ch == '=' {
			return fmt.Errorf("tracestate value contains an invalid delimiter")
		}
	}
	return nil
}

func isTraceStateToken(value string) bool {
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			continue
		}
		switch ch {
		case '_', '-', '*', '/':
			continue
		default:
			return false
		}
	}
	return true
}

var (
	errInvalidTraceparentLength  = errorString("invalid traceparent length")
	errInvalidTraceparentVersion = errorString("invalid traceparent version")
	errInvalidTraceparentFormat  = errorString("invalid traceparent format")
	errInvalidTraceID            = errorString("invalid trace id")
	errInvalidParentID           = errorString("invalid parent id")
	errInvalidTraceFlags         = errorString("invalid trace flags")
)

type errorString string

func (e errorString) Error() string {
	return string(e)
}
