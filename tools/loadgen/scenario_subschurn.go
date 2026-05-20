// tools/loadgen/scenario_subschurn.go
//
// subscription-churn scenario (Phase 3 §3.7): emits subscription joins
// (member-invite path) and leaves (member-remove path) at a configurable
// rate. Operates on a dedicated loadgen-churn- prefixed user/room pool so
// steady-state numbers for other scenarios don't drift.
//
// Maintains a per-(user, room) state machine to avoid double-add /
// double-remove, which would produce spurious "already a member" /
// "not a member" SUT errors.
package main

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// subscriptionChurnScenario emits subscription joins/leaves at a configurable
// rate. Operates on a dedicated loadgen-churn-prefixed user/room pool so
// other scenarios' steady-state numbers don't drift.
//
// Maintains a per-(user, room) state machine to avoid double-add / double-
// remove, which would produce spurious "already a member" / "not a member"
// SUT errors.
type subscriptionChurnScenario struct{}

func (subscriptionChurnScenario) Name() string          { return "subscription-churn" }
func (subscriptionChurnScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the subscription-churn load generator.
func (subscriptionChurnScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &subscriptionChurnGenerator{deps: deps, rf: rf, sm: newChurnStateMachine()}, nil
}

func init() { RegisterScenario(subscriptionChurnScenario{}) }

// subscriptionChurnGenerator is the runner produced by subscriptionChurnScenario.
type subscriptionChurnGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
	sm   *churnStateMachine
}

// churnStateMachine tracks (user, room) membership locally so the scenario
// only emits state-changing churn (avoid double-add / double-remove).
//
// Thread-safe.
type churnStateMachine struct {
	mu      sync.Mutex
	members map[string]bool // key: "user|room"
}

// newChurnStateMachine returns an empty, ready-to-use churnStateMachine.
func newChurnStateMachine() *churnStateMachine {
	return &churnStateMachine{members: make(map[string]bool)}
}

func (s *churnStateMachine) key(user, room string) string {
	return user + "|" + room
}

// IsMember reports whether user is currently recorded as a member of room.
func (s *churnStateMachine) IsMember(user, room string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members[s.key(user, room)]
}

// CanJoin reports whether a join action for (user, room) is valid (i.e., user
// is not already a member).
func (s *churnStateMachine) CanJoin(user, room string) bool { return !s.IsMember(user, room) }

// CanLeave reports whether a leave action for (user, room) is valid (i.e., user
// is currently a member).
func (s *churnStateMachine) CanLeave(user, room string) bool { return s.IsMember(user, room) }

// OnJoin records user as a member of room.
func (s *churnStateMachine) OnJoin(user, room string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members[s.key(user, room)] = true
}

// OnLeave removes user from room's member set.
func (s *churnStateMachine) OnLeave(user, room string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.members, s.key(user, room))
}

// Run emits churn events at the configured rate until ctx is cancelled.
func (g *subscriptionChurnGenerator) Run(ctx context.Context) error {
	rate := g.rf.ChurnRate
	if rate <= 0 {
		rate = 5 // default 5 churn events/sec
	}

	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	histogram := g.deps.Metrics().SubsChurn
	counter := g.deps.Metrics().SubsChurnTotal
	// #nosec G404 -- load-test subs-churn RNG; PCG seeded from clock for traffic shaping, no security context
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0))

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Pick a (user, room) from the churn-prefixed fixture pool.
			user, room := g.pickChurnTarget(rng)
			if user == "" || room == "" {
				continue
			}

			// Decide action: join or leave, based on current state. Force the only
			// valid action; when neither is valid skip (shouldn't happen).
			canJoin := g.sm.CanJoin(user, room)
			canLeave := g.sm.CanLeave(user, room)
			var action string
			switch {
			case canJoin && !canLeave:
				action = "join"
			case canLeave && !canJoin:
				action = "leave"
			case canJoin && canLeave:
				// Logically impossible (a pair can't be both member and non-member),
				// but handle defensively by defaulting to join.
				action = "join"
			default:
				continue // neither valid — skip
			}

			wg.Add(1)
			go func(usr, rm, act string) {
				defer wg.Done()
				start := time.Now()
				err := g.emitChurn(ctx, act, usr, rm)
				lat := time.Since(start)
				outcome := "ok"
				if err != nil {
					outcome = "error"
				} else {
					if act == "join" {
						g.sm.OnJoin(usr, rm)
					} else {
						g.sm.OnLeave(usr, rm)
					}
				}
				histogram.With(prometheus.Labels{"action": act}).Observe(lat.Seconds())
				counter.With(prometheus.Labels{"action": act, "outcome": outcome}).Inc()
			}(user, room, action)
		}
	}
}

// pickChurnTarget returns a (user, room) pair from the churn-prefixed pool.
// STUB for Phase 3.7 initial landing — TODO follow-up wires real fixture
// access by filtering g.deps.Fixtures().Users and .Rooms for the
// "loadgen-churn-" prefix and picking a random pair.
func (g *subscriptionChurnGenerator) pickChurnTarget(_ *rand.Rand) (string, string) {
	// PLACEHOLDER: returns fixed dummy IDs. Real wire-up filters Fixtures for
	// the churn prefix and picks a random pair.
	return "loadgen-churn-user-0000001", "loadgen-churn-room-0000001"
}

// emitChurn is a STUB for Phase 3.7. TODO follow-up: publish the actual
// member-invite (join) or member-remove (leave) request via g.deps.Publisher.
func (g *subscriptionChurnGenerator) emitChurn(_ context.Context, _ string, _ string, _ string) error {
	return nil // PLACEHOLDER
}

// hasChurnPrefix reports whether id starts with the churn-fixture prefix.
func hasChurnPrefix(id string) bool {
	const prefix = "loadgen-churn-"
	return len(id) >= len(prefix) && id[:len(prefix)] == prefix
}
