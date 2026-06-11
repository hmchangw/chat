# Loadgen `max-rps` read-receipt workload — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `read-receipt` workload to the loadgen `max-rps` ramp command that drives the room-service read-receipt RPC at increasing RPS steps and reports the maximum sustainable rate under the configured SLOs.

**Architecture:** Read receipts are a synchronous NATS request/reply read, so this mirrors the existing `history` workload. A new `readReceiptWorkload` implements the existing `rpsWorkload` interface (`RunStep` + `Label`), reusing the ramp engine (`ramp.go`), verdict logic (`verdict.go`), and report rendering (`maxrps_report.go`) unchanged. Fixtures come from the existing history seed (`BuildHistoryFixtures`); a new seed step stamps `lastSeenAt` on a configurable fraction of subscribers so `ListReadReceipts` exercises its real `$match`/`$lookup`/`$unwind` path instead of short-circuiting on an empty match.

**Tech Stack:** Go 1.25, `nats.go`, `go.mongodb.org/mongo-driver/v2`, `testify`, `go.uber.org/mock` (not needed here — hand-written fakes), `testcontainers` via `pkg/testutil`.

**Spec:** `docs/superpowers/specs/2026-06-02-loadgen-read-receipt-maxrps-design.md`

---

## File structure

| File | Create/Modify | Responsibility |
|------|---------------|----------------|
| `tools/loadgen/readreceipt_collector.go` | Create | In-memory latency tape + error/saturation counters. Reuses `errClass*` constants from `history_collector.go`. |
| `tools/loadgen/readreceipt_collector_test.go` | Create | Collector accounting unit tests. |
| `tools/loadgen/readreceipt_requester.go` | Create | `ReadReceiptRequester` interface + NATS impl (`natsReadReceiptRequester`). No dedicated unit test — exercised by the generator tests (Task 3) and the integration test (Task 8), matching the history requester. |
| `tools/loadgen/readreceipt_generator.go` | Create | `readReceiptTarget`, `deriveReadReceiptTargets`, `ReadReceiptGenerator` (Rate ticker + `MaxInFlight` semaphore), `requestOne`. |
| `tools/loadgen/readreceipt_generator_test.go` | Create | Target-derivation + generator rate/saturation unit tests (fake requester). |
| `tools/loadgen/maxrps_readreceipt.go` | Create | `readReceiptWorkload` (`rpsWorkload`), `newReadReceiptWorkload`, `buildReadReceiptInputs`. |
| `tools/loadgen/maxrps_readreceipt_test.go` | Create | `buildReadReceiptInputs` mapping + `rpsWorkload` compile-time check. |
| `tools/loadgen/readreceipt_seed.go` | Create | `selectReaders` (pure), `latestTopLevelByRoom`, `SeedReadReceiptState`. |
| `tools/loadgen/readreceipt_seed_test.go` | Create | `selectReaders` + `latestTopLevelByRoom` unit tests. |
| `tools/loadgen/readreceipt_integration_test.go` | Create | `//go:build integration` end-to-end: seed Mongo, `SeedReadReceiptState`, stub NATS responder, drive generator. |
| `tools/loadgen/maxrps.go` | Modify | Add `read-receipt` to `defaultSteps`; add `case "read-receipt"` to the `runMaxRPS` workload switch; update `--workload` help text. |
| `tools/loadgen/main.go` | Modify | Add `--read-ratio` flag + `case "read-receipt"` to `runSeed`; add `runSeedReadReceipt`; update usage/help strings. |
| `tools/loadgen/README.md` | Modify | Document the new workload + seed step. |

**Reused unchanged:** `BuildHistoryFixtures`, `BuiltinHistoryPreset`, `Seed`, `SeedRoomKeys`, `SeedThreadRooms`, `SeedHistoryCassandra`, `connectStores`, `connectCassandra`, `newNATSHistoryRequester` (transport reused by the integration test), `classifyRequesterError`, `errClass*`, `drainGracePeriod`, `rpsStepInputs`, `seriesSamples`, `runRamp`, `evaluateRPSStep`, `renderRPSReport`, `writeRPSCSV`.

**Note on naming:** The existing `errClass`, `errClassTimeout`, `errClassReply`, `errClassBadReply`, `errClassBadRequest` constants and `classifyRequesterError` live in `history_collector.go` / `history_generator.go` in `package main`. The new files are in the same package, so they reuse these directly — do **not** redefine them.

---

### Task 1: Read-receipt collector

**Files:**
- Create: `tools/loadgen/readreceipt_collector.go`
- Test: `tools/loadgen/readreceipt_collector_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/loadgen/readreceipt_collector_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestReadReceiptCollector_SamplesAndErrors(t *testing.T) {
	c := NewReadReceiptCollector()
	c.RecordSample(15 * time.Millisecond)
	c.RecordSample(20 * time.Millisecond)
	c.RecordError(errClassTimeout)
	c.RecordError(errClassReply)
	c.RecordError(errClassBadReply)
	c.RecordBadRequest()
	c.RecordSaturation()
	c.RecordSaturation()

	assert.Equal(t, []time.Duration{15 * time.Millisecond, 20 * time.Millisecond}, c.Samples())
	// Failed = timeout + reply + bad_reply + bad_request = 4
	assert.Equal(t, 4, c.Failed())
	assert.Equal(t, 2, c.Saturation())
}

func TestReadReceiptCollector_EmptyIsZero(t *testing.T) {
	c := NewReadReceiptCollector()
	assert.Empty(t, c.Samples())
	assert.Equal(t, 0, c.Failed())
	assert.Equal(t, 0, c.Saturation())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=loadgen`
Expected: FAIL — `NewReadReceiptCollector` undefined (compile error).

- [ ] **Step 3: Write minimal implementation**

Create `tools/loadgen/readreceipt_collector.go`:

```go
package main

import (
	"sync"
	"time"
)

// ReadReceiptCollector aggregates latency samples and error/saturation tallies
// for one read-receipt workload step. All methods are safe for concurrent use.
// The errClass constants are shared with the history collector (same package).
type ReadReceiptCollector struct {
	mu         sync.Mutex
	samples    []time.Duration
	errors     map[errClass]int
	saturation int
}

// NewReadReceiptCollector returns an empty collector.
func NewReadReceiptCollector() *ReadReceiptCollector {
	return &ReadReceiptCollector{errors: map[errClass]int{}}
}

// RecordSample stores one completed request's latency.
func (c *ReadReceiptCollector) RecordSample(latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, latency)
}

// RecordError tallies a per-class request failure.
func (c *ReadReceiptCollector) RecordError(class errClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors[class]++
}

// RecordBadRequest tallies a request that failed before issue (e.g. marshal).
func (c *ReadReceiptCollector) RecordBadRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errors[errClassBadRequest]++
}

// RecordSaturation tallies a tick that fired while the in-flight pool was full.
func (c *ReadReceiptCollector) RecordSaturation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saturation++
}

// Samples returns a defensive copy of the latency tape.
func (c *ReadReceiptCollector) Samples() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.samples))
	copy(out, c.samples)
	return out
}

// Failed returns the total error count across all classes.
func (c *ReadReceiptCollector) Failed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, n := range c.errors {
		total += n
	}
	return total
}

// Saturation returns the saturation-event count.
func (c *ReadReceiptCollector) Saturation() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saturation
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/readreceipt_collector.go tools/loadgen/readreceipt_collector_test.go
git commit -m "feat(loadgen): add read-receipt collector"
```

---

### Task 2: Read-receipt requester

**Files:**
- Create: `tools/loadgen/readreceipt_requester.go`

**Note:** The requester is a thin NATS round-trip with the identical shape to `natsHistoryRequester`. Its behavior is exercised end-to-end by the integration test (Task 9) and the generator unit tests (Task 3, via a fake). No standalone unit test file is needed — a unit test of `RequestWithContext` would only re-test the NATS client. This matches the history workload, which has no dedicated requester unit test.

- [ ] **Step 1: Write the implementation**

Create `tools/loadgen/readreceipt_requester.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// ReadReceiptRequester is the narrow request/reply transport the read-receipt
// generator depends on. The production implementation wraps
// nats.Conn.RequestWithContext; tests inject a fake.
type ReadReceiptRequester interface {
	Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

// natsReadReceiptRequester is the production ReadReceiptRequester. Each call
// performs nats.Conn.RequestWithContext under a per-call timeout context.
type natsReadReceiptRequester struct {
	nc *nats.Conn
}

func newNATSReadReceiptRequester(nc *nats.Conn) *natsReadReceiptRequester {
	return &natsReadReceiptRequester{nc: nc}
}

func (r *natsReadReceiptRequester) Request(ctx context.Context, subj string, data []byte, timeout time.Duration) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	msg, err := r.nc.RequestWithContext(reqCtx, subj, data)
	if err != nil {
		return nil, fmt.Errorf("nats request: %w", err)
	}
	return msg.Data, nil
}
```

- [ ] **Step 2: Verify compilation**

Run: `make build SERVICE=loadgen`
Expected: builds (the type is unused until Task 3/4 — Go permits unused package-level types; only unused imports/locals fail). If the build complains about an unused import, it means a typo — fix it.

- [ ] **Step 3: Commit**

```bash
git add tools/loadgen/readreceipt_requester.go
git commit -m "feat(loadgen): add read-receipt requester"
```

---

### Task 3: Target derivation + generator

**Files:**
- Create: `tools/loadgen/readreceipt_generator.go`
- Test: `tools/loadgen/readreceipt_generator_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/loadgen/readreceipt_generator_test.go`:

```go
package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReadReceiptRequester records calls and returns a canned reply/error.
type fakeReadReceiptRequester struct {
	mu    sync.Mutex
	calls int
	reply []byte
	err   error
}

func (f *fakeReadReceiptRequester) Request(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.reply, f.err
}

func (f *fakeReadReceiptRequester) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestDeriveReadReceiptTargets_TopLevelOnly(t *testing.T) {
	plan := &MessagePlan{Messages: []plannedMessage{
		{RoomID: "r1", MessageID: "m1", SenderAccount: "user-1", ThreadParentID: ""},
		{RoomID: "r1", MessageID: "m2", SenderAccount: "user-2", ThreadParentID: "m1"}, // reply → excluded
		{RoomID: "r2", MessageID: "m3", SenderAccount: "user-3", ThreadParentID: "", ThreadRoomID: "tr-r2-1"}, // thread parent → included
	}}
	targets := deriveReadReceiptTargets(plan)
	require.Len(t, targets, 2)
	assert.Equal(t, readReceiptTarget{Account: "user-1", RoomID: "r1", MessageID: "m1"}, targets[0])
	assert.Equal(t, readReceiptTarget{Account: "user-3", RoomID: "r2", MessageID: "m3"}, targets[1])
}

func TestReadReceiptGenerator_RateAndReplies(t *testing.T) {
	req := &fakeReadReceiptRequester{reply: []byte(`{"readers":[]}`)}
	collector := NewReadReceiptCollector()
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:        []readReceiptTarget{{Account: "user-1", RoomID: "r1", MessageID: "m1"}},
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      collector,
		MaxInFlight:    16,
	}, 42)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	assert.Positive(t, req.count(), "generator issued zero requests")
	assert.NotEmpty(t, collector.Samples(), "collector recorded zero samples")
}

func TestReadReceiptGenerator_RejectsNonPositiveRate(t *testing.T) {
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:   []readReceiptTarget{{Account: "user-1", RoomID: "r1", MessageID: "m1"}},
		Rate:      0,
		Requester: &fakeReadReceiptRequester{},
		Collector: NewReadReceiptCollector(),
	}, 42)
	assert.Error(t, gen.Run(context.Background()))
}

func TestReadReceiptGenerator_RecordsReplyError(t *testing.T) {
	req := &fakeReadReceiptRequester{err: context.DeadlineExceeded}
	collector := NewReadReceiptCollector()
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:        []readReceiptTarget{{Account: "user-1", RoomID: "r1", MessageID: "m1"}},
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      collector,
		MaxInFlight:    0, // serial — deterministic single error path
	}, 42)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	assert.Positive(t, collector.Failed(), "deadline-exceeded replies should be tallied as failures")
	assert.Empty(t, collector.Samples())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=loadgen`
Expected: FAIL — `deriveReadReceiptTargets`, `readReceiptTarget`, `NewReadReceiptGenerator`, `ReadReceiptGeneratorConfig` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `tools/loadgen/readreceipt_generator.go`:

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

// readReceiptTarget is one (sender, room, message) tuple the workload can
// request a read-receipt for. The requester account is the message's sender
// because the RPC requires msgSender == requesterAccount.
type readReceiptTarget struct {
	Account   string
	RoomID    string
	MessageID string
}

// deriveReadReceiptTargets selects every top-level message (ThreadParentID == "")
// from the plan as a target. Thread replies are excluded.
func deriveReadReceiptTargets(plan *MessagePlan) []readReceiptTarget {
	out := make([]readReceiptTarget, 0, len(plan.Messages))
	for i := range plan.Messages {
		m := &plan.Messages[i]
		if m.ThreadParentID != "" {
			continue
		}
		out = append(out, readReceiptTarget{
			Account:   m.SenderAccount,
			RoomID:    m.RoomID,
			MessageID: m.MessageID,
		})
	}
	return out
}

// ReadReceiptGeneratorConfig bundles every dependency the generator needs.
type ReadReceiptGeneratorConfig struct {
	Targets        []readReceiptTarget
	SiteID         string
	Rate           int
	RequestTimeout time.Duration
	Requester      ReadReceiptRequester
	Collector      *ReadReceiptCollector
	MaxInFlight    int
}

// ReadReceiptGenerator drives the open-loop request/reply loop. Mirrors
// HistoryGenerator.Run's shape: a ticker paces requests, and when MaxInFlight>0
// each tick dispatches to a bounded goroutine pool with saturation tallied.
type ReadReceiptGenerator struct {
	cfg   ReadReceiptGeneratorConfig
	rngMu sync.Mutex
	rng   *rand.Rand
}

// NewReadReceiptGenerator constructs a generator seeded from `seed`.
func NewReadReceiptGenerator(cfg *ReadReceiptGeneratorConfig, seed int64) *ReadReceiptGenerator {
	return &ReadReceiptGenerator{
		cfg: *cfg,
		rng: rand.New(rand.NewSource(seed)),
	}
}

// Run drives the open-loop publisher until ctx cancels.
func (g *ReadReceiptGenerator) Run(ctx context.Context) error {
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

func (g *ReadReceiptGenerator) requestOne(ctx context.Context) {
	if len(g.cfg.Targets) == 0 {
		return
	}
	t := g.cfg.Targets[g.intn(len(g.cfg.Targets))]

	data, err := json.Marshal(model.ReadReceiptRequest{MessageID: t.MessageID})
	if err != nil {
		g.cfg.Collector.RecordBadRequest()
		return
	}
	subj := subject.MessageReadReceipt(t.Account, t.RoomID, g.cfg.SiteID)

	start := time.Now()
	reply, err := g.cfg.Requester.Request(ctx, subj, data, g.cfg.RequestTimeout)
	latency := time.Since(start)
	if err != nil {
		// Run-level cancellation isn't a real failure — the run is draining.
		if ctx.Err() != nil {
			return
		}
		g.cfg.Collector.RecordError(classifyRequesterError(err))
		return
	}
	// A reply carrying an error field is a logical failure, not a latency sample.
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(reply, &payload); err != nil {
		g.cfg.Collector.RecordError(errClassBadReply)
		return
	}
	if payload.Error != "" {
		g.cfg.Collector.RecordError(errClassReply)
		return
	}
	g.cfg.Collector.RecordSample(latency)
}

func (g *ReadReceiptGenerator) intn(n int) int {
	if n <= 0 {
		return 0
	}
	g.rngMu.Lock()
	defer g.rngMu.Unlock()
	return g.rng.Intn(n)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/readreceipt_generator.go tools/loadgen/readreceipt_generator_test.go
git commit -m "feat(loadgen): add read-receipt target derivation and generator"
```

---

### Task 4: Workload adapter + input mapping

**Files:**
- Create: `tools/loadgen/maxrps_readreceipt.go`
- Test: `tools/loadgen/maxrps_readreceipt_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/loadgen/maxrps_readreceipt_test.go`:

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Compile-time check: readReceiptWorkload satisfies rpsWorkload.
var _ rpsWorkload = (*readReceiptWorkload)(nil)

func TestBuildReadReceiptInputs(t *testing.T) {
	c := NewReadReceiptCollector()
	c.RecordSample(10 * time.Millisecond)
	c.RecordSample(12 * time.Millisecond)
	c.RecordSample(14 * time.Millisecond)
	c.RecordError(errClassTimeout)
	c.RecordError(errClassReply)
	c.RecordSaturation()

	in := buildReadReceiptInputs(1000, 10*time.Second, c)

	assert.Equal(t, 1000, in.TargetRPS)
	assert.Equal(t, 10*time.Second, in.Hold)
	// AttemptedOps = samples 3 + failed 2 = 5
	assert.Equal(t, 5, in.AttemptedOps)
	assert.Equal(t, 2, in.FailedOps)
	assert.Equal(t, 1, in.Saturation)
	assert.Len(t, in.Latencies, 1)
	assert.Equal(t, "read-receipt", in.Latencies[0].Name)
	assert.Len(t, in.Latencies[0].Samples, 3)
	assert.Empty(t, in.Pending)
	assert.False(t, in.Inconclusive)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=loadgen`
Expected: FAIL — `readReceiptWorkload`, `buildReadReceiptInputs` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `tools/loadgen/maxrps_readreceipt.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// buildReadReceiptInputs maps a hold-window collector to the normalized step
// inputs. A single latency series ("read-receipt") gates the verdict; Pending
// is empty because read receipts are synchronous reads with no JetStream
// consumer (same as the history workload).
func buildReadReceiptInputs(targetRPS int, hold time.Duration, c *ReadReceiptCollector) rpsStepInputs {
	samples := c.Samples()
	failed := c.Failed()
	return rpsStepInputs{
		TargetRPS:    targetRPS,
		Hold:         hold,
		AttemptedOps: len(samples) + failed,
		FailedOps:    failed,
		Saturation:   c.Saturation(),
		Latencies: []seriesSamples{
			{Name: "read-receipt", Samples: samples},
		},
	}
}

// readReceiptWorkload drives the room-service read-receipt RPC at a given RPS.
// As with historyWorkload, the natsutil connection (*otelnats.Conn) and metrics
// server are captured by the cleanup closure, not stored on the struct.
type readReceiptWorkload struct {
	cfg            *config
	siteID         string
	targets        []readReceiptTarget
	seed           int64
	requestTimeout time.Duration
	metrics        *Metrics
	requester      ReadReceiptRequester
}

func (w *readReceiptWorkload) Label() string { return "read-receipt" }

// newReadReceiptWorkload wires NATS, the metrics server, the requester, and
// derives top-level read-receipt targets from the history fixtures. The
// returned cleanup shuts the metrics server and drains NATS.
func newReadReceiptWorkload(ctx context.Context, cfg *config, preset *HistoryPreset, seed int64, requestTimeout time.Duration) (*readReceiptWorkload, func(), error) {
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

	res := BuildHistoryFixtures(preset, seed, cfg.SiteID, time.Now().UTC())
	w := &readReceiptWorkload{
		cfg: cfg, siteID: cfg.SiteID, targets: deriveReadReceiptTargets(&res.Plan),
		seed: seed, requestTimeout: requestTimeout, metrics: metrics,
		requester: newNATSReadReceiptRequester(nc.NatsConn()),
	}
	cleanup := func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(shutCtx)
		cancel()
		_ = nc.Drain()
	}
	return w, cleanup, nil
}

func (w *readReceiptWorkload) newGenerator(collector *ReadReceiptCollector, targetRPS int) *ReadReceiptGenerator {
	return NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets: w.targets, SiteID: w.siteID, Rate: targetRPS,
		RequestTimeout: w.requestTimeout, Requester: w.requester,
		Collector: collector, MaxInFlight: w.cfg.MaxInFlight,
	}, w.seed)
}

// RunStep runs warmup (discarded) then hold (measured) as two sequential
// generator runs so the hold collector contains only hold-window data.
// Mirrors historyWorkload.RunStep.
func (w *readReceiptWorkload) RunStep(ctx context.Context, targetRPS int, warmup, hold time.Duration) (rpsStepInputs, error) {
	if warmup > 0 {
		warmCollector := NewReadReceiptCollector()
		if err := runReadReceiptFor(ctx, w.newGenerator(warmCollector, targetRPS), warmup); err != nil {
			return rpsStepInputs{}, err
		}
	}
	collector := NewReadReceiptCollector()
	if err := runReadReceiptFor(ctx, w.newGenerator(collector, targetRPS), hold); err != nil {
		return rpsStepInputs{}, err
	}
	time.Sleep(2 * time.Second) // drain trailing in-flight replies into the collector
	return buildReadReceiptInputs(targetRPS, hold, collector), nil
}

// runReadReceiptFor runs gen.Run in a goroutine for d (or until ctx cancels),
// then stops it. Mirrors history's runFor.
func runReadReceiptFor(ctx context.Context, gen *ReadReceiptGenerator, d time.Duration) error {
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

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/maxrps_readreceipt.go tools/loadgen/maxrps_readreceipt_test.go
git commit -m "feat(loadgen): add read-receipt max-rps workload adapter"
```

---

### Task 5: Reader seeding

**Files:**
- Create: `tools/loadgen/readreceipt_seed.go`
- Test: `tools/loadgen/readreceipt_seed_test.go`

- [ ] **Step 1: Write the failing test**

Create `tools/loadgen/readreceipt_seed_test.go`:

```go
package main

import (
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func subs(n int) []model.Subscription {
	out := make([]model.Subscription, n)
	for i := 0; i < n; i++ {
		out[i] = model.Subscription{ID: string(rune('a' + i))}
	}
	return out
}

func TestSelectReaders_Count(t *testing.T) {
	in := subs(10)
	got := selectReaders(in, 0.7, rand.New(rand.NewSource(1)))
	// ceil(0.7 * 10) = 7
	assert.Len(t, got, 7)
}

func TestSelectReaders_RatioOneSelectsAll(t *testing.T) {
	in := subs(5)
	got := selectReaders(in, 1.0, rand.New(rand.NewSource(1)))
	assert.Len(t, got, 5)
}

func TestSelectReaders_Deterministic(t *testing.T) {
	in := subs(20)
	a := selectReaders(in, 0.5, rand.New(rand.NewSource(99)))
	b := selectReaders(in, 0.5, rand.New(rand.NewSource(99)))
	assert.Equal(t, a, b)
}

func TestSelectReaders_EmptyAndTiny(t *testing.T) {
	assert.Empty(t, selectReaders(nil, 0.7, rand.New(rand.NewSource(1))))
	// ceil(0.7 * 1) = 1
	assert.Len(t, selectReaders(subs(1), 0.7, rand.New(rand.NewSource(1))), 1)
}

func TestLatestTopLevelByRoom(t *testing.T) {
	t0 := time.Unix(1000, 0).UTC()
	plan := &MessagePlan{Messages: []plannedMessage{
		{RoomID: "r1", MessageID: "m1", CreatedAt: t0, ThreadParentID: ""},
		{RoomID: "r1", MessageID: "m2", CreatedAt: t0.Add(time.Hour), ThreadParentID: ""},
		{RoomID: "r1", MessageID: "rep", CreatedAt: t0.Add(2 * time.Hour), ThreadParentID: "m1"}, // reply ignored
		{RoomID: "r2", MessageID: "m3", CreatedAt: t0.Add(30 * time.Minute), ThreadParentID: ""},
	}}
	got := latestTopLevelByRoom(plan)
	assert.Equal(t, t0.Add(time.Hour), got["r1"])
	assert.Equal(t, t0.Add(30*time.Minute), got["r2"])
	assert.NotContains(t, got, "rep")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=loadgen`
Expected: FAIL — `selectReaders`, `latestTopLevelByRoom` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `tools/loadgen/readreceipt_seed.go`:

```go
package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// selectReaders returns ceil(readRatio * len(in)) subscriptions chosen
// uniformly at random via rng. Deterministic for a fixed rng seed. A readRatio
// of 1.0 selects all; an empty input returns nil.
func selectReaders(in []model.Subscription, readRatio float64, rng *rand.Rand) []model.Subscription {
	if len(in) == 0 {
		return nil
	}
	k := int(math.Ceil(readRatio * float64(len(in))))
	if k > len(in) {
		k = len(in)
	}
	if k <= 0 {
		return nil
	}
	perm := rng.Perm(len(in))[:k]
	out := make([]model.Subscription, k)
	for i, idx := range perm {
		out[i] = in[idx]
	}
	return out
}

// latestTopLevelByRoom returns the newest top-level message CreatedAt per room.
// Thread replies (ThreadParentID != "") are ignored — read receipts target
// top-level messages, so the read floor only needs to cover those.
func latestTopLevelByRoom(plan *MessagePlan) map[string]time.Time {
	out := map[string]time.Time{}
	for i := range plan.Messages {
		m := &plan.Messages[i]
		if m.ThreadParentID != "" {
			continue
		}
		if t, ok := out[m.RoomID]; !ok || m.CreatedAt.After(t) {
			out[m.RoomID] = m.CreatedAt
		}
	}
	return out
}

// SeedReadReceiptState stamps lastSeenAt on a readRatio fraction of each room's
// subscribers so ListReadReceipts ($match lastSeenAt >= message.createdAt)
// matches real documents and exercises the $lookup/$unwind path. lastSeenAt is
// set to the room's latest top-level message CreatedAt + 1ms so it covers every
// targetable message in the room. Selection is deterministic on `seed`.
func SeedReadReceiptState(ctx context.Context, db *mongo.Database, subs []model.Subscription, plan *MessagePlan, readRatio float64, seed int64) error {
	latest := latestTopLevelByRoom(plan)

	// Group subscriptions by room.
	byRoom := map[string][]model.Subscription{}
	for i := range subs {
		byRoom[subs[i].RoomID] = append(byRoom[subs[i].RoomID], subs[i])
	}

	rng := rand.New(rand.NewSource(seed))
	coll := db.Collection("subscriptions")
	for roomID, roomSubs := range byRoom {
		floor, ok := latest[roomID]
		if !ok {
			continue // room has no top-level messages — nothing to read
		}
		lastSeen := floor.Add(time.Millisecond).UTC()
		chosen := selectReaders(roomSubs, readRatio, rng)
		if len(chosen) == 0 {
			continue
		}
		ids := make([]string, len(chosen))
		for i := range chosen {
			ids[i] = chosen[i].ID
		}
		if _, err := coll.UpdateMany(ctx,
			bson.M{"_id": bson.M{"$in": ids}},
			bson.M{"$set": bson.M{"lastSeenAt": lastSeen}},
		); err != nil {
			return fmt.Errorf("stamp lastSeenAt for room %q: %w", roomID, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=loadgen`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/readreceipt_seed.go tools/loadgen/readreceipt_seed_test.go
git commit -m "feat(loadgen): add read-receipt reader-state seeding"
```

---

### Task 6: Wire the seed subcommand

**Files:**
- Modify: `tools/loadgen/main.go` (`runSeed`, add `runSeedReadReceipt`, usage strings)

- [ ] **Step 1: Add the `--read-ratio` flag and dispatch case to `runSeed`**

In `tools/loadgen/main.go`, replace the `runSeed` function body's flag block and switch. Find:

```go
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	workload := fs.String("workload", "messages", "messages|members|history")
	preset := fs.String("preset", "", "preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	_ = fs.Parse(args)
```

Replace with:

```go
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	workload := fs.String("workload", "messages", "messages|members|history|read-receipt")
	preset := fs.String("preset", "", "preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	readRatio := fs.Float64("read-ratio", 0.7, "read-receipt only: fraction of each room's subscribers to mark as readers")
	_ = fs.Parse(args)
```

Then find:

```go
	case "history":
		return runSeedHistory(ctx, cfg, *preset, *seed)
	default:
```

Replace with:

```go
	case "history":
		return runSeedHistory(ctx, cfg, *preset, *seed)
	case "read-receipt":
		return runSeedReadReceipt(ctx, cfg, *preset, *seed, *readRatio)
	default:
```

- [ ] **Step 2: Add `runSeedReadReceipt` to `history_main.go`**

`runSeedReadReceipt` runs the full history seed then stamps reader state. Place it in `tools/loadgen/history_main.go` (next to `runSeedHistory`, which it composes). Add this function:

```go
// runSeedReadReceipt seeds the same Mongo+Cassandra fixtures as the history
// workload, then stamps lastSeenAt on a readRatio fraction of each room's
// subscribers so the read-receipt RPC's ListReadReceipts query returns real
// readers. readRatio must be in (0, 1].
func runSeedReadReceipt(ctx context.Context, cfg *config, preset string, seed int64, readRatio float64) int {
	if readRatio <= 0 || readRatio > 1 {
		fmt.Fprintln(os.Stderr, "--read-ratio must be in (0, 1]")
		return 2
	}
	if cfg.CassandraHosts == "" {
		fmt.Fprintln(os.Stderr, "read-receipt workload requires CASSANDRA_HOSTS")
		return 2
	}
	p, ok := BuiltinHistoryPreset(preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown history preset: %s\n", preset)
		return 2
	}

	db, keyStore, cleanup, err := connectStores(ctx, cfg)
	if err != nil {
		return 1
	}
	defer cleanup()

	session, err := connectCassandra(cfg)
	if err != nil {
		slog.Error("cassandra connect", "error", err)
		return 1
	}
	defer cassutil.Close(session)

	now := time.Now().UTC()
	res := BuildHistoryFixtures(&p, seed, cfg.SiteID, now)

	if err := Seed(ctx, db, &res.Fixtures); err != nil {
		slog.Error("seed mongo fixtures", "error", err)
		return 1
	}
	if err := SeedRoomKeys(ctx, keyStore, res.Fixtures.RoomKeys); err != nil {
		slog.Error("seed room keys", "error", err)
		return 1
	}
	if err := SeedThreadRooms(ctx, db, &res.Plan, cfg.SiteID); err != nil {
		slog.Error("seed thread rooms", "error", err)
		return 1
	}
	sizer := msgbucket.New(time.Duration(cfg.MessageBucketHours) * time.Hour)
	if err := SeedHistoryCassandra(ctx, session, sizer, &res.Plan, cfg.SiteID); err != nil {
		slog.Error("seed cassandra messages", "error", err)
		return 1
	}
	if err := SeedReadReceiptState(ctx, db, res.Fixtures.Subscriptions, &res.Plan, readRatio, seed); err != nil {
		slog.Error("seed read-receipt reader state", "error", err)
		return 1
	}

	slog.Info("seed complete (read-receipt)",
		"preset", p.Name,
		"users", len(res.Fixtures.Users),
		"rooms", len(res.Fixtures.Rooms),
		"subs", len(res.Fixtures.Subscriptions),
		"messages", len(res.Plan.Messages),
		"readRatio", readRatio,
		"bucketHours", cfg.MessageBucketHours)
	return 0
}
```

- [ ] **Step 3: Update the top-level usage string**

In `tools/loadgen/main.go`, find:

```go
		fmt.Fprintln(os.Stderr, "usage: loadgen <seed|run|teardown|members-sustained|members-capacity|history-sustained|max-rps> [flags]")
```

This line lists subcommands, not seed workloads, so it stays unchanged. No edit needed in this step — verify the line still reads as above and move on.

- [ ] **Step 4: Verify compilation**

Run: `make build SERVICE=loadgen`
Expected: builds cleanly.

- [ ] **Step 5: Commit**

```bash
git add tools/loadgen/main.go tools/loadgen/history_main.go
git commit -m "feat(loadgen): wire read-receipt seed subcommand"
```

---

### Task 7: Wire the max-rps workload

**Files:**
- Modify: `tools/loadgen/maxrps.go` (`defaultSteps`, `--workload` help, `runMaxRPS` switch)

- [ ] **Step 1: Update `defaultSteps`**

In `tools/loadgen/maxrps.go`, find:

```go
func defaultSteps(workload string) string {
	if workload == "history" {
		return "200,500,1000,2000,5000"
	}
	return "500,1000,2000,5000,10000"
}
```

Replace with:

```go
func defaultSteps(workload string) string {
	if workload == "history" || workload == "read-receipt" {
		return "200,500,1000,2000,5000"
	}
	return "500,1000,2000,5000,10000"
}
```

- [ ] **Step 2: Update the `--workload` flag help**

In `runMaxRPS`, find:

```go
	workload := fs.String("workload", "messages", "messages|history")
```

Replace with:

```go
	workload := fs.String("workload", "messages", "messages|history|read-receipt")
```

- [ ] **Step 3: Add the `read-receipt` case to the workload switch**

In `runMaxRPS`, find the end of the `case "history":` block and the `default:` that follows it:

```go
		w, cleanup, presetID = hw, clean, p.Name
	default:
		fmt.Fprintf(os.Stderr, "unknown workload: %s\n", *workload)
		return 2
	}
```

Replace with:

```go
		w, cleanup, presetID = hw, clean, p.Name
	case "read-receipt":
		p, ok := BuiltinHistoryPreset(*preset)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown history preset: %s\n", *preset)
			return 2
		}
		if *requestTimeout <= 0 {
			fmt.Fprintln(os.Stderr, "--request-timeout must be > 0")
			return 2
		}
		rw, clean, err := newReadReceiptWorkload(ctx, cfg, &p, *seed, *requestTimeout)
		if err != nil {
			slog.Error("init read-receipt workload", "error", err)
			return 1
		}
		w, cleanup, presetID = rw, clean, p.Name
	default:
		fmt.Fprintf(os.Stderr, "unknown workload: %s\n", *workload)
		return 2
	}
```

Note: `*requestTimeout`, `*seed`, `*preset`, `cfg`, and `ctx` are all already in scope in `runMaxRPS` (the `--request-timeout` flag currently labelled "history only" is reused; its help text may stay as-is — it is harmless).

- [ ] **Step 4: Verify compilation**

Run: `make build SERVICE=loadgen`
Expected: builds cleanly.

- [ ] **Step 5: Run the full loadgen unit suite**

Run: `make test SERVICE=loadgen`
Expected: PASS (all tasks 1–7).

- [ ] **Step 6: Commit**

```bash
git add tools/loadgen/maxrps.go
git commit -m "feat(loadgen): add read-receipt workload to max-rps command"
```

---

### Task 8: Integration test

**Files:**
- Create: `tools/loadgen/readreceipt_integration_test.go`

This mirrors `history_integration_test.go`'s plumbing test: real Mongo seed + `SeedReadReceiptState`, a stub NATS responder, and a short generator run. It asserts (a) `SeedReadReceiptState` stamped `lastSeenAt` on the expected number of subscriptions, and (b) the generator round-trips and records samples.

- [ ] **Step 1: Write the integration test**

Create `tools/loadgen/readreceipt_integration_test.go`:

```go
//go:build integration

package main

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

// TestReadReceiptWorkload_EndToEnd seeds Mongo fixtures + reader state, stands a
// stub read-receipt responder, and drives the generator briefly. It verifies
// the seed stamped lastSeenAt and the generator round-trips successfully.
func TestReadReceiptWorkload_EndToEnd(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "loadgen_readreceipt")

	preset, ok := BuiltinHistoryPreset("history-small")
	require.True(t, ok)
	siteID := "site-test"
	const seed = int64(42)
	const readRatio = 0.7

	res := BuildHistoryFixtures(&preset, seed, siteID, time.Now().UTC())
	require.NoError(t, Seed(ctx, db, &res.Fixtures))
	require.NoError(t, SeedReadReceiptState(ctx, db, res.Fixtures.Subscriptions, &res.Plan, readRatio, seed))

	// Expected stamped count: ceil(readRatio*roomSize) per room that has a
	// top-level message. history-small seeds BaselineSize members per room.
	latest := latestTopLevelByRoom(&res.Plan)
	subsByRoom := map[string]int{}
	for i := range res.Fixtures.Subscriptions {
		subsByRoom[res.Fixtures.Subscriptions[i].RoomID]++
	}
	expectedStamped := 0
	for roomID, n := range subsByRoom {
		if _, has := latest[roomID]; has {
			expectedStamped += int(math.Ceil(readRatio * float64(n)))
		}
	}
	stamped, err := db.Collection("subscriptions").CountDocuments(ctx,
		bson.M{"lastSeenAt": bson.M{"$exists": true}})
	require.NoError(t, err)
	assert.Equal(t, int64(expectedStamped), stamped, "stamped lastSeenAt count")

	// Stub responder mirroring room-service's read-receipt subject layout.
	nc, err := nats.Connect(testutil.NATS(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = nc.Drain() })

	sub, err := nc.Subscribe(subject.MessageReadReceiptWildcard(siteID), func(m *nats.Msg) {
		_ = m.Respond([]byte(`{"readers":[]}`))
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Drive the generator briefly.
	collector := NewReadReceiptCollector()
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:        deriveReadReceiptTargets(&res.Plan),
		SiteID:         siteID,
		Rate:           50,
		RequestTimeout: 2 * time.Second,
		Requester:      newNATSReadReceiptRequester(nc),
		Collector:      collector,
		MaxInFlight:    16,
	}, seed)

	runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))
	time.Sleep(500 * time.Millisecond) // drain trailing replies

	assert.NotEmpty(t, collector.Samples(), "generator produced zero samples")
	assert.Equal(t, 0, collector.Failed(), "stub responder should yield zero failures")
}
```

- [ ] **Step 2: Run the integration test**

Run: `make test-integration SERVICE=loadgen`
Expected: PASS. (Requires Docker for `pkg/testutil` Mongo + NATS containers. There is already a `TestMain` in the loadgen package's integration build — confirm `history_integration_test.go` or a sibling provides `func TestMain(m *testing.M) { testutil.RunTests(m) }`; if the package has one, do not add a second.)

- [ ] **Step 3: Commit**

```bash
git add tools/loadgen/readreceipt_integration_test.go
git commit -m "test(loadgen): add read-receipt workload integration test"
```

---

### Task 9: Documentation

**Files:**
- Modify: `tools/loadgen/README.md`

- [ ] **Step 1: Read the README's existing max-rps + seed sections**

Run: `grep -n "max-rps\|seed --workload\|history" tools/loadgen/README.md`
Read those sections to match the existing format and heading style.

- [ ] **Step 2: Add read-receipt documentation**

Add a subsection documenting the new workload, modeled on the existing history sections. Include:

````markdown
### Read-receipt workload (`max-rps --workload read-receipt`)

Drives the room-service read-receipt RPC
(`chat.user.{account}.request.room.{roomID}.{siteID}.message.read-receipt`) — a
synchronous request/reply read ("who has read message X") — at increasing RPS
steps to find the maximum sustainable rate under the latency/error SLOs.

Read receipts reuse the **history** seed: the requester for each target is the
message's sender (the RPC requires `msgSender == requesterAccount`), and only
top-level messages are used as targets. Reader state must be seeded so the
`ListReadReceipts` Mongo query exercises its real `$match`/`$lookup`/`$unwind`
path instead of short-circuiting on an empty `lastSeenAt` match.

Seed (stamps `lastSeenAt` on a `--read-ratio` fraction of each room's
subscribers; default 0.7):

```bash
loadgen seed --workload read-receipt --preset history-small --read-ratio 0.7
```

Run the ramp:

```bash
loadgen max-rps --workload read-receipt --preset history-small \
  --steps 200,500,1k,2k,5k --slo-p95 100ms --slo-p99 250ms
```

`--slo-pending-growth` is ignored (no JetStream consumer, same as history).
The per-request timeout is set with `--request-timeout`.
````

- [ ] **Step 3: Commit**

```bash
git add tools/loadgen/README.md
git commit -m "docs(loadgen): document read-receipt max-rps workload"
```

---

### Task 10: Final verification

- [ ] **Step 1: Format**

Run: `make fmt`

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: no findings in the new files. Fix any reported issues.

- [ ] **Step 3: Full unit suite with race detector**

Run: `make test SERVICE=loadgen`
Expected: PASS.

- [ ] **Step 4: SAST**

Run: `make sast`
Expected: no medium+ findings introduced by the new files. (The `TRUNCATE`/`fmt.Sprintf` query patterns live in pre-existing seed code, not the new files.)

- [ ] **Step 5: Commit any formatting/lint fixups**

```bash
git add -A
git commit -m "chore(loadgen): fmt + lint fixups for read-receipt workload"
```

---

## Self-review notes

- **Spec coverage:** §Architecture → Tasks 4, 7. §Components table (5 files) → Tasks 1 (collector), 2 (requester), 3 (generator+targets), 4 (workload+inputs), 5 (seed). §Data flow / reader seeding → Task 5 + Task 6. §RunStep behavior → Task 4. §Error handling (classification, saturation, read-ratio validation) → Tasks 3, 6. §Testing (unit + integration) → Tasks 1,3,4,5,8. §Documentation → Task 9. §Out of scope items are not implemented.
- **Naming consistency:** `ReadReceiptCollector`/`NewReadReceiptCollector`, `ReadReceiptRequester`/`newNATSReadReceiptRequester`, `ReadReceiptGenerator`/`ReadReceiptGeneratorConfig`/`NewReadReceiptGenerator`, `readReceiptTarget`/`deriveReadReceiptTargets`, `readReceiptWorkload`/`newReadReceiptWorkload`, `buildReadReceiptInputs`, `selectReaders`, `latestTopLevelByRoom`, `SeedReadReceiptState`, `runSeedReadReceipt` — used identically across all tasks that reference them.
- **Reused symbols verified present:** `errClass*`/`classifyRequesterError` (history_generator.go/history_collector.go), `drainGracePeriod` (generator.go), `waitOrCancel`/`rpsStepInputs`/`seriesSamples` (ramp.go/verdict.go), `BuildHistoryFixtures`/`BuiltinHistoryPreset`/`Seed`/`SeedRoomKeys`/`SeedThreadRooms`/`SeedHistoryCassandra`/`connectStores`/`connectCassandra` (history files), `subject.MessageReadReceipt`/`MessageReadReceiptWildcard`, `model.ReadReceiptRequest`.
- **Determinism caveat documented:** message IDs are `hmsg-<roomID>-<index>` (independent of the `now` anchor), so the workload's run-time `BuildHistoryFixtures` produces the same IDs the seed wrote; the handler reads the real `createdAt` from Cassandra, so seed-time and run-time `now` differing is harmless.
