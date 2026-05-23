// tools/loadgen/scenario.go
//
// Scenario interface family + registry.
//
// Adding a new scenario in Phase 3:
//  1. Create scenario_<NAME>.go with a type implementing Scenario + GeneratorFactory.
//  2. Call RegisterScenario in init().
//
// That's it. No edits to main.go or run.go.
//
// Phase 2 §2.2 (loadgen v2 implementation plan).
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
)

// Scenario is the minimum a scenario must implement.
type Scenario interface {
	Name() string
	DefaultPreset() string
}

// GeneratorFactory is a scenario that knows how to construct its load generator.
type GeneratorFactory interface {
	NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error)
}

// ReadinessProber is optional — scenario provides a SUT-readiness probe.
type ReadinessProber interface {
	BuildReadinessProbe(deps ScenarioDeps) func(context.Context) error
}

// LivenessProber is optional — scenario provides a liveness probe.
type LivenessProber interface {
	BuildLivenessProbe(deps ScenarioDeps) func(context.Context) error
}

// AutoWarmer is optional — scenario declares whether auto-warmup is needed.
type AutoWarmer interface {
	NeedsAutoWarmup(p *Preset) bool
}

// ScenarioDeps is the runtime-side capability surface a Scenario may consume.
// Implemented by runDeps (a thin adapter built in executeRun) in Task 2.2;
// tests may provide fakes.
type ScenarioDeps interface {
	Publisher() Publisher
	Requester() Requester
	Collector() *Collector
	Metrics() *Metrics
	Fixtures() *Fixtures
	// Preset returns the run preset (distribution config, mix weights, etc.).
	Preset() *Preset

	// SiteID returns the operator-configured site identifier (SITE_ID env var).
	SiteID() string
	// MaxInFlight returns the per-run cap on concurrent in-flight publishes/requests.
	MaxInFlight() int
	// Omission returns the coordinated-omission tracker shared across the run.
	Omission() *OmissionTracker
	// InjectMode returns the parsed injection mode for the run (frontdoor|canonical).
	InjectMode() InjectMode
	// WarmupDeadline returns the effective warmup deadline for the generator.
	// This may be time.Now() if auto-warmup reset it (history-read path).
	WarmupDeadline() time.Time
	// MessageIDs returns harvested auto-warmup message IDs. Only populated
	// for history-read when auto-warmup ran.
	MessageIDs() []string
	// Note: WarmupPublisher is NOT on this interface — it's a phantom that only
	// executeRun consumes via the concrete *runDeps type directly.

	// Sites returns per-site NATS/JS/Mongo dependencies. Length is always 1 in
	// Phase 2; Phase 3.9 federation overlay will append a second SiteDeps.
	Sites() []SiteDeps
	// Subscribers returns the per-run long-lived subscription registry. Phase
	// 3.2 large-room scenario registers one inbox sub per fixture user here so
	// subscriptions survive for the duration of the run.
	Subscribers() *Subscribers
	// RunID returns the UUIDv7 run identifier. Scenarios use it to write
	// per-run artifacts (e.g., bootstrap_error markers) under RunsDir.
	RunID() string
	// RunsDir returns the artifact-bundle root directory (RUNS_DIR env).
	// Empty when artifact bundling is disabled — scenarios that write
	// per-run markers should no-op when this is "".
	RunsDir() string
}

// Runner is a constructed load generator. Run blocks until ctx is cancelled or
// the scenario reaches natural completion.
type Runner interface {
	Run(ctx context.Context) error
}

// SiteDeps holds per-site NATS, JetStream, and traced-conn dependencies.
// For federation (Phase 3 §3.9), Runtime.Sites() returns two entries —
// one for the local site and one for the remote. Phase 2 always returns
// a single-element slice.
type SiteDeps struct {
	// Name is a human-readable identifier, e.g. "site-local" or "site-remote".
	Name string
	// NC is the traced NATS connection for this site.
	NC *otelnats.Conn
	// JS is the JetStream client for this site.
	JS jetstream.JetStream
}

// Subscribers is a long-lived subscription registry used by large-room
// scenarios (Phase 3 §3.2) that need one inbox subscription per fixture user
// at startup. Subscriptions registered here survive until Runtime.Close.
//
// Implemented by Runtime; wired into runDeps via Runtime.Subscribers().
type Subscribers struct {
	mu   sync.Mutex
	subs map[string]*nats.Subscription
	nc   *nats.Conn
}

// NewSubscribers creates a new Subscribers registry bound to the given NATS
// connection. Call Close to unsubscribe all registered subscriptions.
func NewSubscribers(nc *nats.Conn) *Subscribers {
	return &Subscribers{
		subs: make(map[string]*nats.Subscription),
		nc:   nc,
	}
}

// Subscribe creates a long-lived subscription on the given subject. If the
// subject is already subscribed, returns the existing subscription (idempotent).
func (s *Subscribers) Subscribe(subject string, handler func(*nats.Msg)) (*nats.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.subs[subject]; ok {
		return existing, nil
	}
	sub, err := s.nc.Subscribe(subject, handler)
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", subject, err)
	}
	s.subs[subject] = sub
	return sub, nil
}

// Close unsubscribes all registered subscriptions and clears the registry.
// Safe to call multiple times. Returns the first unsubscribe error encountered.
func (s *Subscribers) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for subject, sub := range s.subs {
		if err := sub.Unsubscribe(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("unsubscribe %s: %w", subject, err)
		}
	}
	s.subs = make(map[string]*nats.Subscription)
	return firstErr
}

// Count returns the number of active subscriptions. Primarily used in tests.
func (s *Subscribers) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// ---------------- registry ----------------

var scenarioRegistry = map[string]Scenario{}

// RegisterScenario adds a scenario to the global registry. Panics on duplicate
// registration to catch init() collisions early.
func RegisterScenario(s Scenario) {
	name := s.Name()
	if _, exists := scenarioRegistry[name]; exists {
		panic(fmt.Sprintf("scenario %q registered twice — check init() in scenario_*.go", name))
	}
	scenarioRegistry[name] = s
}

// LookupScenario returns the scenario for name, or (nil, false) if unknown.
func LookupScenario(name string) (Scenario, bool) {
	s, ok := scenarioRegistry[name]
	return s, ok
}

// AllScenarios returns a copy of the registry. Mutations on the returned map
// do not affect the registry.
func AllScenarios() map[string]Scenario {
	out := make(map[string]Scenario, len(scenarioRegistry))
	for k, v := range scenarioRegistry {
		out[k] = v
	}
	return out
}
