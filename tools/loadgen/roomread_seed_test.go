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
