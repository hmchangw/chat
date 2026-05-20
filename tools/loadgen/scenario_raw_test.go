package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// fakeReader returns ErrRAWNotVisible for the first failuresBefore polls, then nil (visible).
type fakeReader struct {
	failuresBefore int
	calls          int
}

func (f *fakeReader) Lookup(_ context.Context, _ string) error {
	f.calls++
	if f.calls <= f.failuresBefore {
		return ErrRAWNotVisible
	}
	return nil
}

func TestRAW_PollUntilVisible_BoundsLagAndWindow(t *testing.T) {
	reader := &fakeReader{failuresBefore: 3} // hit on the 4th call
	interval := 10 * time.Millisecond
	timeout := 1 * time.Second
	publishedAt := time.Now()

	outcome, err := pollUntilVisible(
		context.Background(),
		reader.Lookup,
		"msg-1",
		publishedAt,
		interval,
		timeout,
	)
	require.NoError(t, err)

	// Lag is bounded by (N+1) × interval where N is failuresBefore.
	expectedMaxLag := time.Duration(4) * interval
	assert.LessOrEqual(t, outcome.Lag, expectedMaxLag+5*time.Millisecond,
		"lag %v should be <= ~40ms (4 polls × 10ms + jitter)", outcome.Lag)

	// Visibility window is at most 1 × interval (time between last-NotFound and first-OK).
	assert.LessOrEqual(t, outcome.VisibilityWindow, interval+5*time.Millisecond,
		"visibility window %v should be <= 1 × interval (~10ms) + jitter", outcome.VisibilityWindow)
}

func TestRAW_PollUntilVisible_TimeoutReturnsError(t *testing.T) {
	reader := &fakeReader{failuresBefore: 999} // never visible within timeout
	interval := 10 * time.Millisecond
	timeout := 50 * time.Millisecond

	_, err := pollUntilVisible(
		context.Background(),
		reader.Lookup,
		"msg-1",
		time.Now(),
		interval,
		timeout,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRAWTimeout)
}

func TestRAWScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("raw-consistency")
	require.True(t, ok)
	assert.Equal(t, "raw-consistency", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

// ---------------------------------------------- publishForRAW

func TestRAWPublishForRAW_BuildsSendMessageRequest(t *testing.T) {
	pub := &recordingPublisher{}
	g := &rawConsistencyGenerator{
		deps: &fakeScenarioDeps{
			pub:    pub,
			siteID: "site-local",
		},
	}
	sub := &model.Subscription{
		RoomID: "room-7",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	err := g.publishForRAW(context.Background(), sub, "msg-abc", time.Now())
	require.NoError(t, err)

	calls := pub.snapshot()
	require.Len(t, calls, 1)
	// Subject must be the user's frontdoor msg.send route — that's the
	// user-visible path RAW is measuring (publish → gatekeeper → canonical
	// → broadcast → history-service visible).
	wantSubj := subject.MsgSend("alice", "room-7", "site-local")
	assert.Equal(t, wantSubj, calls[0].subject,
		"raw-consistency must publish via frontdoor MsgSend (the user-visible path)")

	var req model.SendMessageRequest
	require.NoError(t, json.Unmarshal(calls[0].data, &req))
	assert.Equal(t, "msg-abc", req.ID, "the published message ID must match what the poll loop looks up")
	assert.NotEmpty(t, req.Content, "content body must be non-empty (gatekeeper rejects empty content)")
	assert.NotEmpty(t, req.RequestID, "RequestID is required by the frontdoor handler contract")
}

func TestRAWPublishForRAW_PropagatesPublishError(t *testing.T) {
	pub := &erroringPublisher{err: errors.New("nats down")}
	g := &rawConsistencyGenerator{
		deps: &fakeScenarioDeps{
			pub:    pub,
			siteID: "site-local",
		},
	}
	sub := &model.Subscription{
		RoomID: "room-1",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	err := g.publishForRAW(context.Background(), sub, "msg-1", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats down")
}

// ---------------------------------------------- lookupHistory

// fakeHistoryRequester returns canned bytes for history GetMessageByID requests,
// or a transport error if set.
type fakeHistoryRequester struct {
	reply []byte
	err   error
	calls []requestCall
}

func (f *fakeHistoryRequester) Request(_ context.Context, subj string, data []byte, _ time.Duration) ([]byte, error) {
	f.calls = append(f.calls, requestCall{subject: subj, data: append([]byte(nil), data...)})
	if f.err != nil {
		return nil, f.err
	}
	return f.reply, nil
}

func TestRAWLookupHistory_VisibleWhenMessageFound(t *testing.T) {
	// A real history-service GetMessageByID reply is the message struct JSON.
	// Non-error JSON (no "error" field) means visible.
	msg := model.Message{ID: "msg-abc", RoomID: "room-1", UserAccount: "alice"}
	body, err := json.Marshal(msg)
	require.NoError(t, err)
	req := &fakeHistoryRequester{reply: body}

	sub := &model.Subscription{
		RoomID: "room-1",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	lookup := newHistoryLookup(req, sub, "site-local", 1*time.Second)
	require.NoError(t, lookup(context.Background(), "msg-abc"))

	require.Len(t, req.calls, 1)
	wantSubj := subject.MsgGet("alice", "room-1", "site-local")
	assert.Equal(t, wantSubj, req.calls[0].subject,
		"lookup must target the per-user MsgGet subject — that's the frontdoor history-service handler RAW is measuring")
	var wire getMessageByIDRequestWire
	require.NoError(t, json.Unmarshal(req.calls[0].data, &wire))
	assert.Equal(t, "msg-abc", wire.MessageID, "request body must carry the messageID being polled")
}

func TestRAWLookupHistory_NotVisibleWhenMessageMissing(t *testing.T) {
	// history-service returns model.ErrorResponse{Error: "message not found",
	// Code: "not_found"} via natsrouter when the message isn't present.
	errResp := model.ErrorResponse{Error: "message not found", Code: "not_found"}
	body, err := json.Marshal(errResp)
	require.NoError(t, err)
	req := &fakeHistoryRequester{reply: body}

	sub := &model.Subscription{
		RoomID: "room-1",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	lookup := newHistoryLookup(req, sub, "site-local", 1*time.Second)
	err = lookup(context.Background(), "msg-abc")
	assert.ErrorIs(t, err, ErrRAWNotVisible,
		"a not_found app-error reply means the message hasn't propagated yet; treat as not-visible so the poll loop continues")
}

func TestRAWLookupHistory_TransportErrorPropagates(t *testing.T) {
	req := &fakeHistoryRequester{err: errors.New("nats timeout")}

	sub := &model.Subscription{
		RoomID: "room-1",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	lookup := newHistoryLookup(req, sub, "site-local", 1*time.Second)
	err := lookup(context.Background(), "msg-abc")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRAWNotVisible,
		"transport errors must surface as themselves so pollUntilVisible fails fast instead of polling until timeout")
	assert.Contains(t, err.Error(), "nats timeout")
}

func TestRAWLookupHistory_NonNotFoundAppErrorPropagates(t *testing.T) {
	// A "forbidden" (or any non-not_found) app error must NOT be silenced as
	// "not yet visible" — that would hide misconfiguration (bad ACL, wrong
	// account) by burning the full poll timeout on every publish.
	errResp := model.ErrorResponse{Error: "forbidden", Code: "forbidden"}
	body, err := json.Marshal(errResp)
	require.NoError(t, err)
	req := &fakeHistoryRequester{reply: body}

	sub := &model.Subscription{
		RoomID: "room-1",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	lookup := newHistoryLookup(req, sub, "site-local", 1*time.Second)
	err = lookup(context.Background(), "msg-abc")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRAWNotVisible,
		"non-not_found app errors signal misconfiguration; surface them so pollUntilVisible exits fast")
}
