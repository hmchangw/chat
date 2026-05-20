// tools/loadgen/scenario_notif_test.go
package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func TestNotificationScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("notification-fanout")
	require.True(t, ok)
	assert.Equal(t, "notification-fanout", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestNotificationLag_RecordedOnReceipt(t *testing.T) {
	tracker := newNotifLagTracker()
	publishedAt := time.Now()
	tracker.RecordPublished("msg-1", publishedAt)

	receivedAt := publishedAt.Add(15 * time.Millisecond)
	lag, ok := tracker.LagFor("msg-1", receivedAt)
	require.True(t, ok)
	assert.InDelta(t, 15*time.Millisecond, lag, float64(2*time.Millisecond))
}

func TestNotificationLag_UnknownMessageReturnsFalse(t *testing.T) {
	tracker := newNotifLagTracker()
	_, ok := tracker.LagFor("msg-unknown", time.Now())
	assert.False(t, ok, "unknown messages must return ok=false")
}

// TestNotificationLag_MultiRecipientCardinality verifies the tracker
// allows ONE observation per actual notification receipt, not one per
// published message. Per the scenario spec, fanout to N recipients yields
// N observations for a single publish (LagFor is read-only — does not
// consume the tracker entry).
func TestNotificationLag_MultiRecipientCardinality(t *testing.T) {
	tracker := newNotifLagTracker()
	publishedAt := time.Now()
	tracker.RecordPublished("msg-fanout", publishedAt)

	count := 0
	for i := 0; i < 5; i++ {
		recvAt := publishedAt.Add(time.Duration(i+1) * time.Millisecond)
		if _, ok := tracker.LagFor("msg-fanout", recvAt); ok {
			count++
		}
	}
	assert.Equal(t, 5, count,
		"tracker must allow N observations per (message, recipient) without consuming entries")
}

// TestNotificationHandler_ObservesLagForKnownMessage exercises the
// per-account NATS subscription callback path. The handler extracts the
// message ID from a NotificationEvent payload, looks up publish time,
// and observes the lag.
func TestNotificationHandler_ObservesLagForKnownMessage(t *testing.T) {
	tracker := newNotifLagTracker()
	publishedAt := time.Now()
	tracker.RecordPublished("msg-known", publishedAt)

	var observed []float64
	var mu sync.Mutex
	observe := func(lag time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, lag.Seconds())
	}

	h := newNotificationHandler(tracker, observe)

	evt := model.NotificationEvent{
		Type:      "new_message",
		RoomID:    "room-1",
		Message:   model.Message{ID: "msg-known", RoomID: "room-1"},
		Timestamp: publishedAt.Add(10 * time.Millisecond).UTC().UnixMilli(),
	}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)

	h(&nats.Msg{Subject: subject.Notification("alice"), Data: payload})

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, observed, 1, "handler must record exactly one observation for a known message")
	assert.GreaterOrEqual(t, observed[0], 0.0, "observed lag must be non-negative")
}

// TestNotificationHandler_IgnoresUnknownMessage verifies the handler is
// a no-op when the payload's message ID isn't in the tracker (e.g., a
// notification from a prior run or out-of-window event).
func TestNotificationHandler_IgnoresUnknownMessage(t *testing.T) {
	tracker := newNotifLagTracker()

	var observed []float64
	var mu sync.Mutex
	observe := func(lag time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		observed = append(observed, lag.Seconds())
	}

	h := newNotificationHandler(tracker, observe)

	evt := model.NotificationEvent{
		Type:    "new_message",
		RoomID:  "room-1",
		Message: model.Message{ID: "msg-unknown", RoomID: "room-1"},
	}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)

	h(&nats.Msg{Subject: subject.Notification("alice"), Data: payload})

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, observed, "handler must not observe lag for unknown messages")
}

// TestNotificationHandler_IgnoresMalformedPayload verifies the handler
// drops non-JSON payloads without panicking or observing.
func TestNotificationHandler_IgnoresMalformedPayload(t *testing.T) {
	tracker := newNotifLagTracker()
	var observed []float64
	observe := func(lag time.Duration) { observed = append(observed, lag.Seconds()) }

	h := newNotificationHandler(tracker, observe)
	h(&nats.Msg{Subject: subject.Notification("alice"), Data: []byte("not-json")})

	assert.Empty(t, observed)
}

// fakeSubscribeFn captures Subscribe calls without touching real NATS.
type fakeSubscribeFn struct {
	mu       sync.Mutex
	subjects []string
	handlers map[string]func(*nats.Msg)
}

func newFakeSubscribeFn() *fakeSubscribeFn {
	return &fakeSubscribeFn{handlers: map[string]func(*nats.Msg){}}
}

func (f *fakeSubscribeFn) subscribe(subj string, h func(*nats.Msg)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subjects = append(f.subjects, subj)
	f.handlers[subj] = h
	return nil
}

func (f *fakeSubscribeFn) deliver(subj string, data []byte) {
	f.mu.Lock()
	h := f.handlers[subj]
	f.mu.Unlock()
	if h != nil {
		h(&nats.Msg{Subject: subj, Data: data})
	}
}

// TestSubscribeAllAccounts_OneSubPerUniqueAccount verifies subscribeAllAccounts
// dedupes by account (a user may own multiple subscriptions across rooms;
// only one notification sub is needed per account).
func TestSubscribeAllAccounts_OneSubPerUniqueAccount(t *testing.T) {
	fixtures := Fixtures{
		Users: []model.User{
			{ID: "u-1", Account: "alice"},
			{ID: "u-2", Account: "bob"},
			{ID: "u-3", Account: "alice"}, // duplicate account
		},
	}
	fake := newFakeSubscribeFn()
	count, err := subscribeAllAccounts(&fixtures, fake.subscribe, func(*nats.Msg) {})
	require.NoError(t, err)
	assert.Equal(t, 2, count, "must subscribe once per unique account")
	assert.Len(t, fake.subjects, 2)
	assert.Contains(t, fake.subjects, subject.Notification("alice"))
	assert.Contains(t, fake.subjects, subject.Notification("bob"))
}

// TestSubscribeAllAccounts_DeliversToHandler is a unit-level end-to-end test:
// subscribe, deliver a notification on one subject, confirm the handler is
// invoked.
func TestSubscribeAllAccounts_DeliversToHandler(t *testing.T) {
	fixtures := Fixtures{Users: []model.User{{ID: "u-1", Account: "alice"}}}
	fake := newFakeSubscribeFn()

	var received int32
	var mu sync.Mutex
	count, err := subscribeAllAccounts(&fixtures, fake.subscribe, func(_ *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		received++
	})
	require.NoError(t, err)
	require.Equal(t, 1, count)

	fake.deliver(subject.Notification("alice"), []byte("payload"))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, int32(1), received, "subscriber handler must be invoked once on delivery")
}

// TestPublishNotifTick_RecordsPublishAndPublishes verifies one tick publishes
// a frontdoor message and registers it with the tracker.
func TestPublishNotifTick_RecordsPublishAndPublishes(t *testing.T) {
	preset, _ := BuiltinPreset("small")
	fixtures := BuildFixtures(&preset, 42, "site-local")
	require.NotEmpty(t, fixtures.Subscriptions)

	pub := &recordingPublisher{}
	tracker := newNotifLagTracker()

	g := &notificationFanoutGenerator{
		deps: &fakeScenarioDeps{
			fixtures: &fixtures,
			preset:   &preset,
			siteID:   "site-local",
			pub:      pub,
			metrics:  NewMetrics(),
		},
		rf:      &runFlags{Rate: 1},
		tracker: tracker,
	}

	err := g.publishNotifTick(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, pub.count(), "exactly one publish per tick")

	// Inspect: tracker must have one entry, keyed by the published msgID
	// extracted from the SendMessageRequest body.
	call := pub.snapshot()[0]
	var req model.SendMessageRequest
	require.NoError(t, json.Unmarshal(call.data, &req))
	assert.NotEmpty(t, req.ID, "request must carry a message ID")

	_, ok := tracker.LagFor(req.ID, time.Now())
	assert.True(t, ok, "tracker must hold the published msgID after publishNotifTick")
}

// TestNotificationFanout_Run_ExitsOnContext verifies the Run loop exits
// cleanly on ctx cancel and at least one tick records a publish into the
// tracker. Uses nil Subscribers (test path) — that's logged as a warning
// but the publish side still operates so the tracker grows.
func TestNotificationFanout_Run_ExitsOnContext(t *testing.T) {
	preset, _ := BuiltinPreset("small")
	fixtures := BuildFixtures(&preset, 42, "site-local")
	require.NotEmpty(t, fixtures.Subscriptions)

	pub := &recordingPublisher{}
	g := &notificationFanoutGenerator{
		deps: &fakeScenarioDeps{
			fixtures: &fixtures,
			preset:   &preset,
			siteID:   "site-local",
			pub:      pub,
			metrics:  NewMetrics(),
		},
		rf: &runFlags{Rate: 100}, // 10ms ticks → at least one tick in 50ms
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within deadline after ctx cancel")
	}

	assert.GreaterOrEqual(t, pub.count(), 1, "Run must publish at least one message before ctx cancel")
}

// TestPublishNotifTick_NoFixturesNoOp verifies the tick is a safe no-op
// when there are no subscriptions.
func TestPublishNotifTick_NoFixturesNoOp(t *testing.T) {
	pub := &recordingPublisher{}
	g := &notificationFanoutGenerator{
		deps: &fakeScenarioDeps{
			fixtures: &Fixtures{},
			preset:   &Preset{Name: "empty"},
			siteID:   "site-local",
			pub:      pub,
			metrics:  NewMetrics(),
		},
		rf:      &runFlags{Rate: 1},
		tracker: newNotifLagTracker(),
	}
	err := g.publishNotifTick(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, pub.count())
}
