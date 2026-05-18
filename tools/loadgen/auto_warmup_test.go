package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector_MessageIDs_ReturnsAllSeen(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	now := time.Unix(0, 0)
	c.RecordPublish("req-1", "msg-aaa", now)
	c.RecordPublish("req-2", "msg-bbb", now)
	c.RecordPublishBroadcastOnly("msg-ccc", now)

	ids := c.MessageIDs()
	assert.ElementsMatch(t, []string{"msg-aaa", "msg-bbb", "msg-ccc"}, ids,
		"MessageIDs must return every message ID published, even after RecordReply / RecordBroadcast consumes them")

	// Consume one — IDs list is unaffected.
	c.RecordReply("req-1", now.Add(2*time.Millisecond))
	ids = c.MessageIDs()
	assert.ElementsMatch(t, []string{"msg-aaa", "msg-bbb", "msg-ccc"}, ids)
}

func TestNeedsAutoWarmup_True_WhenMixIncludesIDKinds(t *testing.T) {
	sc, ok := LookupScenario("history-read")
	require.True(t, ok, "history-read must be registered")
	aw, ok := sc.(AutoWarmer)
	require.True(t, ok, "history-read must implement AutoWarmer")
	p, _ := BuiltinPreset("history-read")
	assert.True(t, aw.NeedsAutoWarmup(&p),
		"history-read with default mix needs warm-up (60% kinds need IDs)")
}

func TestNeedsAutoWarmup_False_ForLoadHistoryOnly(t *testing.T) {
	sc, ok := LookupScenario("history-read")
	require.True(t, ok)
	aw, ok := sc.(AutoWarmer)
	require.True(t, ok)
	p := Preset{
		Name:       "lh-only",
		HistoryMix: map[historyRequestKind]int{HistoryLoadHistory: 100},
	}
	assert.False(t, aw.NeedsAutoWarmup(&p),
		"a 100%% LoadHistory mix has no kinds that need IDs")
}

func TestNeedsAutoWarmup_False_ForOtherScenarios(t *testing.T) {
	for _, name := range []string{"search-read", "room-rpc", "messaging-pipeline"} {
		t.Run(name, func(t *testing.T) {
			sc, ok := LookupScenario(name)
			require.True(t, ok, "%s must be registered", name)
			_, isWarmer := sc.(AutoWarmer)
			assert.False(t, isWarmer,
				"%s must not implement AutoWarmer", name)
		})
	}
}

// Bug 3: auto-warmup must always publish via the frontdoor (NATS core),
// regardless of whether the run-level publisher is configured for
// canonical/JetStream injection. Pre-fix the auto-warmup phase reused
// the run-level *natsCorePublisher with useJetStream=true, sending its
// frontdoor subjects through js.PublishMsgAsync — which targets a
// non-stream subject and silently fails or errors, leaving the
// message-ID pool empty for the read scenario.
func TestNewWarmupPublisher_AlwaysFrontdoor(t *testing.T) {
	cases := []struct {
		name string
		run  *natsCorePublisher
	}{
		{
			name: "run-level publisher in canonical+async mode",
			run: &natsCorePublisher{
				useJetStream: true, asyncJS: true, runID: "run-xyz",
			},
		},
		{
			name: "run-level publisher in canonical sync mode",
			run: &natsCorePublisher{
				useJetStream: true, asyncJS: false, runID: "run-xyz",
			},
		},
		{
			name: "run-level publisher already frontdoor",
			run: &natsCorePublisher{
				useJetStream: false, asyncJS: false, runID: "run-xyz",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wp := newWarmupPublisher(tc.run)
			assert.False(t, wp.useJetStream,
				"warmup publisher must never route through JetStream")
			assert.False(t, wp.asyncJS,
				"warmup publisher must never use async-JS path")
			assert.Equal(t, tc.run.runID, wp.runID,
				"warmup publisher must inherit the run ID for trace correlation")
			assert.Same(t, tc.run.pool, wp.pool,
				"warmup publisher must reuse the run's conn pool")
		})
	}
}

func TestRunAutoWarmup_PopulatesMessageIDsViaPublisher(t *testing.T) {
	p, _ := BuiltinPreset("history-read")
	f := BuildFixtures(&p, 1, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	ids, err := runAutoWarmup(ctx, &autoWarmupConfig{
		Preset: &p, Fixtures: f, SiteID: "site-local",
		Rate:      200,
		Publisher: rp, Metrics: m, Collector: c,
		Duration: 100 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, ids, "expected at least one message ID after a 100ms warm-up at 200rps")
	// IDs should be unique.
	seen := map[string]struct{}{}
	for _, id := range ids {
		_, dup := seen[id]
		assert.False(t, dup, "duplicate message ID in pool: %s", id)
		seen[id] = struct{}{}
	}
}
