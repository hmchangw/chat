package main

import (
	"sort"
	"sync"
	"time"
)

// correlationShardCount is the number of hash-keyed sub-maps used to
// shard byReqID + byMsgID. 16 picked because Go's sync.Map uses a
// similar order of magnitude and 16 is a power of two so the FNV-32
// hash maps to a shard via cheap bit-mask. At 16 shards × 5k rps
// publishes the per-shard write rate is ~300 rps, far below the
// contention threshold of a single sync.Mutex.
const correlationShardCount = 16

// correlationShard owns one chunk of the byReqID / byMsgID maps.
type correlationShard struct {
	mu      sync.Mutex
	entries map[string]publishEntry
}

type publishEntry struct {
	publishedAt time.Time
}

// sample pairs a latency with its publish timestamp so warmup can discard by time.
type sample struct {
	publishedAt time.Time
	latency     time.Duration
}

// requestSample carries one (scenario, kind) read-scenario observation.
type requestSample struct {
	publishedAt time.Time
	latency     time.Duration
	errored     bool
}

// requestKey identifies one (scenario, kind) cell.
type requestKey struct{ scenario, kind string }

// requestShard owns the samples for one (scenario, kind) cell. S1:
// each shard has its own mutex so RecordRequest for different keys
// runs without serializing on a single Collector-wide lock. The
// shard map itself is protected by Collector.requestsMu (RWMutex);
// hot-path readers/writers only take the inner shard.mu after a
// brief RLock on the map.
type requestShard struct {
	mu      sync.Mutex
	samples []requestSample
}

// Collector correlates publishes with replies (E1) and broadcasts (E2).
type Collector struct {
	m      *Metrics
	preset string
	mu     sync.Mutex // protects e1, e2, seenMessageIDs, window
	// S2: byReqID + byMsgID are sharded into correlationShardCount
	// buckets by FNV-32 of the ID. Concurrent RecordPublish from
	// different goroutines almost never lands on the same shard, so
	// the per-shard lock is essentially uncontested in steady state.
	byReqIDShards [correlationShardCount]*correlationShard
	byMsgIDShards [correlationShardCount]*correlationShard
	e1            []sample
	e2            []sample
	// S1: requests is sharded per (scenario, kind). requestsMu protects
	// the map structure (insertion of new shards on first write); each
	// shard owns its own mu for the samples slice. Reads use RLock so
	// concurrent RecordRequest for distinct keys don't contend.
	requestsMu     sync.RWMutex
	requests       map[requestKey]*requestShard
	seenMessageIDs []string       // Phase 3 §3.1: append-only log; never deleted
	window         *LatencyWindow // Phase 3 §3.5: sliding window for abort watcher
}

// AttachWindow wires a LatencyWindow into the collector. Subsequent
// RecordRequest calls feed the window so the saturation auto-detect
// watcher (Task 20) can read sliding-window percentiles. Optional;
// nil is a no-op.
func (c *Collector) AttachWindow(w *LatencyWindow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.window = w
}

// NewCollector returns a ready-to-use Collector.
func NewCollector(m *Metrics, preset string) *Collector {
	c := &Collector{
		m: m, preset: preset,
		requests: make(map[requestKey]*requestShard),
	}
	for i := 0; i < correlationShardCount; i++ {
		c.byReqIDShards[i] = &correlationShard{entries: make(map[string]publishEntry)}
		c.byMsgIDShards[i] = &correlationShard{entries: make(map[string]publishEntry)}
	}
	return c
}

// shardForID returns the index of the correlation shard for id.
// Inline FNV-32a folded over the raw string bytes: zero heap allocs
// (no interface, no []byte conversion). At 15k calls/sec the previous
// hash/fnv-backed version was ~480KB/s of short-lived garbage; this
// version is alloc-free and ~3× faster on a 32-char input.
// Uniformity: FNV folds every byte; the low 4 bits (the mask we use)
// are well-distributed for UUIDv7 hex (32 chars) and base62 (17/20 chars).
func shardForID(id string) int {
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for i := 0; i < len(id); i++ {
		h ^= uint32(id[i])
		h *= prime32
	}
	return int(h) & (correlationShardCount - 1)
}

// getOrCreateShard returns the shard for k, creating it on first
// write. Fast path: RLock + map lookup. Slow path (shard missing):
// promote to Lock, double-check, create.
func (c *Collector) getOrCreateShard(k requestKey) *requestShard {
	c.requestsMu.RLock()
	sh, ok := c.requests[k]
	c.requestsMu.RUnlock()
	if ok {
		return sh
	}
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	if sh, ok = c.requests[k]; ok {
		return sh
	}
	sh = &requestShard{}
	c.requests[k] = sh
	return sh
}

// RecordRequest captures one read-scenario request observation under
// (scenario, kind). errored is true when the underlying NATS request
// returned a non-nil error or the reply payload encoded a server error.
//
// S1: this is the hot path. The per-shard mutex isolates one
// (scenario, kind) cell from another, so two RecordRequest calls for
// different keys never contend. The window-attach read takes the
// Collector-wide mu briefly (atomic enough; AttachWindow is called
// once at startup and never replaces, so the read is effectively
// stable).
func (c *Collector) RecordRequest(scenario, kind string, publishedAt time.Time, latency time.Duration, errored bool) {
	sh := c.getOrCreateShard(requestKey{scenario: scenario, kind: kind})
	sh.mu.Lock()
	sh.samples = append(sh.samples, requestSample{
		publishedAt: publishedAt,
		latency:     latency,
		errored:     errored,
	})
	sh.mu.Unlock()

	c.mu.Lock()
	w := c.window
	c.mu.Unlock()
	if w != nil {
		w.AddAt(publishedAt, latency, errored)
	}
}

// RequestStats returns aggregated per-(scenario, kind) percentiles + counts.
// Result is sorted by (scenario, kind) for deterministic output.
//
// S1: snapshot path takes the map RLock briefly, materializes the
// shard list, then drops the RLock and locks each shard.mu in turn.
// New shards appearing AFTER the snapshot don't show up in this
// result — acceptable for a final-report call (caller is post-run).
func (c *Collector) RequestStats() []RequestStat {
	c.requestsMu.RLock()
	keys := make([]requestKey, 0, len(c.requests))
	shards := make([]*requestShard, 0, len(c.requests))
	for k, sh := range c.requests {
		keys = append(keys, k)
		shards = append(shards, sh)
	}
	c.requestsMu.RUnlock()

	out := make([]RequestStat, 0, len(keys))
	for i, k := range keys {
		sh := shards[i]
		sh.mu.Lock()
		if len(sh.samples) == 0 {
			sh.mu.Unlock()
			continue
		}
		latencies := make([]time.Duration, len(sh.samples))
		errors := 0
		for j := range sh.samples {
			latencies[j] = sh.samples[j].latency
			if sh.samples[j].errored {
				errors++
			}
		}
		sh.mu.Unlock()
		out = append(out, RequestStat{
			Scenario: k.scenario,
			Kind:     k.kind,
			Count:    len(latencies),
			Errors:   errors,
			Latency:  ComputePercentiles(latencies),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scenario != out[j].Scenario {
			return out[i].Scenario < out[j].Scenario
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// RecordPublish stores the publish time under both correlation keys.
//
// S2: byReqID + byMsgID writes hit independent shards keyed by hash
// of the ID. The Collector-wide c.mu is only taken to append to
// seenMessageIDs (a single slice, can't easily shard without breaking
// the auto-warmup pool's ordering contract).
func (c *Collector) RecordPublish(requestID, messageID string, t time.Time) {
	rs := c.byReqIDShards[shardForID(requestID)]
	rs.mu.Lock()
	rs.entries[requestID] = publishEntry{publishedAt: t}
	rs.mu.Unlock()

	ms := c.byMsgIDShards[shardForID(messageID)]
	ms.mu.Lock()
	ms.entries[messageID] = publishEntry{publishedAt: t}
	ms.mu.Unlock()

	c.mu.Lock()
	c.seenMessageIDs = append(c.seenMessageIDs, messageID)
	c.mu.Unlock()
}

// RecordReply consumes one pending publish keyed by requestID.
//
// S2: lookup + delete happen under the per-shard mu (no Collector
// mu held). The matched-sample append still takes c.mu — those are
// merged into one shared e1 slice for final reporting; sharding e1
// would complicate Finalize without a measurable contention win
// (E1 lands at the inbound reply rate, ~5k/sec at peak, all from
// one subscription handler goroutine).
func (c *Collector) RecordReply(requestID string, at time.Time) {
	sh := c.byReqIDShards[shardForID(requestID)]
	sh.mu.Lock()
	e, ok := sh.entries[requestID]
	if ok {
		delete(sh.entries, requestID)
	}
	sh.mu.Unlock()
	if !ok {
		return
	}
	d := at.Sub(e.publishedAt)
	c.mu.Lock()
	c.e1 = append(c.e1, sample{publishedAt: e.publishedAt, latency: d})
	c.mu.Unlock()
	c.m.E1Latency.WithLabelValues(c.preset).Observe(d.Seconds())
}

// RecordPublishBroadcastOnly stores only the message-ID correlation, for
// injection modes that bypass the gatekeeper (no reply is expected).
func (c *Collector) RecordPublishBroadcastOnly(messageID string, t time.Time) {
	ms := c.byMsgIDShards[shardForID(messageID)]
	ms.mu.Lock()
	ms.entries[messageID] = publishEntry{publishedAt: t}
	ms.mu.Unlock()

	c.mu.Lock()
	c.seenMessageIDs = append(c.seenMessageIDs, messageID)
	c.mu.Unlock()
}

// MessageIDs returns every message ID published during this Collector's
// lifetime. Append-only: RecordReply / RecordBroadcast consumption does
// not remove entries. Used by the Phase 3 auto warm-up phase to
// pre-populate the HistoryReadGenerator's message-ID pool. Returns a
// copy so callers may freely sort / shuffle it.
func (c *Collector) MessageIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.seenMessageIDs))
	copy(out, c.seenMessageIDs)
	return out
}

// RecordPublishFailed removes entries previously stored by RecordPublish.
// Use when the publish itself failed (message never reached NATS) so the
// orphans do not inflate Finalize's missing-reply / missing-broadcast counts.
// Also prunes the failed messageID from seenMessageIDs so the auto-warmup
// MessageIDs() pool doesn't hand history-read scenarios a message that
// never made it to Cassandra.
//
// S2: each delete hits its own shard. Empty requestID / messageID is a
// no-op (delete on absent key is a no-op in Go).
func (c *Collector) RecordPublishFailed(requestID, messageID string) {
	if requestID != "" {
		rs := c.byReqIDShards[shardForID(requestID)]
		rs.mu.Lock()
		delete(rs.entries, requestID)
		rs.mu.Unlock()
	}
	if messageID != "" {
		ms := c.byMsgIDShards[shardForID(messageID)]
		ms.mu.Lock()
		delete(ms.entries, messageID)
		ms.mu.Unlock()
	}
	c.mu.Lock()
	for i, id := range c.seenMessageIDs {
		if id == messageID {
			c.seenMessageIDs = append(c.seenMessageIDs[:i], c.seenMessageIDs[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
}

// PruneCorrelation clears byReqID / byMsgID. Call between phases (e.g.
// after auto-warmup completes, before the read scenario starts) so a
// trailing reply/broadcast that arrives just after the warmup phase
// ends doesn't get attributed to the next phase, and so leftover
// unmatched warmup orphans don't inflate Finalize's missing counts.
// E1/E2 latency samples and seenMessageIDs are preserved.
//
// S2: visits every shard. Lock acquisition order matches the shard
// index so concurrent PruneCorrelation calls (don't happen today,
// defensive) cannot deadlock. Uses `clear` (Go 1.21+) so the
// underlying hash-table capacity is reused on the next phase — matters
// here because the warmup→read transition refills the same map within
// seconds and dropping capacity would force a rehash.
func (c *Collector) PruneCorrelation() {
	for i := 0; i < correlationShardCount; i++ {
		rs := c.byReqIDShards[i]
		rs.mu.Lock()
		clear(rs.entries)
		rs.mu.Unlock()

		ms := c.byMsgIDShards[i]
		ms.mu.Lock()
		clear(ms.entries)
		ms.mu.Unlock()
	}
}

// RecordBroadcast consumes one pending publish keyed by messageID.
//
// S2: lookup + delete under the per-shard mu; e2 append under c.mu.
func (c *Collector) RecordBroadcast(messageID string, at time.Time) {
	sh := c.byMsgIDShards[shardForID(messageID)]
	sh.mu.Lock()
	e, ok := sh.entries[messageID]
	if ok {
		delete(sh.entries, messageID)
	}
	sh.mu.Unlock()
	if !ok {
		return
	}
	d := at.Sub(e.publishedAt)
	c.mu.Lock()
	c.e2 = append(c.e2, sample{publishedAt: e.publishedAt, latency: d})
	c.mu.Unlock()
	c.m.E2Latency.WithLabelValues(c.preset).Observe(d.Seconds())
}

// DiscardBefore drops any samples whose publish time is before cutoff (warmup).
//
// S1: iterates the shard map under RLock (no shard creation happens
// in DiscardBefore), then locks each shard.mu individually to filter
// its samples slice. The map RLock and the e1/e2 mu are held
// briefly; the bulk of the work is inside per-shard critical
// sections so a long DiscardBefore on one key doesn't block writes
// to a different key.
func (c *Collector) DiscardBefore(cutoff time.Time) {
	c.mu.Lock()
	c.e1 = filterAtOrAfter(c.e1, cutoff)
	c.e2 = filterAtOrAfter(c.e2, cutoff)
	c.mu.Unlock()

	c.requestsMu.RLock()
	shards := make([]*requestShard, 0, len(c.requests))
	for _, sh := range c.requests {
		shards = append(shards, sh)
	}
	c.requestsMu.RUnlock()
	for _, sh := range shards {
		sh.mu.Lock()
		sh.samples = filterRequestsAtOrAfter(sh.samples, cutoff)
		sh.mu.Unlock()
	}
}

func filterAtOrAfter(in []sample, cutoff time.Time) []sample {
	out := in[:0]
	for i := range in {
		if !in[i].publishedAt.Before(cutoff) {
			out = append(out, in[i])
		}
	}
	return out
}

func filterRequestsAtOrAfter(in []requestSample, cutoff time.Time) []requestSample {
	out := in[:0]
	for i := range in {
		if !in[i].publishedAt.Before(cutoff) {
			out = append(out, in[i])
		}
	}
	return out
}

// Finalize returns the count of unmatched publishes as missing replies and broadcasts.
//
// S2: sums across all shards. Walks them in index order under each
// shard's own lock so a concurrent RecordReply on shard 3 doesn't
// block the count of shard 0.
func (c *Collector) Finalize() (missingReplies int, missingBroadcasts int) {
	for i := 0; i < correlationShardCount; i++ {
		rs := c.byReqIDShards[i]
		rs.mu.Lock()
		missingReplies += len(rs.entries)
		rs.mu.Unlock()

		ms := c.byMsgIDShards[i]
		ms.mu.Lock()
		missingBroadcasts += len(ms.entries)
		ms.mu.Unlock()
	}
	return missingReplies, missingBroadcasts
}

// outstandingCorrelations reports the total count of unmatched
// byReqID + byMsgID. Used by the quiescence drain in main.go to
// decide when trailing replies / broadcasts have all landed.
func (c *Collector) outstandingCorrelations() int {
	mr, mb := c.Finalize()
	return mr + mb
}

// E1Count returns the number of matched E1 samples.
func (c *Collector) E1Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.e1)
}

// E2Count returns the number of matched E2 samples.
func (c *Collector) E2Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.e2)
}

// E1Samples returns a sorted copy of E1 latencies for tests/reporting.
func (c *Collector) E1Samples() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLatenciesLocked(c.e1)
}

// E2Samples returns a sorted copy of E2 latencies for tests/reporting.
func (c *Collector) E2Samples() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLatenciesLocked(c.e2)
}

// RequestSampleRow is a single row for the per-sample CSV export.
type RequestSampleRow struct {
	Scenario string
	Kind     string
	Latency  time.Duration
	Errored  bool
}

// RequestSampleRows returns one row per recorded request, suitable for CSV
// export. Order is per-bucket insertion order across deterministic
// scenario+kind iteration.
//
// S1: snapshot path. Map RLock to materialize (key,shard) pairs,
// then lock each shard briefly to copy its samples. Concurrent
// writes to a different shard proceed unblocked. New shards
// appearing AFTER the materialization are excluded from this
// snapshot — acceptable because this is a post-run CSV export.
func (c *Collector) RequestSampleRows() []RequestSampleRow {
	c.requestsMu.RLock()
	type keyedShard struct {
		key   requestKey
		shard *requestShard
	}
	pairs := make([]keyedShard, 0, len(c.requests))
	for k, sh := range c.requests {
		pairs = append(pairs, keyedShard{k, sh})
	}
	c.requestsMu.RUnlock()

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key.scenario != pairs[j].key.scenario {
			return pairs[i].key.scenario < pairs[j].key.scenario
		}
		return pairs[i].key.kind < pairs[j].key.kind
	})
	var rows []RequestSampleRow
	for _, p := range pairs {
		p.shard.mu.Lock()
		for _, s := range p.shard.samples {
			rows = append(rows, RequestSampleRow{
				Scenario: p.key.scenario,
				Kind:     p.key.kind,
				Latency:  s.latency,
				Errored:  s.errored,
			})
		}
		p.shard.mu.Unlock()
	}
	return rows
}

// snapshotLatenciesLocked copies and sorts latencies from in.
// Callers must hold c.mu before calling this method.
func (c *Collector) snapshotLatenciesLocked(in []sample) []time.Duration {
	out := make([]time.Duration, len(in))
	for i := range in {
		out[i] = in[i].latency
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
