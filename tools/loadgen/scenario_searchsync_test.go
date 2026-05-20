package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// fakeSearchRequester returns canned SearchMessagesResponse payloads keyed by
// query text, simulating an ES index that starts empty and gains hits later.
type fakeSearchRequester struct {
	mu sync.Mutex
	// hits maps query text -> sequence of responses to serve in order. When the
	// sequence is exhausted, the last response is replayed indefinitely.
	hits map[string][]model.SearchMessagesResponse
	// calls records each request for assertion in tests.
	calls []fakeSearchCall
	// err, when non-nil, is returned for every call (overrides hits).
	err error
}

type fakeSearchCall struct {
	subject string
	req     model.SearchMessagesRequest
}

func (f *fakeSearchRequester) Request(_ context.Context, subj string, data []byte, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var req model.SearchMessagesRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, fakeSearchCall{subject: subj, req: req})

	seq := f.hits[req.Query]
	if len(seq) == 0 {
		empty, _ := json.Marshal(model.SearchMessagesResponse{Messages: []model.SearchMessage{}})
		return empty, nil
	}
	// Pop from the front if there's still a queue; otherwise replay the tail.
	var resp model.SearchMessagesResponse
	if len(seq) > 1 {
		resp = seq[0]
		f.hits[req.Query] = seq[1:]
	} else {
		resp = seq[0]
	}
	return json.Marshal(resp)
}

func TestSearchIndexLookup_NotVisibleWhenResponseEmpty(t *testing.T) {
	r := &fakeSearchRequester{hits: map[string][]model.SearchMessagesResponse{}}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	assert.ErrorIs(t, err, ErrRAWNotVisible)
}

func TestSearchIndexLookup_VisibleWhenMessageIDMatches(t *testing.T) {
	r := &fakeSearchRequester{
		hits: map[string][]model.SearchMessagesResponse{
			"tok-abc": {{Messages: []model.SearchMessage{{MessageID: "msg-xyz"}}, Total: 1}},
		},
	}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	assert.NoError(t, err)
}

func TestSearchIndexLookup_NotVisibleWhenMessageIDMismatch(t *testing.T) {
	// search-service returns hits, but none of them are ours (token collision
	// with another room's message). Treat as not-yet-visible.
	r := &fakeSearchRequester{
		hits: map[string][]model.SearchMessagesResponse{
			"tok-abc": {{Messages: []model.SearchMessage{{MessageID: "msg-other"}}, Total: 1}},
		},
	}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	assert.ErrorIs(t, err, ErrRAWNotVisible)
}

func TestSearchIndexLookup_ScopesQueryByRoomAndUser(t *testing.T) {
	r := &fakeSearchRequester{hits: map[string][]model.SearchMessagesResponse{}}
	lookup := newSearchIndexLookup(r, "alice", "room-7", "tok-9", "msg-1", 1*time.Second)
	_ = lookup(context.Background(), "msg-1")

	require.Len(t, r.calls, 1)
	call := r.calls[0]
	assert.Contains(t, call.subject, "alice", "subject must target the per-user search subject")
	assert.Equal(t, "tok-9", call.req.Query, "query must be the unique token to keep ES selectivity high")
	assert.Equal(t, []string{"room-7"}, call.req.RoomIDs, "lookup must scope by roomID to bound search-service work")
}

func TestSearchIndexLookup_TransportErrorPropagates(t *testing.T) {
	r := &fakeSearchRequester{err: errors.New("nats timeout")}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRAWNotVisible,
		"transport errors must surface as themselves so the operator distinguishes ES-not-yet-indexed from broken plumbing")
}

func TestSearchIndexToken_DeterministicAndAnalyzerSafe(t *testing.T) {
	// Token derivation must be deterministic per messageID so a publisher and
	// a poller using the same msgID compute the same token without sharing state.
	tok1 := searchIndexToken("msg-abc")
	tok2 := searchIndexToken("msg-abc")
	assert.Equal(t, tok1, tok2)

	// Different messages get different tokens; otherwise concurrent runs would
	// false-positive on each other's docs.
	assert.NotEqual(t, tok1, searchIndexToken("msg-def"))

	// Token must consist solely of [a-z0-9] so search-sync-worker's
	// `custom_analyzer` (custom pattern tokenizer + word_delimiter_graph +
	// lowercase, see search-sync-worker/messages.go:160) preserves it as a
	// single token. The load-bearing analyzer setting is
	// `split_on_numerics=false` — an alphanumeric token like `lgst1a2b3c…`
	// must NOT split into `lgst` + `1a2b3c`, or the SearchMessagesRequest's
	// match-by-token returns the wrong document set.
	for _, r := range tok1 {
		isAlnumLower := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		assert.True(t, isAlnumLower, "token char %q must be lowercase alphanumeric", r)
	}
	assert.GreaterOrEqual(t, len(tok1), 8, "token must be long enough to make collisions across a run vanishingly unlikely")
}

func TestSearchSyncBuildMessageContent_EmbedsToken(t *testing.T) {
	got := searchSyncMessageContent("msg-abc")
	assert.True(t, strings.Contains(got, searchIndexToken("msg-abc")),
		"published message content must contain the lookup token so ES is the one source of truth for visibility")
}

func TestSearchSyncScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("search-sync-lag")
	require.True(t, ok)
	assert.Equal(t, "search-sync-lag", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}

// ----------------------------------------------------------- NewGenerator

func TestSearchSyncNewGenerator_RejectsEmptyFixtures(t *testing.T) {
	sc, ok := LookupScenario("search-sync-lag")
	require.True(t, ok)
	factory := sc.(GeneratorFactory)
	// Even with canonical inject configured, empty fixtures must fail fast —
	// nothing to publish from and no ACL to bootstrap.
	deps := &fakeScenarioDeps{
		fixtures:   &Fixtures{},
		siteID:     "site-local",
		injectMode: InjectCanonical,
	}
	_, err := factory.NewGenerator(deps, &runFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixture subscription")
}

func TestSearchSyncNewGenerator_RejectsFrontdoorInject(t *testing.T) {
	sc, ok := LookupScenario("search-sync-lag")
	require.True(t, ok)
	factory := sc.(GeneratorFactory)
	preset, _ := BuiltinPreset("search-read")
	fixtures := BuildFixtures(&preset, 42, "site-local")
	deps := &fakeScenarioDeps{
		fixtures:   &fixtures,
		preset:     &preset,
		siteID:     "site-local",
		injectMode: InjectFrontdoor,
	}
	_, err := factory.NewGenerator(deps, &runFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "canonical",
		"NewGenerator must explain that --inject=canonical is required (search-sync-worker consumes from MESSAGES_CANONICAL)")
}

func TestSearchSyncNewGenerator_AcceptsCanonicalInject(t *testing.T) {
	sc, ok := LookupScenario("search-sync-lag")
	require.True(t, ok)
	factory := sc.(GeneratorFactory)
	preset, _ := BuiltinPreset("search-read")
	fixtures := BuildFixtures(&preset, 42, "site-local")
	deps := &fakeScenarioDeps{
		fixtures:   &fixtures,
		preset:     &preset,
		siteID:     "site-local",
		injectMode: InjectCanonical,
	}
	r, err := factory.NewGenerator(deps, &runFlags{})
	require.NoError(t, err)
	assert.NotNil(t, r)
}

// ---------------------------------------------- publishCanonicalForSearchSync

func TestPublishCanonicalForSearchSync_PayloadShape(t *testing.T) {
	pub := &recordingPublisher{}
	sub := &model.Subscription{
		RoomID: "room-7",
		User:   model.SubscriptionUser{ID: "u-1", Account: "alice"},
	}
	publishedAt := time.Now()
	err := publishCanonicalForSearchSync(context.Background(), pub, "site-local", sub, "msg-1", "tok-1", publishedAt)
	require.NoError(t, err)

	calls := pub.snapshot()
	require.Len(t, calls, 1)
	// Subject must be the MESSAGES_CANONICAL "created" subject for this site.
	wantSubj := subject.MsgCanonicalCreated("site-local")
	assert.Equal(t, wantSubj, calls[0].subject,
		"canonical-created subject is the load-bearing routing key for search-sync-worker's consumer")

	var evt model.MessageEvent
	require.NoError(t, json.Unmarshal(calls[0].data, &evt))
	assert.Equal(t, model.EventCreated, evt.Event, "search-sync-worker only indexes EventCreated; edited/deleted are different paths")
	assert.Equal(t, publishedAt.UnixMilli(), evt.Timestamp, "event-level timestamp must be the publish-site UnixMilli per CLAUDE.md")
	assert.Equal(t, "msg-1", evt.Message.ID)
	assert.Equal(t, "room-7", evt.Message.RoomID)
	assert.Equal(t, "alice", evt.Message.UserAccount)
	assert.Contains(t, evt.Message.Content, "tok-1",
		"published content must embed the lookup token so ES is the single source of truth for visibility")
	assert.Equal(t, "site-local", evt.SiteID)
}

// ---------------------------------------------- bootstrapSearchSyncACL

func TestBootstrapSearchSyncACL_OneEventPerUniquePair(t *testing.T) {
	pub := &recordingPublisher{}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel}, // dup
		{RoomID: "room-2", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "bob"}, RoomType: model.RoomTypeChannel},
	}
	err := bootstrapSearchSyncACL(context.Background(), pub, "site-local", subs)
	require.NoError(t, err)

	calls := pub.snapshot()
	assert.Len(t, calls, 3, "duplicate (account, room) pair must collapse to a single ACL publish")
	wantSubj := subject.InboxMemberAdded("site-local")
	for _, c := range calls {
		assert.Equal(t, wantSubj, c.subject,
			"ACL events publish to the local INBOX member_added subject (search-sync-worker user-room-sync consumer subscribes here)")
		var evt model.OutboxEvent
		require.NoError(t, json.Unmarshal(c.data, &evt))
		assert.Equal(t, model.OutboxMemberAdded, evt.Type, "search-sync-worker user-room-sync only acts on member_added/removed")
		assert.Equal(t, "site-local", evt.SiteID)
		assert.Greater(t, evt.Timestamp, int64(0), "event timestamp gates the painless LWW logic; must be > 0")

		var inner model.InboxMemberEvent
		require.NoError(t, json.Unmarshal(evt.Payload, &inner))
		assert.NotEmpty(t, inner.RoomID)
		assert.Len(t, inner.Accounts, 1, "one event per (account, room) — bulk fan-out happens upstream in real traffic")
		assert.Nil(t, inner.HistorySharedSince,
			"unrestricted ACL: nil HSS pointer is the painless contract for `hss <= 0 → unrestricted`")
	}
}

func TestBootstrapSearchSyncACL_PropagatesPublishError(t *testing.T) {
	pub := &erroringPublisher{err: errors.New("nats down")}
	subs := []model.Subscription{
		{RoomID: "room-1", User: model.SubscriptionUser{Account: "alice"}, RoomType: model.RoomTypeChannel},
	}
	err := bootstrapSearchSyncACL(context.Background(), pub, "site-local", subs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nats down")
}

type erroringPublisher struct{ err error }

func (e *erroringPublisher) Publish(_ context.Context, _ string, _ []byte) error { return e.err }

// ---------------------------------------------- Run integration

// buildSearchSyncDeps wires a fakeScenarioDeps suitable for Run-level
// integration tests: canonical inject mode (so NewGenerator accepts), the
// given publisher + requester, and a fresh metrics registry the test can
// inspect.
func buildSearchSyncDeps(t *testing.T, pub Publisher, req Requester) *fakeScenarioDeps {
	t.Helper()
	preset, _ := BuiltinPreset("search-read")
	// Keep fixtures tiny so the test loop terminates quickly.
	preset.Users = 1
	preset.Rooms = 1
	preset.DMRatio = 0
	fixtures := BuildFixtures(&preset, 42, "site-local")
	require.NotEmpty(t, fixtures.Subscriptions, "test setup error: fixtures must contain at least one sub")
	return &fakeScenarioDeps{
		fixtures:   &fixtures,
		preset:     &preset,
		siteID:     "site-local",
		pub:        pub,
		req:        req,
		metrics:    NewMetrics(),
		injectMode: InjectCanonical,
	}
}

// coupledPubReq is a Publisher and Requester that share state: the publisher
// records every canonical MessageEvent's msgID, and the requester returns all
// recorded msgIDs on every search request. This lets us drive the scenario's
// publish→poll loop to a "visible" outcome without the test having to predict
// UUIDv7 msgIDs in advance.
type coupledPubReq struct {
	mu     sync.Mutex
	pubIDs []string
	// aclSubject is the subject the publisher should NOT register as a
	// MessageEvent (ACL bootstrap publishes are OutboxEvents).
	aclSubject string
}

func newCoupledPubReq() *coupledPubReq {
	return &coupledPubReq{aclSubject: subject.InboxMemberAdded("site-local")}
}

func (c *coupledPubReq) Publish(_ context.Context, subj string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if subj == c.aclSubject {
		return nil
	}
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err == nil && evt.Message.ID != "" {
		c.pubIDs = append(c.pubIDs, evt.Message.ID)
	}
	return nil
}

func (c *coupledPubReq) Request(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := make([]model.SearchMessage, 0, len(c.pubIDs))
	for _, id := range c.pubIDs {
		msgs = append(msgs, model.SearchMessage{MessageID: id})
	}
	return json.Marshal(model.SearchMessagesResponse{Messages: msgs, Total: int64(len(msgs))})
}

func TestSearchSyncRun_ObservesLagOnVisibleResponse(t *testing.T) {
	coupled := newCoupledPubReq()
	deps := buildSearchSyncDeps(t, coupled, coupled)
	// Short flags so the test completes well under a second.
	rf := &runFlags{
		Rate:           50,
		RequestTimeout: 50 * time.Millisecond,
		SearchSync: SearchSyncFlags{
			PollInterval: 1 * time.Millisecond,
			Timeout:      200 * time.Millisecond,
			ACLWait:      1 * time.Millisecond,
		},
	}
	sc, _ := LookupScenario("search-sync-lag")
	gen, err := sc.(GeneratorFactory).NewGenerator(deps, rf)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	runErr := gen.Run(ctx)
	require.NoError(t, runErr)

	count := getHistogramSampleCount(t, deps.metrics.Registry, "loadgen_search_index_lag_seconds")
	assert.GreaterOrEqual(t, count, uint64(1), "Run must observe at least one lag sample when search returns hits")
	visibleCount := getCounterValue(t, deps.metrics.SearchIndexVisible.WithLabelValues("visible"))
	assert.Greater(t, visibleCount, 0.0, "Run must increment outcome=visible at least once")
}

func TestSearchSyncRun_CountsTimeoutsAsOutcome(t *testing.T) {
	pub := &recordingPublisher{}
	// Empty responses → every poll observes ErrRAWNotVisible → terminates as timeout.
	req := &fakeSearchRequester{hits: map[string][]model.SearchMessagesResponse{}}
	deps := buildSearchSyncDeps(t, pub, req)
	rf := &runFlags{
		Rate:           50,
		RequestTimeout: 5 * time.Millisecond,
		SearchSync: SearchSyncFlags{
			PollInterval: 1 * time.Millisecond,
			Timeout:      20 * time.Millisecond,
			ACLWait:      1 * time.Millisecond,
		},
	}
	sc, _ := LookupScenario("search-sync-lag")
	gen, err := sc.(GeneratorFactory).NewGenerator(deps, rf)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	runErr := gen.Run(ctx)
	require.NoError(t, runErr)

	timeoutCount := getCounterValue(t, deps.metrics.SearchIndexVisible.WithLabelValues("timeout"))
	assert.Greater(t, timeoutCount, 0.0, "polls that never see a hit must classify as outcome=timeout")
	lagCount := getHistogramSampleCount(t, deps.metrics.Registry, "loadgen_search_index_lag_seconds")
	assert.Equal(t, uint64(0), lagCount, "no lag samples should be recorded for timeouts")
}

func TestSearchSyncRun_DrainsInFlightOnCancel(t *testing.T) {
	pub := &recordingPublisher{}
	// blockingRequester counts active in-flight calls and only completes when
	// the context is cancelled; this verifies Run waits for pollers before
	// returning rather than leaking goroutines.
	req := &blockingRequester{}
	deps := buildSearchSyncDeps(t, pub, req)
	rf := &runFlags{
		Rate:           50,
		RequestTimeout: 500 * time.Millisecond,
		SearchSync: SearchSyncFlags{
			PollInterval: 1 * time.Millisecond,
			Timeout:      500 * time.Millisecond,
			ACLWait:      1 * time.Millisecond,
		},
	}
	sc, _ := LookupScenario("search-sync-lag")
	gen, err := sc.(GeneratorFactory).NewGenerator(deps, rf)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- gen.Run(ctx) }()

	// Wait until at least one poller is actually blocked in Request before we
	// cancel — otherwise the test races between Run's ACL-wait/tick scheduling
	// and the cancel, and the "drained pollers" assertion becomes vacuous.
	select {
	case <-req.firstRequest():
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatalf("no poller started within 2s; active-now=%d", req.activeCount())
	}
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s after cancel; active-now=%d", req.activeCount())
	}
	// After Run returns the wait-group drain guarantees no in-flight pollers remain.
	assert.Equal(t, int32(0), req.activeCount(),
		"Run must wait for in-flight pollers; leaked goroutines would still report active")
}

// blockingRequester blocks every Request until ctx cancellation, tracking
// concurrent in-flight count via atomic. Used to verify Run drains pollers
// on shutdown.
//
// firstReq is closed on the first Request call so tests can deterministically
// wait until a poller is actually in flight before cancelling. Tests that use
// time.Sleep to wait for goroutines to start violate CLAUDE.md §3.
type blockingRequester struct {
	active     atomic.Int32
	firstReq   chan struct{}
	firstReqMu sync.Mutex
	signalled  bool
}

func (b *blockingRequester) Request(ctx context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
	b.firstReqMu.Lock()
	if !b.signalled {
		b.signalled = true
		if b.firstReq != nil {
			close(b.firstReq)
		}
	}
	b.firstReqMu.Unlock()
	b.active.Add(1)
	defer b.active.Add(-1)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (b *blockingRequester) activeCount() int32 { return b.active.Load() }

// firstRequest returns a channel that closes the first time Request is called.
// Lazily initialized so callers that don't need it pay nothing.
func (b *blockingRequester) firstRequest() <-chan struct{} {
	b.firstReqMu.Lock()
	defer b.firstReqMu.Unlock()
	if b.firstReq == nil {
		b.firstReq = make(chan struct{})
		if b.signalled {
			close(b.firstReq)
		}
	}
	return b.firstReq
}

// getHistogramSampleCount looks up the named histogram in the given registry
// and returns its sample count.
func getHistogramSampleCount(t *testing.T, reg *prometheus.Registry, name string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		var total uint64
		for _, m := range mf.GetMetric() {
			total += m.GetHistogram().GetSampleCount()
		}
		return total
	}
	return 0
}

// getCounterValue reads the current value of a single counter instance.
func getCounterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}
