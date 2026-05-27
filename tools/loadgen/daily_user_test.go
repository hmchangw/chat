package main

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUserState_StepTransitions(t *testing.T) {
	u := newUserState("u-1", "user-1", []string{"r-1"}, 42)
	u.activeProb = 0.5
	u.idleProb = 0.5
	r := rand.New(rand.NewSource(1))
	activeSeen, idleSeen := false, false
	for i := 0; i < 1000; i++ {
		u.step(r)
		if u.active {
			activeSeen = true
		} else {
			idleSeen = true
		}
	}
	require.True(t, activeSeen)
	require.True(t, idleSeen)
}

func TestPickAction_WeightsApproximatelyMatch(t *testing.T) {
	w := defaultActionWeights()
	r := rand.New(rand.NewSource(7))
	counts := map[actionKind]int{}
	const N = 100000
	for i := 0; i < N; i++ {
		counts[pickAction(r, w)]++
	}
	// Send should dominate (largest weight). Mute/Create should be rare.
	require.Greater(t, counts[actionSend], counts[actionReadReceipt])
	require.Greater(t, counts[actionReadReceipt], counts[actionScrollHistory])
	require.Less(t, counts[actionMuteToggle], counts[actionRoomCreate]+counts[actionMemberAdd]+10) // tiny
}

func TestActionRate_PerSecond(t *testing.T) {
	// daily-heavy: 60+25+3+5+0.5+0.2+0.2 = 93.9 actions/day = 0.00326/sec per user
	r := actionRatePerSecond(defaultActionWeights().totalPerDay(), 8*time.Hour)
	require.InDelta(t, 0.00326, r, 0.0002)
}
