package main

import (
	"errors"
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
