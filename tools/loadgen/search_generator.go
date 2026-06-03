package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// SearchRequester is the narrow consumer interface for the search request/reply
// transport. It mirrors HistoryRequester; the production implementation wraps
// nats.Conn.RequestWithContext and tests inject a recorder.
type SearchRequester interface {
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

// SearchGeneratorConfig bundles every dependency the search generator needs.
type SearchGeneratorConfig struct {
	Preset         *SearchPreset
	Fixtures       *HistoryFixtures
	SiteID         string
	Arm            string // benchmark arm (C/A/B); set on every request's Variant
	Rate           int
	RequestTimeout time.Duration
	Requester      SearchRequester
	Collector      *SearchCollector
	MaxInFlight    int
}

// SearchGenerator drives the search request/reply loop. It mirrors
// HistoryGenerator's open-loop ticker + bounded goroutine pool so operators see
// consistent semantics across workloads. Each request picks an account from the
// fixtures and a query from the preset pool, and carries the configured arm in
// req.Variant.
type SearchGenerator struct {
	cfg SearchGeneratorConfig

	rngMu    sync.Mutex
	rng      *rand.Rand
	accounts []string
}

// NewSearchGenerator constructs a generator seeded from `seed`. The account
// pool is derived from the fixture subscriptions (every account with at least
// one room subscription is a valid searcher).
func NewSearchGenerator(cfg *SearchGeneratorConfig, seed int64) *SearchGenerator {
	r := rand.New(rand.NewSource(seed))
	seen := map[string]struct{}{}
	var accounts []string
	for i := range cfg.Fixtures.Fixtures.Subscriptions {
		acct := cfg.Fixtures.Fixtures.Subscriptions[i].User.Account
		if acct == "" {
			continue
		}
		if _, ok := seen[acct]; ok {
			continue
		}
		seen[acct] = struct{}{}
		accounts = append(accounts, acct)
	}
	return &SearchGenerator{
		cfg:      *cfg,
		rng:      r,
		accounts: accounts,
	}
}

// Run drives the open-loop request loop until ctx cancels. Mirrors
// HistoryGenerator.Run: a punctual ticker dispatches each request to a bounded
// goroutine pool, with a drain grace period on shutdown.
func (g *SearchGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	if g.cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				g.requestOne(ctx)
			}
		}
	}

	sem := make(chan struct{}, g.cfg.MaxInFlight)
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(drainGracePeriod):
			}
			return nil
		case <-tick.C:
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() {
						<-sem
						wg.Done()
					}()
					g.requestOne(ctx)
				}()
			default:
				// In-flight pool saturated; skip this tick rather than block
				// the ticker. Search has no saturation tally, so just drop.
			}
		}
	}
}

func (g *SearchGenerator) requestOne(ctx context.Context) {
	account := g.pickAccount()
	if account == "" {
		return
	}
	query := g.pickQuery()
	body := model.SearchMessagesRequest{
		Query:   query,
		Size:    g.cfg.Preset.Size,
		Variant: g.cfg.Arm,
	}
	data, err := json.Marshal(body)
	if err != nil {
		g.cfg.Collector.Record(g.cfg.Arm, 0, time.Now(), err)
		return
	}
	subj := subject.SearchMessages(account, g.cfg.SiteID)

	start := time.Now()
	_, reqErr := g.cfg.Requester.Request(ctx, subj, data, g.cfg.RequestTimeout)
	latency := time.Since(start)

	// Run-level cancellation isn't a real failure — the run is draining.
	if reqErr != nil && ctx.Err() != nil {
		return
	}
	g.cfg.Collector.Record(g.cfg.Arm, latency, time.Now(), reqErr)
}

func (g *SearchGenerator) pickAccount() string {
	if len(g.accounts) == 0 {
		return ""
	}
	return g.accounts[g.intn(len(g.accounts))]
}

func (g *SearchGenerator) pickQuery() string {
	pool := g.cfg.Preset.Queries
	if len(pool) == 0 {
		return ""
	}
	return pool[g.intn(len(pool))]
}

func (g *SearchGenerator) intn(n int) int {
	if n <= 0 {
		return 0
	}
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return g.rng.Intn(n)
}
