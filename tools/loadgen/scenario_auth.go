// tools/loadgen/scenario_auth.go
//
// auth-load scenario (Phase 3 §3.8): HTTP-benchmarks auth-service endpoints
// (POST /auth for login/refresh, GET /healthz as a liveness/validate proxy)
// via Resty. The auth-reconnect-storm preset switches into a separate code
// path: at T+AuthStormPeriod we drop all M connections, immediately
// re-handshake, and observe loadgen_auth_reconnect_seconds +
// loadgen_auth_reconnects_completed_total.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

type authLoadScenario struct{}

func (authLoadScenario) Name() string          { return "auth-load" }
func (authLoadScenario) DefaultPreset() string { return "small" }

func (authLoadScenario) NewGenerator(deps ScenarioDeps, rf *runFlags) (Runner, error) {
	return &authLoadGenerator{deps: deps, rf: rf}, nil
}

func init() { RegisterScenario(authLoadScenario{}) }

// stormConn is just Close-able — interface abstracts *nats.Conn for tests.
type stormConn interface {
	Close()
}

type authLoadGenerator struct {
	deps ScenarioDeps
	rf   *runFlags

	dial        func(url string) (stormConn, error)
	restyClient *resty.Client
	stormDelay  time.Duration
	stormPeriod time.Duration
}

type AuthLoginResponse struct {
	NATSJWT  string           `json:"natsJwt"`
	UserInfo authUserInfoResp `json:"user"`
}

type authUserInfoResp struct {
	Email   string `json:"email"`
	Account string `json:"account"`
	EngName string `json:"engName"`
}

func doAuthLogin(ctx context.Context, baseURL, account, natsPublicKey string) (*AuthLoginResponse, error) {
	return doAuthLoginWith(ctx, newAuthClient(), baseURL, account, natsPublicKey)
}

func doAuthLoginWith(ctx context.Context, client *resty.Client, baseURL, account, natsPublicKey string) (*AuthLoginResponse, error) {
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

func doAuthValidate(ctx context.Context, baseURL string) error {
	return doAuthValidateWith(ctx, newAuthClient(), baseURL)
}

func doAuthValidateWith(ctx context.Context, client *resty.Client, baseURL string) error {
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

func doAuthRefresh(ctx context.Context, baseURL, account, natsPublicKey string) (*AuthLoginResponse, error) {
	return doAuthLogin(ctx, baseURL, account, natsPublicKey)
}

func newAuthClient() *resty.Client {
	return resty.New().SetTimeout(5 * time.Second)
}

func generateNATSPublicKey() (string, error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", fmt.Errorf("create user nkey: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	return pub, nil
}

func (g *authLoadGenerator) Run(ctx context.Context) error {
	if g.deps.Preset().Name == "auth-reconnect-storm" {
		return g.runReconnectStorm(ctx)
	}
	return g.runNormal(ctx)
}

// runNormal alternates POST /auth and GET /healthz at --rate, recording into
// the standard loadgen Requests / RequestErrors / RequestLatency vectors with
// kind="login"|"validate".
func (g *authLoadGenerator) runNormal(ctx context.Context) error {
	baseURL := g.rf.AuthURL
	if baseURL == "" {
		baseURL = "http://auth-service:8080"
	}

	client := g.restyClient
	if client == nil {
		client = newAuthClient()
	}

	natsPub, err := generateNATSPublicKey()
	if err != nil {
		return fmt.Errorf("generate nats public key: %w", err)
	}

	rate := g.rf.Rate
	if rate <= 0 {
		rate = 10
	}
	interval := time.Second / time.Duration(rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	users := g.deps.Fixtures().Users
	metrics := g.deps.Metrics()
	preset := g.deps.Preset().Name

	var (
		tick int64
		wg   sync.WaitGroup
	)

	slog.Info("auth-load: starting normal-mode HTTP loop",
		"baseURL", baseURL, "rate", rate, "users", len(users))

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			n := atomic.AddInt64(&tick, 1)
			kind := "login"
			if n%2 == 1 {
				kind = "validate"
			}
			var account string
			if len(users) > 0 {
				account = users[int(n)%len(users)].Account
			} else {
				account = "loadgen-anon"
			}
			wg.Add(1)
			go func(k, acct string) {
				defer wg.Done()
				runOneAuthTick(ctx, client, baseURL, k, acct, natsPub, preset, metrics)
			}(kind, account)
		}
	}
}

func runOneAuthTick(ctx context.Context, client *resty.Client, baseURL, kind, account, natsPub, preset string, metrics *Metrics) {
	const phase = "measured"
	metrics.Requests.WithLabelValues(preset, "auth-load", kind, phase).Inc()
	start := time.Now()
	var err error
	switch kind {
	case "login":
		_, err = doAuthLoginWith(ctx, client, baseURL, account, natsPub)
	case "validate":
		err = doAuthValidateWith(ctx, client, baseURL)
	default:
		metrics.RequestErrors.WithLabelValues(preset, "auth-load", kind, phase, "marshal").Inc()
		return
	}
	metrics.RequestLatency.WithLabelValues(preset, "auth-load", kind).Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.RequestErrors.WithLabelValues(preset, "auth-load", kind, phase, "request").Inc()
	}
}

// runReconnectStorm dials M connections, drops them all, re-dials, and
// observes loadgen_auth_reconnect_seconds for the recovery time. Loops every
// stormPeriod; one-shot when zero.
func (g *authLoadGenerator) runReconnectStorm(ctx context.Context) error {
	preset := g.deps.Preset()
	m := preset.AuthIdleConnections
	if m <= 0 {
		m = 100
	}

	dial := g.dial
	if dial == nil {
		dial = func(url string) (stormConn, error) {
			nc, err := nats.Connect(url)
			if err != nil {
				return nil, err
			}
			return nc, nil
		}
	}

	natsURL := ""
	if sites := g.deps.Sites(); len(sites) > 0 && sites[0].NC != nil && sites[0].NC.NatsConn() != nil {
		natsURL = sites[0].NC.NatsConn().ConnectedUrl()
	}

	delay := g.stormDelay
	if delay <= 0 {
		delay = 30 * time.Second
	}
	period := g.stormPeriod
	if period <= 0 {
		period = g.rf.AuthStormPeriod
	}

	metrics := g.deps.Metrics()
	slog.Info("auth-load: starting reconnect-storm",
		"idleConns", m, "firstDropAt", delay.String(),
		"period", period.String(), "natsURL", natsURL)

	first := true
	for {
		wait := period
		if first {
			wait = delay
			first = false
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		if err := runOneStormEvent(ctx, dial, natsURL, m, metrics); err != nil {
			slog.Warn("storm event errored — continuing", "error", err)
		}

		if period == 0 {
			return nil
		}
	}
}

func runOneStormEvent(ctx context.Context, dial func(string) (stormConn, error), url string, m int, metrics *Metrics) error {
	initial := make([]stormConn, 0, m)
	for i := 0; i < m; i++ {
		if ctx.Err() != nil {
			closeAll(initial)
			return ctx.Err()
		}
		c, err := dial(url)
		if err != nil {
			closeAll(initial)
			return fmt.Errorf("initial dial %d/%d: %w", i, m, err)
		}
		initial = append(initial, c)
	}
	closeAll(initial)
	dropAt := time.Now()

	rejoined := make([]stormConn, 0, m)
	for i := 0; i < m; i++ {
		if ctx.Err() != nil {
			closeAll(rejoined)
			return ctx.Err()
		}
		c, err := dial(url)
		if err != nil {
			closeAll(rejoined)
			return fmt.Errorf("rejoin dial %d/%d: %w", i, m, err)
		}
		rejoined = append(rejoined, c)
	}
	recovery := time.Since(dropAt)
	metrics.AuthReconnect.Observe(recovery.Seconds())
	metrics.AuthReconnectsCompleted.Inc()
	slog.Info("storm event recovered", "conns", m, "recovery", recovery.String())
	closeAll(rejoined)
	return nil
}

func closeAll(cs []stormConn) {
	for _, c := range cs {
		if c != nil {
			c.Close()
		}
	}
}

// reconnectStormSim — pure-math simulator retained for TestReconnectStorm_TracksRecovery.
type reconnectStormSim struct {
	n         int
	dropAt    time.Time
	recovered time.Time
	mu        sync.Mutex
}

func newReconnectStormSim(n int) *reconnectStormSim { return &reconnectStormSim{n: n} }

func (s *reconnectStormSim) SimulateDrop() {
	s.mu.Lock()
	s.dropAt = time.Now()
	s.mu.Unlock()
}

func (s *reconnectStormSim) SimulateReconnects(perConnDelay time.Duration) {
	s.mu.Lock()
	s.recovered = s.dropAt.Add(time.Duration(s.n) * perConnDelay)
	s.mu.Unlock()
}

func (s *reconnectStormSim) RecoveryDuration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recovered.Sub(s.dropAt)
}

var _ = doAuthRefresh
