package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
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
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id", nil)
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
// cfg.RunsDir is empty.
func TestRuntime_FinalizeIsNoop(t *testing.T) {
	cfg := defaultTestConfig(t)
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id", nil)
	require.NoError(t, err)
	defer rt.Close() //nolint:errcheck

	require.NoError(t, rt.Finalize(context.Background(), &Summary{}))
}

// TestRuntime_FinalizeWritesBundle verifies that Finalize writes the artifact
// bundle to cfg.RunsDir when it is non-empty.
func TestRuntime_FinalizeWritesBundle(t *testing.T) {
	dir := t.TempDir()
	cfg := defaultTestConfig(t)
	cfg.RunsDir = dir
	rt, err := NewRuntime(context.Background(), &cfg, "bundle-run-id", nil)
	require.NoError(t, err)
	defer rt.Close() //nolint:errcheck

	rt.SetLastSettle(SettleOutcome{
		AllSucceeded: true,
		Succeeded:    3,
		Failed:       0,
		Probes:       []string{"m1", "m2", "m3"},
	})

	summary := &Summary{RunID: "bundle-run-id", Preset: "test", TargetRate: 100}
	require.NoError(t, rt.Finalize(context.Background(), summary))

	// All 9 artifact files must exist.
	for _, f := range []string{
		"summary.json", "histograms.hlog", "settle.json",
		"flags.txt", "env.txt", "stdout.log", "stderr.log",
		"metrics.prom", "timeseries.jsonl",
	} {
		path := filepath.Join(dir, "bundle-run-id", f)
		_, statErr := os.Stat(path)
		assert.NoError(t, statErr, "missing artifact file: %s", f)
	}

	// Verify settle.json contains the expected data.
	settleBytes, err := os.ReadFile(filepath.Join(dir, "bundle-run-id", "settle.json"))
	require.NoError(t, err)
	assert.Contains(t, string(settleBytes), `"AllSucceeded": true`)
	assert.Contains(t, string(settleBytes), `"Succeeded": 3`)
}

// TestRuntime_PreflightIsNoop verifies the Phase 0 stub returns no error.
func TestRuntime_PreflightIsNoop(t *testing.T) {
	cfg := defaultTestConfig(t)
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id", nil)
	require.NoError(t, err)
	defer rt.Close() //nolint:errcheck

	require.NoError(t, rt.Preflight(context.Background()))
}

// TestNewRuntime_BadNATSURL verifies that an unreachable NATS URL
// causes NewRuntime to return an error rather than panic.
func TestNewRuntime_BadNATSURL(t *testing.T) {
	cfg := defaultTestConfig(t)
	cfg.NatsURL = "nats://127.0.0.1:1" // port 1 is always refused
	_, err := NewRuntime(context.Background(), &cfg, "bad-url-run", nil)
	require.Error(t, err)
}

// newTestRuntime is a convenience helper that creates a Runtime using
// defaultTestConfig and registers t.Cleanup to close it. The returned
// cleanup function may be called early if the test needs to force Close
// before the deferred cleanup fires.
func newTestRuntime(t *testing.T) (*Runtime, func()) {
	t.Helper()
	cfg := defaultTestConfig(t)
	rt, err := NewRuntime(context.Background(), &cfg, "test-run-id", nil)
	require.NoError(t, err)
	cleanup := func() { _ = rt.Close() }
	t.Cleanup(cleanup)
	return rt, cleanup
}

// TestRuntime_Sites_ReturnsLengthOne verifies that Sites() always returns a
// single-element slice in Phase 2, with non-nil NC and JS.
func TestRuntime_Sites_ReturnsLengthOne(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	sites := rt.Sites()
	require.Len(t, sites, 1)
	assert.Equal(t, rt.cfg.SiteID, sites[0].Name)
	assert.NotNil(t, sites[0].NC)
	assert.NotNil(t, sites[0].JS)
}

// TestRuntime_Subscribers_IsLongLived verifies that Subscribers() returns a
// non-nil registry and that subscriptions registered through it receive messages.
func TestRuntime_Subscribers_IsLongLived(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	subs := rt.Subscribers()
	require.NotNil(t, subs)

	delivered := make(chan struct{}, 1)
	_, err := subs.Subscribe("test.subject.longlived", func(_ *nats.Msg) {
		delivered <- struct{}{}
	})
	require.NoError(t, err)
	assert.Equal(t, 1, subs.Count())

	// Publish to verify the subscription is live.
	require.NoError(t, rt.NC().NatsConn().Publish("test.subject.longlived", []byte("hi")))
	require.NoError(t, rt.NC().NatsConn().Flush())
	select {
	case <-delivered:
		// good — handler was invoked
	case <-time.After(time.Second):
		t.Fatal("subscription handler not invoked within 1s")
	}
}

// TestSubscribers_SubscribeIsIdempotent verifies that calling Subscribe twice
// on the same subject returns the same subscription object and does not
// create a duplicate.
func TestSubscribers_SubscribeIsIdempotent(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	subs := rt.Subscribers()

	sub1, err := subs.Subscribe("test.idempotent", func(_ *nats.Msg) {})
	require.NoError(t, err)
	require.NotNil(t, sub1)
	assert.Equal(t, 1, subs.Count())

	sub2, err := subs.Subscribe("test.idempotent", func(_ *nats.Msg) {})
	require.NoError(t, err)
	// Must return the same subscription pointer, not a new one.
	assert.Same(t, sub1, sub2)
	// Count must remain 1 — no duplicate registered.
	assert.Equal(t, 1, subs.Count())
}

// TestSubscribers_CloseDrainsAll verifies that Close() unsubscribes all
// registered subscriptions and resets the count to zero.
func TestSubscribers_CloseDrainsAll(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()

	subs := rt.Subscribers()

	_, err := subs.Subscribe("test.close.a", func(_ *nats.Msg) {})
	require.NoError(t, err)
	_, err = subs.Subscribe("test.close.b", func(_ *nats.Msg) {})
	require.NoError(t, err)
	assert.Equal(t, 2, subs.Count())

	require.NoError(t, subs.Close())
	assert.Equal(t, 0, subs.Count())
}
