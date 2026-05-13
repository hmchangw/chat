//go:build e2e

// Tests for the two OUTBOX event types still uncovered after the prior
// "cycling" pass landed member_added / member_removed / role_updated:
//
//   - subscription_read: bob (whose mongo-a record is overridden to
//     siteId=siteB so room-service treats him as a federated user) marks-read
//     on siteA; the OUTBOX event flows to siteB and inbox-worker-b updates
//     bob's lastSeenAt on mongo-b. Both emission AND materialization are
//     asserted.
//   - thread_subscription_upserted: alice posts a thread reply that mentions
//     bob; message-worker on siteA emits a thread_subscription event so
//     siteB's mongo materializes bob's thread membership.

package e2e

import (
	"encoding/json"
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
	t.Parallel()
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
	awaitDurableReady(t, ctx, js, stream.MessagesCanonical(stack.SiteA.SiteID).Name, "message-worker")

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
	// Wait by msgID (parallel-safe).
	awaitMessageOnSite(t, ctx, stack.SiteA, msgID)

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

	// 3. OUTBOX_siteA must have a NEW event (LastSeq incremented). This
	// confirms emission at the room-service boundary.
	require.Eventually(t, func() bool {
		info, err := outbox.Info(ctx)
		if err != nil {
			return false
		}
		return info.State.LastSeq > preMarkSeq
	}, 10*time.Second, 100*time.Millisecond,
		"OUTBOX_siteA LastSeq must increment after mark-read (was %d)", preMarkSeq)

	// Inspect the delta: peek at the messages between preMarkSeq+1 and
	// LastSeq, find one whose subject is outbox.siteA.to.siteB.subscription_read
	// AND whose payload roomID matches OURS. Parallel sibling tests can
	// inject subscription_read events into the same OUTBOX in the same
	// scan window, so the subject alone is not a tight enough filter.
	info, err := outbox.Info(ctx)
	require.NoError(t, err)
	var found bool
	for seq := preMarkSeq + 1; seq <= info.State.LastSeq; seq++ {
		raw, err := outbox.GetMsg(ctx, seq)
		if err != nil {
			continue
		}
		if raw.Subject != "outbox.siteA.to.siteB.subscription_read" {
			continue
		}
		var env model.OutboxEvent
		if jerr := json.Unmarshal(raw.Data, &env); jerr != nil {
			continue
		}
		var payload model.SubscriptionReadEvent
		if jerr := json.Unmarshal(env.Payload, &payload); jerr != nil {
			continue
		}
		if payload.RoomID == roomID && payload.Account == bobAccount {
			found = true
			break
		}
	}
	assert.True(t, found,
		"OUTBOX_siteA must contain a subscription_read event for room=%s account=%s",
		roomID, bobAccount)

	// 4. Materialization on mongo-b: bob already has a subscription on
	// mongo-b (setupCrossSiteRoom waits for it). After inbox-worker-b
	// consumes the subscription_read event, bob's row must carry a
	// lastSeenAt >= the test's start instant. Use the start-of-test
	// instant as the floor so we don't depend on a prior lastSeenAt being
	// absent or older — UpdateSubscriptionRead's $or filter handles both
	// missing and stale values, so we just assert the result.
	floor := time.Now().Add(-5 * time.Second)
	subsB := stack.SiteB.MongoDB(t).Collection("subscriptions")
	require.Eventually(t, func() bool {
		var sub struct {
			LastSeenAt *time.Time `bson:"lastSeenAt"`
		}
		err := subsB.FindOne(ctx, bson.M{
			"u.account": bobAccount,
			"roomId":    roomID,
		}).Decode(&sub)
		if err != nil || sub.LastSeenAt == nil {
			return false
		}
		return sub.LastSeenAt.After(floor)
	}, 15*time.Second, 250*time.Millisecond,
		"bob's lastSeenAt on mongo-b must update after subscription_read federation (floor=%s)",
		floor.Format(time.RFC3339))
}

// TestFederation_CrossSiteThreadSubscriptionUpserted: alice posts a thread
// reply in a cross-site channel; message-worker on siteA emits a
// thread_subscription_upserted event for bob (cross-site member) so siteB
// materializes bob's thread membership.
func TestFederation_CrossSiteThreadSubscriptionUpserted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	harness.CaptureLogs(t, stack,
		"room-worker-a", "message-worker-a", "inbox-worker-b")

	roomID, bobAccount := setupCrossSiteRoom(t)
	alice := stack.SiteA.Authenticate(t, ctx, "alice")

	// 1. alice sends a parent message + waits for ack.
	js := stack.SiteA.JetStream(t)
	awaitDurableReady(t, ctx, js, stream.MessagesCanonical(stack.SiteA.SiteID).Name, "message-worker")

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
	awaitMessageOnSite(t, ctx, stack.SiteA, parentID)

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
	awaitMessageOnSite(t, ctx, stack.SiteA, replyID)

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
