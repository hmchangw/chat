package main

import (
	"fmt"
	"hash/fnv"

	"github.com/nats-io/nats.go"
)

// ConnPool partitions per-user traffic across N data NATS connections
// while keeping a single shared observer connection for reply /
// broadcast / sampler subscriptions. Phase 3 §3.6 motivation: real
// client populations open thousands of connections, so a single shared
// connection hides connection-table / slow-consumer pressure that
// would otherwise show up under load.
//
// ConnPool with size 1 collapses to {observer} alone; this preserves
// today's single-connection behavior as the default.
type ConnPool struct {
	conns    []*nats.Conn
	observer *nats.Conn
}

// NewConnPool dials N data connections plus one observer. urls and
// credsFile are forwarded to natsutil.Connect verbatim. With size <= 1
// only the observer is opened and the data slice is left empty —
// callers see a pool that behaves identically to the pre-Phase-3
// single-connection path.
func NewConnPool(urls, credsFile string, size int, dial func(string, string) (*nats.Conn, error)) (*ConnPool, error) {
	if dial == nil {
		return nil, fmt.Errorf("dial function required")
	}
	observer, err := dial(urls, credsFile)
	if err != nil {
		return nil, fmt.Errorf("dial observer: %w", err)
	}
	if size <= 1 {
		return &ConnPool{observer: observer}, nil
	}
	conns := make([]*nats.Conn, 0, size)
	for i := 0; i < size; i++ {
		c, err := dial(urls, credsFile)
		if err != nil {
			// Best-effort drain of what we already opened.
			_ = observer.Drain()
			for _, prior := range conns {
				_ = prior.Drain()
			}
			return nil, fmt.Errorf("dial conn %d: %w", i, err)
		}
		conns = append(conns, c)
	}
	return &ConnPool{conns: conns, observer: observer}, nil
}

// NewTestConnPool returns a pool with the requested logical size but
// no real connections — used by unit tests that only exercise the
// hash + size accessors.
func NewTestConnPool(size int) *ConnPool {
	return &ConnPool{conns: make([]*nats.Conn, size)}
}

// Size returns the number of data connections (excluding the observer).
// 1 is the degenerate "no fan-out" case.
func (p *ConnPool) Size() int {
	if len(p.conns) == 0 {
		return 1
	}
	return len(p.conns)
}

// IndexFor returns the data-connection index assigned to userID. With
// Size==1 the index is always 0.
func (p *ConnPool) IndexFor(userID string) int {
	if p.Size() <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(userID))
	return int(h.Sum32() % uint32(p.Size()))
}

// For returns the data connection assigned to userID. Falls back to
// the observer when the pool was created with Size <= 1.
func (p *ConnPool) For(userID string) *nats.Conn {
	if len(p.conns) == 0 {
		return p.observer
	}
	return p.conns[p.IndexFor(userID)]
}

// Observer returns the connection used for reply / broadcast / sampler
// subscriptions. Always non-nil after NewConnPool.
func (p *ConnPool) Observer() *nats.Conn {
	return p.observer
}

// Drain drains every connection in the pool. Best-effort: a failure on
// one connection does not prevent the others from draining.
func (p *ConnPool) Drain() {
	for _, c := range p.conns {
		if c != nil {
			_ = c.Drain()
		}
	}
	if p.observer != nil {
		_ = p.observer.Drain()
	}
}

// UserFromSubject extracts the {account} segment from
// `chat.user.{account}.…`. Returns the raw subject for non-user
// subjects so a downstream hash still spreads but lands deterministically.
// Exposed for callers that need to drive ConnPool.For from a subject.
func UserFromSubject(subj string) string {
	const prefix = "chat.user."
	if len(subj) <= len(prefix) || subj[:len(prefix)] != prefix {
		return subj
	}
	rest := subj[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			return rest[:i]
		}
	}
	return rest
}
