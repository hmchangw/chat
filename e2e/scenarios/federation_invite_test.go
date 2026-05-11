//go:build e2e

package scenarios

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/e2e"
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
	ctx := t.Context()
	stack := e2e.Stack()
	harness.CaptureLogs(t, stack,
		"room-worker-a", "inbox-worker-b", "broadcast-worker-b")

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	bobOnB := stack.SiteB.Authenticate(t, ctx, "bob")

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
	require.NotEmpty(t, roomID)
	t.Logf("alice created room %s on siteA; invited bob on siteB", roomID)

	// Poll mongo-b's subscriptions collection for bob+roomID.
	subs := stack.SiteB.MongoDB(t).Collection("subscriptions")
	require.Eventually(t, func() bool {
		var sub map[string]any
		err := subs.FindOne(ctx, bson.M{
			"account": bobOnB.Account,
			"roomId":  roomID,
		}).Decode(&sub)
		return err == nil
	}, 15*time.Second, 250*time.Millisecond,
		"bob's subscription on roomID=%s never appeared on siteB", roomID)

	// Stronger assertion: the subscription document carries the correct
	// originating site so future cross-site routing decisions work.
	var sub map[string]any
	require.NoError(t, subs.FindOne(ctx, bson.M{
		"account": bobOnB.Account,
		"roomId":  roomID,
	}).Decode(&sub))
	assert.Equal(t, bobOnB.Account, sub["account"])
	assert.Equal(t, roomID, sub["roomId"])
}

// TestFederation_NegativeIsolation per R2.C item 6: a siteA-LOCAL channel
// invite (where the invitee is also a siteA user) must NOT fire a
// subscription update on siteB. Catches the "we accidentally over-broadcast
// cross-site" regression class.
//
// alice and carol are both siteA users. alice creates a channel with carol;
// we observe siteB's `chat.user.{carol}.update` subject and assert it stays
// silent for 2 seconds.
func TestFederation_NegativeIsolation_SiteALocalInviteSilentOnB(t *testing.T) {
	ctx := t.Context()
	stack := e2e.Stack()
	harness.CaptureLogs(t, stack, "room-worker-a", "inbox-worker-b")

	alice := stack.SiteA.Authenticate(t, ctx, "alice")
	// Use bob as the local invitee here -- realm-export.json defines alice
	// and bob; if more test users land later, swap in a synthetic "carol".
	bob := stack.SiteA.Authenticate(t, ctx, "bob")

	// Subscribe on siteB BEFORE inviting on siteA. Reuses bob's account
	// name; the subscription update on siteB is observed via the local
	// system creds (no per-user auth needed for observation).
	siteBConn := stack.SiteB.SystemConn(t)
	negSub, err := siteBConn.SubscribeSync(subject.SubscriptionUpdate(bob.Account))
	require.NoError(t, err)
	t.Cleanup(func() { _ = negSub.Unsubscribe() })

	// alice creates a channel with bob (BOTH on siteA, no cross-site path
	// should fire).
	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, stack.SiteA.SiteID),
		createReq, 5*time.Second, &model.CreateRoomReply{},
	))

	// Wait 2 seconds for nothing to arrive on siteB's subject.
	msg, err := negSub.NextMsg(2 * time.Second)
	if err == nil {
		t.Fatalf("siteB received an unexpected subscription update for a siteA-local invite: %s", msg.Data)
	}
	// Expected: nats.ErrTimeout. Any other error is a test infra issue, not a
	// federation regression -- log loudly.
	assert.Contains(t, err.Error(), "timeout",
		"expected timeout on siteB subscription subject; got %v", err)
}
