// tools/loadgen/scenario_firstdm.go
//
// first-dm scenario (Phase 3 §3.13): exercises the "lazy room-create" path for
// first-time DMs. Each tick picks a user pair from a dedicated
// loadgen-firstdm-prefixed pool (provisioned by `seed --include-first-dm-fixtures`),
// sends the first DM, and measures four sub-lag stages along the
// publish→canonical path:
//
//	stage=room    — publishedAt → observer sees room-create event
//	stage=subs    — room-create → observer sees subscription-create events
//	stage=persist — subscription-create → canonical message persisted
//	stage=e2e     — publishedAt → canonical persisted
//
// DM room IDs are computed via idgen.BuildDMRoomID(a, b) per CLAUDE.md §6.
package main

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// firstDMScenario exercises the "lazy room-create" path for first-time DMs.
// Each tick picks a user pair from a dedicated loadgen-firstdm-prefixed pool
// (provisioned by `seed --include-first-dm-fixtures`), sends the first DM,
// and measures four sub-lag stages:
//
//	stage=room    — publishedAt → observer sees room-create event
//	stage=subs    — room-create → observer sees subscription-create events
//	stage=persist — subscription-create → canonical message persisted
//	stage=e2e     — publishedAt → canonical persisted
//
// DM room IDs computed via idgen.BuildDMRoomID(a, b) per CLAUDE.md §6.
type firstDMScenario struct{}

func (firstDMScenario) Name() string          { return "first-dm" }
func (firstDMScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the first-dm load generator.
func (firstDMScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &firstDMGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(firstDMScenario{}) }

// firstDMGenerator is the runner produced by firstDMScenario.
type firstDMGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
	pool *firstDMPool
}

// firstDMPool maintains a list of user pairs that have NEVER messaged each
// other. Iterates monotonically — each pair fires exactly once per run,
// unless recycle=true (wraps around the list).
//
// Thread-safe.
type firstDMPool struct {
	mu      sync.Mutex
	pairs   [][2]string
	idx     int
	recycle bool
}

// newFirstDMPool constructs a firstDMPool from the given pairs.
// When recycle is false the pool is exhausted after all pairs have been
// consumed and Next returns (zero, false). When recycle is true the pool
// wraps back to the beginning.
func newFirstDMPool(pairs [][2]string, recycle bool) *firstDMPool {
	return &firstDMPool{pairs: pairs, recycle: recycle}
}

// Next returns the next (userA, userB) pair, or ([2]string{}, false) when the
// pool is exhausted. If recycle is true, wraps to the first pair after
// exhausting the list.
func (p *firstDMPool) Next() ([2]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pairs) == 0 {
		return [2]string{}, false
	}
	if p.idx >= len(p.pairs) {
		if !p.recycle {
			return [2]string{}, false
		}
		p.idx = 0
	}
	pair := p.pairs[p.idx]
	p.idx++
	return pair, true
}

// Run emits first-DM events at the configured rate until ctx is cancelled or
// the pair pool is exhausted.
func (g *firstDMGenerator) Run(ctx context.Context) error {
	// Collect first-dm-prefixed user pairs from the fixture pool. The pool is
	// structured: loadgen-firstdm-user-0000001 + loadgen-firstdm-user-0000002
	// form pair 1, ...0000003 + ...0000004 form pair 2, etc.
	pairs := collectFirstDMPairs(g.deps.Fixtures())
	if len(pairs) == 0 {
		return nil // no work; --include-first-dm-fixtures was not used
	}
	g.pool = newFirstDMPool(pairs, g.rf.FirstDMRecycle)

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 5
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	hist := g.deps.Metrics().FirstDMLag
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pair, ok := g.pool.Next()
			if !ok {
				return nil // pool exhausted (non-recycle); clean exit
			}
			wg.Add(1)
			go func(p [2]string) {
				defer wg.Done()
				outcomes := g.sendFirstDM(ctx, p[0], p[1])
				for stage, lat := range outcomes {
					hist.With(prometheus.Labels{"stage": stage}).Observe(lat.Seconds())
				}
			}(pair)
		}
	}
}

// sendFirstDM publishes a first-time DM between userA and userB and observes
// the 4 sub-lag stages. STUB for Phase 3.13 initial landing — TODO follow-up:
//  1. Subscribe to room-create + subscription-create OUTBOX subjects for both users
//     via g.deps.Subscribers().Subscribe(...) BEFORE publishing.
//  2. Publish the DM via Publisher to idgen.BuildDMRoomID(userA, userB).
//  3. Record publishedAt; wait for handlers to fire; record per-stage timestamps.
//  4. Compute lag for each stage.
//
// Returns a map of stage→lag. Stub returns nothing.
func (g *firstDMGenerator) sendFirstDM(_ context.Context, _ string, _ string) map[string]time.Duration {
	// PLACEHOLDER for Phase 3.13 follow-up.
	return map[string]time.Duration{} // empty → no observations
}

// collectFirstDMPairs scans the fixture pool for loadgen-firstdm- prefixed
// users and partitions them into pairs by index (first 2 = pair 1, etc.).
func collectFirstDMPairs(f *Fixtures) [][2]string {
	var users []string
	for i := range f.Users {
		if hasFirstDMPrefix(f.Users[i].ID) {
			users = append(users, f.Users[i].ID)
		}
	}
	pairs := make([][2]string, 0, len(users)/2)
	for i := 0; i+1 < len(users); i += 2 {
		pairs = append(pairs, [2]string{users[i], users[i+1]})
	}
	return pairs
}

// hasFirstDMPrefix returns true if the ID starts with the first-DM fixture prefix.
func hasFirstDMPrefix(id string) bool {
	const prefix = "loadgen-firstdm-"
	if len(id) < len(prefix) {
		return false
	}
	return id[:len(prefix)] == prefix
}
