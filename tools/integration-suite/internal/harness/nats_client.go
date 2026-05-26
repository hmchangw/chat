package harness

import (
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
)

// NATSConnPool caches NATS connections keyed by (account, site).
// One pool per scenario; closed on scenario teardown.
type NATSConnPool struct {
	mu    sync.Mutex
	conns map[string]*nats.Conn
}

// NewNATSConnPool returns an empty pool.
func NewNATSConnPool() *NATSConnPool {
	return &NATSConnPool{conns: map[string]*nats.Conn{}}
}

// Conn returns a connection for (account, site), opening one if needed.
// `natsURL` is the broker URL for the site. `creds` carries the per-user
// JWT and nkey seed used to authenticate.
func (p *NATSConnPool) Conn(account, site, natsURL string, creds *Credentials) (*nats.Conn, error) {
	if creds == nil {
		return nil, fmt.Errorf("nats: no credentials for account %q (Given step missing?)", account)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	key := account + "@" + site
	if c, ok := p.conns[key]; ok && c.IsConnected() {
		return c, nil
	}

	c, err := nats.Connect(natsURL,
		nats.UserJWTAndSeed(creds.JWT, creds.Seed),
		nats.Name("integration-suite/"+key),
	)
	if err != nil {
		return nil, fmt.Errorf("nats: connect %q as %q: %w", natsURL, account, err)
	}
	p.conns[key] = c
	return c, nil
}

// CloseAll drains and closes every cached connection.
func (p *NATSConnPool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.conns {
		_ = c.Drain()
		delete(p.conns, k)
	}
}
