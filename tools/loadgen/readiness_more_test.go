package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestScenarioNeedsReadiness_Table(t *testing.T) {
	cases := []struct {
		scenario string
		want     bool
	}{
		{"history-read", true},
		{"search-read", true},
		{"room-rpc", true},
		{"messaging-pipeline", false},
		{"unknown-scenario", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			assert.Equal(t, tc.want, scenarioNeedsReadiness(tc.scenario))
		})
	}
}

func TestBuildReadinessProbe_AllScenariosReturnNonNil(t *testing.T) {
	sub := &model.Subscription{
		User:   model.SubscriptionUser{ID: "u-1", Account: "user-1"},
		RoomID: "room-1",
		SiteID: "site-local",
	}
	for _, scenario := range []string{"history-read", "search-read", "room-rpc"} {
		t.Run(scenario, func(t *testing.T) {
			fn := buildReadinessProbe(scenario, sub, "site-local", &recordingRequester{})
			require.NotNil(t, fn, "probe fn must be non-nil for %s", scenario)
		})
	}
}

func TestBuildReadinessProbe_UnknownScenarioReturnsNopFn(t *testing.T) {
	sub := &model.Subscription{
		User:   model.SubscriptionUser{ID: "u-1", Account: "user-1"},
		RoomID: "room-1",
		SiteID: "site-local",
	}
	fn := buildReadinessProbe("unknown", sub, "site-local", &recordingRequester{})
	require.NotNil(t, fn)
	// The nop returns nil (success) without ever calling the requester.
	rr := &recordingRequester{}
	fn2 := buildReadinessProbe("unknown", sub, "site-local", rr)
	require.NoError(t, fn2(t.Context()))
	assert.Equal(t, 0, rr.count(), "unknown scenario probe must not call the requester")
}
