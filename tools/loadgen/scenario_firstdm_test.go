package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func TestFirstDMScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("first-dm")
	require.True(t, ok)
	assert.Equal(t, "first-dm", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

func TestFirstDMPool_MonotonicConsumption(t *testing.T) {
	pool := newFirstDMPool([][2]string{
		{"u1", "u2"},
		{"u3", "u4"},
		{"u5", "u6"},
	}, false /* recycle */)

	p1, ok := pool.Next()
	require.True(t, ok)
	assert.Equal(t, [2]string{"u1", "u2"}, p1)

	p2, ok := pool.Next()
	require.True(t, ok)
	assert.Equal(t, [2]string{"u3", "u4"}, p2)

	p3, ok := pool.Next()
	require.True(t, ok)
	assert.Equal(t, [2]string{"u5", "u6"}, p3)

	// Exhausted.
	_, ok = pool.Next()
	assert.False(t, ok, "pool must return false when exhausted (non-recycle)")
}

func TestFirstDMPool_RecycleWraps(t *testing.T) {
	pool := newFirstDMPool([][2]string{
		{"u1", "u2"},
		{"u3", "u4"},
	}, true /* recycle */)

	_, ok := pool.Next()
	require.True(t, ok)
	_, ok = pool.Next()
	require.True(t, ok)
	// Third call should wrap around.
	p, ok := pool.Next()
	require.True(t, ok, "recycle must return true after wraparound")
	assert.Equal(t, [2]string{"u1", "u2"}, p, "wrap returns to first pair")
}

func TestAugmentWithFirstDMFixtures_AddsPairs(t *testing.T) {
	p := &Preset{Name: "test", Users: 10, Rooms: 5}
	f := BuildFixtures(p, 42, "site-local")
	initialUsers := len(f.Users)

	augmentWithFirstDMFixtures(&f, p, 5)

	// Should add 10 users (5 pairs × 2 users).
	assert.Equal(t, initialUsers+10, len(f.Users), "expected 10 new users (5 pairs × 2)")

	// All new users have the loadgen-firstdm- prefix.
	firstDMUsers := 0
	for _, u := range f.Users {
		if hasFirstDMPrefix(u.ID) {
			firstDMUsers++
		}
	}
	assert.Equal(t, 10, firstDMUsers)
}

// TestAugmentWithFirstDMFixtures_PopulatesUserFields verifies that first-dm
// fixture users carry the EngName / ChineseName / SiteID fields that
// room-service requires for the DM create path. Without these the
// scenario's RoomCreate request would fail validation (errInvalidUserData).
func TestAugmentWithFirstDMFixtures_PopulatesUserFields(t *testing.T) {
	f := Fixtures{}
	augmentWithFirstDMFixtures(&f, &Preset{}, 2)
	require.NotEmpty(t, f.Users)
	for _, u := range f.Users {
		assert.NotEmpty(t, u.EngName, "first-dm user %s must have EngName", u.Account)
		assert.NotEmpty(t, u.ChineseName, "first-dm user %s must have ChineseName", u.Account)
		assert.NotEmpty(t, u.SiteID, "first-dm user %s must have SiteID", u.Account)
	}
}

// TestFirstDMTracker_RecordsAllStages exercises the happy path: a tracker
// receives the four stage observations in order and Lags returns the correct
// per-stage durations against the recorded publishedAt anchor.
func TestFirstDMTracker_RecordsAllStages(t *testing.T) {
	tr := newFirstDMTracker()
	roomID := "dm-room-abc"
	publishedAt := time.Now()
	tr.RecordPublish(roomID, "msg-1", "userA", "userB", publishedAt)

	tr.RecordStage(roomID, firstDMStageRoom, publishedAt.Add(5*time.Millisecond))
	tr.RecordSubsFor(roomID, "userA", publishedAt.Add(10*time.Millisecond))
	tr.RecordSubsFor(roomID, "userB", publishedAt.Add(15*time.Millisecond))
	tr.RecordStage(roomID, firstDMStageE2E, publishedAt.Add(50*time.Millisecond))

	lags, ok := tr.Lags(roomID)
	require.True(t, ok)
	assert.InDelta(t, 5*time.Millisecond, lags[firstDMStageRoom], float64(2*time.Millisecond))
	// subs is when BOTH users' subs arrive — i.e. max of the two.
	assert.InDelta(t, 15*time.Millisecond, lags[firstDMStageSubs], float64(2*time.Millisecond))
	assert.InDelta(t, 50*time.Millisecond, lags[firstDMStageE2E], float64(2*time.Millisecond))
}

// TestFirstDMTracker_SubsRequiresBothUsers verifies that the subs stage is
// not emitted until both userA and userB SubscriptionUpdate events have
// arrived for the room.
func TestFirstDMTracker_SubsRequiresBothUsers(t *testing.T) {
	tr := newFirstDMTracker()
	roomID := "dm-room-xyz"
	publishedAt := time.Now()
	tr.RecordPublish(roomID, "msg-1", "userA", "userB", publishedAt)
	tr.RecordSubsFor(roomID, "userA", publishedAt.Add(5*time.Millisecond))

	lags, ok := tr.Lags(roomID)
	require.True(t, ok)
	_, hasSubs := lags[firstDMStageSubs]
	assert.False(t, hasSubs, "subs stage must wait for both users")

	tr.RecordSubsFor(roomID, "userB", publishedAt.Add(10*time.Millisecond))
	lags, ok = tr.Lags(roomID)
	require.True(t, ok)
	assert.InDelta(t, 10*time.Millisecond, lags[firstDMStageSubs], float64(2*time.Millisecond))
}

// TestFirstDMTracker_BoundedSize confirms FIFO eviction caps the tracker's
// memory footprint under sustained publish rates.
func TestFirstDMTracker_BoundedSize(t *testing.T) {
	tr := newFirstDMTracker()
	for i := 0; i < firstDMTrackerCap+200; i++ {
		tr.RecordPublish(padInt(i), padInt(i)+"-msg", "a", "b", time.Now())
	}
	assert.LessOrEqual(t, tr.Size(), firstDMTrackerCap, "tracker must FIFO-evict to stay bounded")
}

// TestFirstDMTracker_UnknownRoomIsNoop confirms that observing stages for a
// room that wasn't published doesn't grow the tracker or panic.
func TestFirstDMTracker_UnknownRoomIsNoop(t *testing.T) {
	tr := newFirstDMTracker()
	before := tr.Size()
	tr.RecordStage("not-tracked", firstDMStageRoom, time.Now())
	tr.RecordSubsFor("not-tracked", "u1", time.Now())
	assert.Equal(t, before, tr.Size())
	_, ok := tr.Lags("not-tracked")
	assert.False(t, ok)
}

// TestFirstDMTracker_ByMessageID exercises the per-message-ID reverse lookup
// used by the e2e observer.
func TestFirstDMTracker_ByMessageID(t *testing.T) {
	tr := newFirstDMTracker()
	roomID := "dm-room-mid"
	publishedAt := time.Now()
	tr.RecordPublish(roomID, "msg-7", "userA", "userB", publishedAt)

	gotRoom, ok := tr.RoomIDForMessage("msg-7")
	require.True(t, ok)
	assert.Equal(t, roomID, gotRoom)

	_, ok = tr.RoomIDForMessage("nope")
	assert.False(t, ok)
}

// TestParseRoomCreatePayload_ExtractsRoomID verifies the canonical room-create
// observer payload parser extracts the RoomID from CreateRoomRequest.
func TestParseRoomCreatePayload_ExtractsRoomID(t *testing.T) {
	req := model.CreateRoomRequest{
		RoomID:           "dm-roomid-1",
		RequesterAccount: "userA",
		Users:            []string{"userB"},
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)

	roomID, ok := parseRoomCreatePayload(data)
	require.True(t, ok)
	assert.Equal(t, "dm-roomid-1", roomID)
}

// TestParseSubscriptionUpdatePayload_ExtractsRoomAndAccount verifies the
// subscription.update parser extracts the roomID + account.
func TestParseSubscriptionUpdatePayload_ExtractsRoomAndAccount(t *testing.T) {
	evt := model.SubscriptionUpdateEvent{
		UserID: "userA-id",
		Subscription: model.Subscription{
			RoomID: "dm-roomid-1",
			User:   model.SubscriptionUser{ID: "userA-id", Account: "userA"},
		},
		Action: "added",
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	roomID, account, ok := parseSubscriptionUpdatePayload(data)
	require.True(t, ok)
	assert.Equal(t, "dm-roomid-1", roomID)
	assert.Equal(t, "userA", account)
}

// TestParseUserRoomEventPayload_ExtractsMessageID verifies the broadcast
// observer parser extracts the message ID from a RoomEvent envelope.
func TestParseUserRoomEventPayload_ExtractsMessageID(t *testing.T) {
	evt := struct {
		Type    string `json:"type"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}{
		Type: "newMessage",
	}
	evt.Message.ID = "msg-id-1"
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	msgID, ok := parseUserRoomEventPayload(data)
	require.True(t, ok)
	assert.Equal(t, "msg-id-1", msgID)
}

// TestParseUserRoomEventPayload_RejectsMalformed verifies the parser rejects
// payloads without a message.id field or that are non-JSON.
func TestParseUserRoomEventPayload_RejectsMalformed(t *testing.T) {
	_, ok := parseUserRoomEventPayload([]byte(`{"type":"newMessage"}`))
	assert.False(t, ok)

	_, ok = parseUserRoomEventPayload([]byte(`not-json`))
	assert.False(t, ok)
}

// TestParseSubscriptionUpdatePayload_FallsBackToUserID covers the case where
// Subscription.User.Account is empty but UserID is populated.
func TestParseSubscriptionUpdatePayload_FallsBackToUserID(t *testing.T) {
	evt := model.SubscriptionUpdateEvent{
		UserID:       "userA-id",
		Subscription: model.Subscription{RoomID: "r1"},
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)

	_, account, ok := parseSubscriptionUpdatePayload(data)
	require.True(t, ok)
	assert.Equal(t, "userA-id", account, "parser must surface UserID when Account is empty")
}

// TestFirstDMScenario_PoolExhaustionExitsCleanly verifies that the Run loop
// exits without error when no first-DM fixtures are seeded.
func TestFirstDMScenario_PoolExhaustionExitsCleanly(t *testing.T) {
	deps := newFakeFirstDMDeps()
	rf := &runFlags{Rate: 100, FirstDMRecycle: false}

	gen := &firstDMGenerator{deps: deps, rf: rf}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := gen.Run(ctx)
	assert.NoError(t, err)
}

// TestFirstDMGenerator_ObservesAllStagesIntoHistogram confirms the histogram
// wiring: when sendFirstDM returns per-stage durations the Run loop observes
// them into FirstDMLag with matching stage labels.
func TestFirstDMGenerator_ObservesAllStagesIntoHistogram(t *testing.T) {
	deps := newFakeFirstDMDeps()
	deps.fixtures.Users = []model.User{
		{ID: "loadgen-firstdm-user-0000001", Account: "loadgen-firstdm-user-0000001", SiteID: "site-local"},
		{ID: "loadgen-firstdm-user-0000002", Account: "loadgen-firstdm-user-0000002", SiteID: "site-local"},
	}

	gen := &firstDMGenerator{deps: deps, rf: &runFlags{Rate: 50, FirstDMRecycle: false}}
	gen.sendOverride = func(_ context.Context, _, _ string) map[string]time.Duration {
		return map[string]time.Duration{
			firstDMStageRoom: 1 * time.Millisecond,
			firstDMStageSubs: 5 * time.Millisecond,
			firstDMStageE2E:  20 * time.Millisecond,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := gen.Run(ctx)
	require.NoError(t, err)

	for _, stage := range []string{
		firstDMStageRoom, firstDMStageSubs, firstDMStageE2E,
	} {
		count := histogramSampleCount(t, deps.metrics.FirstDMLag, prometheus.Labels{"stage": stage})
		assert.Positive(t, count, "stage=%s histogram must record at least one observation", stage)
	}
}

// TestBuildCreateDMRequestBody verifies the helper constructs a payload the
// room-service handler will accept as a DM create.
func TestBuildCreateDMRequestBody(t *testing.T) {
	data := buildCreateDMRequestBody("userB")
	var got model.CreateRoomRequest
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, []string{"userB"}, got.Users)
	assert.Empty(t, got.Name)
	assert.Empty(t, got.Orgs)
	assert.Empty(t, got.Channels)
}

// TestSetupFirstDMSubscriptions_SubjectCoverage verifies the setup helper
// registers subscriptions on the three observer subjects.
func TestSetupFirstDMSubscriptions_SubjectCoverage(t *testing.T) {
	subs := newFakeFirstDMSubs()
	tr := newFirstDMTracker()
	err := setupFirstDMSubscriptions(subs, tr, "site-local")
	require.NoError(t, err)

	assert.Contains(t, subs.subjects, subject.RoomCanonical("site-local", "create"))
	assert.Contains(t, subs.subjects, "chat.user.*.event.subscription.update")
	assert.Contains(t, subs.subjects, subject.UserRoomEventWildcard())
	assert.NotContains(t, subs.subjects, subject.MsgCanonicalCreated("site-local"),
		"MsgCanonicalCreated subscription was removed — message-worker consumes that "+
			"subject silently with no persist-complete event, so the stage was a "+
			"loadgen→broker→loadgen self-loop")
}

// TestSetupFirstDMSubscriptions_HandlersDispatch invokes the captured handlers
// with synthetic payloads and confirms the tracker records each stage.
func TestSetupFirstDMSubscriptions_HandlersDispatch(t *testing.T) {
	subs := newFakeFirstDMSubs()
	tr := newFirstDMTracker()
	require.NoError(t, setupFirstDMSubscriptions(subs, tr, "site-local"))

	roomID := "dm-room-handlers"
	publishedAt := time.Now()
	tr.RecordPublish(roomID, "msg-h1", "userA", "userB", publishedAt)

	rcData, err := json.Marshal(model.CreateRoomRequest{RoomID: roomID})
	require.NoError(t, err)
	subs.handlers[subject.RoomCanonical("site-local", "create")](rcData)

	subAData, err := json.Marshal(model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{RoomID: roomID, User: model.SubscriptionUser{Account: "userA"}},
	})
	require.NoError(t, err)
	subBData, err := json.Marshal(model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{RoomID: roomID, User: model.SubscriptionUser{Account: "userB"}},
	})
	require.NoError(t, err)
	subs.handlers["chat.user.*.event.subscription.update"](subAData)
	subs.handlers["chat.user.*.event.subscription.update"](subBData)

	roomEvt, err := json.Marshal(map[string]any{
		"type":    "newMessage",
		"message": map[string]any{"id": "msg-h1"},
	})
	require.NoError(t, err)
	subs.handlers[subject.UserRoomEventWildcard()](roomEvt)

	lags, ok := tr.Lags(roomID)
	require.True(t, ok)
	for _, st := range []string{firstDMStageRoom, firstDMStageSubs, firstDMStageE2E} {
		_, present := lags[st]
		assert.True(t, present, "stage=%s should be recorded", st)
	}
}

// TestRunIteration_DMRoomIDMatchesIdgen confirms the deterministic DM room
// ID is unaffected by pair order.
func TestRunIteration_DMRoomIDMatchesIdgen(t *testing.T) {
	a, b := "loadgen-firstdm-user-0000001", "loadgen-firstdm-user-0000002"
	id1 := idgen.BuildDMRoomID(a, b)
	id2 := idgen.BuildDMRoomID(b, a)
	assert.Equal(t, id1, id2)
	assert.NotEmpty(t, id1)
}

// TestSendFirstDM_HappyPath exercises the full sendFirstDM path with mocked
// Requester + Publisher. Stage observations are fed into the tracker from a
// helper goroutine to simulate the four observer callbacks; the test
// verifies that the returned lag map covers all four stages and that the
// Requester + Publisher were invoked with the expected subjects.
func TestSendFirstDM_HappyPath(t *testing.T) {
	deps := newFakeFirstDMDeps()
	gen := &firstDMGenerator{
		deps:    deps,
		rf:      &runFlags{},
		tracker: newFirstDMTracker(),
	}

	userA, userB := "loadgen-firstdm-user-0000001", "loadgen-firstdm-user-0000002"
	dmRoomID := idgen.BuildDMRoomID(userA, userB)

	// Feed observer-style stage updates after sendFirstDM has had a chance
	// to register the publish in the tracker. The polling cadence inside
	// waitForStages is 5ms, so 20ms of staggered observations gives ample
	// time for the waiter to see the complete set before the 10s timeout.
	done := make(chan map[string]time.Duration, 1)
	go func() {
		done <- gen.sendFirstDM(context.Background(), userA, userB)
	}()

	// Wait until the tracker has an entry before recording stages, so
	// RecordStage doesn't get dropped for an unknown room.
	require.Eventually(t, func() bool {
		_, ok := gen.tracker.Lags(dmRoomID)
		return ok
	}, time.Second, 5*time.Millisecond, "tracker must record publish before observer callbacks fire")

	gen.tracker.RecordStage(dmRoomID, firstDMStageRoom, time.Now())
	gen.tracker.RecordSubsFor(dmRoomID, userA, time.Now())
	gen.tracker.RecordSubsFor(dmRoomID, userB, time.Now())
	gen.tracker.RecordStage(dmRoomID, firstDMStageE2E, time.Now())

	select {
	case lags := <-done:
		assert.Len(t, lags, 3, "all three stages should be present")
		for _, st := range []string{firstDMStageRoom, firstDMStageSubs, firstDMStageE2E} {
			_, present := lags[st]
			assert.True(t, present, "stage=%s must be in returned map", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sendFirstDM did not return within timeout")
	}

	// Verify the Requester was called with the RoomCreate subject for userA.
	require.NotEmpty(t, deps.requester.requests)
	expectedSubj := subject.RoomCreate(userA, deps.siteID)
	assert.Equal(t, expectedSubj, deps.requester.requests[0].subject)

	// Verify the canonical publish landed on the right subject.
	require.NotEmpty(t, deps.publisher.calls)
	assert.Equal(t, subject.MsgCanonicalCreated(deps.siteID), deps.publisher.calls[0].subject)
}

// TestSendFirstDM_RequestFailureReturnsPartialLags verifies that when the
// Requester returns an error the function bails out and returns whatever
// partial stages the tracker has accumulated — no panic, no hang.
func TestSendFirstDM_RequestFailureReturnsPartialLags(t *testing.T) {
	deps := newFakeFirstDMDeps()
	deps.requester.err = errors.New("simulated request failure")
	gen := &firstDMGenerator{
		deps:    deps,
		rf:      &runFlags{},
		tracker: newFirstDMTracker(),
	}

	lags := gen.sendFirstDM(context.Background(), "userA", "userB")
	// Tracker had the publish recorded but no stages observed.
	assert.Empty(t, lags)
}

// TestWaitForStages_TimeoutReturnsPartial verifies that waitForStages
// respects the per-iteration timeout: it returns whatever stages are
// present without blocking forever when the SUT goes silent.
func TestWaitForStages_TimeoutReturnsPartial(t *testing.T) {
	deps := newFakeFirstDMDeps()
	gen := &firstDMGenerator{
		deps:    deps,
		rf:      &runFlags{},
		tracker: newFirstDMTracker(),
	}

	roomID := "dm-room-timeout"
	publishedAt := time.Now().Add(-firstDMStageTimeout - time.Second) // pre-aged
	gen.tracker.RecordPublish(roomID, "msg-t", "userA", "userB", publishedAt)
	gen.tracker.RecordStage(roomID, firstDMStageRoom, time.Now())

	// publishedAt is in the past beyond firstDMStageTimeout, so
	// waitForStages should return immediately on its first deadline check.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	lags := gen.waitForStages(ctx, roomID, publishedAt)
	_, hasRoom := lags[firstDMStageRoom]
	assert.True(t, hasRoom, "the room stage should still be present after timeout")
	assert.NotEqual(t, 4, len(lags), "incomplete iteration must not return all 4 stages")
}

// TestCollectLags_UnknownRoom returns an empty (non-nil) map.
func TestCollectLags_UnknownRoom(t *testing.T) {
	deps := newFakeFirstDMDeps()
	gen := &firstDMGenerator{
		deps:    deps,
		rf:      &runFlags{},
		tracker: newFirstDMTracker(),
	}
	lags := gen.collectLags("never-tracked")
	assert.NotNil(t, lags)
	assert.Empty(t, lags)
}

// --- helpers ---

// histogramSampleCount returns the total sample count for the given label
// combination across all buckets of the HistogramVec child.
func histogramSampleCount(t *testing.T, vec *prometheus.HistogramVec, labels prometheus.Labels) uint64 {
	t.Helper()
	obs, err := vec.GetMetricWith(labels)
	require.NoError(t, err)
	hist, ok := obs.(prometheus.Histogram)
	require.True(t, ok)
	var m dto.Metric
	require.NoError(t, hist.Write(&m))
	return m.GetHistogram().GetSampleCount()
}

// --- fakes ---

// fakeFirstDMDeps is a minimal ScenarioDeps implementation for first-dm tests.
type fakeFirstDMDeps struct {
	publisher *fakeFirstDMPublisher
	requester *fakeFirstDMRequester
	collector *Collector
	metrics   *Metrics
	fixtures  *Fixtures
	preset    *Preset
	siteID    string
}

func newFakeFirstDMDeps() *fakeFirstDMDeps {
	metrics := NewMetrics()
	return &fakeFirstDMDeps{
		publisher: &fakeFirstDMPublisher{},
		requester: &fakeFirstDMRequester{},
		collector: NewCollector(metrics, ""),
		metrics:   metrics,
		fixtures:  &Fixtures{},
		preset:    &Preset{Name: "test"},
		siteID:    "site-local",
	}
}

func (f *fakeFirstDMDeps) Publisher() Publisher       { return f.publisher }
func (f *fakeFirstDMDeps) Requester() Requester       { return f.requester }
func (f *fakeFirstDMDeps) Collector() *Collector      { return f.collector }
func (f *fakeFirstDMDeps) Metrics() *Metrics          { return f.metrics }
func (f *fakeFirstDMDeps) Fixtures() *Fixtures        { return f.fixtures }
func (f *fakeFirstDMDeps) Preset() *Preset            { return f.preset }
func (f *fakeFirstDMDeps) SiteID() string             { return f.siteID }
func (f *fakeFirstDMDeps) MaxInFlight() int           { return 1 }
func (f *fakeFirstDMDeps) Omission() *OmissionTracker { return nil }
func (f *fakeFirstDMDeps) InjectMode() InjectMode     { return InjectFrontdoor }
func (f *fakeFirstDMDeps) WarmupDeadline() time.Time  { return time.Time{} }
func (f *fakeFirstDMDeps) MessageIDs() []string       { return nil }
func (f *fakeFirstDMDeps) Sites() []SiteDeps          { return nil }
func (f *fakeFirstDMDeps) Subscribers() *Subscribers  { return nil }

type fakeFirstDMPublisher struct {
	mu     sync.Mutex
	calls  []fakePublishCall
	reject error
}

type fakePublishCall struct {
	subject string
	data    []byte
}

func (p *fakeFirstDMPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reject != nil {
		return p.reject
	}
	p.calls = append(p.calls, fakePublishCall{subject: subj, data: append([]byte(nil), data...)})
	return nil
}

type fakeFirstDMRequester struct {
	mu       sync.Mutex
	requests []fakeRequestCall
	reply    []byte
	err      error
}

type fakeRequestCall struct {
	subject string
	data    []byte
}

func (r *fakeFirstDMRequester) Request(_ context.Context, subj string, data []byte, _ time.Duration) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, fakeRequestCall{subject: subj, data: append([]byte(nil), data...)})
	if r.err != nil {
		return nil, r.err
	}
	if r.reply != nil {
		return r.reply, nil
	}
	return json.Marshal(model.CreateRoomReply{Status: model.CreateRoomReplyAccepted, RoomType: "dm"})
}

// fakeFirstDMSubs implements firstDMSubscriber by capturing the subject
// strings and handlers so tests can invoke the handler synchronously.
type fakeFirstDMSubs struct {
	subjects map[string]struct{}
	handlers map[string]func([]byte)
}

func newFakeFirstDMSubs() *fakeFirstDMSubs {
	return &fakeFirstDMSubs{
		subjects: map[string]struct{}{},
		handlers: map[string]func([]byte){},
	}
}

func (s *fakeFirstDMSubs) SubscribeData(subj string, handler func([]byte)) error {
	s.subjects[subj] = struct{}{}
	s.handlers[subj] = handler
	return nil
}
