package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
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
	// PublishErrors now has 3 labels: {preset, phase, reason}.
	m.PublishErrors.WithLabelValues("small", "measured", "publish").Inc()
	m.PublishErrors.WithLabelValues("small", "measured", "publish").Inc()
	m.PublishErrors.WithLabelValues("small", "measured", "gatekeeper").Inc()
	m.PublishErrors.WithLabelValues("large", "measured", "publish").Inc()
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
	require.NoError(t, writeCSVFile(path, "test-run-id", c))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	// Header present
	require.True(t, strings.HasPrefix(out, "run_id,row_index,request_id,metric,latency_ns"))
	// At least one E1 row and one E2 row
	require.Contains(t, out, ",E1,")
	require.Contains(t, out, ",E2,")
}

func TestWriteCSVFile_EmptyCollector(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")

	path := filepath.Join(t.TempDir(), "empty.csv")
	require.NoError(t, writeCSVFile(path, "test-run-id", c))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := string(data)
	// Header still present, no data rows
	require.True(t, strings.HasPrefix(out, "run_id,row_index,request_id,metric,latency_ns"))
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

// S5: a publisher built with async enabled records the flag so the
// publish path can branch onto js.PublishMsgAsync. The constructor
// leaves asyncJS off; runtime opts the caller in based on the
// --js-async-max-pending flag in main.go (default 4096 → async on).
func TestNewNatsCorePublisher_AsyncDefaultsOff(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	assert.False(t, p.asyncJS, "constructor must leave asyncJS off; runtime wiring opts in via --js-async-max-pending")
}

func TestNatsCorePublisher_WithAsyncFlipsFlag(t *testing.T) {
	p := newNatsCorePublisher(nil, InjectCanonical, nil)
	p.asyncJS = true
	assert.True(t, p.asyncJS)
}

// S5: jetstream client construction should request a non-zero
// PublishAsyncMaxPending bound when async mode is enabled. Validate the
// helper that builds the jetstream options list rather than the full
// jetstream.New plumbing (which needs a live nats.Conn).
func TestJetStreamPublishOpts_AsyncEnabled(t *testing.T) {
	opts := jetstreamPublishOpts(4096, NewMetrics(), "small", nil)
	assert.Len(t, opts, 2, "want both MaxPending and ErrHandler when async is enabled")
}

func TestJetStreamPublishOpts_AsyncDisabledIsEmpty(t *testing.T) {
	// 0 means "leave async pending at the nats.go default and skip the
	// err handler"; effectively disables the S5 path.
	opts := jetstreamPublishOpts(0, NewMetrics(), "small", nil)
	assert.Empty(t, opts, "zero MaxPending must produce no jetstream opts so callers fall back to sync publishes")
}

// S5: the async err handler is the entire correctness story for
// reporting failed publishes. Validate it directly.
//
// On invocation it MUST:
//  1. bump loadgen_publish_errors_total{preset, reason="async_ack"}
//  2. evict the orphan messageID from the collector so it doesn't
//     inflate Finalize's MissingBroadcasts count.
func TestNewAsyncErrHandler_BumpsMetricAndEvictsOrphan(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	// Seed the collector with a publish so we can confirm eviction.
	t0 := time.Unix(100, 0)
	c.RecordPublishBroadcastOnly("msg-abc", t0)
	require.Contains(t, c.MessageIDs(), "msg-abc",
		"precondition: collector knows about msg-abc")

	h := newAsyncErrHandler(m, "small", c)

	// Canonical event payload — handler's JSON stub targets {"message":{"id":...}}.
	payload := []byte(`{"message":{"id":"msg-abc","roomId":"r1"},"siteId":"s1","timestamp":1}`)
	h(nil, &nats.Msg{Data: payload}, errors.New("simulated async ack failure"))

	// Metric bumped.
	got := counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "async_ack")
	assert.Equal(t, 1.0, got, "must increment PublishErrors{reason=async_ack} on async failure")

	// Orphan evicted from the broadcast correlation map AND from the
	// seenMessageIDs pool (so auto-warmup doesn't hand a dead ID to
	// downstream history-read scenarios).
	assert.NotContains(t, c.MessageIDs(), "msg-abc",
		"async err handler must evict orphan messageID via RecordPublishFailed")
}

func TestNewAsyncErrHandler_NilCollectorIsSafe(t *testing.T) {
	m := NewMetrics()
	h := newAsyncErrHandler(m, "small", nil)
	// Must not panic when collector is nil (defensive — production wires
	// it but tests / future callers might not).
	require.NotPanics(t, func() {
		h(nil, &nats.Msg{Data: []byte(`{"message":{"id":"x"}}`)}, errors.New("x"))
	})
	got := counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "async_ack")
	assert.Equal(t, 1.0, got, "metric still bumps even when collector is nil")
}

func TestNewAsyncErrHandler_MalformedPayloadDoesNotPanic(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	h := newAsyncErrHandler(m, "small", c)
	require.NotPanics(t, func() {
		h(nil, &nats.Msg{Data: []byte(`not json`)}, errors.New("x"))
		h(nil, &nats.Msg{Data: nil}, errors.New("x"))
		h(nil, nil, errors.New("x"))
	})
	got := counterValueLabeled(m, "loadgen_publish_errors_total", "reason", "async_ack")
	assert.Equal(t, 3.0, got, "metric bumps on every invocation regardless of payload shape")
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

func TestGatheredCounterLabelPair(t *testing.T) {
	m := NewMetrics()
	// PublishErrors: {preset, phase, reason}.
	m.PublishErrors.WithLabelValues("small", "measured", "gatekeeper").Inc()
	m.PublishErrors.WithLabelValues("small", "measured", "gatekeeper").Inc()
	m.PublishErrors.WithLabelValues("small", "warmup", "gatekeeper").Inc()   // warmup; must not count
	m.PublishErrors.WithLabelValues("small", "measured", "async_ack").Inc()  // different reason
	m.PublishErrors.WithLabelValues("large", "measured", "gatekeeper").Inc() // different preset

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)

	// Filter: phase="measured" AND reason="gatekeeper" across all presets.
	// Expects small(2) + large(1) = 3; warmup row must be excluded.
	got := gatheredCounterLabelPair(mfs, "loadgen_publish_errors_total", "phase", "measured", "reason", "gatekeeper")
	assert.Equal(t, float64(3), got, "should count only phase=measured,reason=gatekeeper rows")

	// Verify warmup IS in the counter. Get all gatekeeper entries regardless of phase.
	// This should be 4 (2 measured small + 1 warmup small + 1 measured large = 4 total for reason=gatekeeper).
	gotAllPhases := gatheredCounterValue(mfs, "loadgen_publish_errors_total", "reason", "gatekeeper")
	assert.Greater(t, gotAllPhases, got, "phase filter must exclude warmup-phase samples (unfiltered total > measured-only)")

	// phase=warmup filter gives exactly the warmup row.
	gotWarmup := gatheredCounterLabelPair(mfs, "loadgen_publish_errors_total", "phase", "warmup", "reason", "gatekeeper")
	assert.Equal(t, float64(1), gotWarmup)

	// Unmatched pair → 0.
	assert.Equal(t, float64(0), gatheredCounterLabelPair(mfs, "loadgen_publish_errors_total", "phase", "measured", "reason", "nope"))
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

// Bug 6: --abort-window-max-samples default of 10_000 deafens the abort
// watcher at default --rate=500 × default --abort-p99-sustain=30s
// (= 15_000 samples needed). Auto-size the cap when the user did not
// pass the flag explicitly; honor an explicit --abort-window-max-samples
// verbatim, including 0 (disabled).
func TestResolveAbortWindowMaxSamples(t *testing.T) {
	cases := []struct {
		name       string
		userSet    bool
		userVal    int
		peakRPS    int
		maxSustain time.Duration
		want       int
	}{
		{
			name:    "default rate + default sustain auto-sized to >= peak*sustain",
			userSet: false, userVal: 10000,
			peakRPS: 500, maxSustain: 30 * time.Second,
			// 500 * 30s = 15000; 2x headroom = 30000.
			want: 30000,
		},
		{
			name:    "high-rate run",
			userSet: false, userVal: 10000,
			peakRPS: 5000, maxSustain: 60 * time.Second,
			want: 600000,
		},
		{
			name:    "explicit user value preserved verbatim",
			userSet: true, userVal: 10000,
			peakRPS: 5000, maxSustain: 60 * time.Second,
			want: 10000,
		},
		{
			name:    "explicit user value 0 (disabled) preserved",
			userSet: true, userVal: 0,
			peakRPS: 5000, maxSustain: 60 * time.Second,
			want: 0,
		},
		{
			name:    "explicit user value above auto-size preserved",
			userSet: true, userVal: 1000000,
			peakRPS: 500, maxSustain: 30 * time.Second,
			want: 1000000,
		},
		{
			name:    "low-rate run gets a tighter auto-size",
			userSet: false, userVal: 10000,
			peakRPS: 100, maxSustain: 5 * time.Second,
			// 2 * 100 * 5 = 1000 — wasted memory to inflate further.
			want: 1000,
		},
		{
			name:    "zero peak rate keeps the registered default",
			userSet: false, userVal: 10000,
			peakRPS: 0, maxSustain: 30 * time.Second,
			// no traffic to bound: keep the registered default.
			want: 10000,
		},
		{
			name:    "zero sustain keeps the default",
			userSet: false, userVal: 10000,
			peakRPS: 500, maxSustain: 0,
			want: 10000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAbortWindowMaxSamples(tc.userSet, tc.userVal, tc.peakRPS, tc.maxSustain)
			assert.Equal(t, tc.want, got)
		})
	}
}

// Bug 6 wiring: peakRPS picks the higher of --rate vs --ramp-to so a
// ramp that climbs above the static rate isn't deafened mid-run.
func TestResolveAbortPeakRPS(t *testing.T) {
	cases := []struct {
		name         string
		rate, rampTo int
		want         int
	}{
		{"no ramp, use rate", 500, 0, 500},
		{"ramp-to higher than rate", 500, 2000, 2000},
		{"rate higher than ramp-to", 1000, 200, 1000},
		{"both zero", 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveAbortPeakRPS(tc.rate, tc.rampTo))
		})
	}
}

// Bug 2: actualRate must be computed per-scenario.
//
//   - messaging-pipeline + frontdoor: matched-replies + missing-replies / measured
//   - messaging-pipeline + canonical: sentMeasured / measured (no RPC traffic)
//   - read scenarios:                 sum of post-warmup request counts / measured
//
// Pre-fix the read path was forced through the messaging branch and read
// 0 (no E1 traffic); fix routes by scenario classification.
func TestComputeActualRate(t *testing.T) {
	mkStat := func(scenario, kind string, count int) RequestStat {
		return RequestStat{Scenario: scenario, Kind: kind, Count: count}
	}
	cases := []struct {
		name           string
		scenario       string
		inject         InjectMode
		measured       time.Duration
		e1Count        int
		missingReplies int
		sentMeasured   int
		requestStats   []RequestStat
		want           float64
	}{
		{
			name:     "messaging frontdoor uses E1+missing",
			scenario: "messaging-pipeline", inject: InjectFrontdoor,
			measured: 10 * time.Second, e1Count: 80, missingReplies: 20, sentMeasured: 999,
			want: 10.0,
		},
		{
			name:     "messaging canonical falls back to sentMeasured",
			scenario: "messaging-pipeline", inject: InjectCanonical,
			measured: 10 * time.Second, e1Count: 0, missingReplies: 0, sentMeasured: 250,
			want: 25.0,
		},
		{
			name:     "history-read sums request stats across kinds",
			scenario: "history-read", inject: InjectFrontdoor,
			measured: 10 * time.Second,
			requestStats: []RequestStat{
				mkStat("history-read", "history", 200),
				mkStat("history-read", "page", 50),
			},
			want: 25.0,
		},
		{
			name:     "search-read with auto-warmup canonical inject still uses request stats",
			scenario: "search-read", inject: InjectCanonical,
			measured: 5 * time.Second,
			requestStats: []RequestStat{
				mkStat("search-read", "search", 100),
			},
			want: 20.0,
		},
		{
			name:     "room-rpc uses request stats",
			scenario: "room-rpc", inject: InjectFrontdoor,
			measured: 4 * time.Second,
			requestStats: []RequestStat{
				mkStat("room-rpc", "create", 40),
			},
			want: 10.0,
		},
		{
			name:     "zero measured returns zero",
			scenario: "messaging-pipeline", inject: InjectFrontdoor,
			measured: 0, e1Count: 100, missingReplies: 0,
			want: 0.0,
		},
		{
			name:     "negative measured returns zero",
			scenario: "history-read", inject: InjectFrontdoor,
			measured:     -1 * time.Second,
			requestStats: []RequestStat{mkStat("history-read", "history", 100)},
			want:         0.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeActualRate(tc.scenario, tc.inject, tc.measured,
				tc.e1Count, tc.missingReplies, tc.sentMeasured, tc.requestStats)
			assert.InDelta(t, tc.want, got, 0.0001)
		})
	}
}
