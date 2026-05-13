//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/e2e/harness"
	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

// loadHistoryRequest mirrors history-service/internal/models.LoadHistoryRequest.
// Defined locally because that package is internal and not importable from
// e2e/. Mirrors only the JSON-serialized fields, not Go-side types.
type loadHistoryRequest struct {
	Before *int64 `json:"before,omitempty"`
	Limit  int    `json:"limit"`
}

// loadHistoryResponse mirrors history-service/internal/models.LoadHistoryResponse.
// pkg/model/cassandra.Message is the concrete element type (public).
type loadHistoryResponse struct {
	Messages          []cassandra.Message `json:"messages"`
	MinUserLastSeenAt *int64              `json:"minUserLastSeenAt,omitempty"`
}

// roomEventMessageID returns the embedded message ID or "" if absent.
// Used so require.Equal compares against the EXPECTED ID even when the
// loop ran out without ever assigning the event.
func roomEventMessageID(e *model.RoomEvent) string {
	if e == nil || e.Message == nil {
		return ""
	}
	return e.Message.ID
}

// send path:
//
//	auth alice -> create channel room -> server invites bob -> bob auths ->
//	bob subscribes to chat.room.{roomID}.event -> alice sends message with
//	@bob mention -> bob receives broadcast -> notification fires for bob ->
//	history-service.LoadHistory returns the message with sender + mention.
//
// Per amendment R1 10.D: uses awaitCanonicalAcked between send and history
// to close the message-worker -> Cassandra write race.
//
// Per amendment R2.C item 2: asserts m.MessageID + m.Sender.Account +
// m.CreatedAt.IsZero on the cassandra.Message field shape (which differs
// from pkg/model.Message's UserAccount / ID / etc -- LoadHistoryResponse
// carries cassandra.Message via history-service/internal/models).
//
// Per amendment R2.C item 3 / R3.A: notification assertion reads
// notif.Message.ID (not notif.MessageID -- the top-level field doesn't
// exist) and notif.Message.Mentions for the @bob mention check.
func TestMessage_SendAndBroadcast_SingleSite(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	// Pre-conditions: workers must have subscribed to their durables before
	// we publish. message-worker on MESSAGES_CANONICAL is the key one.
	js := site.JetStream(t)
	canonicalStream := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonicalStream, "message-worker")

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	ids := harness.NewTestIDs(t)

	// 1. alice creates a channel room with bob as the initial member. Non-
	// empty Name + at least one user is sufficient to trigger channel-mode
	// per room-service/helper.go:determineRoomType. Server assigns the
	// roomID and returns it on the sync reply (R1 10.E -- harness doesn't
	// pre-mint).
	requestID := idgen.GenerateRequestID()
	createReq := model.CreateRoomRequest{
		Name:  "e2e-" + t.Name(),
		Users: []string{bob.Account},
	}
	var createReply model.CreateRoomReply
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	), "request_id=%s", requestID)
	roomID := createReply.RoomID
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	require.NotEmpty(t, roomID, "server-assigned room ID must be non-empty")
	t.Logf("created channel roomID=%s", roomID)

	// CreateRoomReply is "request accepted" not "data is durable" -- wait
	// for room-worker to actually persist alice's + bob's subscriptions
	// before sending (gatekeeper's "user is not subscribed" check races
	// otherwise). Bug-hunter BLOCKER #2.
	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	// 2. bob subscribes to the channel's room-event subject BEFORE alice
	// sends. subject.RoomEvent(roomID) is the specific subject for this
	// room; the wildcard RoomEventWildcard() (no args) is for cross-room
	// observers. Per-room subscribe is more discriminating.
	bobSub, err := bob.Conn().SubscribeSync(subject.RoomEvent(roomID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	// 2b. bob also subscribes to notifications BEFORE the send.
	notifSub, err := bob.Conn().SubscribeSync(subject.Notification(bob.Account))
	require.NoError(t, err)
	t.Cleanup(func() { _ = notifSub.Unsubscribe() })

	// alice sends a message tagging bob.
	msgRequestID := idgen.GenerateRequestID()
	body := "hello @bob, ping?"
	sendReq := model.SendMessageRequest{
		ID:        ids.MessageID,
		Content:   body,
		RequestID: msgRequestID,
	}
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		msgRequestID,
		// subject.MsgSend(account, roomID, siteID) is the user-facing send subj.
		// message-gatekeeper consumes it via the MESSAGES stream.
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		sendReq, 10*time.Second,
	))

	// 5. bob's per-room broadcast subscription. Channel-create emits system
	// events (room_created, members_added) on the same subject; loop to
	// find the broadcast that corresponds specifically to alice's user
	// message. Without this filter the test is flaky -- under tight timing
	// the system message can arrive before alice's send.
	var roomEvent model.RoomEvent
	bcastDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(bcastDeadline) {
		m, mErr := bobSub.NextMsg(5 * time.Second)
		if mErr != nil {
			break
		}
		var candidate model.RoomEvent
		if jerr := json.Unmarshal(m.Data, &candidate); jerr != nil {
			continue
		}
		if candidate.Message != nil && candidate.Message.ID == ids.MessageID {
			roomEvent = candidate
			break
		}
	}
	require.Equal(t, ids.MessageID, roomEventMessageID(&roomEvent),
		"bob never received broadcast for alice's specific message %s", ids.MessageID)
	assert.Equal(t, model.RoomEventNewMessage, roomEvent.Type)
	assert.Equal(t, roomID, roomEvent.RoomID)
	assert.Equal(t, body, roomEvent.Message.Content)
	assert.Equal(t, alice.Account, roomEvent.Message.UserAccount,
		"broadcast carries pkg/model.Message (with UserAccount field) inside RoomEvent")

	// 6. notification-worker fires a notification to bob. The subject is
	// per-account (chat.user.{account}.notification) so stale notifications
	// from prior tests can interleave -- loop until we find the matching
	// MessageID. Mention resolution runs ASYNC of notification dispatch so
	// we don't assert on notif.Message.Mentions here; that check happens on
	// the history record below.
	var notif model.NotificationEvent
	notifDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(notifDeadline) {
		notifMsg, mErr := notifSub.NextMsg(5 * time.Second)
		if mErr != nil {
			break
		}
		var candidate model.NotificationEvent
		if jerr := json.Unmarshal(notifMsg.Data, &candidate); jerr != nil {
			continue
		}
		if candidate.Message.ID == ids.MessageID {
			notif = candidate
			break
		}
	}
	assert.Equal(t, "new_message", notif.Type)
	assert.Equal(t, roomID, notif.RoomID)
	assert.Equal(t, ids.MessageID, notif.Message.ID,
		"notification for our message must arrive on bob's notification subject")

	// 7. Wait for the message to be in Cassandra before reading history.
	// Wait by msgID (parallel-safe; the seq-based wait can be satisfied
	// by a sibling test's message under the shared message-worker durable).
	awaitMessageOnSite(t, ctx, site, ids.MessageID)

	// 8. history-service.LoadHistory returns the message. RoomID is in the
	// subject; request body carries Before/Limit.
	histReq := loadHistoryRequest{Limit: 50}
	var histResp loadHistoryResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgHistory(alice.Account, roomID, site.SiteID),
		histReq, 5*time.Second, &histResp,
	))
	// History contains system messages (room_created, members_added) plus
	// the user message. Filter to find ours specifically; don't assume Len==1.
	var m *cassandra.Message
	for i := range histResp.Messages {
		if histResp.Messages[i].MessageID == ids.MessageID {
			m = &histResp.Messages[i]
			break
		}
	}
	require.NotNil(t, m, "alice's message %s not found in history (got %d entries)",
		ids.MessageID, len(histResp.Messages))
	// cassandra.Message field shape: MessageID (not ID), Sender Participant
	// (not UserAccount), Msg (not Content).
	assert.Equal(t, alice.Account, m.Sender.Account)
	assert.Equal(t, body, m.Msg)
	assert.False(t, m.CreatedAt.IsZero(), "history record must have CreatedAt set")
	assert.Equal(t, roomID, m.RoomID)
	// Mention resolution: the persisted message DOES carry resolved Mentions
	// (in contrast to the notification, which can race). This is the strong
	// assertion the bug-hunter HIGH flagged as belonging on a stable surface.
	if assert.NotEmpty(t, m.Mentions, "history record's mention list must include bob") {
		var mentionAccounts []string
		for _, p := range m.Mentions {
			mentionAccounts = append(mentionAccounts, p.Account)
		}
		assert.Contains(t, mentionAccounts, bob.Account)
	}

	// R4.D: cross-site negative LoadHistory. The site-A-local message must NOT
	// be retrievable via siteB's history-service. Catches "wrote to wrong
	// site's Cassandra" or "history-service reading wrong keyspace" regressions
	// that the positive-only assertion above would miss.
	//
	// Reviewer fix: explicitly require either an error reply (history-service-b
	// rejects the unknown room) OR a non-error reply that genuinely doesn't
	// contain our siteA message. The prior version only ran the inner check
	// when err == nil, so a non-nil err meant the test passed without actually
	// exercising the negative-isolation guarantee.
	siteBConn := stack.SiteB.SystemConn(t)
	histReqB := loadHistoryRequest{Limit: 50}
	var histRespB loadHistoryResponse
	histErr := requestReply(
		siteBConn,
		subject.MsgHistory(alice.Account, roomID, stack.SiteB.SiteID),
		histReqB, 5*time.Second, &histRespB,
	)
	if histErr != nil {
		// Reject with structured ErrorResponse is the cleanest negative-
		// isolation outcome on a siteB-unknown room. Confirm the shape.
		er := asErrorReply(histErr)
		require.NotNil(t, er,
			"siteB history rejection must be a structured ErrorResponse; got %T (%v)", histErr, histErr)
	} else {
		// If siteB DID accept the query, the response MUST not contain
		// alice's siteA-local message.
		for _, hm := range histRespB.Messages {
			assert.NotEqual(t, ids.MessageID, hm.MessageID,
				"siteB history must NOT contain alice's siteA-local message %s", ids.MessageID)
		}
	}
}

// TestMessage_SendAndBroadcast_DM walks the DM variant of the send path.
// Per amendment R2.C item 1: this exercises broadcast-worker's DM branch
// (handler.go:85-93 has a `switch room.Type` with distinct Channel vs DM
// fan-out), which the channel-only happy path would not catch.
//
// Key differences from the channel test:
//   - CreateRoomRequest{Name: "", Users: [bob]} triggers DM mode
//     (per room-service/helper.go:determineRoomType).
//   - bob subscribes to subject.UserRoomEvent(bob.Account) -- the DM
//     broadcast envelope is per-user, not per-room.
func TestMessage_SendAndBroadcast_DM(t *testing.T) {
	ctx := t.Context()
	site := stack.SiteA

	js := site.JetStream(t)
	canonicalStream := stream.MessagesCanonical(site.SiteID).Name
	awaitDurableReady(t, ctx, js, canonicalStream, "message-worker")

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")
	ids := harness.NewTestIDs(t)

	// 1. alice creates a DM with bob. Empty Name + exactly one user + no
	// orgs/channels => DM per determineRoomType. The DM may already exist
	// from a prior test in this run; either path yields the roomID.
	createReq := model.CreateRoomRequest{
		Name:  "",
		Users: []string{bob.Account},
	}
	var roomID string
	var createReply model.CreateRoomReply
	err := requestReply(
		alice.Conn(),
		subject.RoomCreate(alice.Account, site.SiteID),
		createReq, 5*time.Second, &createReply,
	)
	if err == nil {
		roomID = createReply.RoomID
		assert.Equal(t, string(model.RoomTypeDM), createReply.RoomType)
	} else {
		er := asErrorReply(err)
		require.NotNil(t, er, "expected *errorReply, got %T: %v", err, err)
		require.NotEmpty(t, er.Resp.RoomID, "existing-DM errorReply must carry RoomID")
		roomID = er.Resp.RoomID
		t.Logf("DM already exists from prior test; reusing RoomID=%s", roomID)
	}
	require.NotEmpty(t, roomID)

	// Wait for subscriptions to persist before sending.
	mongoA := site.MongoDB(t)
	awaitSubscription(t, ctx, mongoA, alice.Account, roomID)
	awaitSubscription(t, ctx, mongoA, bob.Account, roomID)

	// 2. bob subscribes to the per-user DM event envelope BEFORE the send.
	bobSub, err := bob.Conn().SubscribeSync(subject.UserRoomEvent(bob.Account))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	// alice sends a DM.
	msgRequestID := idgen.GenerateRequestID()
	body := "DM hello from " + t.Name()
	sendReq := model.SendMessageRequest{
		ID:        ids.MessageID,
		Content:   body,
		RequestID: msgRequestID,
	}
	require.NoError(t, sendAndAwaitReply(
		t,
		alice.Conn(),
		alice.Account,
		msgRequestID,
		subject.MsgSend(alice.Account, roomID, site.SiteID),
		sendReq, 10*time.Second,
	))

	// 4. bob receives via the per-user DM envelope (NOT the per-room one).
	msg := awaitMessage(t, bobSub, 10*time.Second)
	var roomEvent model.RoomEvent
	require.NoError(t, json.Unmarshal(msg.Data, &roomEvent))
	assert.Equal(t, model.RoomEventNewMessage, roomEvent.Type)
	assert.Equal(t, roomID, roomEvent.RoomID)
	require.NotNil(t, roomEvent.Message)
	assert.Equal(t, ids.MessageID, roomEvent.Message.ID)
	assert.Equal(t, body, roomEvent.Message.Content)

	// 5. Wait for ack + verify history. By-msgID for parallel safety.
	awaitMessageOnSite(t, ctx, site, ids.MessageID)

	histReq := loadHistoryRequest{Limit: 50}
	var histResp loadHistoryResponse
	require.NoError(t, requestReply(
		alice.Conn(),
		subject.MsgHistory(alice.Account, roomID, site.SiteID),
		histReq, 5*time.Second, &histResp,
	))
	// DM history accumulates across test runs (same alicebob roomID).
	// Find our specific message.
	var found bool
	for _, m := range histResp.Messages {
		if m.MessageID == ids.MessageID {
			found = true
			assert.Equal(t, body, m.Msg)
			break
		}
	}
	assert.True(t, found, "alice's DM message %s not in history (got %d entries)",
		ids.MessageID, len(histResp.Messages))
}
