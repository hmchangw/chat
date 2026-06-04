# Room-Read Max-RPS Workload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `room-read` workload to `tools/loadgen`'s `max-rps` subcommand that measures the maximum sustainable RPS of marking a room as read (room-service's `message.read` RPC) under a realistic read pattern.

**Architecture:** A new workload implements the existing `rpsWorkload` interface (`RunStep` + `Label`), so it inherits the ramp engine, SLO gating, verdict, report, and CSV output. It is a synchronous NATS request/reply workload (latency-gated only, no pending durables), mirroring the existing `history` workload's structure. Fixtures reuse the existing `messages` presets but are stamped with read-state (`rooms.LastMsgAt` set to a future timestamp, `subscriptions.LastSeenAt` spread behind it, `rooms.MinUserLastSeenAt` = per-room floor) so the room read-floor recompute path stays live for the whole run.

**Tech Stack:** Go 1.25, NATS request/reply (`nats.go`), MongoDB (`mongo-driver/v2`), `stretchr/testify`, `testcontainers-go` via `pkg/testutil`.

**Key reference files (read before starting):**
- `tools/loadgen/maxrps_history.go` — adapter pattern to mirror (`RunStep`, `runFor`, `buildHistoryInputs`).
- `tools/loadgen/history_generator.go` — generator pattern (open-loop ticker + semaphore, Zipf room pick, `issue`).
- `tools/loadgen/history_collector.go` — collector pattern + shared `errClass` consts (`errClassTimeout`, `errClassReply`, `errClassBadReply`).
- `tools/loadgen/history_main.go:296` — `newNATSHistoryRequester` (generic NATS request/reply wrapper, reused as-is).
- `tools/loadgen/seed.go` — `Seed` / `Teardown` (reused) and `insertDocs`.
- `tools/loadgen/preset.go` — `BuiltinPreset`, `BuildFixtures`, `Fixtures`, `Preset`.
- `tools/loadgen/verdict.go` — `rpsStepInputs`, `seriesSamples`.
- `tools/loadgen/ramp.go` — `waitOrCancel`, `rpsWorkload`.
- `tools/loadgen/maxrps.go` — `runMaxRPS` switch to extend.
- `room-service/handler.go:1099` — `handleMessageRead` (the system under test).
- `pkg/subject/subject.go:379` — `subject.MessageRead(account, roomID, siteID)` and `MessageReadWildcard(siteID)`.

**Shared symbols already in `package main` (do NOT redefine):** `errClass` and its consts, `classifyRequesterError`, `drainGracePeriod` (= 5s, `generator.go:82`), `waitOrCancel`, `runFor` (`maxrps_history.go`), `Metrics`/`NewMetrics`/`Handler`, `rpsStepInputs`, `seriesSamples`, `rpsThresholds`, `runRamp`, `renderRPSReport`, `writeRPSCSV`, `maxRPSExitCode`, `parseRPSSteps`, `connectStores`, `Seed`, `Teardown`, `BuildFixtures`, `BuiltinPreset`, `newNATSHistoryRequester`.

**Commands:**
- Unit tests for the package: `make test SERVICE=tools/loadgen`
- Integration test: `make test-integration SERVICE=tools/loadgen`
- Lint: `make lint`

---

### Task 1: Read-state backfill fixtures

Builds the messages fixtures and stamps read-state so the floor-recompute path stays live. Pure function — no I/O.

**Files:**
- Create: `tools/loadgen/roomread_seed.go`
- Test: `tools/loadgen/roomread_seed_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRoomReadFixtures_StampsReadState(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	f := BuildRoomReadFixtures(&p, 42, "site-test", now)

	require.NotEmpty(t, f.Rooms)
	require.NotEmpty(t, f.Subscriptions)

	// Every room's LastMsgAt is in the future relative to `now`, so reads
	// (which stamp LastSeenAt = present) never "catch up" and the floor scan
	// fires on every request.
	for i := range f.Rooms {
		require.NotNil(t, f.Rooms[i].LastMsgAt, "room %s LastMsgAt", f.Rooms[i].ID)
		assert.True(t, f.Rooms[i].LastMsgAt.After(now), "room %s LastMsgAt must be after now", f.Rooms[i].ID)
	}

	// Every subscription's LastSeenAt is behind its room's LastMsgAt.
	lastMsgByRoom := map[string]time.Time{}
	for i := range f.Rooms {
		lastMsgByRoom[f.Rooms[i].ID] = *f.Rooms[i].LastMsgAt
	}
	for i := range f.Subscriptions {
		s := &f.Subscriptions[i]
		require.NotNil(t, s.LastSeenAt, "sub %s LastSeenAt", s.ID)
		assert.True(t, s.LastSeenAt.Before(lastMsgByRoom[s.RoomID]),
			"sub %s LastSeenAt must be before room LastMsgAt", s.ID)
	}

	// Each room's MinUserLastSeenAt equals the min LastSeenAt among its members.
	wantMin := map[string]time.Time{}
	for i := range f.Subscriptions {
		s := &f.Subscriptions[i]
		if cur, ok := wantMin[s.RoomID]; !ok || s.LastSeenAt.Before(cur) {
			wantMin[s.RoomID] = *s.LastSeenAt
		}
	}
	for i := range f.Rooms {
		require.NotNil(t, f.Rooms[i].MinUserLastSeenAt, "room %s MinUserLastSeenAt", f.Rooms[i].ID)
		assert.True(t, f.Rooms[i].MinUserLastSeenAt.Equal(wantMin[f.Rooms[i].ID]),
			"room %s floor mismatch", f.Rooms[i].ID)
	}
}

func TestBuildRoomReadFixtures_Deterministic(t *testing.T) {
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	a := BuildRoomReadFixtures(&p, 42, "site-test", now)
	b := BuildRoomReadFixtures(&p, 42, "site-test", now)

	require.Equal(t, len(a.Subscriptions), len(b.Subscriptions))
	for i := range a.Subscriptions {
		assert.True(t, a.Subscriptions[i].LastSeenAt.Equal(*b.Subscriptions[i].LastSeenAt),
			"sub %d LastSeenAt not deterministic", i)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=tools/loadgen`
Expected: FAIL — `undefined: BuildRoomReadFixtures`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"math/rand"
	"time"
)

// roomReadFutureOffset places every seeded room's LastMsgAt comfortably past
// the longest ramp window. Because a read stamps LastSeenAt = time.Now() (the
// present), a reader's updated LastSeenAt stays before this future LastMsgAt, so
// handleMessageRead never short-circuits on "already caught up" and the
// MinSubscriptionLastSeenByRoomID floor scan fires on every request.
const roomReadFutureOffset = 24 * time.Hour

// roomReadLastSeenSpreadMin bounds how far behind LastMsgAt each subscription's
// seeded LastSeenAt may sit (in minutes). A spread means different members pin
// different floors, so the floor WRITE fires at a rate set by the read
// distribution rather than on every request.
const roomReadLastSeenSpreadMin = 7 * 24 * 60 // one week

// BuildRoomReadFixtures reuses the messages preset fixtures and stamps the
// read-state the mark-as-read workload needs: every room's LastMsgAt is set
// ahead of `now`, every subscription's LastSeenAt is spread behind it, and each
// room's MinUserLastSeenAt is set to the per-room minimum LastSeenAt (the floor
// the room document carries before the run). Deterministic for a given seed.
func BuildRoomReadFixtures(p *Preset, seed int64, siteID string, now time.Time) Fixtures {
	f := BuildFixtures(p, seed, siteID)
	r := rand.New(rand.NewSource(seed))
	lastMsgAt := now.UTC().Add(roomReadFutureOffset)

	for i := range f.Rooms {
		t := lastMsgAt
		f.Rooms[i].LastMsgAt = &t
	}

	minByRoom := make(map[string]time.Time, len(f.Rooms))
	for i := range f.Subscriptions {
		behind := time.Duration(1+r.Intn(roomReadLastSeenSpreadMin)) * time.Minute
		ls := lastMsgAt.Add(-behind).UTC()
		f.Subscriptions[i].LastSeenAt = &ls
		roomID := f.Subscriptions[i].RoomID
		if cur, ok := minByRoom[roomID]; !ok || ls.Before(cur) {
			minByRoom[roomID] = ls
		}
	}

	for i := range f.Rooms {
		if m, ok := minByRoom[f.Rooms[i].ID]; ok {
			mt := m.UTC()
			f.Rooms[i].MinUserLastSeenAt = &mt
		}
	}
	return f
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/roomread_seed.go tools/loadgen/roomread_seed_test.go
git commit -m "feat(loadgen): read-state backfill fixtures for room-read workload"
```

---

### Task 2: Room-read collector

Single-series latency tape + error/saturation counters. Reuses the shared `errClass` consts from `history_collector.go`.

**Files:**
- Create: `tools/loadgen/roomread_collector.go`
- Test: `tools/loadgen/roomread_collector_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRoomReadCollector_Aggregates(t *testing.T) {
	c := NewRoomReadCollector()

	c.RecordSample(RoomReadSample{Latency: 5 * time.Millisecond, At: time.Now()})
	c.RecordSample(RoomReadSample{Latency: 7 * time.Millisecond, At: time.Now()})
	c.RecordError(errClassTimeout, 9*time.Millisecond)
	c.RecordError(errClassReply, 2*time.Millisecond)
	c.RecordBadReply(3 * time.Millisecond)
	c.RecordSaturation()
	c.RecordSaturation()

	assert.Len(t, c.Samples(), 2)
	assert.Equal(t, 1, c.TimeoutErrors())
	assert.Equal(t, 1, c.ReplyErrors())
	assert.Equal(t, 1, c.BadReplyCount())
	assert.Equal(t, 2, c.SaturationCount())
}

func TestRoomReadCollector_SamplesIsCopy(t *testing.T) {
	c := NewRoomReadCollector()
	c.RecordSample(RoomReadSample{Latency: time.Millisecond, At: time.Now()})
	got := c.Samples()
	got[0].Latency = 999 * time.Second
	assert.Equal(t, time.Millisecond, c.Samples()[0].Latency, "Samples must return a defensive copy")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=tools/loadgen`
Expected: FAIL — `undefined: NewRoomReadCollector` / `RoomReadSample`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"sync"
	"time"
)

// RoomReadSample captures one completed message.read round-trip.
type RoomReadSample struct {
	Latency time.Duration
	At      time.Time
}

// RoomReadCollector aggregates samples and errors across a workload run.
// All methods are safe for concurrent use. Reuses the package-shared errClass
// consts (errClassTimeout / errClassReply / errClassBadReply).
type RoomReadCollector struct {
	mu         sync.Mutex
	samples    []RoomReadSample
	errors     map[errClass]int
	saturation int
}

// NewRoomReadCollector returns an empty collector.
func NewRoomReadCollector() *RoomReadCollector {
	return &RoomReadCollector{errors: map[errClass]int{}}
}

// RecordSample stores one completed-call sample.
func (c *RoomReadCollector) RecordSample(s RoomReadSample) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, s)
}

// RecordError tallies a per-class transport/reply error.
func (c *RoomReadCollector) RecordError(class errClass, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors[class]++
}

// RecordBadReply tallies a reply that was not the expected {"status":"accepted"}.
func (c *RoomReadCollector) RecordBadReply(_ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors[errClassBadReply]++
}

// RecordSaturation tallies a tick that fired while the in-flight semaphore was full.
func (c *RoomReadCollector) RecordSaturation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saturation++
}

// Samples returns a defensive copy of the sample tape.
func (c *RoomReadCollector) Samples() []RoomReadSample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]RoomReadSample, len(c.samples))
	copy(out, c.samples)
	return out
}

// TimeoutErrors returns the timeout-class error count.
func (c *RoomReadCollector) TimeoutErrors() int { return c.errCount(errClassTimeout) }

// ReplyErrors returns the reply-class error count.
func (c *RoomReadCollector) ReplyErrors() int { return c.errCount(errClassReply) }

// BadReplyCount returns the count of non-accepted / undecodable replies.
func (c *RoomReadCollector) BadReplyCount() int { return c.errCount(errClassBadReply) }

func (c *RoomReadCollector) errCount(class errClass) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errors[class]
}

// SaturationCount returns the count of saturation events.
func (c *RoomReadCollector) SaturationCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saturation
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/roomread_collector.go tools/loadgen/roomread_collector_test.go
git commit -m "feat(loadgen): room-read collector"
```

---

### Task 3: Room-read generator

Open-loop generator that picks a room (Zipf, mirroring history's hot-room skew), picks a random member, and issues the `message.read` RPC. Reuses `classifyRequesterError` and `drainGracePeriod`.

**Files:**
- Create: `tools/loadgen/roomread_generator.go`
- Test: `tools/loadgen/roomread_generator_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRoomReadRequester records every subject it is asked to request and
// returns a configurable reply/error.
type fakeRoomReadRequester struct {
	mu       sync.Mutex
	subjects []string
	reply    []byte
	err      error
}

func (f *fakeRoomReadRequester) Request(_ context.Context, subj string, _ []byte, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subjects = append(f.subjects, subj)
	if f.err != nil {
		return nil, f.err
	}
	return f.reply, nil
}

func (f *fakeRoomReadRequester) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.subjects))
	copy(out, f.subjects)
	return out
}

func newRoomReadTestGen(t *testing.T, req RoomReadRequester, c *RoomReadCollector) *roomReadGenerator {
	t.Helper()
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	f := BuildRoomReadFixtures(&p, 42, "site-test", time.Now().UTC())
	return newRoomReadGenerator(&roomReadGeneratorConfig{
		Fixtures:       &f,
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      c,
		MaxInFlight:    8,
	}, 42)
}

func TestRoomReadGenerator_EmitsMessageReadSubjects(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"accepted"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	time.Sleep(100 * time.Millisecond)

	subs := req.recorded()
	require.NotEmpty(t, subs, "generator issued no requests")
	for _, s := range subs {
		assert.True(t, strings.HasPrefix(s, "chat.user."), "unexpected subject %q", s)
		assert.True(t, strings.HasSuffix(s, ".message.read"), "unexpected subject %q", s)
	}
	assert.NotEmpty(t, c.Samples(), "accepted replies should be recorded as samples")
}

func TestRoomReadGenerator_BadReplyRecorded(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"nope"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	time.Sleep(100 * time.Millisecond)

	assert.Empty(t, c.Samples(), "non-accepted replies must not count as samples")
	assert.Greater(t, c.BadReplyCount(), 0, "non-accepted replies must count as bad replies")
}

func TestRoomReadGenerator_TimeoutRecorded(t *testing.T) {
	req := &fakeRoomReadRequester{err: context.DeadlineExceeded}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	time.Sleep(100 * time.Millisecond)

	assert.Greater(t, c.TimeoutErrors(), 0, "DeadlineExceeded must count as a timeout")
}

func TestRoomReadGenerator_RequiresPositiveRate(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"accepted"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)
	gen.cfg.Rate = 0
	assert.Error(t, gen.Run(context.Background()))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=tools/loadgen`
Expected: FAIL — `undefined: RoomReadRequester` / `newRoomReadGenerator` / `roomReadGeneratorConfig`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// RoomReadRequester is the narrow request/reply transport seam. The production
// implementation is newNATSHistoryRequester (a generic nats.Conn wrapper);
// tests inject a recorder.
type RoomReadRequester interface {
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

// roomReadReply is the message.read handler's success envelope.
type roomReadReply struct {
	Status string `json:"status"`
}

// roomReadGeneratorConfig bundles every dependency the generator needs.
type roomReadGeneratorConfig struct {
	Fixtures       *Fixtures
	SiteID         string
	Rate           int
	RequestTimeout time.Duration
	Requester      RoomReadRequester
	Collector      *RoomReadCollector
	MaxInFlight    int
}

// roomReadGenerator drives the open-loop message.read request/reply loop.
// Rooms are picked with a Zipf skew (hot rooms read more often, concentrating
// floor-write contention) and a random member of the chosen room is the reader.
type roomReadGenerator struct {
	cfg roomReadGeneratorConfig

	rngMu sync.Mutex
	rng   *rand.Rand
	zipf  *rand.Zipf

	roomSubs map[string][]model.Subscription
}

// newRoomReadGenerator constructs a generator seeded from `seed`. Zipf params
// match the history workload (s=1.1, v=1.0) for consistent hot-room skew.
func newRoomReadGenerator(cfg *roomReadGeneratorConfig, seed int64) *roomReadGenerator {
	r := rand.New(rand.NewSource(seed))
	rooms := cfg.Fixtures.Rooms
	roomCount := len(rooms)
	if roomCount < 1 {
		roomCount = 1
	}
	zipfN := uint64(roomCount - 1)
	if zipfN < 1 {
		zipfN = 1
	}
	z := rand.NewZipf(r, 1.1, 1.0, zipfN)

	lookup := make(map[string][]model.Subscription, roomCount)
	for i := range cfg.Fixtures.Subscriptions {
		s := &cfg.Fixtures.Subscriptions[i]
		lookup[s.RoomID] = append(lookup[s.RoomID], *s)
	}

	return &roomReadGenerator{cfg: *cfg, rng: r, zipf: z, roomSubs: lookup}
}

// Run drives the open-loop publisher until ctx cancels. Mirrors HistoryGenerator.Run.
func (g *roomReadGenerator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()

	if g.cfg.MaxInFlight <= 0 {
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				g.requestOne(ctx)
			}
		}
	}

	sem := make(chan struct{}, g.cfg.MaxInFlight)
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(drainGracePeriod):
			}
			return nil
		case <-tick.C:
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() {
						<-sem
						wg.Done()
					}()
					g.requestOne(ctx)
				}()
			default:
				g.cfg.Collector.RecordSaturation()
			}
		}
	}
}

func (g *roomReadGenerator) requestOne(ctx context.Context) {
	roomID := g.pickRoom()
	if roomID == "" {
		return
	}
	subs := g.roomSubs[roomID]
	if len(subs) == 0 {
		return
	}
	reader := subs[g.intn(len(subs))]
	g.doRead(ctx, roomID, reader.User.Account)
}

func (g *roomReadGenerator) doRead(ctx context.Context, roomID, account string) {
	subj := subject.MessageRead(account, roomID, g.cfg.SiteID)
	start := time.Now()
	reply, err := g.cfg.Requester.Request(ctx, subj, nil, g.cfg.RequestTimeout)
	latency := time.Since(start)

	if err != nil {
		// Run-level cancellation isn't a real failure — the run is draining.
		if ctx.Err() != nil {
			return
		}
		g.cfg.Collector.RecordError(classifyRequesterError(err), latency)
		return
	}

	var parsed roomReadReply
	if jerr := json.Unmarshal(reply, &parsed); jerr != nil || parsed.Status != "accepted" {
		g.cfg.Collector.RecordBadReply(latency)
		return
	}
	g.cfg.Collector.RecordSample(RoomReadSample{Latency: latency, At: time.Now()})
}

func (g *roomReadGenerator) pickRoom() string {
	rooms := g.cfg.Fixtures.Rooms
	if len(rooms) == 0 {
		return ""
	}
	g.rngMu.Lock()
	idx := g.zipf.Uint64()
	g.rngMu.Unlock()
	if int(idx) >= len(rooms) {
		idx = uint64(len(rooms) - 1)
	}
	return rooms[idx].ID
}

func (g *roomReadGenerator) intn(n int) int {
	if n <= 0 {
		return 0
	}
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return g.rng.Intn(n)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/roomread_generator.go tools/loadgen/roomread_generator_test.go
git commit -m "feat(loadgen): room-read open-loop generator"
```

---

### Task 4: Room-read workload adapter

Implements `rpsWorkload`. `buildRoomReadInputs` maps collector state into `rpsStepInputs` (single series, no pending). `newRoomReadWorkload` wires NATS + metrics. Reuses `runFor`, `Metrics`, `newNATSHistoryRequester`.

**Files:**
- Create: `tools/loadgen/maxrps_roomread.go`
- Test: `tools/loadgen/maxrps_roomread_test.go`

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time check: roomReadWorkload satisfies rpsWorkload.
var _ rpsWorkload = (*roomReadWorkload)(nil)

func TestBuildRoomReadInputs_MapsCollector(t *testing.T) {
	c := NewRoomReadCollector()
	c.RecordSample(RoomReadSample{Latency: 4 * time.Millisecond, At: time.Now()})
	c.RecordSample(RoomReadSample{Latency: 6 * time.Millisecond, At: time.Now()})
	c.RecordError(errClassTimeout, time.Millisecond)
	c.RecordBadReply(time.Millisecond)
	c.RecordSaturation()

	in := buildRoomReadInputs(1000, 30*time.Second, c)

	assert.Equal(t, 1000, in.TargetRPS)
	assert.Equal(t, 30*time.Second, in.Hold)
	assert.Equal(t, 2, in.FailedOps)             // 1 timeout + 1 bad reply
	assert.Equal(t, 4, in.AttemptedOps)          // 2 samples + 2 failed
	assert.Equal(t, 1, in.Saturation)
	assert.Empty(t, in.Pending, "synchronous RPC has no pending durables")
	require.Len(t, in.Latencies, 1)
	assert.Equal(t, "room-read", in.Latencies[0].Name)
	assert.Len(t, in.Latencies[0].Samples, 2)
}

func TestRoomReadWorkload_Label(t *testing.T) {
	w := &roomReadWorkload{}
	assert.Equal(t, "room-read", w.Label())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=tools/loadgen`
Expected: FAIL — `undefined: roomReadWorkload` / `buildRoomReadInputs`.

- [ ] **Step 3: Write minimal implementation**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// roomReadLatencies extracts the latency tape from a sample slice.
func roomReadLatencies(samples []RoomReadSample) []time.Duration {
	out := make([]time.Duration, len(samples))
	for i := range samples {
		out[i] = samples[i].Latency
	}
	return out
}

// buildRoomReadInputs assembles normalized step inputs from a (hold-only)
// collector. message.read is synchronous request/reply, so there is no consumer
// queue and Pending stays empty; the single "room-read" latency series gates.
func buildRoomReadInputs(targetRPS int, hold time.Duration, c *RoomReadCollector) rpsStepInputs {
	samples := c.Samples()
	failed := c.TimeoutErrors() + c.ReplyErrors() + c.BadReplyCount()
	return rpsStepInputs{
		TargetRPS:    targetRPS,
		Hold:         hold,
		AttemptedOps: len(samples) + failed,
		FailedOps:    failed,
		Saturation:   c.SaturationCount(),
		Latencies: []seriesSamples{
			{Name: "room-read", Samples: roomReadLatencies(samples)},
		},
	}
}

// roomReadWorkload drives message.read requests at a given RPS. As with the
// other workloads the natsutil connection and metrics server are captured by the
// cleanup closure, not stored on the struct.
type roomReadWorkload struct {
	cfg            *config
	preset         *Preset
	fixtures       Fixtures
	seed           int64
	requestTimeout time.Duration
	metrics        *Metrics
	requester      RoomReadRequester
}

func (w *roomReadWorkload) Label() string { return "room-read" }

// newRoomReadWorkload connects NATS, starts the metrics server, and builds the
// fixtures used for target selection. Only room IDs and subscriber accounts are
// read from the fixtures (both deterministic on seed), so selection stays
// consistent with whatever `loadgen seed --workload=room-read` wrote earlier.
func newRoomReadWorkload(ctx context.Context, cfg *config, preset *Preset, seed int64, requestTimeout time.Duration) (*roomReadWorkload, func(), error) {
	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		return nil, nil, fmt.Errorf("nats connect: %w", err)
	}
	metrics := NewMetrics()
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: metrics.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()
	w := &roomReadWorkload{
		cfg:            cfg,
		preset:         preset,
		fixtures:       BuildRoomReadFixtures(preset, seed, cfg.SiteID, time.Now().UTC()),
		seed:           seed,
		requestTimeout: requestTimeout,
		metrics:        metrics,
		requester:      newNATSHistoryRequester(nc.NatsConn()),
	}
	cleanup := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(shutCtx)
		cancel()
		_ = nc.Drain()
	}
	return w, cleanup, nil
}

func (w *roomReadWorkload) newGenerator(collector *RoomReadCollector, targetRPS int) *roomReadGenerator {
	return newRoomReadGenerator(&roomReadGeneratorConfig{
		Fixtures:       &w.fixtures,
		SiteID:         w.cfg.SiteID,
		Rate:           targetRPS,
		RequestTimeout: w.requestTimeout,
		Requester:      w.requester,
		Collector:      collector,
		MaxInFlight:    w.cfg.MaxInFlight,
	}, w.seed)
}

// RunStep runs warmup (discarded) then hold (measured) as two sequential
// generator runs so the hold collector contains only hold-window data.
func (w *roomReadWorkload) RunStep(ctx context.Context, targetRPS int, warmup, hold time.Duration) (rpsStepInputs, error) {
	if warmup > 0 {
		warmCollector := NewRoomReadCollector()
		if err := runFor(ctx, w.newGenerator(warmCollector, targetRPS), warmup); err != nil {
			return rpsStepInputs{}, err
		}
	}
	collector := NewRoomReadCollector()
	if err := runFor(ctx, w.newGenerator(collector, targetRPS), hold); err != nil {
		return rpsStepInputs{}, err
	}
	time.Sleep(2 * time.Second) // drain trailing in-flight replies into the collector
	return buildRoomReadInputs(targetRPS, hold, collector), nil
}
```

> **Note on `runFor`:** it is defined in `maxrps_history.go` as `runFor(ctx context.Context, gen *HistoryGenerator, d time.Duration) error`. Its parameter is typed `*HistoryGenerator`, so it cannot accept `*roomReadGenerator`. In Step 3, instead of calling the existing `runFor`, inline the same logic with the room-read generator. Replace the two `runFor(ctx, w.newGenerator(...), ...)` calls with a local helper:

```go
// runRoomReadFor runs gen.Run for d (or until ctx cancels), then stops it.
// Mirrors maxrps_history.go's runFor for the room-read generator type.
func runRoomReadFor(ctx context.Context, gen *roomReadGenerator, d time.Duration) error {
	genCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = gen.Run(genCtx)
	}()
	err := waitOrCancel(ctx, d)
	cancel()
	wg.Wait()
	return err
}
```

Add `"sync"` to the import block and use `runRoomReadFor` in `RunStep` instead of `runFor`.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/maxrps_roomread.go tools/loadgen/maxrps_roomread_test.go
git commit -m "feat(loadgen): room-read max-rps workload adapter"
```

---

### Task 5: Wire `room-read` into the CLI

Add the `room-read` case to the `max-rps`, `seed`, and `teardown` subcommand switches.

**Files:**
- Modify: `tools/loadgen/maxrps.go` (the `switch *workload` in `runMaxRPS`, around line 73; flag doc string at line 25)
- Modify: `tools/loadgen/main.go` (the `switch *workload` in `runSeed` ~line 114 and `runTeardown` ~line 200; add `runSeedRoomRead` + `runTeardownRoomRead`)
- Test: `tools/loadgen/main_test.go` (add a seed-routing assertion if the existing tests cover the switch; otherwise the integration test in Task 7 covers wiring)

- [ ] **Step 1: Add the `room-read` case to `runMaxRPS`**

In `tools/loadgen/maxrps.go`, update the workload flag description:

```go
	workload := fs.String("workload", "messages", "messages|history|room-read")
```

Then add a new `case` to the `switch *workload` block (after the `history` case, before `default`):

```go
	case "room-read":
		p, ok := BuiltinPreset(*preset)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown preset: %s\n", *preset)
			return 2
		}
		if *requestTimeout <= 0 {
			fmt.Fprintln(os.Stderr, "--request-timeout must be > 0")
			return 2
		}
		rw, clean, err := newRoomReadWorkload(ctx, cfg, &p, *seed, *requestTimeout)
		if err != nil {
			slog.Error("init room-read workload", "error", err)
			return 1
		}
		w, cleanup, presetID = rw, clean, p.Name
```

> `requestTimeout` is the existing flag (`--request-timeout`, default 5s) currently documented as "history only"; it now also applies to room-read. Update its flag doc string to `"history/room-read only: per-request timeout"`.

- [ ] **Step 2: Add seed + teardown routing in `main.go`**

In `runSeed`'s switch, after the `history` case:

```go
	case "room-read":
		return runSeedRoomRead(ctx, cfg, *preset, *seed)
```

In `runTeardown`'s switch, after the `history` case:

```go
	case "room-read":
		return runTeardownRoomRead(ctx, cfg, *preset, *seed)
```

Update both flag doc strings from `"messages|members|history"` to `"messages|members|history|room-read"`.

Add the two helpers (place them next to `runSeedMessages` / `runTeardownMessages`):

```go
func runSeedRoomRead(ctx context.Context, cfg *config, preset string, seed int64) int {
	p, ok := BuiltinPreset(preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", preset)
		return 2
	}
	db, _, cleanup, err := connectStores(ctx, cfg)
	if err != nil {
		return 1
	}
	defer cleanup()
	fixtures := BuildRoomReadFixtures(&p, seed, cfg.SiteID, time.Now().UTC())
	if err := Seed(ctx, db, &fixtures); err != nil {
		slog.Error("seed", "error", err)
		return 1
	}
	slog.Info("seed complete (room-read)",
		"preset", p.Name,
		"users", len(fixtures.Users),
		"rooms", len(fixtures.Rooms),
		"subs", len(fixtures.Subscriptions))
	return 0
}

func runTeardownRoomRead(ctx context.Context, cfg *config, preset string, seed int64) int {
	if _, ok := BuiltinPreset(preset); !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", preset)
		return 2
	}
	db, _, cleanup, err := connectStores(ctx, cfg)
	if err != nil {
		return 1
	}
	defer cleanup()
	if err := Teardown(ctx, db); err != nil {
		slog.Error("teardown", "error", err)
		return 1
	}
	slog.Info("teardown complete (room-read)", "preset", preset)
	return 0
}
```

> The `seed` arg is accepted for signature symmetry with the other workloads and to keep the call sites uniform; teardown drops collections wholesale so it does not need to rebuild fixtures. Keep the parameter (the switch passes `*seed`).

> Confirm `"time"` is already imported in `main.go` (it is, used elsewhere). No new imports needed.

- [ ] **Step 3: Verify compilation + existing tests pass**

Run: `make test SERVICE=tools/loadgen`
Expected: PASS (build succeeds; existing tests unaffected).

- [ ] **Step 4: Lint**

Run: `make lint`
Expected: no new findings.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/maxrps.go tools/loadgen/main.go
git commit -m "feat(loadgen): wire room-read into seed/teardown/max-rps subcommands"
```

---

### Task 6: Deploy Makefile targets + README

**Files:**
- Modify: `tools/loadgen/deploy/Makefile`
- Modify: `tools/loadgen/README.md`

- [ ] **Step 1: Add Makefile targets**

In `tools/loadgen/deploy/Makefile`, add `seed-roomread` and `teardown-roomread` to the `.PHONY` line, then add the targets (after `teardown-members`). `run-max-rps` already accepts `WORKLOAD`, so no new run target is needed.

```make
seed-roomread:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen seed --workload=room-read --preset=$(PRESET)

teardown-roomread:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen teardown --workload=room-read --preset=$(PRESET)
```

Update the `run-max-rps` help comment to list the new workload:

```make
run-max-rps: ## Ramp RPS to find the max under SLO (WORKLOAD=messages|history|room-read PRESET=.. STEPS=..)
```

- [ ] **Step 2: Add README section**

Append to `tools/loadgen/README.md` (after the members section):

````markdown
## Room-read workload (mark-as-read benchmark)

Finds the maximum sustainable RPS for marking a room as read
(`room-service.handleMessageRead`, the `message.read` request/reply RPC). The
workload reuses the messages presets but seeds read-state so the room
read-floor recompute path stays exercised: every room's `lastMsgAt` is stamped
ahead of the run window and members' `lastSeenAt` are spread behind it, so each
read is "a user opening a room with unread content" — the floor scan fires on
every request and the floor write fires at a rate set by room size and the read
distribution.

### Quick start

```
make -C tools/loadgen/deploy up
make -C tools/loadgen/deploy seed-roomread PRESET=medium
make -C tools/loadgen/deploy run-max-rps WORKLOAD=room-read PRESET=medium
```

Override the ramp with `STEPS` (default `200,500,1000,2000,5000`):

```
make -C tools/loadgen/deploy run-max-rps WORKLOAD=room-read PRESET=medium STEPS=500,1k,2k,5k
```

Tear down the fixtures:

```
make -C tools/loadgen/deploy teardown-roomread PRESET=medium
```

### Notes

- Synchronous request/reply: gated on p95/p99 latency and error rate only
  (no consumer-pending signal). Defaults: `--slo-p95=100ms`, `--slo-p99=250ms`,
  `--slo-error-rate=0.001`; override via the shared `max-rps` flags.
- Single-site only: all seeded users are local, so no cross-site outbox event is
  published on the read path.
- Presets are the messages presets (`small`/`medium`/`large`/`realistic`); room
  size distribution drives floor-write contention.
````

- [ ] **Step 3: Commit**

```bash
git add tools/loadgen/deploy/Makefile tools/loadgen/README.md
git commit -m "docs(loadgen): room-read deploy targets and README"
```

---

### Task 7: Integration test

End-to-end against a real Mongo (seed via the production helpers) and real NATS (stub `message.read` responder). Mirrors `history_integration_test.go`. The package already has a `TestMain` (in `integration_test.go`) — do NOT add another.

**Files:**
- Create: `tools/loadgen/roomread_integration_test.go`

- [ ] **Step 1: Write the integration test**

```go
//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

// TestRoomReadWorkload_EndToEnd seeds read-state fixtures into a real Mongo,
// then drives the generator briefly against a stub message.read responder and
// asserts the seeded read-state is present and the generator records samples.
func TestRoomReadWorkload_EndToEnd(t *testing.T) {
	ctx := context.Background()

	db := testutil.MongoDB(t, "loadgen_roomread")

	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	siteID := "site-test"
	now := time.Now().UTC()
	fixtures := BuildRoomReadFixtures(&p, 42, siteID, now)

	require.NoError(t, Seed(ctx, db, &fixtures))

	// Cross-check: every seeded room carries a future LastMsgAt + a floor.
	var roomCount int64
	roomCount, err := db.Collection("rooms").CountDocuments(ctx,
		map[string]any{"lastMsgAt": map[string]any{"$gt": now}})
	require.NoError(t, err)
	assert.Equal(t, int64(len(fixtures.Rooms)), roomCount, "all rooms should have a future lastMsgAt")

	// Stub message.read responder.
	nc, err := nats.Connect(testutil.NATS(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nc.Drain() })

	sub, err := nc.Subscribe(subject.MessageReadWildcard(siteID), func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"status":"accepted"}`))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Drive the generator briefly.
	collector := NewRoomReadCollector()
	requester := newNATSHistoryRequester(nc)
	gen := newRoomReadGenerator(&roomReadGeneratorConfig{
		Fixtures:       &fixtures,
		SiteID:         siteID,
		Rate:           50,
		RequestTimeout: 2 * time.Second,
		Requester:      requester,
		Collector:      collector,
		MaxInFlight:    16,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))
	time.Sleep(500 * time.Millisecond) // drain trailing replies

	assert.NotEmpty(t, collector.Samples(), "generator produced zero samples")
	assert.Equal(t, 0, collector.TimeoutErrors(), "no requests should time out against the stub")
	assert.Equal(t, 0, collector.ReplyErrors(), "stub never returns an error")
	assert.Equal(t, 0, collector.BadReplyCount(), "stub always returns accepted")
}
```

- [ ] **Step 2: Run the integration test**

Run: `make test-integration SERVICE=tools/loadgen`
Expected: PASS (requires Docker for Mongo + NATS testcontainers).

- [ ] **Step 3: Commit**

```bash
git add tools/loadgen/roomread_integration_test.go
git commit -m "test(loadgen): room-read workload integration test"
```

---

## Final verification

- [ ] `make test SERVICE=tools/loadgen` — all unit tests pass.
- [ ] `make test-integration SERVICE=tools/loadgen` — integration test passes.
- [ ] `make lint` — clean.
- [ ] Confirm coverage of the new files is ≥80% (`go test -coverprofile` on the package), ≥90% for generator + collector.
- [ ] Manual smoke (optional, requires the stack): `make -C tools/loadgen/deploy up && make -C tools/loadgen/deploy seed-roomread PRESET=medium && make -C tools/loadgen/deploy run-max-rps WORKLOAD=room-read PRESET=medium`.

## Notes on scope

- No `docs/client-api.md` change: `message.read`'s request/response schema is unchanged; this adds only a benchmark.
- No new third-party dependencies.
- The cross-site outbox branch in `handleMessageRead` is intentionally not exercised (all seeded users are local) — consistent with loadgen's single-site non-goal.
