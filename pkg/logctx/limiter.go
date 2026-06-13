package logctx

import (
	"context"

	"github.com/nats-io/nats.go"
	"golang.org/x/time/rate"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// allower is the minimal limiter surface Admit needs; satisfied by
// *rate.Limiter and by deterministic test doubles.
type allower interface{ Allow() bool }

// denyAll honors nothing. It is the default so a service that never calls
// Configure emits no verbose output even for flagged traffic.
type denyAll struct{}

func (denyAll) Allow() bool { return false }

var limiter allower = denyAll{}

// Config tunes the per-instance honored-debug rate cap (env prefix DEBUG_LOG_).
type Config struct {
	Rate  float64 `env:"RATE"  envDefault:"50"` // honored debug messages/sec
	Burst int     `env:"BURST" envDefault:"50"`
}

// Configure installs the package rate limiter. Call once at startup, before any
// message is served — it is not safe to call concurrently with Admit.
func Configure(c Config) {
	limiter = rate.NewLimiter(rate.Limit(c.Rate), c.Burst)
}

// Admit decides — once — the verbose-logging threshold honored for this message
// on THIS instance:
//  1. parse X-Debug into a rung; if off, return ctx untouched (no token spent);
//  2. store the rung on ctx so the intent propagates downstream, regardless of
//     the cap;
//  3. if the rate limiter allows, store the honored slog threshold so the
//     context-aware Handler will emit this message's verbose lines.
//
// The honor decision is made once here, so a message's verbose lines are
// all-or-nothing — never a half-emitted trace.
func Admit(ctx context.Context, headers nats.Header) context.Context {
	rung := natsutil.ParseDebugLevel(headers.Get(natsutil.DebugHeader))
	if rung == natsutil.DebugOff {
		return ctx
	}
	ctx = natsutil.WithDebugLevel(ctx, rung)
	if limiter.Allow() {
		ctx = withHonoredThreshold(ctx, Threshold(rung))
	}
	return ctx
}
