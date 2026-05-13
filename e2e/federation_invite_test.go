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

// TestFederation_CrossSiteInvite is the headline federation test in v1
// (per R1 12.A, the original cross-site MESSAGE test was dropped because
// messages don't federate today).
//
// Flow:
//  1. bob authenticates on siteB FIRST so mongo-b records him as siteId=siteB.
//  2. Harness seeds bob into mongo-a's users collection with siteId=siteB
//     (per R1 12.D -- this simulates the production user-replication path
//     that's out of scope for this suite).
//  3. alice (on siteA) creates a channel and invites bob (a siteB user).
//  4. room-worker-a publishes outbox.siteA.to.siteB.member_added.
//  5. The gateway-mediated INBOX_siteB Source pulls it onto siteB.
//  6. inbox-worker-b consumes it and writes a subscription record to mongo-b.
//  7. Poll mongo-b's subscriptions for bob+roomID; assert it appears with
//     the expected fields.
func TestFederation_CrossSiteInvite(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	stack := stack
	harness.CaptureLogs(t, stack,
		"room-worker-a", "inbox-worker-b", "broadcast-worker-b")

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	// Per-test ephemeral siteB user so this test can run in parallel
	// with other federation tests without contention on a shared bob.
	ephemeral := stack.SiteB.MintEphemeralUser(t, ctx)
	bobOnB := stack.SiteB.Authenticate(t, ctx, ephemeral)

	// Per R1 12.D: simulate cross-site user discovery by seeding mongo-a's
	// users collection. Without this, room-worker-a's invite path would
	// either reject bob (unknown user) or route him to siteA's outbox path.
	stack.SiteA.SeedRemoteUser(t, ctx, bobOnB.Account, stack.SiteB.SiteID)

	// alice creates a channel and invites bob (siteB) in one shot.
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
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, stack.SiteA), asSiteDB(t, stack.SiteB)}, roomID)
	require.NotEmpty(t, roomID)
	t.Logf("alice created room %s on siteA; invited bob on siteB", roomID)

	// Poll mongo-b's subscriptions collection for bob+roomID. The field is
	// nested under `u.account` in the subscription document, not flat
	// `account` -- see pkg/model.Subscription (the Mongo bson tag is `u`).
	subs := stack.SiteB.MongoDB(t).Collection("subscriptions")
	filter := bson.M{"u.account": bobOnB.Account, "roomId": roomID}
	require.Eventually(t, func() bool {
		var sub map[string]any
		return subs.FindOne(ctx, filter).Decode(&sub) == nil
	}, 30*time.Second, 250*time.Millisecond,
		"bob's subscription on roomID=%s never appeared on siteB", roomID)

	var sub map[string]any
	require.NoError(t, subs.FindOne(ctx, filter).Decode(&sub))
	assert.Equal(t, roomID, sub["roomId"])
	// `siteId` on a subscription record reflects the room's home site
	// (siteA for this test), letting downstream consumers route correctly.
	assert.Equal(t, stack.SiteA.SiteID, sub["siteId"])
}

// TestFederation_NegativeIsolation_StateStaysSiteLocal per R2.C item 6 (revised
// from the original "subject-silent-on-B" check after live-run discovery):
//
// The original conception was that `chat.user.{account}.update` on siteB
// would NOT fire for a siteA-local invite. Live testing showed that broadcast
// NATS subjects ARE gateway-federated by design -- subject interest propagates
// regardless of the publishing site. So observing siteB's NATS is not a
// meaningful negative-isolation signal.
//
// The MEANINGFUL boundary is mongo state: a siteA-local invite must NOT
// produce a subscription row in mongo-b. mongo state is per-site and never
// federated; that's the actual isolation contract.
//
// Catches the regression class "inbox-worker-b receives + materializes events
// that should have stayed siteA-local."
func TestFederation_NegativeIsolation_StateStaysSiteLocal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	stack := stack
	harness.CaptureLogs(t, stack, "room-worker-a", "inbox-worker-b")

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	// Ephemeral siteA-local user -- this test asserts NO cross-site
	// materialization, so a fresh per-test account avoids contention.
	ephemeral := stack.SiteA.MintEphemeralUser(t, ctx)
	bob := stack.SiteA.Authenticate(t, ctx, ephemeral) // siteA-local user

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, stack.SiteA.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, stack.SiteA), asSiteDB(t, stack.SiteB)}, roomID)
	require.NotEmpty(t, roomID)

	// Wait for siteA's room-worker to finish (the local subscription should
	// appear on mongo-a). This gives any cross-site path enough time to also
	// fire if it WERE going to misbehave.
	awaitSubscription(t, ctx, stack.SiteA.MongoDB(t), bob.Account, roomID)

	// Mongo-b must NEVER receive a subscription for this room (it's siteA-local).
	// require.Never is deterministic: it polls for the full duration and fails
	// fast if a sub ever appears, instead of relying on a single post-Sleep
	// check that an event arriving at 1.5s would silently slip past.
	subsB := stack.SiteB.MongoDB(t).Collection("subscriptions")
	require.Never(t, func() bool {
		count, err := subsB.CountDocuments(ctx, bson.M{
			"u.account": bob.Account,
			"roomId":    roomID,
		})
		return err == nil && count > 0
	}, 5*time.Second, 250*time.Millisecond,
		"mongo-b must have no subscription for siteA-local room %s", roomID)
}
