//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// TestRequestID_PropagatesThroughBroadcastChain: a request-ID set on the
// outbound msg.send header MUST appear on the broadcast RoomEvent header
// bob receives. Confirms propagation through message-gatekeeper ->
// MESSAGES_CANONICAL -> broadcast-worker.
//
// Header key is natsutil.RequestIDHeader (X-Request-ID; verified against
// pkg/natsutil/request_id.go).
func TestRequestID_PropagatesThroughBroadcastChain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	site := stack.SiteA

	alice := site.Authenticate(t, ctx, "alice")
	bob := site.Authenticate(t, ctx, "bob")

	// Set up a channel + bob's broadcast subscription.
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

	// Wait for subscriptions before sending; otherwise gatekeeper rejects.
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	bobSub, err := bob.Conn().SubscribeSync(subject.RoomEvent(roomID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	// Generate a UUIDv7 request ID and embed it both in the SendMessageRequest
	// AND the NATS header. The gatekeeper extracts the header value via
	// natsutil.ContextWithRequestIDFromHeaders; downstream workers carry it
	// through ctx and re-attach it on the broadcast.
	requestID := idgen.GenerateRequestID()
	msgID := idgen.GenerateMessageID()
	body := "request-id propagation " + t.Name()

	payload, err := json.Marshal(model.SendMessageRequest{
		ID:        msgID,
		Content:   body,
		RequestID: requestID,
	})
	require.NoError(t, err)

	respSub, err := alice.Conn().SubscribeSync(userResponseSubject(alice.Account, requestID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = respSub.Unsubscribe() })

	// Build a *nats.Msg directly so we can attach the X-Request-ID header.
	outboundMsg := &nats.Msg{
		Subject: subject.MsgSend(alice.Account, roomID, site.SiteID),
		Data:    payload,
		Header:  nats.Header{natsutil.RequestIDHeader: []string{requestID}},
	}
	require.NoError(t, alice.Conn().PublishMsg(outboundMsg))

	// Wait for the async send reply (proves the gatekeeper accepted).
	_, err = respSub.NextMsg(10 * time.Second)
	require.NoError(t, err, "gatekeeper must reply to msg.send")

	// Receive broadcasts on bob's side. Channel creation emits system messages
	// (room_created, members_added) BEFORE alice's user message; each carries
	// its own X-Request-ID. Loop to find the broadcast that corresponds to
	// alice's specific message.
	deadline := time.Now().Add(10 * time.Second)
	var matched bool
	for time.Now().Before(deadline) {
		broadcastMsg, mErr := bobSub.NextMsg(2 * time.Second)
		if mErr != nil {
			break
		}
		// Parse the body to find the message we care about.
		var ev model.RoomEvent
		if jerr := json.Unmarshal(broadcastMsg.Data, &ev); jerr != nil {
			continue
		}
		if ev.Message == nil || ev.Message.ID != msgID {
			continue
		}
		gotRequestID := broadcastMsg.Header.Get(natsutil.RequestIDHeader)
		assert.Equal(t, requestID, gotRequestID,
			"X-Request-ID must propagate end-to-end: gatekeeper -> message-worker -> broadcast-worker")
		matched = true
		break
	}
	require.True(t, matched, "did not observe alice's broadcast carrying msgID=%s within 10s", msgID)
}
