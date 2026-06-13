package readers

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// NATSReplyReader is a "pull" reader: the verb dispatcher feeds the
// captured reply (or transport error) into the reader via the Inject
// method, which then emits one event. There's no polling because the
// reply is point-in-time.
type NATSReplyReader struct {
	in chan Event

	// dropped counts replies discarded because the inject buffer was
	// full (§2.9 substrate-loudness). Expected to stay 0 — one reply
	// per scenario into a depth-4 buffer — but a non-zero value means
	// the matcher never saw a reply that the system did send, so it's
	// surfaced via slog.Warn on every drop rather than vanishing.
	dropped atomic.Uint64
}

// NewNATSReplyReader returns a NATSReplyReader ready to receive injected replies.
func NewNATSReplyReader() *NATSReplyReader {
	return &NATSReplyReader{in: make(chan Event, 4)}
}

// Watch relays injected reply events until ctx is cancelled.
func (r *NATSReplyReader) Watch(ctx context.Context, _ string, _ time.Time) (<-chan Event, error) {
	out := make(chan Event, 4)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-r.in:
				out <- ev
			}
		}
	}()
	return out, nil
}

// Inject is called by the dispatcher when a reply (or transport error)
// arrives. The dispatcher has the traceparent context and measured
// latency; this method records them along with the full reply outcome
// as a structured ReplyPayload. The matcher does the interpretation —
// see internal/matchers/matches_shape.go's struct fall-through path.
func (r *NATSReplyReader) Inject(out *verbs.Outcome, latency time.Duration, traceparent string, ts time.Time, ownerSvc, task string) {
	select {
	case r.in <- Event{
		Location:    "reply",
		Timestamp:   ts,
		Traceparent: traceparent,
		OwnerSvc:    ownerSvc,
		Task:        task,
		Payload:     NewReplyPayload(out, latency),
		Type:        EventCascade,
	}:
	default:
		// Buffer full — should not happen (depth-4 buffer, one reply per
		// scenario). If it ever does, the reply is gone before the matcher
		// can see it: a positive `reply` assertion will time out with a
		// misleading "no reply" reason, and an absence assertion will
		// falsely-green. Never silent — count + warn loudly (§2.9).
		n := r.dropped.Add(1)
		slog.Warn("reply reader: inject buffer full — reply DROPPED before matcher could see it; a positive reply assertion will misleadingly time out and an absence assertion may falsely pass",
			"location", "reply", "owner_svc", ownerSvc, "dropped_total", n, "buffer_cap", cap(r.in))
	}
}
