package seed

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestDecoded_Counts(t *testing.T) {
	users, rooms, subs, err := Decoded()
	require.NoError(t, err)
	assert.Len(t, users, 4, "expected 4 seed users")
	assert.Len(t, rooms, 2, "expected 2 seed rooms")
	assert.Len(t, subs, 4, "expected 4 seed subscriptions")
}

func TestDecoded_UserShapeSatisfiesRoomServiceGuard(t *testing.T) {
	users, _, _, err := Decoded()
	require.NoError(t, err)

	// room-service/handler.go:186-188 requires non-empty EngName + ChineseName.
	// Every seeded user must clear this guard or scenarios fail with errInvalidUserData.
	for _, u := range users {
		assert.NotEmpty(t, u.ID, "user %q missing ID", u.Account)
		assert.NotEmpty(t, u.Account, "user with ID %q missing account", u.ID)
		assert.NotEmpty(t, u.EngName, "user %q missing EngName (room-service handler.go:186 guard)", u.Account)
		assert.NotEmpty(t, u.ChineseName, "user %q missing ChineseName (room-service handler.go:186 guard)", u.Account)
		assert.Equal(t, "site-local", u.SiteID, "user %q wrong siteId", u.Account)
		assert.Equal(t, "u-"+u.Account, u.ID, "user %q ID violates u-<account> convention", u.Account)
	}
}

func TestDecoded_RoleMatrix(t *testing.T) {
	_, _, subs, err := Decoded()
	require.NoError(t, err)

	type fact struct{ account, room, role string }
	got := map[fact]bool{}
	for _, s := range subs {
		for _, r := range s.Roles {
			got[fact{s.User.Account, s.RoomID, string(r)}] = true
		}
	}

	// The locked role matrix per §3.0 of the corrections spec:
	want := []fact{
		{"alice", "r-engineering", "owner"},
		{"alice", "r-design", "member"},
		{"bob", "r-engineering", "member"},
		{"carol", "r-design", "owner"},
	}
	for _, f := range want {
		assert.True(t, got[f], "missing role fact: %s is %s of %s", f.account, f.role, f.room)
	}

	// Dave is in no rooms — confirm no subscription mentions him.
	for _, s := range subs {
		assert.NotEqual(t, "dave", s.User.Account, "dave should have no subscriptions (in-no-rooms fixture)")
	}
}

func TestDecoded_RoomShape(t *testing.T) {
	_, rooms, _, err := Decoded()
	require.NoError(t, err)
	for _, r := range rooms {
		assert.Equal(t, model.RoomTypeChannel, r.Type, "room %q should be channel", r.ID)
		assert.Equal(t, "site-local", r.SiteID)
		assert.NotZero(t, r.CreatedAt, "room %q missing createdAt", r.ID)
		assert.Equal(t, "r-"+r.Name, r.ID, "room %q ID violates r-<slug> convention", r.ID)
	}
}

func TestDecoded_SubscriptionShape(t *testing.T) {
	_, _, subs, err := Decoded()
	require.NoError(t, err)
	for _, s := range subs {
		assert.NotEmpty(t, s.ID)
		assert.NotEmpty(t, s.User.ID)
		assert.NotEmpty(t, s.User.Account)
		assert.NotEmpty(t, s.RoomID)
		assert.Equal(t, "site-local", s.SiteID)
		assert.NotEmpty(t, s.Roles, "subscription %q has no roles", s.ID)
		assert.Equal(t, "sub-"+s.User.Account+"-"+s.RoomID, s.ID, "subscription %q ID violates sub-<account>-<room> convention", s.ID)
	}
}
