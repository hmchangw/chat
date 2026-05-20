package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

type recordingPublisher struct {
	mu    sync.Mutex
	calls []publishCall
}

type publishCall struct {
	subject string
	data    []byte
}

func (r *recordingPublisher) Publish(_ context.Context, subject string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, publishCall{subject: subject, data: append([]byte(nil), data...)})
	return nil
}

func (r *recordingPublisher) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordingPublisher) snapshot() []publishCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]publishCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// Bug 4 part A: when the preset's SenderDist == DistZipf, the per-tick
// sender selection must skew heavily towards the head of the list
// instead of uniform. The realistic preset claims DistZipf but pre-fix
// the generator silently used uniform IntN(len(subs)).
func TestSenderPicker_ZipfSkewsTowardsTopUsers(t *testing.T) {
	const n = 100
	const draws = 20000
	sp := newSenderPicker(n, DistZipf, 42)
	counts := make([]int, n)
	for i := 0; i < draws; i++ {
		counts[sp.pick()]++
	}
	// Top sender should win at least 2x its uniform share (200 / 20000).
	uniformShare := draws / n
	assert.Greater(t, counts[0], 2*uniformShare,
		"Zipf head should dominate by >=2x uniform; got top=%d uniform=%d", counts[0], uniformShare)
	// Tail should be sparse: bottom 10% combined < top sender.
	tail := 0
	for i := n - n/10; i < n; i++ {
		tail += counts[i]
	}
	assert.Less(t, tail, counts[0],
		"Zipf tail (bottom 10%%) should be less than top sender alone")
}

func TestSenderPicker_UniformIsApproximatelyEven(t *testing.T) {
	const n = 100
	const draws = 20000
	sp := newSenderPicker(n, DistUniform, 42)
	counts := make([]int, n)
	for i := 0; i < draws; i++ {
		counts[sp.pick()]++
	}
	// No bucket should grossly dominate (uniform expectation = 200,
	// allow 4x slack).
	uniformShare := draws / n
	for i := 0; i < n; i++ {
		assert.Less(t, counts[i], 4*uniformShare,
			"uniform draw at idx %d=%d should be near %d", i, counts[i], uniformShare)
	}
}

func TestSenderPicker_EmptyReturnsZero(t *testing.T) {
	sp := newSenderPicker(0, DistUniform, 42)
	assert.Equal(t, 0, sp.pick(), "empty picker is a no-op; caller guards on len")
}

// Bug 4 part B: ThreadRate should mark a fraction of published messages
// as thread replies. Pre-fix the field was unread; verify the threadPool
// honors the rate within a tolerance band.
func TestThreadPool_HonorsThreadRate(t *testing.T) {
	tp := newThreadPool(64)
	// Seed with parents so picks succeed.
	for i := 0; i < 10; i++ {
		tp.add(fmt.Sprintf("p-%d", i))
	}
	const draws = 5000
	const rate = 0.30
	hits := 0
	for i := 0; i < draws; i++ {
		if id, _ := tp.maybeParent(rate); id != "" {
			hits++
		}
	}
	frac := float64(hits) / float64(draws)
	assert.InDelta(t, rate, frac, 0.04, "thread-pick fraction should be near rate; got %.3f", frac)
}

func TestThreadPool_ZeroRateNeverPicks(t *testing.T) {
	tp := newThreadPool(64)
	tp.add("p-1")
	for i := 0; i < 100; i++ {
		id, _ := tp.maybeParent(0.0)
		assert.Empty(t, id, "rate=0 must never pick a parent")
	}
}

func TestThreadPool_EmptyPoolReturnsEmpty(t *testing.T) {
	tp := newThreadPool(64)
	for i := 0; i < 100; i++ {
		id, _ := tp.maybeParent(1.0)
		assert.Empty(t, id, "empty pool returns empty even at rate=1.0")
	}
}

func TestThreadPool_RingBufferBoundsMemory(t *testing.T) {
	tp := newThreadPool(4)
	for i := 0; i < 100; i++ {
		tp.add(fmt.Sprintf("p-%d", i))
	}
	assert.LessOrEqual(t, tp.size(), 4, "ring buffer must cap at capacity")
}

// Bug 4 wiring: when a preset has ThreadRate>0 and the warmup phase
// has populated some message IDs, a non-trivial share of published
// SendMessageRequests should carry ThreadParentMessageID.
func TestGenerator_ThreadRate_PopulatesParentField(t *testing.T) {
	p := Preset{
		Name: "thread-test", Users: 5, Rooms: 2,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 50, Max: 50},
		ThreadRate:   0.5, // half of messages should be replies
	}
	f := BuildFixtures(&p, 7, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m, Collector: c,
	}, 7)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))
	calls := rp.snapshot()
	require.NotEmpty(t, calls, "should have published")
	threaded := 0
	for _, c := range calls {
		var req model.SendMessageRequest
		if err := json.Unmarshal(c.data, &req); err != nil {
			continue
		}
		if req.ThreadParentMessageID != "" {
			threaded++
		}
	}
	// We need a non-zero fraction. After the first publish the pool has
	// one message, so subsequent publishes can thread off it. Expect at
	// least ~20% threading in a 0.5-rate run (warmup of empty pool + RNG).
	assert.Greater(t, threaded, len(calls)/5,
		"expected >=20%% of messages to be thread replies; got %d/%d", threaded, len(calls))
}

type errorPublisher struct{}

func (e *errorPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return fmt.Errorf("publish error")
}

func TestGenerator_SendsExpectedCount(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset:    &p,
		Fixtures:  f,
		SiteID:    "site-local",
		Rate:      200,
		Inject:    InjectFrontdoor,
		Publisher: rp,
		Metrics:   m,
		Collector: c,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	count := rp.count()
	// 200 msg/s for ~250ms: expect 30-70 publishes (wide tolerance for scheduler).
	assert.GreaterOrEqual(t, count, 30)
	assert.LessOrEqual(t, count, 70)
}

func TestGenerator_UsesFrontdoorSubject(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	calls := rp.snapshot()
	require.NotEmpty(t, calls)
	for i := range calls {
		assert.Contains(t, calls[i].subject, ".msg.send")
		assert.Contains(t, calls[i].subject, "site-local")
	}
}

func TestGenerator_UsesCanonicalSubjectWhenInjectCanonical(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectCanonical,
		Publisher: rp, Metrics: m,
		Collector: c,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	calls := rp.snapshot()
	require.NotEmpty(t, calls)
	for i := range calls {
		assert.Contains(t, calls[i].subject, "chat.msg.canonical.site-local.created")
	}

	// In canonical mode, the Generator should NOT populate byReqID because
	// canonical injection bypasses the gatekeeper (no reply is expected).
	// Consequently Finalize should report zero missing replies even though
	// no replies ever arrived.
	missingReplies, _ := c.Finalize()
	assert.Equal(t, 0, missingReplies)
}

func TestGenerator_IncrementsPublishedMetric(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)

	var got int64
	metrics, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, mf := range metrics {
		if mf.GetName() == "loadgen_published_total" {
			for _, metric := range mf.GetMetric() {
				got += int64(metric.GetCounter().GetValue())
			}
		}
	}
	assert.Greater(t, got, int64(0))
}

func TestGenerator_Run_ReturnsErrorForZeroRate(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 0, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	err := g.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate must be > 0")
}

func TestGenerator_PublishError_IncrementsErrorMetric(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	ep := &errorPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: ep, Metrics: m,
		Collector: c,
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)

	var publishErrors int64
	metrics, err := m.Registry.Gather()
	require.NoError(t, err)
	for _, mf := range metrics {
		if mf.GetName() == "loadgen_publish_errors_total" {
			for _, metric := range mf.GetMetric() {
				publishErrors += int64(metric.GetCounter().GetValue())
			}
		}
	}
	assert.Greater(t, publishErrors, int64(0))

	// Publish errors should have cleaned up the pending entries, so Finalize
	// reports no "missing replies" or "missing broadcasts" attributable to
	// publish-side failures.
	missingReplies, missingBroadcasts := c.Finalize()
	assert.Equal(t, 0, missingReplies)
	assert.Equal(t, 0, missingBroadcasts)
}

func TestGenerator_Content_WithMentionRate(t *testing.T) {
	p, _ := BuiltinPreset("realistic")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	// Run long enough to statistically hit the 10% mention rate.
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 99)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	calls := rp.snapshot()
	require.NotEmpty(t, calls)
	// With 10% mention rate and ~100 messages, at least one should contain "@user-".
	foundMention := false
	for i := range calls {
		if strings.Contains(string(calls[i].data), "@user-") {
			foundMention = true
			break
		}
	}
	assert.True(t, foundMention, "expected at least one message with a mention")
}

func TestGenerator_EmptySubscriptions_NoPublish(t *testing.T) {
	p, _ := BuiltinPreset("small")
	rp := &recordingPublisher{}
	m := NewMetrics()
	// Use empty fixtures — no subscriptions.
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 200, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	assert.Equal(t, 0, rp.count())
}

func TestGenerator_MaxInFlightZeroRunsSerially(t *testing.T) {
	// MaxInFlight=0 preserves the legacy serial-on-ticker behavior.
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 200, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector:   c,
		MaxInFlight: 0,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	// Same tolerance as the default SendsExpectedCount test.
	count := rp.count()
	assert.GreaterOrEqual(t, count, 30)
	assert.LessOrEqual(t, count, 70)
}

// blockingPublisher blocks every Publish call until unblock is closed.
// Used to force worker-pool saturation.
type blockingPublisher struct {
	unblock chan struct{}
	mu      sync.Mutex
	count   int
}

func (b *blockingPublisher) Publish(ctx context.Context, _ string, _ []byte) error {
	select {
	case <-b.unblock:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	b.count++
	b.mu.Unlock()
	return nil
}

func TestGenerator_PoolSaturationCountedAsError(t *testing.T) {
	// With MaxInFlight=1 and a publisher that never returns while the run is
	// active, every tick after the first must see the pool saturated and
	// increment loadgen_publish_errors_total{reason="saturated"}.
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	bp := &blockingPublisher{unblock: make(chan struct{})}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate: 500, Inject: InjectFrontdoor,
		Publisher: bp, Metrics: m,
		Collector:   c,
		MaxInFlight: 1,
	}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	close(bp.unblock)

	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	var saturated float64
	for _, mf := range mfs {
		if mf.GetName() != "loadgen_publish_errors_total" {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "reason" && l.GetValue() == "saturated" {
					saturated += metric.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Greater(t, saturated, float64(0), "expected saturated counter to increment under pool-full conditions")
}

func TestBuildHistoryRequest(t *testing.T) {
	user := model.User{ID: "u-000001", Account: "user-1", SiteID: "site-local"}
	room := model.Room{ID: "room-000001", SiteID: "site-local"}
	args := historyRequestArgs{
		User: user, Room: room,
		MessageID: "m-deadbeef", Limit: 50,
	}

	t.Run("LoadHistory", func(t *testing.T) {
		subj, body, err := buildHistoryRequest(HistoryLoadHistory, &args)
		require.NoError(t, err)
		assert.Equal(t,
			"chat.user.user-1.request.room.room-000001.site-local.msg.history",
			subj)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		assert.EqualValues(t, 50, got["limit"])
	})

	t.Run("GetMessageByID", func(t *testing.T) {
		subj, body, err := buildHistoryRequest(HistoryGetMessageByID, &args)
		require.NoError(t, err)
		assert.Equal(t,
			"chat.user.user-1.request.room.room-000001.site-local.msg.get",
			subj)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "m-deadbeef", got["messageId"])
	})

	t.Run("LoadSurroundingMessages", func(t *testing.T) {
		subj, body, err := buildHistoryRequest(HistoryLoadSurrounding, &args)
		require.NoError(t, err)
		assert.Equal(t,
			"chat.user.user-1.request.room.room-000001.site-local.msg.surrounding",
			subj)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "m-deadbeef", got["messageId"])
		assert.EqualValues(t, 50, got["limit"])
	})

	t.Run("GetThreadMessages", func(t *testing.T) {
		subj, body, err := buildHistoryRequest(HistoryGetThreadMessages, &args)
		require.NoError(t, err)
		assert.Equal(t,
			"chat.user.user-1.request.room.room-000001.site-local.msg.thread",
			subj)
		var got map[string]any
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "m-deadbeef", got["threadMessageId"])
		assert.EqualValues(t, 50, got["limit"])
	})

	t.Run("UnknownKindReturnsError", func(t *testing.T) {
		_, _, err := buildHistoryRequest(historyRequestKind(99), &args)
		assert.Error(t, err)
	})
}

func TestBuildSearchRequest(t *testing.T) {
	user := model.User{ID: "u-000001", Account: "user-1", SiteID: "site-local"}
	args := searchRequestArgs{User: user, Query: "hello", Scope: "channel", Size: 20}

	t.Run("SearchMessages", func(t *testing.T) {
		subj, body, err := buildSearchRequest(SearchMessagesKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.search.messages", subj)
		var got model.SearchMessagesRequest
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "hello", got.Query)
		assert.Equal(t, 20, got.Size)
	})

	t.Run("SearchRooms", func(t *testing.T) {
		subj, body, err := buildSearchRequest(SearchRoomsKind, &args)
		require.NoError(t, err)
		assert.Equal(t, "chat.user.user-1.request.search.rooms", subj)
		var got model.SearchRoomsRequest
		require.NoError(t, json.Unmarshal(body, &got))
		assert.Equal(t, "hello", got.Query)
		assert.Equal(t, "channel", got.RoomType)
		assert.Equal(t, 20, got.Size)
	})

	t.Run("UnknownKindReturnsError", func(t *testing.T) {
		_, _, err := buildSearchRequest(searchRequestKind(99), &args)
		assert.Error(t, err)
	})
}

func TestGenerator_Ramp_OverridesFixedRate(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate:      0, // ramp-only
		Inject:    InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
		Ramp: &Ramp{
			From: 100, To: 1000,
			Duration: 200 * time.Millisecond,
			Shape:    RampLinear,
		},
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))
	count := rp.count()
	// Ramp 100→1000 over 200ms (avg ~550) + 50ms tail at 1000 ≈ 160+ publishes.
	// Wide tolerance for scheduler.
	assert.GreaterOrEqual(t, count, 50, "ramp should drive publishes; got %d", count)
}

func TestGenerator_RejectsZeroRateAndNoRamp(t *testing.T) {
	p, _ := BuiltinPreset("small")
	g := NewGenerator(&GeneratorConfig{
		Preset: &p, Fixtures: Fixtures{}, SiteID: "site-local",
		Rate: 0, Inject: InjectFrontdoor,
		Publisher: &recordingPublisher{}, Metrics: NewMetrics(),
		Collector: NewCollector(NewMetrics(), p.Name),
	}, 1)
	err := g.Run(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRate)
}

// TestGenerator_NoDMRatioRaceWithMaxInFlight is a race-detector regression for
// Fix 1: g.rng was accessed concurrently from publishOne goroutines when
// DMRatio > 0 AND MaxInFlight > 0. With -race this would fail before the fix.
// After the fix (randv2 package-level globals), it must pass cleanly.
func TestGenerator_NoDMRatioRaceWithMaxInFlight(t *testing.T) {
	p := &Preset{
		Name:         "race-test",
		Users:        20,
		Rooms:        20,
		DMRatio:      0.6,
		RoomSizeDist: DistUniform,
		SenderDist:   DistUniform,
		ContentBytes: Range{Min: 50, Max: 50},
	}
	f := BuildFixtures(p, 1, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset:      p,
		Fixtures:    f,
		SiteID:      "site-local",
		Rate:        500,
		Inject:      InjectFrontdoor,
		Publisher:   rp,
		Metrics:     m,
		Collector:   c,
		MaxInFlight: 8,
	}, 42)

	// Run for 1000 ticks' worth of time — the race detector will catch any
	// concurrent access to a shared rand source if the fix was reverted.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))
	assert.Greater(t, rp.count(), 0, "should have published at least one message")
}

// TestParseInjectMode lives in flags_test.go (predates PR #203's exported
// ParseInjectMode helper, but exercises identical semantics). The two parsers
// have not yet been consolidated — both parseInjectMode (flags.go, internal)
// and ParseInjectMode (generator.go, exported for members handlers) coexist.
// Tracked as a follow-up; not blocking.
