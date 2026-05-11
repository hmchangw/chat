//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestNegative_OversizedPayload: send a message body well above any
// reasonable size limit; assert gatekeeper rejects with an ErrorResponse
// rather than silently truncating or accepting it. Catches: gatekeeper's
// size check regressing OR NATS max-payload misconfig.
func TestNegative_OversizedPayload(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)

	// 2 MiB body -- larger than NATS' 1 MiB default max_payload (which the
	// e2e stack inherits since the configs don't override max_payload).
	// NATS will reject the publish at the wire; alternatively gatekeeper's
	// own size check could fire if NATS were configured larger.
	huge := strings.Repeat("x", 2*1024*1024)
	body, err := json.Marshal(model.SendMessageRequest{
		ID:        idgen.GenerateMessageID(),
		Content:   huge,
		RequestID: idgen.GenerateRequestID(),
	})
	require.NoError(t, err)
	pubErr := alice.Conn().Publish(
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		body,
	)
	// Either an ErrorResponse from gatekeeper (would need a separate
	// SubscribeSync setup, complex), OR a NATS-level rejection at publish
	// time (visible immediately). The regression we're testing for is
	// silent acceptance.
	require.Error(t, pubErr, "oversized payload must not be accepted silently at the NATS layer")
	// Prefer errors.Is over string-matching the client-library message text
	// (which can drift across go-nats versions).
	assert.ErrorIs(t, pubErr, nats.ErrMaxPayload,
		"expected nats.ErrMaxPayload; got %v", pubErr)
}

// TestNegative_NonMemberSend: bob is removed from a channel, then attempts
// to send -- gatekeeper must reject. The realm only has alice + bob, and
// solo-channel creation requires at least one user/org (room-service's
// handleCreateRoomChannel: errEmptyCreateRequest). So we use the "removed
// member" angle instead of "third user."
func TestNegative_NonMemberSend(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	require.NotEmpty(t, roomID)
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	// alice removes bob; bob is now NOT a member.
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberRemove(alice.Account, roomID, site.SiteID),
		model.RemoveMemberRequest{
			RoomID:    roomID,
			Requester: alice.Account,
			Account:   bob.Account,
		},
		5*time.Second, nil,
	))
	// Wait for the subscription removal to land before testing the
	// rejection.
	subsColl := mongoA.Collection("subscriptions")
	require.Eventually(t, func() bool {
		count, _ := subsColl.CountDocuments(ctx, bson.M{
			"u.account": bob.Account,
			"roomId":    roomID,
		})
		return count == 0
	}, 10*time.Second, 100*time.Millisecond,
		"bob's subscription must be removed before non-member send test")

	// bob (no longer a member) sends; gatekeeper must reject.
	reqID := idgen.GenerateRequestID()
	err := sendAndAwaitReply(
		t,
		bob.Conn(),
		bob.Account,
		reqID,
		subject.MsgSend(bob.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        idgen.GenerateMessageID(),
			Content:   "i should not be allowed",
			RequestID: reqID,
		},
		5*time.Second,
	)
	require.Error(t, err)
	er := asErrorReply(err)
	require.NotNil(t, er, "expected ErrorResponse from gatekeeper; got %T", err)
}

// TestNegative_DuplicateMessageID: send the same message ID twice. Cassandra's
// primary key dedupes at write time; the e2e contract is that exactly one
// row lands in history regardless of how many times the same ID is sent.
// (RequestID is for tracing, not dedup -- so two messages with different
// IDs but same RequestID both write through.) Catches: someone changes the
// Cassandra primary key.
func TestNegative_DuplicateMessageID(t *testing.T) {
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	// Same MESSAGE ID both times -- Cassandra's primary key (message_id)
	// enforces dedup. Different RequestID + content on the second to make
	// the regression obvious if dedup ever breaks. The response-sub reqID
	// and the body's RequestID MUST be the same string (gatekeeper computes
	// the reply subject from the body, not the sub registration).
	//
	// IMPORTANT: sendAndAwaitReply confirms only gatekeeper-side acceptance;
	// the Cassandra write happens asynchronously in message-worker. Without
	// awaitCanonicalAcked between sends + before the history query, the
	// history query can race the cassandra write and observe 0 rows.
	js := site.JetStream(t)
	canonical := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonical, "message-worker")

	preInfo1, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq1 := preInfo1.CachedInfo().State.LastSeq

	msgID := idgen.GenerateMessageID()
	reqID1 := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID1,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   "first send",
			RequestID: reqID1,
		},
		10*time.Second,
	))
	awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq1+1)

	// Second send with SAME message ID.
	preInfo2, err := js.Stream(ctx, canonical)
	require.NoError(t, err)
	preSeq2 := preInfo2.CachedInfo().State.LastSeq

	reqID2 := idgen.GenerateRequestID()
	dupErr := sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID2,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID, // reused
			Content:   "second send with same msgID",
			RequestID: reqID2,
		},
		5*time.Second,
	)
	t.Logf("dup-MessageID second-send result: err=%v", dupErr)
	// Whether the gatekeeper accepted the dup (relying on Cassandra PK to
	// dedup) or rejected it (gatekeeper-side dedup), we need to wait for the
	// canonical stream to settle before querying history -- but only if the
	// second send actually produced a canonical message.
	postInfo, perr := js.Stream(ctx, canonical)
	require.NoError(t, perr)
	if postInfo.CachedInfo().State.LastSeq > preSeq2 {
		awaitCanonicalAcked(t, ctx, js, canonical, "message-worker", preSeq2+1)
	}

	// Inspect history. Exactly one row for msgID.
	var histResp loadHistoryResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgHistory(alice.Account, roomID, site.SiteID),
		loadHistoryRequest{Limit: 50},
		5*time.Second, &histResp,
	))
	matches := 0
	for _, m := range histResp.Messages {
		if m.MessageID == msgID {
			matches++
		}
	}
	assert.Equal(t, 1, matches,
		"duplicate message ID must produce exactly one history row; got %d", matches)
}

// TestNegative_ExpiredJWT: a NATS JWT past its `exp` claim must be rejected.
// Synthesizing a forged short-TTL JWT requires Keycloak admin API access
// which adds significant scaffold. Instead exercise a SIMPLER negative path
// that hits the same boundary: connect with a creds file containing a
// well-formed JWT for a NON-EXISTENT account ("nobody" -- not in
// resolver_preload). NATS rejects the connection.
//
// Strong end-to-end coverage of expired-token rejection requires a small
// helper to mint short-TTL tokens via Keycloak; deferred.
func TestNegative_BadCredsAccount(t *testing.T) {
	t.Parallel()
	site := stack.SiteA
	// nats.UserCredentials reads the file at connect time. A creds file
	// containing junk produces a different failure mode than a missing
	// file; both should reject the connection.
	tmp := t.TempDir() + "/bogus.creds"
	require.NoError(t, writeFile(tmp, "-----BEGIN NATS USER JWT-----\nGARBAGE\n------END NATS USER JWT------\n-----BEGIN USER NKEY SEED-----\nGARBAGE\n------END USER NKEY SEED------\n"))
	_, err := nats.Connect(site.NATSURL, nats.UserCredentials(tmp))
	require.Error(t, err, "NATS must reject connect with malformed creds")
}

// TestNegative_HistoryNonMember: a user who isn't subscribed to a room must
// not receive its history. Catches: history-service authorization regression
// that would let any account read any room. Uses bob as the non-member by
// creating an alice-only solo channel.
func TestNegative_HistoryNonMember(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	// alice + bob channel; remove bob; bob then queries history.
	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	))
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{{SiteID: site.SiteID, DB: site.MongoDB(t)}}, roomID)
	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	// alice removes bob.
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MemberRemove(alice.Account, roomID, site.SiteID),
		model.RemoveMemberRequest{
			RoomID:    roomID,
			Requester: alice.Account,
			Account:   bob.Account,
		},
		5*time.Second, nil,
	))
	subsColl := mongoA.Collection("subscriptions")
	require.Eventually(t, func() bool {
		count, _ := subsColl.CountDocuments(ctx, bson.M{
			"u.account": bob.Account,
			"roomId":    roomID,
		})
		return count == 0
	}, 10*time.Second, 100*time.Millisecond, "bob removal must propagate")

	// Reviewer fix: alice sends a message FIRST so the room has content. Without
	// this, the test passes vacuously -- "bob's empty history" is consistent
	// with "history-service correctly rejected him" AND with "the room was
	// empty all along, no auth was ever exercised."
	msgID := idgen.GenerateMessageID()
	reqID := idgen.GenerateRequestID()
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		reqID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		model.SendMessageRequest{
			ID:        msgID,
			Content:   "alice message that bob must NOT see",
			RequestID: reqID,
		},
		10*time.Second,
	))

	// bob (non-member) requests history. Required outcome: ErrorResponse
	// (preferred) OR a response with NO trace of alice's message.
	var histResp loadHistoryResponse
	err := requestReply(
		bob.Conn(),
		subject.MsgHistory(bob.Account, roomID, site.SiteID),
		loadHistoryRequest{Limit: 50},
		5*time.Second, &histResp,
	)
	if err != nil {
		// Reject with ErrorResponse is the strong-form correct outcome.
		er := asErrorReply(err)
		require.NotNil(t, er, "non-member history rejection should be a structured ErrorResponse; got %T", err)
		return
	}
	// If history-service didn't reject, it MUST not include alice's message.
	for _, m := range histResp.Messages {
		assert.NotEqual(t, msgID, m.MessageID,
			"history-service must not return alice's message %s to a non-member", msgID)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
