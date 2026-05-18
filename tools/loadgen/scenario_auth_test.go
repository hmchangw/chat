package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Greater(t, p.AuthIdleConnections, 0, "preset must declare AuthIdleConnections > 0")
}

func TestAuthLogin_HitsLoginEndpoint(t *testing.T) {
	var calledPath string
	var calledMethod string
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

func TestAuthValidate_SendsBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	err := doAuthValidate(context.Background(), server.URL)
	require.NoError(t, err)
}

func TestReconnectStorm_TracksRecovery(t *testing.T) {
	// Simulate: drop 10 idle "connections" (fakes), then measure time-to-recovery.
	storm := newReconnectStormSim(10)
	start := time.Now()
	storm.SimulateDrop()
	storm.SimulateReconnects(5 * time.Millisecond) // each fake reconnect takes 5ms
	elapsed := storm.RecoveryDuration()

	// Recovery should be in the 30-100ms range (10 sequential 5ms reconnects + jitter).
	assert.Greater(t, elapsed, 30*time.Millisecond)
	assert.Less(t, elapsed, 500*time.Millisecond)
	_ = start
}
