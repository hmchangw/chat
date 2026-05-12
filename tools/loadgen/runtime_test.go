package main

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startEmbeddedNATS starts an in-process NATS server on a random port,
// registers t.Cleanup for shutdown, and returns the client URL.
func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := &natsserver.Options{Port: -1}
	ns, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second), "nats server did not become ready")
	t.Cleanup(ns.Shutdown)
	return ns.ClientURL()
}

// defaultTestConfig returns a config with a working NATS URL and safe
// defaults for all other fields. The metrics HTTP server is disabled
// (empty addr) to avoid port conflicts in parallel tests.
func defaultTestConfig(t *testing.T) config {
	t.Helper()
	return config{
		NatsURL:       startEmbeddedNATS(t),
		NatsCredsFile: "",
		SiteID:        "site-test",
		MongoURI:      "mongodb://localhost:27017",
		MongoDB:       "loadgen-test",
		MetricsAddr:   "", // disabled — avoids port conflicts in unit tests
		PProfAddr:     "",
		MaxInFlight:   10,
	}
}

func TestNewRuntime_WiresAllDependencies(t *testing.T) {
	cfg := defaultTestConfig(t)
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id")
	require.NoError(t, err)
	defer rt.Close() //nolint:errcheck

	assert.NotNil(t, rt.NC())
	assert.NotNil(t, rt.JS())
	assert.NotNil(t, rt.Pool())
	assert.NotNil(t, rt.Collector())
	assert.NotNil(t, rt.Metrics())
	assert.Equal(t, "test-run-id", rt.RunID())
}

// TestRuntime_FinalizeIsNoop verifies that Finalize does not error when
// cfg.RunsDir is empty. Full filesystem assertions are deferred to Phase 1b.
func TestRuntime_FinalizeIsNoop(t *testing.T) {
	cfg := defaultTestConfig(t)
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id")
	require.NoError(t, err)
	defer rt.Close() //nolint:errcheck

	require.NoError(t, rt.Finalize(context.Background()))
}

// TestRuntime_PrefightIsNoop verifies the Phase 0 stub returns no error.
func TestRuntime_PrefightIsNoop(t *testing.T) {
	cfg := defaultTestConfig(t)
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id")
	require.NoError(t, err)
	defer rt.Close() //nolint:errcheck

	require.NoError(t, rt.Preflight(context.Background()))
}

// TestNewRuntime_BadNATSURL verifies that an unreachable NATS URL
// causes NewRuntime to return an error rather than panic.
func TestNewRuntime_BadNATSURL(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.NatsURL = "nats://127.0.0.1:1" // port 1 is always refused
	_, err := NewRuntime(context.Background(), &cfg, "bad-url-run")
	require.Error(t, err)
}
