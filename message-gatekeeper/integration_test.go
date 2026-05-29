//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }

// TestMongoStore_GetSubscription_ExcludesTombstone verifies the sender
// access-control reader treats a soft-deleted (tombstoned) subscription as
// "not subscribed" so a removed user cannot post.
func TestMongoStore_GetSubscription_ExcludesTombstone(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "message_gatekeeper_test")
	store := NewMongoStore(db)

	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s-tomb", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
		RoomID: "r1", SiteID: "site-a", RoomType: model.RoomTypeChannel,
		Roles: []model.Role{model.RoleMember}, Deleted: true, MembershipEventTimestamp: 200,
	})
	require.NoError(t, err)

	_, err = store.GetSubscription(ctx, "alice", "r1")
	assert.ErrorIs(t, err, errNotSubscribed, "tombstoned sub must read as not-subscribed")

	// A sibling active sub for another account is still visible.
	_, err = db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s-active", User: model.SubscriptionUser{ID: "u-bob", Account: "bob"},
		RoomID: "r1", SiteID: "site-a", RoomType: model.RoomTypeChannel,
		Roles: []model.Role{model.RoleMember}, MembershipEventTimestamp: 100,
	})
	require.NoError(t, err)

	sub, err := store.GetSubscription(ctx, "bob", "r1")
	require.NoError(t, err)
	assert.Equal(t, "bob", sub.User.Account)
}
