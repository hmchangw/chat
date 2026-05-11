package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"

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

// NewConnPoolWithObserver builds a ConnPool reusing an already-opened
// observer connection. dataSize is the number of additional data
// connections to dial (zero or one means "fall back to observer for
// every publish/request"). Used when the caller already needed an
// observer for reply / broadcast subscriptions and wants the pool to
// share it instead of opening one more.
func NewConnPoolWithObserver(observer *nats.Conn, urls, credsFile string, dataSize int, dial func(string, string) (*nats.Conn, error)) (*ConnPool, error) {
	if observer == nil {
		return nil, fmt.Errorf("observer connection required")
	}
	if dataSize <= 1 {
		return &ConnPool{observer: observer}, nil
	}
	if dial == nil {
		return nil, fmt.Errorf("dial function required when dataSize > 1")
	}
	conns := make([]*nats.Conn, 0, dataSize)
	for i := 0; i < dataSize; i++ {
		c, err := dial(urls, credsFile)
		if err != nil {
			for _, prior := range conns {
				_ = prior.Drain()
			}
			return nil, fmt.Errorf("dial conn %d: %w", i, err)
		}
		conns = append(conns, c)
	}
	return &ConnPool{conns: conns, observer: observer}, nil
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

// LoadCredsDir lists *.creds files in dir (sorted lexically) and
// returns their absolute paths. Used by `--nats-creds-dir` so each
// data connection in the ConnPool can dial with a different
// credential — laying the plumbing for the C2 "per-fixture-user
// JWT/NKey" auth path. Today the loadgen compose stack does not
// include auth-service (deferred), so callers typically leave this
// empty and dial unauthenticated. When auth-service lands in the
// stack, operators point `--nats-creds-dir` at a directory of
// pre-minted user creds and the pool rotates them across data conns.
//
// Returns an empty slice + nil error when dir is empty; non-existent
// dir or a read error returns an error.
func LoadCredsDir(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read creds dir %q: %w", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".creds" {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out, nil
}

// NewConnPoolWithCreds builds a ConnPool where each data connection
// dials with a different credentials file rotated from credsFiles.
// Like NewConnPoolWithObserver, the observer is reused from the
// caller-supplied connection. When credsFiles is empty, falls back to
// the global credsFile parameter for every dial — backwards-compatible
// with NewConnPoolWithObserver.
//
// dataSize <= 1 collapses to {observer} (no data conns dialed).
//
// F3 known limitation: when both --nats-creds-dir and --connections > 1
// are set, the data-conn-to-cred mapping is round-robin by index
// (conns[i] uses credsFiles[i % len(credsFiles)]). The pool's
// pool.For(userID) is hashed independently from the cred rotation —
// so a publish for user "alice" lands on whatever conn-index alice
// hashes to, NOT on the conn dialed with alice's credentials. Once
// auth-service lands and enforces per-user JWT subjects, the SUT will
// reject these requests with permission-denied. Mitigation today: use
// either auth (--nats-creds-dir) OR per-user fan-out (--connections),
// not both, until the user→creds binding lands as part of the full C2
// follow-up. Liveness probes already work around this by routing
// through pool.Observer(), which always uses the global creds.
func NewConnPoolWithCreds(observer *nats.Conn, urls, credsFile string, credsFiles []string, dataSize int, dial func(url, creds string) (*nats.Conn, error)) (*ConnPool, error) {
	if observer == nil {
		return nil, fmt.Errorf("observer connection required")
	}
	if dataSize <= 1 {
		return &ConnPool{observer: observer}, nil
	}
	if dial == nil {
		return nil, fmt.Errorf("dial function required when dataSize > 1")
	}
	conns := make([]*nats.Conn, 0, dataSize)
	for i := 0; i < dataSize; i++ {
		creds := credsFile
		if len(credsFiles) > 0 {
			creds = credsFiles[i%len(credsFiles)]
		}
		c, err := dial(urls, creds)
		if err != nil {
			for _, prior := range conns {
				_ = prior.Drain()
			}
			return nil, fmt.Errorf("dial conn %d: %w", i, err)
		}
		conns = append(conns, c)
	}
	return &ConnPool{conns: conns, observer: observer}, nil
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
