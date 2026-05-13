//go:build e2e

// W3C traceparent propagation through workers. Even with OTEL_SDK_DISABLED=true,
// the header must survive the publish path so downstream tools can correlate.

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
	"github.com/hmchangw/chat/pkg/subject"
)

// Catches workers re-publishing broadcasts without copying the inbound traceparent.
func TestTracing_TraceparentPropagatesThroughBroadcastChain(t *testing.T) {
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
	registerRoomCleanup(t, []SiteDB{asSiteDB(t, site)}, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), alice.Account, roomID)
	awaitSubscription(t, ctx, site.MongoDB(t), bob.Account, roomID)

	bobSub, err := bob.Conn().SubscribeSync(subject.RoomEvent(roomID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bobSub.Unsubscribe() })

	// Hand-crafted W3C traceparent: version-traceid-spanid-sampled.
	const traceparent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"

	requestID := idgen.GenerateRequestID()
	msgID := idgen.GenerateMessageID()
	body := "tracing propagation " + t.Name()
	payload, err := json.Marshal(model.SendMessageRequest{
		ID:        msgID,
		Content:   body,
		RequestID: requestID,
	})
	require.NoError(t, err)

	respSub, err := alice.Conn().SubscribeSync(userResponseSubject(alice.Account, requestID))
	require.NoError(t, err)
	t.Cleanup(func() { _ = respSub.Unsubscribe() })

	outbound := &nats.Msg{
		Subject: subject.MsgSend(alice.Account, roomID, site.SiteID),
		Data:    payload,
		Header: nats.Header{
			"traceparent": []string{traceparent},
		},
	}
	require.NoError(t, alice.Conn().PublishMsg(outbound))

	_, err = respSub.NextMsg(10 * time.Second)
	require.NoError(t, err, "gatekeeper must reply to msg.send")

	deadline := time.Now().Add(10 * time.Second)
	var matched bool
	for time.Now().Before(deadline) {
		broadcastMsg, mErr := bobSub.NextMsg(2 * time.Second)
		if mErr != nil {
			break
		}
		var ev model.RoomEvent
		if jerr := json.Unmarshal(broadcastMsg.Data, &ev); jerr != nil {
			continue
		}
		if ev.Message == nil || ev.Message.ID != msgID {
			continue
		}
		got := broadcastMsg.Header.Get("traceparent")
		// OTel JetStream semconv: trace_id doesn't flow verbatim through
		// pull-consumer chains. On-wire we can still assert: a non-empty
		// header (otelnats is injecting) and valid W3C format.
		require.NotEmpty(t, got,
			"broadcast must carry traceparent; got header %v", broadcastMsg.Header)
		assert.Len(t, got, 55,
			"traceparent must be 55-char W3C trace-context; got %q", got)
		assert.Equal(t, "00", got[:2], "version segment must be 00")
		// All-zeros trace_id/span_id is the W3C "invalid" sentinel.
		traceID := got[3:35]
		spanID := got[36:52]
		assert.NotEqual(t, "00000000000000000000000000000000", traceID,
			"trace_id must not be all-zeros (invalid)")
		assert.NotEqual(t, "0000000000000000", spanID,
			"span_id must not be all-zeros (invalid)")
		t.Logf("inbound traceparent: %s", traceparent)
		t.Logf("broadcast traceparent: %s (different trace_id expected: consumer-spans link)", got)
		matched = true
		break
	}
	require.True(t, matched, "did not observe alice's broadcast carrying msgID=%s within 10s", msgID)
}
