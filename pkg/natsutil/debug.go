// debug.go: opt-in per-request verbose-logging intent, propagated between
// context.Context and nats.Header exactly like X-Request-ID (see request_id.go).
//
// The wire token is the only stable contract. The internal DebugLevel type is a
// private detail: new rungs, or a future comma-list of facets, are additive and
// non-breaking because clients only ever send a token string.
package natsutil

import (
	"context"
	"strings"
)

// DebugHeader carries the requested verbose-logging rung for this request.
const DebugHeader = "X-Debug"

// DebugLevel is the requested verbosity rung. Off is the zero value; the ladder
// is cumulative (each rung includes the ones below it):
//
//	off < flow < debug < trace
//
// flow  — cross-service path + timing breadcrumbs
// debug — + in-service decision branches
// trace — + per-item / per-recipient lines
type DebugLevel int

const (
	DebugOff DebugLevel = iota
	DebugFlow
	DebugBasic
	DebugTrace
)

// ParseDebugLevel maps an inbound header value to a rung (trimmed, case-insensitive).
// It is strict by design: any unrecognized value is DebugOff, so a stray
// "X-Debug: 0" (or typo) can never silently enable verbose logging.
//
//	"" / "0" / "false" / "off" / "no" / unknown → DebugOff
//	"flow"                                       → DebugFlow
//	"1" / "true" / "on" / "debug"                → DebugBasic
//	"trace"                                      → DebugTrace
func ParseDebugLevel(v string) DebugLevel {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "flow":
		return DebugFlow
	case "debug", "1", "true", "on":
		return DebugBasic
	case "trace":
		return DebugTrace
	default:
		return DebugOff
	}
}

// String renders a rung to its canonical header token. DebugOff (and any
// out-of-range value) renders as "" — it is never emitted on the wire.
func (l DebugLevel) String() string {
	switch l {
	case DebugFlow:
		return "flow"
	case DebugBasic:
		return "debug"
	case DebugTrace:
		return "trace"
	default:
		return ""
	}
}

// debugCtxKey is a distinct key type so the debug value never collides with the
// request-ID value, even though both use the zero key.
type debugCtxKey int

const debugLevelKey debugCtxKey = 0

// WithDebugLevel returns ctx carrying the requested rung. DebugOff is a no-op
// (returns the parent unchanged), mirroring WithRequestID's empty-is-no-op.
func WithDebugLevel(ctx context.Context, l DebugLevel) context.Context {
	if l == DebugOff {
		return ctx
	}
	return context.WithValue(ctx, debugLevelKey, l)
}

// DebugLevelFromContext returns the requested rung stored in ctx, or DebugOff.
func DebugLevelFromContext(ctx context.Context) DebugLevel {
	l, _ := ctx.Value(debugLevelKey).(DebugLevel)
	return l
}
