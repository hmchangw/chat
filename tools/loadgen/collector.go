package main

import (
	"fmt"
	"sort"
	"sync"
	"time"

	hdr "github.com/HdrHistogram/hdrhistogram-go"
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

// requestKey identifies one (scenario, kind, phase) cell.
// Phase is "warmup" or "measured"; it is a first-class dimension of
// the storage key so warmup traffic does not pollute measured-window
// percentiles.
type requestKey struct{ scenario, kind, phase string }

// requestShard owns the HDR histogram cell for one (scenario, kind, phase)
// triplet. The shard map (Collector.requests) is protected by
// Collector.requestsMu (RWMutex); hot-path readers/writers hold the
// inner shard.mu after a brief RLock on the outer map.
//
// S1 (HDR): replaced []requestSample with *CellHistogram so memory is
// bounded (~30 KB per cell) regardless of sample count. Phase 1a.2.
// errors counts error observations for the corresponding cell so that
// RequestStats can still report per-(scenario,kind) error counts without
// storing individual samples.
type requestShard struct {
	mu     sync.Mutex
	cell   *CellHistogram
	errors int64
}

// RecentMessage carries a published message's ID and room metadata so the
// read-receipts scenario (Phase 3 §3.16) can fire MessageRead events for a
// configurable fraction of recipients without an extra lookup.
type RecentMessage struct {
	MessageID string
	RoomID    string
	RoomType  string // "channel" or "dm"
}

// recentCap is the ring-buffer capacity for the recent-published list.
// At 1024 entries × ~60 bytes each the working set is ~60 KB — small enough
// to copy cheaply for RecentMessages snapshots.
const recentCap = 1024

// Collector correlates publishes with replies (E1) and broadcasts (E2).
//
// LOCK-ORDER INVARIANT (do not violate when adding methods):
// Any method that needs both a shard mutex AND c.mu must acquire the
// shard mutex FIRST, release it, then acquire c.mu. The two locks
// are NEVER held simultaneously by any production path. This holds
// across all sharded structures (requestsMu, requestShard.mu,
// correlationShard.mu) and c.mu. Phase S commits S1 (e7b7fe4) and
// S2 (ecb859f) follow this discipline; the round-trip pattern is:
//
//	sh.mu.Lock()
//	... append / lookup / delete ...
//	sh.mu.Unlock()
//	c.mu.Lock()
//	... touch e1 / e2 / seenMessageIDs / window ...
//	c.mu.Unlock()
//
// Adding a method that holds shard.mu and c.mu nested would let a
// concurrent caller deadlock.
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
	// S1 (HDR): requests is sharded per (scenario, kind, phase).
	// requestsMu protects the map structure (insertion of new shards
	// on first write); each shard owns its own mu for the CellHistogram.
	// Reads use RLock so concurrent RecordRequest for distinct keys
	// don't contend.
	requestsMu     sync.RWMutex
	requests       map[requestKey]*requestShard
	seenMessageIDs []string       // Phase 3 §3.1: append-only log; never deleted
	window         *LatencyWindow // Phase 3 §3.5: sliding window for abort watcher
	// recentMu protects the recent ring buffer. Separate from c.mu to
	// avoid extending the lock-order invariant to a new field; RecordPublished
	// is called from hot-path goroutines and must not race with c.mu holders.
	recentMu sync.Mutex
	recent   []RecentMessage // ring buffer; capacity recentCap
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
	sh = &requestShard{cell: NewCellHistogram()} // errors starts at 0
	c.requests[k] = sh
	return sh
}

// RecordRequest captures one read-scenario request observation under
// (scenario, kind, phase). phase is "warmup" when the observation falls
// within the pre-measured warmup interval, and "measured" afterward.
// errored is true when the underlying NATS request returned a non-nil
// error or the reply payload encoded a server error.
//
// S1 (HDR): records into a CellHistogram (bounded ~30 KB) instead of
// appending to a []requestSample slice. Memory is constant regardless
// of sample count. The window-attach read takes the Collector-wide mu
// briefly; AttachWindow is called once at startup and never replaces,
// so the read is effectively stable.
func (c *Collector) RecordRequest(scenario, kind, phase string, latency time.Duration, errored bool) {
	k := requestKey{scenario: scenario, kind: kind, phase: phase}
	sh := c.getOrCreateShard(k)
	sh.mu.Lock()
	sh.cell.Record(latency)
	if errored {
		sh.errors++
	}
	sh.mu.Unlock()

	c.mu.Lock()
	w := c.window
	c.mu.Unlock()
	if w != nil {
		w.Add(latency, errored)
	}
}

// RequestPercentiles returns the latency quantiles for the given
// (scenario, kind, phase) cell. qs is a slice of quantile values in
// [0.0, 1.0]. Returns a slice of the same length as qs. If no samples
// have been recorded for this cell, all values are zero.
func (c *Collector) RequestPercentiles(scenario, kind, phase string, qs []float64) []time.Duration {
	k := requestKey{scenario: scenario, kind: kind, phase: phase}
	c.requestsMu.RLock()
	sh, ok := c.requests[k]
	c.requestsMu.RUnlock()
	out := make([]time.Duration, len(qs))
	if !ok {
		return out
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	for i, q := range qs {
		out[i] = sh.cell.Quantile(q)
	}
	return out
}

// RequestCount returns the total number of samples recorded for the
// given (scenario, kind, phase) cell. Returns 0 if no samples have
// been recorded.
func (c *Collector) RequestCount(scenario, kind, phase string) int64 {
	k := requestKey{scenario: scenario, kind: kind, phase: phase}
	c.requestsMu.RLock()
	sh, ok := c.requests[k]
	c.requestsMu.RUnlock()
	if !ok {
		return 0
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.cell.Count()
}

// RequestStats returns aggregated per-(scenario, kind) percentiles + counts
// for the "measured" phase only. Result is sorted by (scenario, kind)
// for deterministic output.
//
// S1 (HDR): reads from CellHistogram cells for the "measured" phase.
// The DiscardBefore warmup-cutoff pattern is superseded by phase labels:
// callers no longer need to call DiscardBefore before RequestStats.
func (c *Collector) RequestStats() []RequestStat {
	c.requestsMu.RLock()
	type keyedShard struct {
		key   requestKey
		shard *requestShard
	}
	pairs := make([]keyedShard, 0, len(c.requests))
	for k, sh := range c.requests {
		if k.phase != "measured" {
			continue
		}
		pairs = append(pairs, keyedShard{k, sh})
	}
	c.requestsMu.RUnlock()

	out := make([]RequestStat, 0, len(pairs))
	for _, p := range pairs {
		p.shard.mu.Lock()
		count := p.shard.cell.Count()
		errors := p.shard.errors
		var latency Percentiles
		if count > 0 {
			latency = Percentiles{
				P50: p.shard.cell.Quantile(0.50),
				P95: p.shard.cell.Quantile(0.95),
				P99: p.shard.cell.Quantile(0.99),
				Max: p.shard.cell.Quantile(1.00),
			}
		}
		p.shard.mu.Unlock()
		if count == 0 {
			continue
		}
		out = append(out, RequestStat{
			Scenario: p.key.scenario,
			Kind:     p.key.kind,
			Count:    int(count),
			Errors:   int(errors),
			Latency:  latency,
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

// DiscardBefore drops E1 and E2 samples whose publish time is before
// cutoff (warmup). Request samples are now phase-keyed (HDR cells
// identified by "warmup" / "measured") so no per-request filtering
// is needed here.
func (c *Collector) DiscardBefore(cutoff time.Time) {
	c.mu.Lock()
	c.e1 = filterAtOrAfter(c.e1, cutoff)
	c.e2 = filterAtOrAfter(c.e2, cutoff)
	c.mu.Unlock()
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
	return snapshotLatencies(c.e1)
}

// E2Samples returns a sorted copy of E2 latencies for tests/reporting.
func (c *Collector) E2Samples() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return snapshotLatencies(c.e2)
}

// RequestSampleRow is a single row for the per-sample CSV export.
type RequestSampleRow struct {
	Scenario string
	Kind     string
	Phase    string
	Latency  time.Duration
}

// RequestSampleRows returns one row per recorded request (approximated
// from HDR histogram cells), suitable for CSV export. Because the
// underlying storage is now HDR histograms (bounded memory, ~30 KB per
// cell), individual sample values are not retained. This method returns
// one synthetic row per (scenario, kind, phase) cell reporting the P50
// as a representative latency — suitable for audit purposes but not for
// sample-by-sample replay.
//
// TODO(Phase 1b §1.8): replace this stub with the HDR artifact-bundle
// exporter once the Phase 1b log format is determined.
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
		if pairs[i].key.kind != pairs[j].key.kind {
			return pairs[i].key.kind < pairs[j].key.kind
		}
		return pairs[i].key.phase < pairs[j].key.phase
	})
	var rows []RequestSampleRow
	for _, p := range pairs {
		p.shard.mu.Lock()
		count := p.shard.cell.Count()
		p50 := p.shard.cell.Quantile(0.50)
		p.shard.mu.Unlock()
		if count == 0 {
			continue
		}
		rows = append(rows, RequestSampleRow{
			Scenario: p.key.scenario,
			Kind:     p.key.kind,
			Phase:    p.key.phase,
			Latency:  p50,
		})
	}
	return rows
}

// ExportHistograms returns a snapshot of every (scenario, kind, phase) HDR
// histogram cell recorded during this Collector's lifetime. The returned map
// uses the key format "scenario|kind|phase" so it is directly JSON-serializable
// (no struct keys). Scenario, kind, and phase names MUST NOT contain the '|'
// separator — this is enforced by convention (loadgen presets use kebab-case names
// like "history-read"). Each value is a *hdr.Snapshot produced by CellHistogram.Export().
//
// Thread-safe: acquires the outer requestsMu (RLock) to snapshot the key set,
// then acquires each shard's mu to call Export(). The LOCK-ORDER INVARIANT
// (shard mu before c.mu) is respected — neither c.mu nor requestsMu is held
// concurrently with any shard mu.
func (c *Collector) ExportHistograms() map[string]*hdr.Snapshot {
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

	out := make(map[string]*hdr.Snapshot, len(pairs))
	for _, p := range pairs {
		p.shard.mu.Lock()
		snap := p.shard.cell.Export()
		p.shard.mu.Unlock()
		key := fmt.Sprintf("%s|%s|%s", p.key.scenario, p.key.kind, p.key.phase)
		out[key] = snap
	}
	return out
}

// RecordPublished appends msg to the recent ring buffer. Called by publishOne
// after a successful publish so the read-receipts scenario (Phase 3 §3.16)
// can pick message IDs from a sliding window of recent activity.
// The ring is bounded by recentCap; oldest entries are overwritten in FIFO order.
func (c *Collector) RecordPublished(msg RecentMessage) {
	c.recentMu.Lock()
	defer c.recentMu.Unlock()
	if cap(c.recent) == 0 {
		c.recent = make([]RecentMessage, 0, recentCap)
	}
	if len(c.recent) < recentCap {
		c.recent = append(c.recent, msg)
	} else {
		// Shift left and overwrite the tail. At recentCap=1024 × ~60 bytes the
		// per-call copy is ~60 KB — acceptable at typical publish rates (≤5k/s).
		copy(c.recent, c.recent[1:])
		c.recent[len(c.recent)-1] = msg
	}
}

// RecentMessages returns up to n recently-published messages (newest last).
// Returns a copy so callers can iterate without holding the lock.
func (c *Collector) RecentMessages(n int) []RecentMessage {
	c.recentMu.Lock()
	defer c.recentMu.Unlock()
	total := len(c.recent)
	if n > total {
		n = total
	}
	if n == 0 {
		return nil
	}
	out := make([]RecentMessage, n)
	copy(out, c.recent[total-n:])
	return out
}

// snapshotLatencies is defined in members_collector.go as a package-level
// helper so both Collector and MemberCollector can share it.
