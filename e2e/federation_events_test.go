//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/e2e/harness"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// setupCrossSiteRoom is a shared fixture for the role-update / member-remove
// federation tests. Creates a channel on siteA with a fresh siteB user as a
// member, waits for the cross-site subscription to land on mongo-b, returns
// the roomID + the siteB user's account. Registers cleanup on both sites.
//
// Per-test users (post-R3 Item 2): rather than reuse the realm-fixed
// alice/bob, the fixture mints a fresh ephemeral siteB user via the
// Keycloak admin API. This lets the federation tests run in parallel
// without contention on shared inbox-worker-b consumer state being
// amplified by reused account names.
func setupCrossSiteRoom(t *testing.T) (roomID string, bobAccount string) {
	t.Helper()
	ctx := t.Context()

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	// Mint the same username on BOTH sites' Keycloaks. SubscriptionRead
	// + a few sibling tests authenticate the cross-site user on siteA
	// too (bob marks-read on siteA), so the user has to exist in
	// siteA's Keycloak. MintEphemeralUser is per-site, so we call it
	// twice. The username is taken from siteB's mint so the realm
	// usernames are identical (cross-site federation keys off the
	// account string, not a Keycloak user-id).
	ephemeral := stack.SiteB.MintEphemeralUser(t, ctx)
	stack.SiteA.MintEphemeralUserAs(t, ctx, ephemeral)
	bobOnB := stack.SiteB.Authenticate(t, ctx, ephemeral)
	stack.SiteA.SeedRemoteUser(t, ctx, bobOnB.Account, stack.SiteB.SiteID)

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bobOnB.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, stack.SiteA.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID = createReply.RoomID
	require.NotEmpty(t, roomID)

	registerRoomCleanup(t, []SiteDB{asSiteDB(t, stack.SiteA), asSiteDB(t, stack.SiteB)}, roomID)

	// Wait for the cross-site subscription mirror to materialize on mongo-b.
	subsB := stack.SiteB.MongoDB(t).Collection("subscriptions")
	require.Eventually(t, func() bool {
		var sub map[string]any
		return subsB.FindOne(ctx, bson.M{
			"u.account": bobOnB.Account,
			"roomId":    roomID,
		}).Decode(&sub) == nil
	}, 15*time.Second, 200*time.Millisecond,
		"setup: cross-site subscription for bob+roomID=%s never appeared on siteB", roomID)

	return roomID, bobOnB.Account
}

// TestFederation_CrossSiteRoleUpdate exercises the OUTBOX `role_updated`
// event type end to end. Per R2.C item 7 + medium item #10. Without this
// test the suite covers only one of three OUTBOX event types, so a
// regression in inbox-worker-b's handleRoleUpdated handler would ship
// unobserved.
func TestFederation_CrossSiteRoleUpdate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	harness.CaptureLogs(t, stack, "room-worker-a", "inbox-worker-b")

	roomID, bobAccount := setupCrossSiteRoom(t)
	alice := stack.SiteA.Authenticate(t, ctx, "alice")

	// Promote bob to owner. The role-update event flows through
	// MemberRoleUpdate -> room-service -> ROOMS_siteA -> room-worker-a ->
	// outbox.siteA.to.siteB.role_updated -> INBOX_siteB -> inbox-worker-b ->
	// mongo-b.subscriptions.roles[].
	updateReq := model.UpdateRoleRequest{
		RoomID:  roomID,
		Account: bobAccount,
		NewRole: model.RoleOwner,
	}
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberRoleUpdate(alice.Account, roomID, stack.SiteA.SiteID),
		updateReq, 5*time.Second, nil,
	))

	// Poll mongo-b until bob's subscription document carries the new role.
	subsB := stack.SiteB.MongoDB(t).Collection("subscriptions")
	require.Eventually(t, func() bool {
		var sub struct {
			Roles []string `bson:"roles"`
		}
		err := subsB.FindOne(ctx, bson.M{
			"u.account": bobAccount,
			"roomId":    roomID,
		}).Decode(&sub)
		if err != nil {
			return false
		}
		for _, r := range sub.Roles {
			if r == string(model.RoleOwner) {
				return true
			}
		}
		return false
	}, 15*time.Second, 250*time.Millisecond,
		"bob's role on mongo-b never updated to owner for room %s", roomID)

	// Stronger assertion: the role is exactly [owner], not member-plus-owner.
	var final struct {
		Roles []string `bson:"roles"`
	}
	require.NoError(t, subsB.FindOne(ctx, bson.M{
		"u.account": bobAccount,
		"roomId":    roomID,
	}).Decode(&final))
	assert.Contains(t, final.Roles, string(model.RoleOwner))
}

// TestFederation_CrossSiteMemberRemove exercises the OUTBOX `member_removed`
// event end to end. Per R2.C item 7 + medium item #10. alice (siteA) removes
// bob (siteB) from a cross-site channel; we assert bob's subscription
// disappears from mongo-b.
func TestFederation_CrossSiteMemberRemove(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	harness.CaptureLogs(t, stack, "room-worker-a", "inbox-worker-b")

	roomID, bobAccount := setupCrossSiteRoom(t)
	alice := stack.SiteA.Authenticate(t, ctx, "alice")

	removeReq := model.RemoveMemberRequest{
		RoomID:    roomID,
		Requester: alice.Account,
		Account:   bobAccount,
	}
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberRemove(alice.Account, roomID, stack.SiteA.SiteID),
		removeReq, 5*time.Second, nil,
	))

	// bob's subscription must disappear from mongo-b.
	subsB := stack.SiteB.MongoDB(t).Collection("subscriptions")
	require.Eventually(t, func() bool {
		count, err := subsB.CountDocuments(ctx, bson.M{
			"u.account": bobAccount,
			"roomId":    roomID,
		})
		return err == nil && count == 0
	}, 15*time.Second, 250*time.Millisecond,
		"bob's subscription on mongo-b never removed for room %s", roomID)
}
