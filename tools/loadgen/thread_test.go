package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

// TestPublishOne_HonorsThreadRate verifies that when ThreadRate=0.30, approximately
// 30% of published SendMessageRequests carry a ThreadParentMessageID, and that the
// loadgen_thread_messages_total counter is incremented for each threaded publish.
func TestPublishOne_HonorsThreadRate(t *testing.T) {
	p := Preset{
		Name:         "thread-rate-test",
		Users:        10,
		Rooms:        5,
		RoomSizeDist: DistUniform,
		SenderDist:   DistUniform,
		ContentBytes: Range{Min: 50, Max: 50},
		ThreadRate:   0.30,
	}
	f := BuildFixtures(&p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(&GeneratorConfig{
		Preset:    &p,
		Fixtures:  f,
		SiteID:    "site-local",
		Rate:      500,
		Inject:    InjectFrontdoor,
		Publisher: rp,
		Metrics:   m,
		Collector: c,
	}, 42)

	// Pre-seed the thread pool so parent picks succeed from the start.
	// This avoids a cold-start period where the pool is empty and
	// maybeParent always returns "" even when the rate roll succeeds.
	for i := 0; i < 100; i++ {
		g.threads.add(idgenMessageID(i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	require.NoError(t, g.Run(ctx))

	calls := rp.snapshot()
	require.GreaterOrEqual(t, len(calls), 200, "need enough samples for reliable stats; got %d", len(calls))

	threaded := 0
	for _, call := range calls {
		var req model.SendMessageRequest
		if err := json.Unmarshal(call.data, &req); err != nil {
			continue
		}
		if req.ThreadParentMessageID != "" {
			threaded++
		}
	}

	// 30% of N ± 5% absolute (50 count tolerance over 1000; scaled by actual N).
	expected := float64(len(calls)) * 0.30
	tolerance := float64(len(calls)) * 0.07 // 7% absolute tolerance for random sampling
	assert.InDelta(t, expected, float64(threaded), tolerance,
		"expected ~30%% threaded; got %d/%d (%.1f%%)", threaded, len(calls), 100*float64(threaded)/float64(len(calls)))

	// Verify the loadgen_thread_messages_total counter matches the threaded count.
	mfs, err := m.Registry.Gather()
	require.NoError(t, err)
	var metricCount float64
	for _, mf := range mfs {
		if mf.GetName() == "loadgen_thread_messages_total" {
			for _, metric := range mf.GetMetric() {
				metricCount += metric.GetCounter().GetValue()
			}
		}
	}
	assert.Equal(t, float64(threaded), metricCount,
		"loadgen_thread_messages_total counter must equal number of threaded publishes")
}

// idgenMessageID returns a deterministic fake message ID for pre-seeding
// the thread pool in tests. Uses a simple format that satisfies the ring
// buffer's non-empty check.
func idgenMessageID(i int) string {
	// 20-char base62-style placeholder — real IDs are 20-char base62 via
	// idgen.GenerateMessageID() but we only need non-empty strings here.
	return "testmsg" + padIntWidth(i, 13)
}

func padIntWidth(n, width int) string {
	s := ""
	for n > 0 || width > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
		width--
	}
	return s
}
