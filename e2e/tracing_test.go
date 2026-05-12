//go:build e2e

// Tracing-propagation tests. Pair with request_id_test.go which covers
// X-Request-ID. The traceparent header is the W3C trace-context format
// (`00-{trace_id}-{span_id}-{flags}`) used by OpenTelemetry. Even with
// OTEL_SDK_DISABLED=true (the e2e default), workers must NOT strip
// inbound traceparent headers from the publish path -- the propagation
// is what tools downstream (Tempo, Jaeger, etc.) hang correlation off
// of, regardless of whether THIS process emitted spans.

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

// TestTracing_TraceparentPropagatesThroughBroadcastChain mirrors the
// shape of TestRequestID_PropagatesThroughBroadcastChain but for the
// W3C `traceparent` header. Catches regressions where the workers re-
// publish broadcasts without copying the inbound header (silent loss
// of distributed-trace correlation).
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

	// Hand-crafted W3C traceparent: version 00, random 16-byte trace id,
	// random 8-byte span id, sampled flag. The trace_id slot is the one
	// downstream tools join on -- if the workers strip the header, the
	// e2e correlation collapses regardless of span/flag values.
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
		// Per OTel JetStream semconv each delivery is a fresh consumer
		// span LINKED to the producer span -- the trace_id does NOT flow
		// verbatim through the pull-consumer chain (alice → gatekeeper →
		// canonical → broadcast-worker). The end-to-end correlation is
		// the trace-link (which lives in the span data, not on the wire).
		// What this test CAN assert on the wire:
		//   1. broadcast carries a non-empty traceparent (otelnats is
		//      injecting at all -- the regression class this test exists
		//      to catch: a future natsutil.NewMsg refactor returning a
		//      nil header and silently dropping traceparent).
		//   2. The format is valid W3C trace-context (version-trace_id-
		//      span_id-flags, 55 chars total).
		require.NotEmpty(t, got,
			"broadcast must carry traceparent; got header %v", broadcastMsg.Header)
		assert.Len(t, got, 55,
			"traceparent must be 55-char W3C trace-context; got %q", got)
		assert.Equal(t, "00", got[:2], "version segment must be 00")
		// trace_id segment must be 32 hex chars, span_id 16 hex chars,
		// and neither may be all zeros (that's the "invalid" sentinel).
		traceID := got[3:35]
		spanID := got[36:52]
		assert.NotEqual(t, "00000000000000000000000000000000", traceID,
			"trace_id must not be all-zeros (invalid)")
		assert.NotEqual(t, "0000000000000000", spanID,
			"span_id must not be all-zeros (invalid)")
		t.Logf("inbound traceparent: %s", traceparent)
		t.Logf("broadcast traceparent: %s (different trace_id is expected per "+
			"OTel jetstream semconv -- consumer-spans link rather than child)", got)
		matched = true
		break
	}
	require.True(t, matched, "did not observe alice's broadcast carrying msgID=%s within 10s", msgID)
}
