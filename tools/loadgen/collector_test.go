package main

import (
	"strconv"
	"sync"
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

func TestCollector_AttachWindow_FeedsRecordRequest(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	w := NewLatencyWindow(10 * time.Second)
	c.AttachWindow(w)

	t0 := time.Unix(100, 0)
	c.RecordRequest("history", "load_history", t0, 4*time.Millisecond, false)
	c.RecordRequest("history", "load_history", t0, 18*time.Millisecond, true)

	// Window should contain both samples.
	p99 := w.P99(t0.Add(1*time.Second), 10*time.Second)
	assert.GreaterOrEqual(t, p99, 4*time.Millisecond,
		"AttachWindow must wire RecordRequest into the window")
	rate := w.ErrorRate(t0.Add(1*time.Second), 10*time.Second)
	assert.InDelta(t, 0.5, rate, 0.001, "one of two errored")
}

// S1: Collector's per-(scenario,kind) sample storage is now sharded
// behind a per-key mutex so concurrent RecordRequest calls for
// different keys don't serialize on a single Collector-wide mu. The
// API contract is unchanged: RequestStats sees every sample, in
// deterministic (scenario, kind) order, regardless of which goroutine
// recorded it.
func TestCollector_RecordRequest_ConcurrentDifferentKeys(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)

	const N = 100
	keys := []struct{ scenario, kind string }{
		{"history", "load_history"},
		{"history", "get_message_by_id"},
		{"search", "search_messages"},
		{"room", "rooms_list"},
	}
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		go func(scenario, kind string) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				c.RecordRequest(scenario, kind, t0, time.Duration(i)*time.Millisecond, i%10 == 0)
			}
		}(k.scenario, k.kind)
	}
	wg.Wait()

	stats := c.RequestStats()
	require.Len(t, stats, len(keys))
	for _, s := range stats {
		assert.Equal(t, N, s.Count, "all samples for key %s/%s must land in the shard", s.Scenario, s.Kind)
		assert.Equal(t, N/10, s.Errors, "every 10th sample errored, so 10 errors per key")
	}
}

// S1: concurrent recorders for the SAME key still serialize on the
// per-shard mutex (correctness), but a stress run must not race or
// drop samples. The -race build flag catches races; this test
// asserts the sample count under high contention.
func TestCollector_RecordRequest_ConcurrentSameKey(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)

	const goroutines = 16
	const perGoroutine = 250
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				c.RecordRequest("history", "load_history", t0, time.Duration(i)*time.Microsecond, false)
			}
		}()
	}
	wg.Wait()

	stats := c.RequestStats()
	require.Len(t, stats, 1)
	assert.Equal(t, goroutines*perGoroutine, stats[0].Count,
		"per-shard mutex must serialize same-key writes without dropping samples")
}

// S1: DiscardBefore must filter across ALL shards. A bug where the
// shard map iteration misses a key would surface as a phantom sample
// in the warmup-discarded window.
func TestCollector_DiscardBefore_AcrossShards(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	cutoff := time.Unix(200, 0)
	beforeCutoff := time.Unix(100, 0)
	afterCutoff := time.Unix(300, 0)

	// One pre-cutoff and one post-cutoff sample per key, across 3 keys.
	for _, k := range []struct{ scenario, kind string }{
		{"history", "load_history"},
		{"search", "search_messages"},
		{"room", "rooms_list"},
	} {
		c.RecordRequest(k.scenario, k.kind, beforeCutoff, 1*time.Millisecond, false)
		c.RecordRequest(k.scenario, k.kind, afterCutoff, 2*time.Millisecond, false)
	}
	c.DiscardBefore(cutoff)

	stats := c.RequestStats()
	require.Len(t, stats, 3)
	for _, s := range stats {
		assert.Equal(t, 1, s.Count, "post-cutoff sample only for %s/%s", s.Scenario, s.Kind)
	}
}

// S2: byReqID + byMsgID are sharded by ID hash. The API contract is
// unchanged: RecordPublish from many goroutines + concurrent
// RecordReply/RecordBroadcast must produce a consistent E1/E2 count
// and zero outstanding correlations.
func TestCollector_Correlation_ConcurrentPublishAndConsume(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)

	const N = 512 // multiple of 8 (producer count) so N/8 is exact
	// Phase 1: publish N (req, msg) pairs concurrently from 8 producers.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < N/8; i++ {
				id := strconv.Itoa(base*1000 + i)
				c.RecordPublish("r-"+id, "m-"+id, t0)
			}
		}(g)
	}
	wg.Wait()
	assert.Equal(t, 2*N, c.outstandingCorrelations(), "8×N/8 publishes × 2 maps")

	// Phase 2: consume all replies + broadcasts concurrently. Two
	// readers per ID so we can race a hit against a miss without
	// double-counting.
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < N/8; i++ {
				id := strconv.Itoa(base*1000 + i)
				c.RecordReply("r-"+id, t0.Add(2*time.Millisecond))
				c.RecordBroadcast("m-"+id, t0.Add(5*time.Millisecond))
			}
		}(g)
	}
	wg.Wait()

	assert.Equal(t, N, c.E1Count(), "every publish must have produced one E1 match")
	assert.Equal(t, N, c.E2Count(), "every publish must have produced one E2 match")
	assert.Equal(t, 0, c.outstandingCorrelations(), "all correlations consumed")
}

// S2 review-A WARN #2: N goroutines racing to consume the SAME
// requestID must result in exactly one E1 match. The shard's
// lookup-then-delete is the linearization point; this test asserts
// no double-count under that contention.
func TestCollector_RecordReply_SameIDRacingConsumers(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)
	c.RecordPublish("r-shared", "m-shared", t0)

	const racers = 16
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.RecordReply("r-shared", t0.Add(2*time.Millisecond))
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, c.E1Count(), "exactly one of the %d racers must observe the entry", racers)
}

func TestCollector_RecordBroadcast_SameIDRacingConsumers(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)
	c.RecordPublish("r-shared", "m-shared", t0)

	const racers = 16
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.RecordBroadcast("m-shared", t0.Add(3*time.Millisecond))
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, c.E2Count(), "exactly one of the %d racers must observe the entry", racers)
}

// S2 review-A WARN #3: RecordPublishFailed must accept empty
// requestID and/or messageID without crashing or touching a real
// shard. The S5 async err handler in main.go calls
// RecordPublishFailed("", id) when an async ack fails, so the empty
// case is a real production path.
func TestCollector_RecordPublishFailed_EmptyIDsAreSafe(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)
	c.RecordPublish("r-1", "m-1", t0)

	// Empty messageID — only the requestID should be evicted.
	c.RecordPublishFailed("r-1", "")
	missingR, missingB := c.Finalize()
	assert.Equal(t, 0, missingR, "requestID evicted")
	assert.Equal(t, 1, missingB, "messageID untouched because '' is a no-op")

	// Empty requestID — drop the messageID. seenMessageIDs is pruned.
	c.RecordPublishFailed("", "m-1")
	missingR, missingB = c.Finalize()
	assert.Equal(t, 0, missingR)
	assert.Equal(t, 0, missingB, "messageID evicted via the '' requestID path")
	assert.Empty(t, c.MessageIDs(), "seenMessageIDs pruned")

	// Both empty — pure no-op, no panic.
	require.NotPanics(t, func() {
		c.RecordPublishFailed("", "")
	})
}

// S2: PruneCorrelation must clear every shard, leaving the
// seenMessageIDs and E1/E2 samples intact.
func TestCollector_PruneCorrelation_ClearsAllShards(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)
	// Spread IDs across many hash bucket positions to exercise shards.
	for i := 0; i < 200; i++ {
		id := strconv.Itoa(i)
		c.RecordPublish("r-"+id, "m-"+id, t0)
	}
	// Match a few to populate E1/E2 — we want to confirm those aren't
	// nuked by PruneCorrelation.
	c.RecordReply("r-1", t0.Add(time.Millisecond))
	c.RecordBroadcast("m-2", t0.Add(time.Millisecond))

	require.Equal(t, 1, c.E1Count())
	require.Equal(t, 1, c.E2Count())
	require.Greater(t, c.outstandingCorrelations(), 0)

	c.PruneCorrelation()

	assert.Equal(t, 0, c.outstandingCorrelations(), "all shards must be cleared")
	assert.Equal(t, 1, c.E1Count(), "E1 samples preserved across prune")
	assert.Equal(t, 1, c.E2Count(), "E2 samples preserved across prune")
	assert.Equal(t, 200, len(c.MessageIDs()), "seenMessageIDs preserved across prune")
}

func TestCollector_OutstandingCorrelations(t *testing.T) {
	m := NewMetrics()
	c := NewCollector(m, "test")
	t0 := time.Unix(100, 0)
	// 3 publishes, each landing in both byReqID + byMsgID.
	c.RecordPublish("r-1", "m-1", t0)
	c.RecordPublish("r-2", "m-2", t0)
	c.RecordPublish("r-3", "m-3", t0)
	assert.Equal(t, 6, c.outstandingCorrelations(),
		"3 publishes × 2 maps = 6 outstanding correlations")

	c.RecordReply("r-1", t0.Add(2*time.Millisecond))
	assert.Equal(t, 5, c.outstandingCorrelations(),
		"reply consumes one byReqID entry")

	c.RecordBroadcast("m-1", t0.Add(5*time.Millisecond))
	assert.Equal(t, 4, c.outstandingCorrelations(),
		"broadcast consumes one byMsgID entry")
}
