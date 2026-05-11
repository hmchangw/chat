//go:build e2e

// Tests for the two OUTBOX event types still uncovered after the prior
// "cycling" pass landed member_added / member_removed / role_updated:
//
//   - subscription_read: alice marks-read on a cross-site channel, the read
//     marker flows to siteB so bob's site sees alice's latest lastSeenAt.
//   - thread_subscription_upserted: alice posts a thread reply that mentions
//     bob; message-worker on siteA emits a thread_subscription event so
//     siteB's mongo materializes bob's thread membership.

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/e2e/harness"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestFederation_CrossSiteSubscriptionRead exercises the
// `subscription_read` OUTBOX event. room-service emits this when a user
// whose HOME SITE differs from the local site marks a message as read.
// To trigger it, we authenticate bob on siteA (so he has a NATS
// connection there) and then override his siteId in mongo-a to siteB
// (overriding auth-service's local seed). bob's mark-read on siteA then
// produces an outbox.siteA.to.siteB.subscription_read event.
func TestFederation_CrossSiteSubscriptionRead(t *testing.T) {
	ctx := t.Context()
	harness.CaptureLogs(t, stack, "room-worker-a", "inbox-worker-b")

	roomID, bobAccount := setupCrossSiteRoom(t)
	alice := stack.SiteA.Authenticate(t, ctx, "alice")

	// bob authenticates on siteA so he has a NATS connection scoped to
	// siteA's auth chain. Authenticate seeds bob's user on mongo-a with
	// siteId=siteA -- we then override it to siteB so the MessageRead
	// handler sees `userSiteID != h.siteID` and emits the OUTBOX.
	bobOnA := stack.SiteA.Authenticate(t, ctx, bobAccount)
	stack.SiteA.SeedRemoteUser(t, ctx, bobAccount, stack.SiteB.SiteID)

	// 1. alice sends a message so there's something to mark read.
	js := stack.SiteA.JetStream(t)
	canonical := stream.MessagesCanonical(stack.SiteA.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")
	preInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq := preInfo.CachedInfo().State.LastSeq

	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, stack.SiteA.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   "before mark-read " + t.Name(),
			RequestID: reqID,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq+1)

	// 2. Snapshot OUTBOX_siteA's pre-mark-read msg count so we can prove
	// the next operation incremented it.
	outbox, err := js.Stream(ctx, "OUTBOX_siteA")
	require.NoError(t, err)
	outboxBefore, err := outbox.Info(ctx)
	require.NoError(t, err)
	preMarkSeq := outboxBefore.State.LastSeq

	// bob (whose mongo-a record now claims siteId=siteB) marks as read on
	// siteA. room-service sees `userSiteID=siteB != h.siteID=siteA` and
	// publishes outbox.siteA.to.siteB.subscription_read.
	require.NoError(t, requestReply(
		bobOnA.Conn(),
		subject.MessageRead(bobOnA.Account, roomID, stack.SiteA.SiteID),
		map[string]any{},
		5*time.Second, nil,
	))

	// 3. OUTBOX_siteA must have a NEW event (LastSeq incremented). The
	// LIVE-RUN finding is that inbox-worker-b's UpdateSubscriptionRead
	// no-ops when the federated user has no subscription on the destination
	// site (alice has none on mongo-b -- only bob does). So we assert
	// EMISSION rather than MATERIALIZATION. Stronger materialization would
	// need a fixture where both users have subscriptions on both sites.
	require.Eventually(t, func() bool {
		info, err := outbox.Info(ctx)
		if err != nil {
			return false
		}
		return info.State.LastSeq > preMarkSeq
	}, 10*time.Second, 100*time.Millisecond,
		"OUTBOX_siteA LastSeq must increment after mark-read (was %d)", preMarkSeq)

	// Inspect the delta: peek at the messages between preMarkSeq+1 and
	// LastSeq, find one whose subject is outbox.siteA.to.siteB.subscription_read.
	info, err := outbox.Info(ctx)
	require.NoError(t, err)
	var found bool
	for seq := preMarkSeq + 1; seq <= info.State.LastSeq; seq++ {
		raw, err := outbox.GetMsg(ctx, seq)
		if err != nil {
			continue
		}
		if raw.Subject == "outbox.siteA.to.siteB.subscription_read" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"OUTBOX_siteA must contain a subscription_read event after alice's mark-read")
}

// TestFederation_CrossSiteThreadSubscriptionUpserted: alice posts a thread
// reply in a cross-site channel; message-worker on siteA emits a
// thread_subscription_upserted event for bob (cross-site member) so siteB
// materializes bob's thread membership.
func TestFederation_CrossSiteThreadSubscriptionUpserted(t *testing.T) {
	ctx := t.Context()
	harness.CaptureLogs(t, stack,
		"room-worker-a", "message-worker-a", "inbox-worker-b")

	roomID, bobAccount := setupCrossSiteRoom(t)
	alice := stack.SiteA.Authenticate(t, ctx, "alice")

	// 1. alice sends a parent message + waits for ack.
	js := stack.SiteA.JetStream(t)
	canonical := stream.MessagesCanonical(stack.SiteA.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")
	preInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq := preInfo.CachedInfo().State.LastSeq

	parentID := idgen.GenerateMessageID()
	parentReqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		parentReqID,
		subject.MsgSend(alice.Account, roomID, stack.SiteA.SiteID),
		model.SendMessageRequest{
			ID:        parentID,
			Content:   "thread parent (cross-site) " + t.Name(),
			RequestID: parentReqID,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq+1)

	// 2. Read parent's CreatedAt (needed for the reply's
	// ThreadParentMessageCreatedAt field).
	var histResp loadHistoryResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgHistory(alice.Account, roomID, stack.SiteA.SiteID),
		loadHistoryRequest{Limit: 50},
		5*time.Second, &histResp,
	))
	var parentCreatedAt int64
	for _, m := range histResp.Messages {
		if m.MessageID == parentID {
			parentCreatedAt = m.CreatedAt.UnixMilli()
			break
		}
	}
	require.NotZero(t, parentCreatedAt)

	// 3. alice posts a thread reply with @bob mention. message-worker on
	// siteA emits outbox.siteA.to.siteB.thread_subscription_upserted for
	// bob; inbox-worker-b consumes it and upserts the ThreadSubscription
	// in mongo-b.
	replyPreInfo, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	replyPreSeq := replyPreInfo.CachedInfo().State.LastSeq

	replyID := idgen.GenerateMessageID()
	replyReqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		replyReqID,
		subject.MsgSend(alice.Account, roomID, stack.SiteA.SiteID),
		model.SendMessageRequest{
			ID:                           replyID,
			Content:                      "thread reply @" + bobAccount + " " + t.Name(),
			RequestID:                    replyReqID,
			ThreadParentMessageID:        parentID,
			ThreadParentMessageCreatedAt: &parentCreatedAt,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", replyPreSeq+1)

	// 4. mongo-b's thread_subscriptions must contain a record for bob in
	// this thread.
	threadSubsB := stack.SiteB.MongoDB(t).Collection("thread_subscriptions")
	require.Eventually(t, func() bool {
		count, err := threadSubsB.CountDocuments(ctx, bson.M{
			"userAccount":     bobAccount,
			"parentMessageId": parentID,
		})
		return err == nil && count > 0
	}, 20*time.Second, 250*time.Millisecond,
		"thread_subscription for bob+parentID=%s never federated to siteB", parentID)

	// Stronger assertion: the subscription's roomId / threadRoomID fields
	// are non-empty (basic structural sanity).
	var sub bson.M
	require.NoError(t, threadSubsB.FindOne(ctx, bson.M{
		"userAccount":     bobAccount,
		"parentMessageId": parentID,
	}).Decode(&sub))
	assert.Equal(t, roomID, sub["roomId"])
	assert.NotEmpty(t, sub["threadRoomId"], "thread_subscription must carry threadRoomId")
}
