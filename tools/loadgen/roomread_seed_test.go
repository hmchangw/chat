package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRoomReadFixtures_StampsReadState(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	f := BuildRoomReadFixtures(&p, 42, "site-test", now)

	require.NotEmpty(t, f.Rooms)
	require.NotEmpty(t, f.Subscriptions)

	for i := range f.Rooms {
		require.NotNil(t, f.Rooms[i].LastMsgAt, "room %s LastMsgAt", f.Rooms[i].ID)
		assert.True(t, f.Rooms[i].LastMsgAt.After(now), "room %s LastMsgAt must be after now", f.Rooms[i].ID)
	}

	lastMsgByRoom := map[string]time.Time{}
	for i := range f.Rooms {
		lastMsgByRoom[f.Rooms[i].ID] = *f.Rooms[i].LastMsgAt
	}
	for i := range f.Subscriptions {
		s := &f.Subscriptions[i]
		require.NotNil(t, s.LastSeenAt, "sub %s LastSeenAt", s.ID)
		assert.True(t, s.LastSeenAt.Before(lastMsgByRoom[s.RoomID]),
			"sub %s LastSeenAt must be before room LastMsgAt", s.ID)
	}

	wantMin := map[string]time.Time{}
	for i := range f.Subscriptions {
		s := &f.Subscriptions[i]
		if cur, ok := wantMin[s.RoomID]; !ok || s.LastSeenAt.Before(cur) {
			wantMin[s.RoomID] = *s.LastSeenAt
		}
	}
	for i := range f.Rooms {
		require.NotNil(t, f.Rooms[i].MinUserLastSeenAt, "room %s MinUserLastSeenAt", f.Rooms[i].ID)
		assert.True(t, f.Rooms[i].MinUserLastSeenAt.Equal(wantMin[f.Rooms[i].ID]),
			"room %s floor mismatch", f.Rooms[i].ID)
	}
}

func TestBuildRoomReadFixtures_FloorIsAchievedLowerBound(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	f := BuildRoomReadFixtures(&p, 42, "site-test", now)

	wantLastMsg := now.Add(roomReadFutureOffset)
	subsByRoom := map[string][]time.Time{}
	for i := range f.Subscriptions {
		s := &f.Subscriptions[i]
		subsByRoom[s.RoomID] = append(subsByRoom[s.RoomID], *s.LastSeenAt)
		// Each LastSeenAt sits within [lastMsgAt - spread, lastMsgAt - 1min].
		assert.False(t, s.LastSeenAt.After(wantLastMsg.Add(-time.Minute)),
			"sub %s LastSeenAt too late", s.ID)
		assert.False(t, s.LastSeenAt.Before(wantLastMsg.Add(-time.Duration(roomReadLastSeenSpreadMin)*time.Minute)),
			"sub %s LastSeenAt too early", s.ID)
	}
	for i := range f.Rooms {
		r := &f.Rooms[i]
		assert.True(t, r.LastMsgAt.Equal(wantLastMsg),
			"room %s LastMsgAt should equal now+offset exactly", r.ID)
		members := subsByRoom[r.ID]
		require.NotEmpty(t, members, "small preset rooms have members")
		achieved := false
		for _, ls := range members {
			assert.False(t, ls.Before(*r.MinUserLastSeenAt),
				"floor must be <= every member LastSeenAt")
			if ls.Equal(*r.MinUserLastSeenAt) {
				achieved = true
			}
		}
		assert.True(t, achieved, "room %s floor must equal some member's LastSeenAt", r.ID)
	}
}

func TestBuildRoomReadFixtures_Deterministic(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	a := BuildRoomReadFixtures(&p, 42, "site-test", now)
	b := BuildRoomReadFixtures(&p, 42, "site-test", now)

	require.Equal(t, len(a.Subscriptions), len(b.Subscriptions))
	for i := range a.Subscriptions {
		assert.True(t, a.Subscriptions[i].LastSeenAt.Equal(*b.Subscriptions[i].LastSeenAt),
			"sub %d LastSeenAt not deterministic", i)
	}
}
