package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func subscribersByRoom(f *Fixtures) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for i := range f.Subscriptions {
		s := &f.Subscriptions[i]
		if out[s.RoomID] == nil {
			out[s.RoomID] = map[string]bool{}
		}
		out[s.RoomID][s.User.ID] = true
	}
	return out
}

func TestBuildThreadFixtures_Deterministic(t *testing.T) {
	p, ok := BuiltinPreset("medium")
	require.True(t, ok)

	a := BuildThreadFixtures(&p, 42, 3, "site-a")
	b := BuildThreadFixtures(&p, 42, 3, "site-a")
	assert.Equal(t, a.ParentsByRoom, b.ParentsByRoom)
}

func TestBuildThreadFixtures_ParentsPerRoomAndOwnership(t *testing.T) {
	p, ok := BuiltinPreset("medium")
	require.True(t, ok)

	tf := BuildThreadFixtures(&p, 42, 4, "site-a")
	require.NotEmpty(t, tf.Subscriptions)

	subs := subscribersByRoom(&tf.Fixtures)
	for _, room := range tf.Rooms {
		parents := tf.ParentsByRoom[room.ID]
		require.Len(t, parents, 4, "room %s parent count", room.ID)
		for _, pm := range parents {
			require.Len(t, pm.MessageID, 20, "message id length")
			assert.True(t, subs[room.ID][pm.SenderID],
				"parent sender %s must subscribe to room %s", pm.SenderID, room.ID)
		}
	}
}

func TestBuildThreadFixtures_EverySeededRoomHasParents(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)

	tf := BuildThreadFixtures(&p, 7, 2, "site-a")
	subs := subscribersByRoom(&tf.Fixtures)
	for roomID := range subs {
		assert.GreaterOrEqual(t, len(tf.ParentsByRoom[roomID]), 1,
			"room %s has subscribers but no parents", roomID)
	}
}

func TestBuildThreadFixtures_DefaultParentsPerRoom(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	tf := BuildThreadFixtures(&p, 1, 0, "site-a") // 0 => defaultParentsPerRoom
	assert.Equal(t, defaultParentsPerRoom, tf.ParentsPerRoom)
	require.NotEmpty(t, tf.ParentsByRoom)
	for _, parents := range tf.ParentsByRoom {
		assert.Len(t, parents, defaultParentsPerRoom)
	}
}

func TestBuildThreadFixtures_SenderEngNameMatchesUser(t *testing.T) {
	p, ok := BuiltinPreset("medium")
	require.True(t, ok)
	tf := BuildThreadFixtures(&p, 42, 3, "site-a")

	engByID := map[string]string{}
	for i := range tf.Users {
		engByID[tf.Users[i].ID] = tf.Users[i].EngName
	}
	for _, parents := range tf.ParentsByRoom {
		for _, pm := range parents {
			assert.Equal(t, engByID[pm.SenderID], pm.SenderEngName,
				"SenderEngName must match the user's EngName for sender %s", pm.SenderID)
		}
	}
}
