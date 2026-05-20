package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestAuthLoadScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("auth-load")
	require.True(t, ok)
	assert.Equal(t, "auth-load", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestAuthReconnectStormPreset_Registered(t *testing.T) {
	p, ok := BuiltinPreset("auth-reconnect-storm")
	require.True(t, ok)
	assert.Greater(t, p.AuthIdleConnections, 0)
}

func TestAuthLogin_HitsLoginEndpoint(t *testing.T) {
	var calledPath, calledMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		calledMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"natsJwt":"fake.jwt.token","user":{"account":"alice"}}`))
	}))
	defer server.Close()

	_, err := doAuthLogin(context.Background(), server.URL, "alice", "UABCDEFGHIJKLMNOPQRSTUVWXYZ1234")
	require.NoError(t, err)
	assert.Equal(t, "/auth", calledPath)
	assert.Equal(t, http.MethodPost, calledMethod)
}

func TestAuthValidate_HitsHealthz(t *testing.T) {
	var calledPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	err := doAuthValidate(context.Background(), server.URL)
	require.NoError(t, err)
	assert.Equal(t, "/healthz", calledPath)
}

func TestAuthLogin_ReturnsErrorOn500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := doAuthLogin(context.Background(), server.URL, "alice", "UABC")
	require.Error(t, err)
}

func TestReconnectStorm_TracksRecovery(t *testing.T) {
	storm := newReconnectStormSim(10)
	storm.SimulateDrop()
	storm.SimulateReconnects(5 * time.Millisecond)
	elapsed := storm.RecoveryDuration()
	assert.Greater(t, elapsed, 30*time.Millisecond)
	assert.Less(t, elapsed, 500*time.Millisecond)
}

func newAuthFixtures() *Fixtures {
	return &Fixtures{
		Users: []model.User{
			{ID: "u-1", Account: "alice", SiteID: "site-local"},
			{ID: "u-2", Account: "bob", SiteID: "site-local"},
		},
	}
}

func newAuthDeps(t *testing.T, preset *Preset) *fakeScenarioDeps {
	t.Helper()
	m := NewMetrics()
	return &fakeScenarioDeps{
		fixtures: newAuthFixtures(),
		preset:   preset,
		siteID:   "site-local",
		metrics:  m,
	}
}

func TestAuthLoadGenerator_NormalMode_HitsAuthAndHealthz(t *testing.T) {
	var (
		mu         sync.Mutex
		authHits   int
		healthHits int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/auth":
			authHits++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"natsJwt":"fake","user":{"account":"x"}}`))
		case "/healthz":
			healthHits++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	deps := newAuthDeps(t, &Preset{Name: "small"})
	g := &authLoadGenerator{deps: deps, rf: &runFlags{Rate: 200, AuthURL: server.URL}}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, authHits, 0)
	assert.Greater(t, healthHits, 0)
}

func TestAuthLoadGenerator_NormalMode_RecordsRequestMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"natsJwt":"x","user":{"account":"x"}}`))
	}))
	defer server.Close()

	deps := newAuthDeps(t, &Preset{Name: "small"})
	g := &authLoadGenerator{deps: deps, rf: &runFlags{Rate: 200, AuthURL: server.URL}}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	loginCount := promCounterValue(t, deps.metrics.Requests, "small", "auth-load", "login", "measured")
	validateCount := promCounterValue(t, deps.metrics.Requests, "small", "auth-load", "validate", "measured")
	assert.Greater(t, loginCount, 0.0)
	assert.Greater(t, validateCount, 0.0)
}

func TestAuthLoadGenerator_NormalMode_RecordsRequestErrorsOn500(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	deps := newAuthDeps(t, &Preset{Name: "small"})
	g := &authLoadGenerator{deps: deps, rf: &runFlags{Rate: 200, AuthURL: server.URL}}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	loginErrors := promCounterErrorSum(t, deps.metrics.RequestErrors, "small", "auth-load", "login")
	assert.Greater(t, loginErrors, 0.0)
}

func TestAuthLoadGenerator_NormalMode_RespectsCtxCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"natsJwt":"x","user":{"account":"x"}}`))
	}))
	defer server.Close()

	deps := newAuthDeps(t, &Preset{Name: "small"})
	g := &authLoadGenerator{deps: deps, rf: &runFlags{Rate: 100, AuthURL: server.URL}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	time.AfterFunc(20*time.Millisecond, cancel)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}

func TestAuthLoadGenerator_NormalMode_NoFixturesNoCrash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"natsJwt":"x","user":{"account":"x"}}`))
	}))
	defer server.Close()

	deps := newAuthDeps(t, &Preset{Name: "small"})
	deps.fixtures = &Fixtures{}

	g := &authLoadGenerator{deps: deps, rf: &runFlags{Rate: 100, AuthURL: server.URL}}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))
}

type fakeStormConn struct {
	closed atomic.Bool
}

func (f *fakeStormConn) Close() { f.closed.Store(true) }

func TestReconnectStorm_OneShot_RecordsRecoveryAndCompletion(t *testing.T) {
	preset := &Preset{Name: "auth-reconnect-storm", AuthIdleConnections: 5, AuthStormPeriod: 0}
	deps := newAuthDeps(t, preset)

	var dialCount atomic.Int32
	dial := func(_ string) (stormConn, error) {
		dialCount.Add(1)
		return &fakeStormConn{}, nil
	}

	g := &authLoadGenerator{
		deps:        deps,
		rf:          &runFlags{AuthStormPeriod: 0},
		dial:        dial,
		stormDelay:  10 * time.Millisecond,
		stormPeriod: 0,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	require.NoError(t, g.runReconnectStorm(ctx))

	assert.Equal(t, int32(10), dialCount.Load())

	completed := promCounterPlain(t, deps.metrics.AuthReconnectsCompleted)
	assert.InDelta(t, 1.0, completed, 0.0001)

	count := promHistogramCount(t, deps.metrics.AuthReconnect)
	assert.Equal(t, uint64(1), count)
}

func TestReconnectStorm_Periodic_FiresMultipleEvents(t *testing.T) {
	preset := &Preset{Name: "auth-reconnect-storm", AuthIdleConnections: 3, AuthStormPeriod: 20 * time.Millisecond}
	deps := newAuthDeps(t, preset)

	dial := func(_ string) (stormConn, error) { return &fakeStormConn{}, nil }

	g := &authLoadGenerator{
		deps:        deps,
		rf:          &runFlags{AuthStormPeriod: 20 * time.Millisecond},
		dial:        dial,
		stormDelay:  5 * time.Millisecond,
		stormPeriod: 20 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, g.runReconnectStorm(ctx))

	completed := promCounterPlain(t, deps.metrics.AuthReconnectsCompleted)
	assert.GreaterOrEqual(t, completed, 2.0)
}

func TestReconnectStorm_RespectsCtxCancel(t *testing.T) {
	preset := &Preset{Name: "auth-reconnect-storm", AuthIdleConnections: 2}
	deps := newAuthDeps(t, preset)

	dial := func(_ string) (stormConn, error) { return &fakeStormConn{}, nil }

	g := &authLoadGenerator{
		deps:        deps,
		rf:          &runFlags{},
		dial:        dial,
		stormDelay:  10 * time.Second,
		stormPeriod: 0,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.runReconnectStorm(ctx) }()
	time.AfterFunc(30*time.Millisecond, cancel)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("runReconnectStorm did not honor ctx cancel")
	}
}

func promCounterValue(t *testing.T, c *prometheus.CounterVec, lbls ...string) float64 {
	t.Helper()
	counter, err := c.GetMetricWithLabelValues(lbls...)
	require.NoError(t, err)
	m := &dto.Metric{}
	require.NoError(t, counter.Write(m))
	return m.Counter.GetValue()
}

func promCounterErrorSum(t *testing.T, c *prometheus.CounterVec, preset, scenario, kind string) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	var sum float64
	for metric := range ch {
		m := &dto.Metric{}
		require.NoError(t, metric.Write(m))
		match := map[string]string{}
		for _, lp := range m.Label {
			match[lp.GetName()] = lp.GetValue()
		}
		if match["preset"] == preset && match["scenario"] == scenario && match["kind"] == kind {
			sum += m.Counter.GetValue()
		}
	}
	return sum
}

func promCounterPlain(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, c.Write(m))
	return m.Counter.GetValue()
}

func promHistogramCount(t *testing.T, h prometheus.Histogram) uint64 {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, h.Write(m))
	return m.Histogram.GetSampleCount()
}
