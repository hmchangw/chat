//go:build e2e

package scenarios

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/e2e"
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
	ctx := t.Context()
	site := e2e.Stack().SiteA

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

	// Receive the broadcast on bob's side and assert the header.
	broadcastMsg := awaitMessage(t, bobSub, 10*time.Second)
	gotRequestID := broadcastMsg.Header.Get(natsutil.RequestIDHeader)
	assert.Equal(t, requestID, gotRequestID,
		"X-Request-ID must propagate end-to-end: gatekeeper -> message-worker -> broadcast-worker")
}
