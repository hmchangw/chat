package main

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseInjectMode(t *testing.T) {
	cases := []struct {
		in      string
		want    InjectMode
		wantErr bool
	}{
		{"frontdoor", InjectFrontdoor, false},
		{"canonical", InjectCanonical, false},
		{"", "", true},
		{"unknown", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseInjectMode(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseScenarioFlag(t *testing.T) {
	for _, scenario := range []string{"messaging-pipeline", "history-read", "search-read", "room-rpc"} {
		assert.NoError(t, parseScenarioFlag(scenario))
	}
	for _, bad := range []string{"", "unknown", "history", "messaging_pipeline"} {
		err := parseScenarioFlag(bad)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown scenario")
	}
}

func TestParseRampShape(t *testing.T) {
	l, err := parseRampShape("linear")
	require.NoError(t, err)
	assert.Equal(t, RampLinear, l)

	e, err := parseRampShape("exponential")
	require.NoError(t, err)
	assert.Equal(t, RampExponential, e)

	_, err = parseRampShape("logarithmic")
	require.Error(t, err)
}

func TestBuildRamp_NoRampReturnsNil(t *testing.T) {
	got, err := buildRamp(0, 0, 0, "linear")
	require.NoError(t, err)
	assert.Nil(t, got, "all-zero ramp fields → no ramp")
}

func TestBuildRamp_PartialFieldsErrors(t *testing.T) {
	_, err := buildRamp(100, 0, 5*time.Second, "linear")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRampFields))

	_, err = buildRamp(0, 1000, 5*time.Second, "linear")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRampFields))

	_, err = buildRamp(100, 1000, 0, "linear")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRampFields))
}

func TestBuildRamp_AllFieldsHappyPath(t *testing.T) {
	r, err := buildRamp(100, 1000, 10*time.Second, "linear")
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Equal(t, 100, r.From)
	assert.Equal(t, 1000, r.To)
	assert.Equal(t, 10*time.Second, r.Duration)
	assert.Equal(t, RampLinear, r.Shape)
}

func TestBuildRamp_BadShape(t *testing.T) {
	_, err := buildRamp(100, 1000, 10*time.Second, "logarithmic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown ramp shape")
}

func TestGuardMongoDB_Allowed(t *testing.T) {
	for _, db := range []string{"loadgen", "loadgen-site-a", "loadgen_test"} {
		assert.NoError(t, guardMongoDB(db, false), "DB %q should pass guard", db)
	}
}

func TestGuardMongoDB_Refused(t *testing.T) {
	for _, db := range []string{"chat", "production", "app_prod", "", "load-gen"} {
		err := guardMongoDB(db, false)
		require.Error(t, err, "DB %q should be refused", db)
		assert.True(t, errors.Is(err, ErrMongoDBNotIsolated))
	}
}

func TestGuardMongoDB_OverrideBypasses(t *testing.T) {
	assert.NoError(t, guardMongoDB("production", true),
		"override must allow operations on non-loadgen DBs")
}

func TestParseRunFlags_AllExistingFlags(t *testing.T) {
	args := []string{
		"--scenario=history-read", "--preset=medium", "--rate=750",
		"--duration=2m", "--warmup=15s", "--inject=frontdoor",
		"--abort-on-p99-ms=200", "--abort-p99-sustain=45s",
		"--abort-on-error-pct=0.01", "--abort-error-sustain=20s",
		"--abort-window-max-samples=20000",
		"--ramp-from=0", "--ramp-to=0", "--ramp-duration=0",
		"--auto-warmup=true", "--auto-warmup-rate=250",
		"--progress-interval=5s",
		"--skip-readiness=false", "--readiness-timeout=20s",
		"--liveness-interval=10s", "--liveness-failures=2",
		"--js-async-max-pending=2048",
		"--connections=4", "--csv=/tmp/x.csv",
		"--nats-creds-dir=", "--ramp-shape=linear",
	}
	rf, err := ParseRunFlags(args)
	require.NoError(t, err)
	assert.Equal(t, "history-read", rf.Scenario)
	assert.Equal(t, "medium", rf.Preset)
	assert.Equal(t, 750, rf.Rate)
	assert.Equal(t, 2*time.Minute, rf.Duration)
	assert.Equal(t, 200, rf.Abort.P99Ms)
	assert.Equal(t, 45*time.Second, rf.Abort.P99Sustain)
	assert.Equal(t, 20000, rf.Abort.WindowMaxSamples)
	assert.True(t, rf.Abort.WindowMaxSamplesSet, "passing --abort-window-max-samples must set WindowMaxSamplesSet=true")
	assert.Equal(t, 250, rf.AutoWarmup.Rate)
	assert.Equal(t, 4, rf.Conn.Connections)
}

// --max-in-flight overrides cfg.MaxInFlight when set > 0; 0 leaves the env
// default intact. The override is consumed in dispatch.go before NewRuntime
// builds runDeps, so this test pins only the flag-parse half of the contract.
func TestParseRunFlags_MaxInFlight(t *testing.T) {
	// Unset: zero value.
	rf, err := ParseRunFlags([]string{"--preset=small", "--scenario=messaging-pipeline"})
	require.NoError(t, err)
	assert.Equal(t, 0, rf.MaxInFlight, "unset --max-in-flight must read as 0 so dispatch keeps the env default")

	// Set: passes through.
	rf, err = ParseRunFlags([]string{"--preset=small", "--scenario=messaging-pipeline", "--max-in-flight=42"})
	require.NoError(t, err)
	assert.Equal(t, 42, rf.MaxInFlight)
}

// pickMaxInFlight (dispatch_members.go) and the identical override logic
// in dispatch.go must agree on precedence: positive flag wins, otherwise env.
func TestPickMaxInFlight_PrecedenceRule(t *testing.T) {
	assert.Equal(t, 200, pickMaxInFlight(0, 200), "0 flag must fall back to env default")
	assert.Equal(t, 50, pickMaxInFlight(50, 200), "positive flag must override env default")
	assert.Equal(t, 200, pickMaxInFlight(-1, 200), "negative flag must fall back; only >0 counts as set")
}

// --search-sync-skip-acl-bootstrap defaults off and is set when passed.
func TestParseRunFlags_SearchSyncSkipACLBootstrap(t *testing.T) {
	rf, err := ParseRunFlags([]string{"--preset=search-read", "--scenario=search-sync-lag"})
	require.NoError(t, err)
	assert.False(t, rf.SearchSync.SkipACLBootstrap, "default must be off — first-run-after-seed needs the bootstrap")

	rf, err = ParseRunFlags([]string{"--preset=search-read", "--scenario=search-sync-lag", "--search-sync-skip-acl-bootstrap"})
	require.NoError(t, err)
	assert.True(t, rf.SearchSync.SkipACLBootstrap)
}

func TestPrintRunHelp_MatchesGolden(t *testing.T) {
	var buf bytes.Buffer
	PrintRunHelp(&buf)
	got := buf.Bytes()
	goldenPath := "testdata/refactor-baseline/run-help.golden"
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "missing golden file %s", goldenPath)
	assert.Equal(t, string(want), string(got),
		"PrintRunHelp drifted from golden file %s; if intentional, regenerate the golden file", goldenPath)
}
