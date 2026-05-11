package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLastToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"chat.user.alice.response.abc-123", "abc-123"},
		{"abc", "abc"},       // no dot
		{"", ""},             // empty
		{"a.b.c.d.e.f", "f"}, // many dots
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			assert.Equal(t, c.want, lastToken(c.in))
		})
	}
}

func TestCounterValue(t *testing.T) {
	m := NewMetrics()
	m.Published.WithLabelValues("small", "measured", "0", "100-200").Inc()
	m.Published.WithLabelValues("small", "measured", "0", "100-200").Inc()
	m.Published.WithLabelValues("medium", "measured", "0", "100-200").Inc()
	assert.Equal(t, float64(3), counterValue(m, "loadgen_published_total"))
	assert.Equal(t, float64(0), counterValue(m, "nonexistent_metric"))
}

func TestCounterValueLabeled(t *testing.T) {
	m := NewMetrics()
	m.PublishErrors.WithLabelValues("small", "publish").Inc()
	m.PublishErrors.WithLabelValues("small", "publish").Inc()
	m.PublishErrors.WithLabelValues("small", "gatekeeper").Inc()
	m.PublishErrors.WithLabelValues("large", "publish").Inc()
	// By reason=publish: two "small" + one "large" = 3
	assert.Equal(t, float64(3), counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "publish"))
	// By reason=gatekeeper: one
	assert.Equal(t, float64(1), counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "gatekeeper"))
	// Unknown label value
	assert.Equal(t, float64(0), counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "nope"))
}

func TestWriteCSVFile_RoundTrip(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("r-1", "m-1", now)
	c.RecordReply("r-1", now.Add(5*time.Millisecond))
	c.RecordBroadcast("m-1", now.Add(8*time.Millisecond))

	path := filepath.Join(t.TempDir(), "out.csv")
	require.NoError(t, writeCSVFile(path, c))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	// Header present
	require.True(t, strings.HasPrefix(out, "row_index,request_id,metric,latency_ns"))
	// At least one E1 row and one E2 row
	require.Contains(t, out, ",E1,")
	require.Contains(t, out, ",E2,")
}

func TestWriteCSVFile_EmptyCollector(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")

	path := filepath.Join(t.TempDir(), "empty.csv")
	require.NoError(t, writeCSVFile(path, c))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	// Header still present, no data rows
	require.True(t, strings.HasPrefix(out, "row_index,request_id,metric,latency_ns"))
	require.NotContains(t, out, ",E1,")
	require.NotContains(t, out, ",E2,")
}

func TestNewNatsCorePublisher_CanonicalSetsUseJetStream(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	require.True(t, p.useJetStream)
}

func TestNewNatsCorePublisher_FrontdoorDoesNotSetUseJetStream(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectFrontdoor, nil)
	require.False(t, p.useJetStream)
}

func TestNewNatsCorePublisher_FieldWiring(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	assert.Nil(t, p.pool)
	assert.Nil(t, p.js)
	assert.True(t, p.useJetStream)

	p2 := newNatsCorePublisher(nil, InjectFrontdoor, nil)
	assert.Nil(t, p2.pool)
	assert.Nil(t, p2.js)
	assert.False(t, p2.useJetStream)
}

func TestMetricsHandler_ServesOpenMetrics(t *testing.T) {
	m := NewMetrics()
	m.Published.WithLabelValues("small", "measured", "0", "100-200").Inc()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "loadgen_published_total")
}

func TestMetricsHandler_ContentType(t *testing.T) {
	m := NewMetrics()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	ct := rec.Header().Get("Content-Type")
	require.NotEmpty(t, ct)
	// Prometheus text format
	require.Contains(t, ct, "text/plain")
}

func TestDrainTrailingReplies_ReturnsImmediatelyWhenAlreadyDrained(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	// No publishes; outstanding count is 0 from the start.
	start := time.Now()
	drainTrailingReplies(c, 5*time.Second, 10*time.Millisecond, 3)
	elapsed := time.Since(start)
	// Should return after the first ticker fire (≤ 20ms) since outstanding==0.
	assert.Less(t, elapsed, 50*time.Millisecond, "drain must return fast when already drained")
}

func TestDrainTrailingReplies_TimesOutWhenStuck(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	// Add 5 outstanding correlations; never complete them.
	t0 := time.Unix(100, 0)
	for i := 0; i < 5; i++ {
		c.RecordPublish("r-"+string(rune('A'+i)), "m-"+string(rune('A'+i)), t0)
	}
	// outstanding stays at 10 (5 byReqID + 5 byMsgID); stable for 3 ticks
	// at 10ms intervals → drain returns after ~30ms via the stable-tick
	// detector, NOT the maxWait deadline.
	start := time.Now()
	drainTrailingReplies(c, 200*time.Millisecond, 10*time.Millisecond, 3)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 100*time.Millisecond,
		"stable-tick detector should fire well before maxWait")
}

func TestDrainTrailingReplies_HonorsMaxWait(t *testing.T) {
	// With stableTicks much larger than the interval, the loop will hit
	// the deadline before stabilizing.
	m := NewMetrics()
	c := NewCollector(m, "test")
	c.RecordPublish("r-1", "m-1", time.Unix(0, 0))
	start := time.Now()
	drainTrailingReplies(c, 30*time.Millisecond, 5*time.Millisecond, 100)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 25*time.Millisecond)
	assert.Less(t, elapsed, 100*time.Millisecond, "must not exceed maxWait by much")
}

func TestConsumerSnapshots_BuildsFromSamplers(t *testing.T) {
	// Build snapshots from empty samplers slice → empty result.
	got := consumerSnapshots(nil)
	assert.Empty(t, got)
}

func TestGatheredCounterValue_NoMatch(t *testing.T) {
	m := NewMetrics()
	m.Published.WithLabelValues("p", "measured", "0", "100-200").Inc()
	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	// Unknown metric name → 0.
	assert.Equal(t, float64(0), gatheredCounterValue(mfs, "nonexistent", "", ""))
	// Known metric, unmatched label → 0.
	assert.Equal(t, float64(0),
		gatheredCounterValue(mfs, "loadgen_published_total", "preset", "nope"))
}

func TestDispatch_UnknownSubcommand(t *testing.T) {
	// Save + restore os.Args; inject a bad subcommand and assert exit code 2.
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{"loadgen", "nonsense-subcommand"}
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := dispatch(t.Context(), cfg)
	assert.Equal(t, 2, code, "unknown subcommand must exit 2")
}

func TestRunSeed_MissingPresetFlag(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runSeed(t.Context(), cfg, []string{})
	assert.Equal(t, 2, code, "missing --preset must exit 2")
}

func TestRunSeed_UnknownPreset(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runSeed(t.Context(), cfg, []string{"--preset=nonexistent"})
	assert.Equal(t, 2, code, "unknown preset must exit 2")
}

func TestRunRun_MissingPresetFlag(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg, []string{})
	assert.Equal(t, 2, code, "missing --preset must exit 2")
}

func TestRunRun_UnknownScenario(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg, []string{"--preset=small", "--scenario=bogus"})
	assert.Equal(t, 2, code, "unknown scenario must exit 2")
}

func TestRunRun_UnknownInject(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg, []string{"--preset=small", "--inject=sideways"})
	assert.Equal(t, 2, code, "unknown inject mode must exit 2")
}

func TestRunRun_UnknownPreset(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg, []string{"--preset=nonexistent"})
	assert.Equal(t, 2, code, "unknown preset must exit 2")
}

func TestRunRun_BadRampShape(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg,
		[]string{"--preset=small", "--ramp-from=10", "--ramp-to=100", "--ramp-duration=5s", "--ramp-shape=logarithmic"})
	assert.Equal(t, 2, code, "bad ramp shape must exit 2")
}

func TestRunRun_PartialRamp(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg,
		[]string{"--preset=small", "--ramp-from=10", "--ramp-duration=5s"}) // missing --ramp-to
	assert.Equal(t, 2, code, "partial ramp config must exit 2")
}

func TestRunRun_RampAndRateConflict(t *testing.T) {
	cfg := &config{NatsURL: "nats://stub", MongoURI: "mongodb://stub"}
	code := runRun(t.Context(), cfg,
		[]string{"--preset=small", "--rate=500", "--ramp-from=10", "--ramp-to=100", "--ramp-duration=5s"})
	assert.Equal(t, 2, code, "rate + ramp must exit 2")
}

func TestRunSeed_MongoDBGuardRefusesNonLoadgenDB(t *testing.T) {
	cfg := &config{
		NatsURL:  "nats://stub",
		MongoURI: "mongodb://stub",
		MongoDB:  "chat", // production-like name; guard must refuse
	}
	code := runSeed(t.Context(), cfg, []string{"--preset=small"})
	assert.Equal(t, 2, code, "guard must refuse seeding into non-loadgen DB")
}

// The override-bypass path is unit-tested at the guard level via
// TestGuardMongoDB_OverrideBypasses. Exercising it through runSeed
// would block on mongoutil.Connect's ~30s timeout for the stub URI;
// not worth the wall-clock for the additional signal.

func TestRunTeardown_MongoDBGuardRefuses(t *testing.T) {
	cfg := &config{
		NatsURL:  "nats://stub",
		MongoURI: "mongodb://stub",
		MongoDB:  "chat",
	}
	code := runTeardown(t.Context(), cfg)
	assert.Equal(t, 2, code, "guard must refuse teardown of non-loadgen DB")
}
