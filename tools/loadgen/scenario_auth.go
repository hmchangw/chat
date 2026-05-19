// tools/loadgen/scenario_auth.go
//
// auth-load scenario (Phase 3 §3.8): HTTP-benchmarks auth-service endpoints
// (POST /auth for login/refresh, GET /healthz as validate proxy) via Resty.
// Models the load profile of a fleet of clients authenticating concurrently.
//
// The auth-reconnect-storm preset additionally spins up M idle NATS
// connections, drops them all at T+AuthStormPeriod, and measures time-to-recovery.
// Reported as loadgen_auth_reconnect_seconds histogram +
// loadgen_auth_reconnects_completed_total counter.
//
// Endpoints (per audit, see docs/scenarios/auth-load.md):
//
//	POST /auth    — issue NATS JWT (prod mode: ssoToken + natsPublicKey;
//	               dev mode: account + natsPublicKey)
//	GET  /healthz — health probe (stand-in for "validate JWT" concept;
//	               NATS-JWT validation is SUT-internal, not a REST endpoint)
//
// doAuthLogin → POST /auth (dev-mode re-auth pattern)
// doAuthValidate → GET /healthz (liveness / validate proxy)
// doAuthRefresh → POST /auth (re-auth — same endpoint, no separate refresh)
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
)

// authLoadScenario benchmarks auth-service HTTP endpoints using Resty.
type authLoadScenario struct{}

func (authLoadScenario) Name() string          { return "auth-load" }
func (authLoadScenario) DefaultPreset() string { return "small" }

// NewGenerator constructs the auth-load runner.
func (authLoadScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &authLoadGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(authLoadScenario{}) }

// authLoadGenerator is the runner produced by authLoadScenario.
type authLoadGenerator struct {
	deps ScenarioDeps
	rf   *runFlags
}

// AuthLoginResponse mirrors auth-service's POST /auth response.
// In prod mode the handler validates an SSO token and issues a NATS JWT.
// In dev mode it accepts an account name directly (no SSO).
type AuthLoginResponse struct {
	NATSJWT  string           `json:"natsJwt"`
	UserInfo authUserInfoResp `json:"user"`
}

type authUserInfoResp struct {
	Email   string `json:"email"`
	Account string `json:"account"`
	EngName string `json:"engName"`
}

// doAuthLogin POSTs to /auth in dev mode (account + natsPublicKey) and
// returns the parsed response. natsPublicKey must be a valid NATS user
// public key (starts with "U", 56 chars or a valid NKey encoding).
func doAuthLogin(ctx context.Context, baseURL, account, natsPublicKey string) (*AuthLoginResponse, error) {
	client := resty.New().SetTimeout(5 * time.Second)
	var resp AuthLoginResponse
	httpResp, err := client.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/json").
		SetBody(map[string]string{
			"account":       account,
			"natsPublicKey": natsPublicKey,
		}).
		SetResult(&resp).
		Post(baseURL + "/auth")
	if err != nil {
		return nil, fmt.Errorf("auth login: %w", err)
	}
	if httpResp.StatusCode() >= 400 {
		return nil, fmt.Errorf("auth login: HTTP %d", httpResp.StatusCode())
	}
	return &resp, nil
}

// doAuthValidate GETs /healthz as a stand-in for "validate" — auth-service
// has no dedicated validate endpoint since NATS-JWT validation is SUT-internal.
func doAuthValidate(ctx context.Context, baseURL string) error {
	client := resty.New().SetTimeout(5 * time.Second)
	httpResp, err := client.R().
		SetContext(ctx).
		Get(baseURL + "/healthz")
	if err != nil {
		return fmt.Errorf("auth validate: %w", err)
	}
	if httpResp.StatusCode() >= 400 {
		return fmt.Errorf("auth validate: HTTP %d", httpResp.StatusCode())
	}
	return nil
}

// doAuthRefresh POSTs to /auth to re-issue a NATS JWT — same endpoint as
// login (auth-service has no separate refresh path; NATS JWT expiry triggers
// a full re-auth with the original credentials). Used by the Phase 3.8
// follow-up tick loop that alternates login/refresh/validate cycles.
func doAuthRefresh(ctx context.Context, baseURL, account, natsPublicKey string) (*AuthLoginResponse, error) {
	return doAuthLogin(ctx, baseURL, account, natsPublicKey)
}

// Run is a SKELETON for Phase 3.8 initial landing. The tick loop alternates
// between the three auth operations. Real wire-up needs:
//
//  1. An auth-service URL (from --auth-url flag or env AUTH_SERVICE_URL).
//  2. A pool of (account, natsPublicKey) credentials for /auth.
//  3. JWT cache so subsequent validate calls have live tokens to probe.
//
// The auth-reconnect-storm preset takes a separate path: runReconnectStorm.
func (g *authLoadGenerator) Run(ctx context.Context) error {
	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	ticker := time.NewTicker(time.Second / time.Duration(rate))
	defer ticker.Stop()

	// Route to the storm path when the preset requests it.
	if g.deps.Preset().Name == "auth-reconnect-storm" {
		return g.runReconnectStorm(ctx)
	}

	// Normal HTTP benchmark (stubbed for Phase 3.8 initial landing).
	// TODO Phase 3.8 follow-up: real /auth + /healthz cycle with metrics.
	// The tick loop will call: doAuthLogin (cold start), doAuthValidate (probe),
	// doAuthRefresh (re-auth when NATS JWT nears expiry).
	_ = doAuthRefresh // referenced here so the symbol is kept until follow-up lands
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// STUB tick — real implementation sends doAuthLogin / doAuthValidate.
		}
	}
}

// runReconnectStorm implements the auth-reconnect-storm preset path.
// At each AuthStormPeriod interval (default 30s), drops all M idle NATS
// connections and measures time-to-recovery (M successful re-handshakes).
//
// STUB for Phase 3.8 initial landing. Real wire-up creates a dedicated
// ConnPool of M connections, calls Close on each, then re-dials and
// observes via loadgen_auth_reconnect_seconds + loadgen_auth_reconnects_completed_total.
func (g *authLoadGenerator) runReconnectStorm(ctx context.Context) error {
	_ = ctx    // consumed by real implementation in Phase 3.8 follow-up
	return nil // PLACEHOLDER — full implementation in Phase 3.8 follow-up
}

// reconnectStormSim is a simulation helper used by tests to validate the
// recovery-tracking math without needing real NATS connections.
type reconnectStormSim struct {
	n         int
	dropAt    time.Time
	recovered time.Time
	mu        sync.Mutex
}

func newReconnectStormSim(n int) *reconnectStormSim {
	return &reconnectStormSim{n: n}
}

// SimulateDrop records the moment connections are "dropped".
func (s *reconnectStormSim) SimulateDrop() {
	s.mu.Lock()
	s.dropAt = time.Now()
	s.mu.Unlock()
}

// SimulateReconnects pretends each of the n connections re-handshakes in
// perConnDelay time. Recovery time is computed deterministically as
// dropAt + n*perConnDelay to avoid time.Sleep (CLAUDE.md §3 forbids Sleep
// for goroutine synchronisation). The test calls RecoveryDuration after
// SimulateReconnects returns to verify the computed gap.
func (s *reconnectStormSim) SimulateReconnects(perConnDelay time.Duration) {
	s.mu.Lock()
	s.recovered = s.dropAt.Add(time.Duration(s.n) * perConnDelay)
	s.mu.Unlock()
}

// RecoveryDuration returns the elapsed time between SimulateDrop and the last
// SimulateReconnects completion.
func (s *reconnectStormSim) RecoveryDuration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recovered.Sub(s.dropAt)
}
