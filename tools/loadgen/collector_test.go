package main

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector_E1ReplyMatches(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("req-1", "msg-1", now)
	c.RecordReply("req-1", now.Add(5*time.Millisecond))
	assert.Equal(t, 1, c.E1Count())
	assert.Equal(t, []time.Duration{5 * time.Millisecond}, c.E1Samples())
}

func TestCollector_E1UnknownIgnored(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	c.RecordReply("unknown", time.Unix(0, 0))
	assert.Equal(t, 0, c.E1Count())
}

func TestCollector_E2BroadcastMatches(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("req-1", "msg-1", now)
	c.RecordBroadcast("msg-1", now.Add(8*time.Millisecond))
	assert.Equal(t, 1, c.E2Count())
	assert.Equal(t, []time.Duration{8 * time.Millisecond}, c.E2Samples())
}

func TestCollector_E1AndE2Independent(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("req-1", "msg-1", now)
	c.RecordReply("req-1", now.Add(5*time.Millisecond))
	c.RecordBroadcast("msg-1", now.Add(8*time.Millisecond))
	assert.Equal(t, 1, c.E1Count())
	assert.Equal(t, 1, c.E2Count())
}

func TestCollector_MissingCountsAtFinalize(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("req-1", "msg-1", now)
	c.RecordPublish("req-2", "msg-2", now)
	c.RecordReply("req-1", now.Add(5*time.Millisecond))
	// req-2 reply never arrives; msg-1 and msg-2 broadcasts never arrive
	missingReplies, missingBroadcasts := c.Finalize()
	assert.Equal(t, 1, missingReplies)
	assert.Equal(t, 2, missingBroadcasts)
}

func TestCollector_WarmupDiscards(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	start := time.Unix(0, 0)
	warmupEnd := start.Add(1 * time.Second)
	// In warmup window:
	c.RecordPublish("req-warm", "msg-warm", start)
	c.RecordReply("req-warm", start.Add(10*time.Millisecond))
	// Past warmup:
	c.RecordPublish("req-real", "msg-real", warmupEnd.Add(100*time.Millisecond))
	c.RecordReply("req-real", warmupEnd.Add(105*time.Millisecond))

	c.DiscardBefore(warmupEnd)
	require.Equal(t, 1, c.E1Count())
	assert.Equal(t, []time.Duration{5 * time.Millisecond}, c.E1Samples())
}

func TestCollector_E2UnknownIgnored(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	c.RecordBroadcast("unknown", time.Unix(0, 0))
	assert.Equal(t, 0, c.E2Count())
}

func TestCollector_SamplesReturnedSorted(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	// Publish three messages, record replies in a non-sorted order.
	c.RecordPublish("r-1", "m-1", now)
	c.RecordPublish("r-2", "m-2", now)
	c.RecordPublish("r-3", "m-3", now)
	c.RecordReply("r-1", now.Add(10*time.Millisecond))
	c.RecordReply("r-2", now.Add(2*time.Millisecond))
	c.RecordReply("r-3", now.Add(7*time.Millisecond))
	assert.Equal(t, []time.Duration{
		2 * time.Millisecond, 7 * time.Millisecond, 10 * time.Millisecond,
	}, c.E1Samples())
}

func TestCollector_ConcurrentRecordAndSnapshot(t *testing.T) {
	// Race-detector-friendly stress: one goroutine records publishes and
	// replies; another polls E1Samples. Verifies that no data race occurs
	// when snapshots are taken concurrently with mutations.
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			rid := "r-" + strconv.Itoa(i)
			mid := "m-" + strconv.Itoa(i)
			c.RecordPublish(rid, mid, now)
			c.RecordReply(rid, now.Add(time.Duration(i)*time.Microsecond))
		}
	}()
	for i := 0; i < 500; i++ {
		_ = c.E1Samples()
	}
	<-done
	require.GreaterOrEqual(t, c.E1Count(), 1)
}

func TestCollector_RecordPublishFailedRemovesOrphans(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublish("r-1", "m-1", now)
	c.RecordPublish("r-2", "m-2", now)
	// r-1 / m-1 get replied + broadcast; r-2 / m-2 "failed to publish" and get cleaned up.
	c.RecordReply("r-1", now.Add(5*time.Millisecond))
	c.RecordBroadcast("m-1", now.Add(8*time.Millisecond))
	c.RecordPublishFailed("r-2", "m-2")

	missingReplies, missingBroadcasts := c.Finalize()
	assert.Equal(t, 0, missingReplies)
	assert.Equal(t, 0, missingBroadcasts)
}

func TestCollector_RecordPublishBroadcastOnly_IgnoredByE1(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublishBroadcastOnly("m-1", now)
	// A reply correlated by requestID should NOT find this message
	// because we didn't populate byReqID.
	c.RecordReply("some-req-id", now.Add(5*time.Millisecond))
	assert.Equal(t, 0, c.E1Count())

	// A broadcast matching the msg-id should be recorded.
	c.RecordBroadcast("m-1", now.Add(8*time.Millisecond))
	assert.Equal(t, 1, c.E2Count())
}

func TestCollector_RecordPublishBroadcastOnly_FinalizeNoMissingReplies(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "small")
	now := time.Unix(0, 0)
	c.RecordPublishBroadcastOnly("m-1", now)
	c.RecordPublishBroadcastOnly("m-2", now)
	c.RecordBroadcast("m-1", now.Add(5*time.Millisecond))
	// m-2 never gets a broadcast — that's the only missing event class.
	missingReplies, missingBroadcasts := c.Finalize()
	assert.Equal(t, 0, missingReplies, "canonical mode should never produce missing replies")
	assert.Equal(t, 1, missingBroadcasts)
}

func TestCollector_RecordRequest_AppendsSamples(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "history-read")
	t0 := time.Unix(100, 0)
	c.RecordRequest("history", "load_history", t0, 4*time.Millisecond, false)
	c.RecordRequest("history", "load_history", t0, 8*time.Millisecond, false)
	c.RecordRequest("history", "load_history", t0, 18*time.Millisecond, true) // error
	c.RecordRequest("history", "get_message_by_id", t0, 1*time.Millisecond, false)

	stats := c.RequestStats()
	require.Len(t, stats, 2)

	// stats are sorted (scenario, kind) for deterministic output.
	var loadHistory, getByID *RequestStat
	for i := range stats {
		switch stats[i].Kind {
		case "load_history":
			loadHistory = &stats[i]
		case "get_message_by_id":
			getByID = &stats[i]
		}
	}
	require.NotNil(t, loadHistory)
	require.NotNil(t, getByID)

	assert.Equal(t, 3, loadHistory.Count, "all three load_history calls counted")
	assert.Equal(t, 1, loadHistory.Errors, "one was an error")
	assert.Greater(t, int64(loadHistory.Latency.P99), int64(0))

	assert.Equal(t, 1, getByID.Count)
	assert.Equal(t, 0, getByID.Errors)
}

func TestCollector_RequestStats_DiscardsBeforeCutoff(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "history-read")
	warmup := time.Unix(100, 0)
	cutoff := time.Unix(110, 0)
	measured := time.Unix(120, 0)

	c.RecordRequest("history", "load_history", warmup, 5*time.Millisecond, false)   // discarded
	c.RecordRequest("history", "load_history", measured, 7*time.Millisecond, false) // kept

	c.DiscardBefore(cutoff)
	stats := c.RequestStats()
	require.Len(t, stats, 1)
	assert.Equal(t, 1, stats[0].Count, "warmup samples must be discarded")
}

func TestCollector_PruneCorrelation_ClearsByReqAndByMsg_PreservesSamples(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)
	// One matched + one unmatched.
	c.RecordPublish("req-1", "msg-1", t0)
	c.RecordPublish("req-2", "msg-2", t0)
	c.RecordReply("req-1", t0.Add(2*time.Millisecond))

	c.PruneCorrelation()

	missingReplies, missingBroadcasts := c.Finalize()
	assert.Zero(t, missingReplies, "PruneCorrelation must drop unmatched byReqID")
	assert.Zero(t, missingBroadcasts, "PruneCorrelation must drop unmatched byMsgID")

	// E1 sample from the matched publish must survive.
	require.Len(t, c.E1Samples(), 1, "PruneCorrelation must NOT drop completed E1 samples")
	// seenMessageIDs append-only history must survive.
	assert.ElementsMatch(t, []string{"msg-1", "msg-2"}, c.MessageIDs())
}

func TestCollector_RecordPublishFailed_PrunesSeenMessageIDs(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	now := time.Unix(100, 0)
	c.RecordPublish("req-good", "msg-good", now)
	c.RecordPublish("req-bad", "msg-bad", now)

	c.RecordPublishFailed("req-bad", "msg-bad")

	ids := c.MessageIDs()
	assert.ElementsMatch(t, []string{"msg-good"}, ids,
		"failed publish must be removed from MessageIDs() so history-read doesn't poll non-existent IDs")
}
