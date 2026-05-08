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
	p, _ := BuiltinPreset("history-read")
	assert.True(t, needsAutoWarmup("history-read", &p),
		"history-read with default mix needs warm-up (60% kinds need IDs)")
}

func TestNeedsAutoWarmup_False_ForLoadHistoryOnly(t *testing.T) {
	p := Preset{
		Name:       "lh-only",
		HistoryMix: map[historyRequestKind]int{HistoryLoadHistory: 100},
	}
	assert.False(t, needsAutoWarmup("history-read", &p),
		"a 100%% LoadHistory mix has no kinds that need IDs")
}

func TestNeedsAutoWarmup_False_ForOtherScenarios(t *testing.T) {
	p, _ := BuiltinPreset("search-read")
	assert.False(t, needsAutoWarmup("search-read", &p))
	p2, _ := BuiltinPreset("room-rpc")
	assert.False(t, needsAutoWarmup("room-rpc", &p2))
	p3, _ := BuiltinPreset("realistic")
	assert.False(t, needsAutoWarmup("messaging-pipeline", &p3))
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
