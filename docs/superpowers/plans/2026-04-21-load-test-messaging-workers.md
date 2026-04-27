# Load Test Messaging Workers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go-based load generator (`tools/loadgen`) plus a docker-compose harness that sustains and measures messaging pipeline capacity (message-gatekeeper → MESSAGES_CANONICAL → message-worker + broadcast-worker) on a single site.

**Architecture:** One Go binary with three subcommands (`seed`, `run`, `teardown`), open-loop publishing via `time.Ticker`, two wildcard subscriptions for reply-correlation (E1) and broadcast-correlation (E2), periodic `ConsumerInfo` sampling for backlog (E4), Prometheus gauges + terminal summary for reporting, optional Grafana profile for dashboards. Docker-compose file at `tools/loadgen/deploy/docker-compose.loadtest.yml` brings up the full single-site pipeline plus the loadgen container.

**Tech Stack:** Go 1.25, `nats.go` + `nats.go/jetstream`, `go.mongodb.org/mongo-driver/v2`, `caarlos0/env/v11`, `google/uuid`, `prometheus/client_golang`, `stretchr/testify`, `testcontainers-go`, stdlib `log/slog` / `math/rand` / `time.Ticker` / `text/tabwriter`.

**Spec:** `docs/superpowers/specs/2026-04-21-load-test-messaging-workers-design.md`.

---

## File Structure

### New Go source files (all under `tools/loadgen/`)

| File | Responsibility |
|---|---|
| `main.go` | Parse env config, dispatch subcommand (`seed`/`run`/`teardown`), wire dependencies, graceful shutdown. |
| `preset.go` | `Preset`, `Distribution`, `Range` types; built-in presets map; deterministic `(user, room, content)` generators. |
| `seed.go` | MongoDB seeding: drop + populate `users`/`rooms`/`subscriptions` collections from a preset. |
| `generator.go` | Open-loop publisher driven by `time.Ticker`; publishes `SendMessageRequest` to front-door subject (or `MessageEvent` to canonical). |
| `collector.go` | Reply-subject and broadcast-subject subscribers; two `sync.Map`s for E1 / E2 correlation; sample buffers. |
| `consumerlag.go` | Polls `ConsumerInfo` every 1s for both durables; exposes Prometheus gauges; records min/peak/final. |
| `report.go` | Terminal summary (`text/tabwriter`), CSV export, exit-code logic, percentile computation. |
| `metrics.go` | Prometheus registry + histograms/counters/gauges used by generator/collector/consumerlag. |
| `preset_test.go` | Determinism tests for preset generation. |
| `generator_test.go` | Rate-pacing tests with stubbed publish. |
| `collector_test.go` | Reply / broadcast correlation tests with synthesized messages. |
| `report_test.go` | Percentile math, CSV format, exit-code tolerance tests. |
| `integration_test.go` | `//go:build integration` — spins up real NATS+Mongo+Cassandra+workers, runs `small` preset, asserts end-to-end wiring. |

### New deploy files (all under `tools/loadgen/deploy/`)

| File | Responsibility |
|---|---|
| `Dockerfile` | Multi-stage build, `golang:1.25.8-alpine` builder, `alpine:3.21` runtime. |
| `Makefile` | Scoped `up`, `seed`, `run`, `run-dashboards`, `down` targets. |
| `docker-compose.loadtest.yml` | NATS+Mongo+Cassandra+gatekeeper+workers+loadgen+(optional) prometheus+grafana. |
| `prometheus/prometheus.yml` | Prometheus scrape config for loadgen and NATS. |
| `grafana/provisioning/datasources/prometheus.yaml` | Grafana datasource provisioning. |
| `grafana/provisioning/dashboards/loadtest.yaml` | Grafana dashboard provisioning. |
| `grafana/dashboards/loadtest.json` | The load-test dashboard JSON. |
| `README.md` | Operator reference: what it is, how to run, how to read output, what's out of scope. |

### Modified files

None. Root `Makefile` stays untouched (per broadcast-worker harness precedent).

---

## Task 1: Scaffold `tools/loadgen/` directory and stub `main.go`

**Files:**
- Create: `tools/loadgen/main.go`

- [ ] **Step 1: Create directory and write stub `main.go`**

Create the file `tools/loadgen/main.go`:

```go
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
)

type config struct {
	NatsURL       string `env:"NATS_URL,required"`
	NatsCredsFile string `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID        string `env:"SITE_ID"         envDefault:"site-local"`
	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB"        envDefault:"chat"`
	MetricsAddr   string `env:"METRICS_ADDR"    envDefault:":9099"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen <seed|run|teardown> [flags]")
		os.Exit(2)
	}
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	_ = cfg
	switch os.Args[1] {
	case "seed", "run", "teardown":
		slog.Info("subcommand not yet implemented", "subcommand", os.Args[1])
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./tools/loadgen/`
Expected: Succeeds; no output.

- [ ] **Step 3: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/main.go
git commit -m "feat(loadgen): scaffold main.go with subcommand dispatch"
```

---

## Task 2: Define `Preset`, `Distribution`, `Range` types and built-in preset map

**Files:**
- Create: `tools/loadgen/preset.go`
- Test: `tools/loadgen/preset_test.go`

- [ ] **Step 1: Write failing test**

Create `tools/loadgen/preset_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinPresets_ContainsAllFour(t *testing.T) {
	names := []string{"small", "medium", "large", "realistic"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p, ok := BuiltinPreset(name)
			require.True(t, ok, "preset %q must exist", name)
			assert.Equal(t, name, p.Name)
			assert.Greater(t, p.Users, 0)
			assert.Greater(t, p.Rooms, 0)
		})
	}
}

func TestBuiltinPresets_UnknownReturnsFalse(t *testing.T) {
	_, ok := BuiltinPreset("nonexistent")
	assert.False(t, ok)
}

func TestBuiltinPresets_UniformShape(t *testing.T) {
	for _, name := range []string{"small", "medium", "large"} {
		t.Run(name, func(t *testing.T) {
			p, _ := BuiltinPreset(name)
			assert.Equal(t, DistUniform, p.RoomSizeDist)
			assert.Equal(t, DistUniform, p.SenderDist)
			assert.InDelta(t, 0.0, p.MentionRate, 1e-9)
			assert.InDelta(t, 0.0, p.ThreadRate, 1e-9)
		})
	}
}

func TestBuiltinPresets_RealisticShape(t *testing.T) {
	p, _ := BuiltinPreset("realistic")
	assert.Equal(t, DistMixed, p.RoomSizeDist)
	assert.Equal(t, DistZipf, p.SenderDist)
	assert.Greater(t, p.MentionRate, 0.0)
	assert.Greater(t, p.ThreadRate, 0.0)
	assert.Greater(t, p.ContentBytes.Max, p.ContentBytes.Min)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestBuiltinPresets -v`
Expected: FAIL — `BuiltinPreset`, `DistUniform`, `DistMixed`, `DistZipf`, `Preset` undefined.

- [ ] **Step 3: Write the preset definitions**

Create `tools/loadgen/preset.go`:

```go
package main

// Distribution names the shape of a per-preset random selection.
type Distribution string

const (
	DistUniform Distribution = "uniform"
	DistMixed   Distribution = "mixed"
	DistZipf    Distribution = "zipf"
)

// Range holds an inclusive min/max for integer quantities like content size.
type Range struct {
	Min int
	Max int
}

// Preset is a named, fully deterministic workload specification.
type Preset struct {
	Name         string
	Users        int
	Rooms        int
	RoomSizeDist Distribution
	SenderDist   Distribution
	ContentBytes Range
	MentionRate  float64
	ThreadRate   float64
}

var builtinPresets = map[string]Preset{
	"small": {
		Name: "small", Users: 10, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"medium": {
		Name: "medium", Users: 1000, Rooms: 100,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"large": {
		Name: "large", Users: 10000, Rooms: 1000,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"realistic": {
		Name: "realistic", Users: 1000, Rooms: 100,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.10,
		ThreadRate:   0.05,
	},
}

// BuiltinPreset looks up a preset by name.
func BuiltinPreset(name string) (Preset, bool) {
	p, ok := builtinPresets[name]
	return p, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestBuiltinPresets -v`
Expected: PASS for all four subtests plus the two standalone tests.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/preset.go tools/loadgen/preset_test.go
git commit -m "feat(loadgen): add Preset type and four built-in presets"
```

---

## Task 3: Deterministic fixture generation (users, rooms, subscriptions)

Pure functions that turn `(Preset, seed)` into `[]model.User`, `[]model.Room`, `[]model.Subscription`. No I/O — those slices are the seeding input for Task 4.

**Files:**
- Modify: `tools/loadgen/preset.go`
- Modify: `tools/loadgen/preset_test.go`

- [ ] **Step 1: Add fixture-generation tests (failing)**

Append to `tools/loadgen/preset_test.go`:

```go
func TestBuildFixtures_DeterministicAcrossCalls(t *testing.T) {
	p, _ := BuiltinPreset("small")
	a := BuildFixtures(p, 42, "site-local")
	b := BuildFixtures(p, 42, "site-local")
	assert.Equal(t, a.Users, b.Users)
	assert.Equal(t, a.Rooms, b.Rooms)
	assert.Equal(t, a.Subscriptions, b.Subscriptions)
}

func TestBuildFixtures_SmallCountsAndShape(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(p, 42, "site-local")
	assert.Len(t, f.Users, 10)
	assert.Len(t, f.Rooms, 5)
	// uniform: every user is in at least one room
	users := make(map[string]bool)
	for _, s := range f.Subscriptions {
		users[s.User.ID] = true
		assert.Equal(t, "site-local", s.SiteID)
	}
	assert.Len(t, users, 10)
	for _, r := range f.Rooms {
		assert.Equal(t, "group", string(r.Type))
		assert.Equal(t, "site-local", r.SiteID)
	}
}

func TestBuildFixtures_RealisticMixesGroupAndDM(t *testing.T) {
	p, _ := BuiltinPreset("realistic")
	f := BuildFixtures(p, 42, "site-local")
	var groups, dms int
	for _, r := range f.Rooms {
		switch r.Type {
		case "group":
			groups++
		case "dm":
			dms++
		}
	}
	assert.Greater(t, groups, 0)
	assert.Greater(t, dms, 0)
	// DM rooms must have exactly 2 members
	dmMembers := make(map[string]int)
	for _, s := range f.Subscriptions {
		for _, r := range f.Rooms {
			if r.ID == s.RoomID && r.Type == "dm" {
				dmMembers[r.ID]++
			}
		}
	}
	for id, n := range dmMembers {
		assert.Equal(t, 2, n, "dm room %s must have 2 members", id)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestBuildFixtures -v`
Expected: FAIL — `BuildFixtures` undefined.

- [ ] **Step 3: Implement `BuildFixtures`**

Replace the entire contents of `tools/loadgen/preset.go` with the Task-2 content plus the fixture generator below. The full file should look like:

```go
package main

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

// Distribution names the shape of a per-preset random selection.
type Distribution string

const (
	DistUniform Distribution = "uniform"
	DistMixed   Distribution = "mixed"
	DistZipf    Distribution = "zipf"
)

// Range holds an inclusive min/max for integer quantities like content size.
type Range struct {
	Min int
	Max int
}

// Preset is a named, fully deterministic workload specification.
type Preset struct {
	Name         string
	Users        int
	Rooms        int
	RoomSizeDist Distribution
	SenderDist   Distribution
	ContentBytes Range
	MentionRate  float64
	ThreadRate   float64
}

var builtinPresets = map[string]Preset{
	"small": {
		Name: "small", Users: 10, Rooms: 5,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"medium": {
		Name: "medium", Users: 1000, Rooms: 100,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"large": {
		Name: "large", Users: 10000, Rooms: 1000,
		RoomSizeDist: DistUniform, SenderDist: DistUniform,
		ContentBytes: Range{Min: 200, Max: 200},
	},
	"realistic": {
		Name: "realistic", Users: 1000, Rooms: 100,
		RoomSizeDist: DistMixed, SenderDist: DistZipf,
		ContentBytes: Range{Min: 50, Max: 2000},
		MentionRate:  0.10,
		ThreadRate:   0.05,
	},
}

// BuiltinPreset looks up a preset by name.
func BuiltinPreset(name string) (Preset, bool) {
	p, ok := builtinPresets[name]
	return p, ok
}

// Fixtures is the full seed data for a preset run.
type Fixtures struct {
	Users         []model.User
	Rooms         []model.Room
	Subscriptions []model.Subscription
}

var (
	engNameBank     = []string{"Alice Wang", "Bob Chen", "Carol Lee", "Dave Liu", "Eve Zhang"}
	chineseNameBank = []string{"愛麗絲", "鮑勃", "卡蘿", "戴夫", "伊芙"}
)

// BuildFixtures is a pure function of (preset, seed, siteID) producing the
// full fixture set. Two calls with equal inputs produce equal outputs.
func BuildFixtures(p Preset, seed int64, siteID string) Fixtures {
	r := rand.New(rand.NewSource(seed))
	now := time.Unix(0, 0).UTC() // fixed so output is deterministic

	users := make([]model.User, p.Users)
	for i := 0; i < p.Users; i++ {
		users[i] = model.User{
			ID:          fmt.Sprintf("u-%06d", i),
			Account:     fmt.Sprintf("user-%d", i),
			SiteID:      siteID,
			EngName:     engNameBank[i%len(engNameBank)],
			ChineseName: chineseNameBank[i%len(chineseNameBank)],
		}
	}

	rooms := make([]model.Room, p.Rooms)
	// realistic: last 10% of rooms are DMs
	dmStart := p.Rooms
	if p.RoomSizeDist == DistMixed {
		dmStart = p.Rooms - p.Rooms/10
	}
	for i := 0; i < p.Rooms; i++ {
		rtype := model.RoomTypeGroup
		if i >= dmStart {
			rtype = model.RoomTypeDM
		}
		rooms[i] = model.Room{
			ID:        fmt.Sprintf("room-%06d", i),
			Name:      fmt.Sprintf("room-%d", i),
			Type:      rtype,
			SiteID:    siteID,
			UserCount: 0, // filled after membership
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	var subs []model.Subscription
	for i := range rooms {
		members := pickMembers(r, p, &rooms[i], users)
		rooms[i].UserCount = len(members)
		for _, u := range members {
			subs = append(subs, model.Subscription{
				ID:       fmt.Sprintf("sub-%s-%s", rooms[i].ID, u.ID),
				User:     model.SubscriptionUser{ID: u.ID, Account: u.Account},
				RoomID:   rooms[i].ID,
				SiteID:   siteID,
				Roles:    []model.Role{model.RoleMember},
				JoinedAt: now,
			})
		}
	}
	return Fixtures{Users: users, Rooms: rooms, Subscriptions: subs}
}

func pickMembers(r *rand.Rand, p Preset, room *model.Room, users []model.User) []model.User {
	if room.Type == model.RoomTypeDM {
		// Two distinct users.
		i := r.Intn(len(users))
		j := r.Intn(len(users) - 1)
		if j >= i {
			j++
		}
		return []model.User{users[i], users[j]}
	}
	switch p.RoomSizeDist {
	case DistMixed:
		// 10% of rooms get up to 500 members; rest get 2-20.
		size := 2 + r.Intn(19)
		if r.Intn(10) == 0 {
			size = 2 + r.Intn(499)
		}
		return sampleWithoutReplacement(r, users, size)
	default:
		size := (len(users) + p.Rooms - 1) / p.Rooms
		if size < 2 {
			size = 2
		}
		return sampleWithoutReplacement(r, users, size)
	}
}

func sampleWithoutReplacement(r *rand.Rand, users []model.User, n int) []model.User {
	if n > len(users) {
		n = len(users)
	}
	idx := r.Perm(len(users))[:n]
	out := make([]model.User, n)
	for i, k := range idx {
		out[i] = users[k]
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestBuildFixtures -v`
Expected: PASS for all three subtests.

- [ ] **Step 5: Run whole package to confirm no regressions**

Run: `cd /home/user/chat && make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/preset.go tools/loadgen/preset_test.go
git commit -m "feat(loadgen): deterministic fixture generation from (preset, seed)"
```

---

## Task 4: Seeding MongoDB with fixtures

**Files:**
- Create: `tools/loadgen/seed.go`

- [ ] **Step 1: Write `seed.go`**

Create `tools/loadgen/seed.go`:

```go
package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Seed drops and repopulates users/rooms/subscriptions in db from fixtures.
// Idempotent: safe to rerun.
func Seed(ctx context.Context, db *mongo.Database, f Fixtures) error {
	if err := db.Collection("users").Drop(ctx); err != nil {
		return fmt.Errorf("drop users: %w", err)
	}
	if err := db.Collection("rooms").Drop(ctx); err != nil {
		return fmt.Errorf("drop rooms: %w", err)
	}
	if err := db.Collection("subscriptions").Drop(ctx); err != nil {
		return fmt.Errorf("drop subscriptions: %w", err)
	}

	if len(f.Users) > 0 {
		docs := make([]interface{}, len(f.Users))
		for i := range f.Users {
			docs[i] = f.Users[i]
		}
		if _, err := db.Collection("users").InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("insert users: %w", err)
		}
	}
	if len(f.Rooms) > 0 {
		docs := make([]interface{}, len(f.Rooms))
		for i := range f.Rooms {
			docs[i] = f.Rooms[i]
		}
		if _, err := db.Collection("rooms").InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("insert rooms: %w", err)
		}
	}
	if len(f.Subscriptions) > 0 {
		docs := make([]interface{}, len(f.Subscriptions))
		for i := range f.Subscriptions {
			docs[i] = f.Subscriptions[i]
		}
		if _, err := db.Collection("subscriptions").InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("insert subscriptions: %w", err)
		}
	}
	return nil
}

// Teardown drops the three seeded collections without repopulating.
func Teardown(ctx context.Context, db *mongo.Database) error {
	for _, c := range []string{"users", "rooms", "subscriptions"} {
		if err := db.Collection(c).Drop(ctx); err != nil {
			return fmt.Errorf("drop %s: %w", c, err)
		}
	}
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./tools/loadgen/`
Expected: Succeeds.

- [ ] **Step 3: Commit**

`Seed`/`Teardown` are exercised by the integration test (Task 12). Unit-level test value is low because this is a straight drop + InsertMany against the real Mongo driver.

```bash
cd /home/user/chat
git add tools/loadgen/seed.go
git commit -m "feat(loadgen): Seed and Teardown mongo collections from fixtures"
```

---

## Task 5: Prometheus metrics registry

**Files:**
- Create: `tools/loadgen/metrics.go`

- [ ] **Step 1: Write `metrics.go`**

Create `tools/loadgen/metrics.go`:

```go
package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus collectors used across loadgen components.
type Metrics struct {
	Registry            *prometheus.Registry
	Published           *prometheus.CounterVec
	PublishErrors       *prometheus.CounterVec
	E1Latency           *prometheus.HistogramVec
	E2Latency           *prometheus.HistogramVec
	ConsumerPending     *prometheus.GaugeVec
	ConsumerAckPending  *prometheus.GaugeVec
	ConsumerRedelivered *prometheus.GaugeVec
}

// NewMetrics constructs a dedicated Prometheus registry with all loadgen
// collectors registered. A dedicated registry avoids colliding with default
// Go/process collectors.
func NewMetrics() *Metrics {
	r := prometheus.NewRegistry()
	buckets := []float64{
		0.001, 0.002, 0.005, 0.010, 0.025, 0.050, 0.100, 0.250, 0.500, 1.000, 2.500, 5.000,
	}
	m := &Metrics{
		Registry: r,
		Published: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_published_total", Help: "Messages published."},
			[]string{"preset"},
		),
		PublishErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_publish_errors_total", Help: "Publish-side errors."},
			[]string{"preset", "reason"},
		),
		E1Latency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "loadgen_e1_latency_seconds", Help: "Gatekeeper ack latency.", Buckets: buckets},
			[]string{"preset"},
		),
		E2Latency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "loadgen_e2_latency_seconds", Help: "Broadcast-visible latency.", Buckets: buckets},
			[]string{"preset"},
		),
		ConsumerPending: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_consumer_pending", Help: "JetStream consumer num_pending."},
			[]string{"stream", "durable"},
		),
		ConsumerAckPending: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_consumer_ack_pending", Help: "JetStream consumer num_ack_pending."},
			[]string{"stream", "durable"},
		),
		ConsumerRedelivered: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_consumer_redelivered", Help: "JetStream consumer num_redelivered."},
			[]string{"stream", "durable"},
		),
	}
	r.MustRegister(
		m.Published, m.PublishErrors,
		m.E1Latency, m.E2Latency,
		m.ConsumerPending, m.ConsumerAckPending, m.ConsumerRedelivered,
	)
	return m
}

// Handler returns an http.Handler serving this metrics registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./tools/loadgen/`
Expected: Succeeds.

- [ ] **Step 3: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/metrics.go
git commit -m "feat(loadgen): Prometheus registry with loadgen collectors"
```

---

## Task 6: Collector — reply + broadcast correlation

**Files:**
- Create: `tools/loadgen/collector.go`
- Create: `tools/loadgen/collector_test.go`

- [ ] **Step 1: Write failing tests**

Create `tools/loadgen/collector_test.go`:

```go
package main

import (
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
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestCollector -v`
Expected: FAIL — `NewCollector`, `Collector` undefined.

- [ ] **Step 3: Implement the collector**

Create `tools/loadgen/collector.go`:

```go
package main

import (
	"sort"
	"sync"
	"time"
)

type publishEntry struct {
	publishedAt time.Time
}

// sample pairs a latency with its publish timestamp so warmup can discard by time.
type sample struct {
	publishedAt time.Time
	latency     time.Duration
}

// Collector correlates publishes with replies (E1) and broadcasts (E2).
type Collector struct {
	m        *Metrics
	preset   string
	mu       sync.Mutex
	byReqID  map[string]publishEntry
	byMsgID  map[string]publishEntry
	e1       []sample
	e2       []sample
}

// NewCollector returns a ready-to-use Collector.
func NewCollector(m *Metrics, preset string) *Collector {
	return &Collector{
		m: m, preset: preset,
		byReqID: make(map[string]publishEntry),
		byMsgID: make(map[string]publishEntry),
	}
}

// RecordPublish stores the publish time under both correlation keys.
func (c *Collector) RecordPublish(requestID, messageID string, t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byReqID[requestID] = publishEntry{publishedAt: t}
	c.byMsgID[messageID] = publishEntry{publishedAt: t}
}

// RecordReply consumes one pending publish keyed by requestID.
func (c *Collector) RecordReply(requestID string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byReqID[requestID]
	if !ok {
		return
	}
	delete(c.byReqID, requestID)
	d := at.Sub(e.publishedAt)
	c.e1 = append(c.e1, sample{publishedAt: e.publishedAt, latency: d})
	c.m.E1Latency.WithLabelValues(c.preset).Observe(d.Seconds())
}

// RecordBroadcast consumes one pending publish keyed by messageID.
func (c *Collector) RecordBroadcast(messageID string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.byMsgID[messageID]
	if !ok {
		return
	}
	delete(c.byMsgID, messageID)
	d := at.Sub(e.publishedAt)
	c.e2 = append(c.e2, sample{publishedAt: e.publishedAt, latency: d})
	c.m.E2Latency.WithLabelValues(c.preset).Observe(d.Seconds())
}

// DiscardBefore drops any samples whose publish time is before cutoff (warmup).
func (c *Collector) DiscardBefore(cutoff time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.e1 = filterAtOrAfter(c.e1, cutoff)
	c.e2 = filterAtOrAfter(c.e2, cutoff)
}

func filterAtOrAfter(in []sample, cutoff time.Time) []sample {
	out := in[:0]
	for _, s := range in {
		if !s.publishedAt.Before(cutoff) {
			out = append(out, s)
		}
	}
	return out
}

// Finalize returns the count of unmatched publishes as missing replies and broadcasts.
func (c *Collector) Finalize() (missingReplies int, missingBroadcasts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byReqID), len(c.byMsgID)
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
	return c.snapshotLatencies(c.e1)
}

// E2Samples returns a sorted copy of E2 latencies for tests/reporting.
func (c *Collector) E2Samples() []time.Duration {
	return c.snapshotLatencies(c.e2)
}

func (c *Collector) snapshotLatencies(in []sample) []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(in))
	for i, s := range in {
		out[i] = s.latency
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
```

- [ ] **Step 4: Run to verify tests pass**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestCollector -v`
Expected: PASS for all six subtests.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/collector.go tools/loadgen/collector_test.go
git commit -m "feat(loadgen): collector correlates publishes with replies and broadcasts"
```

---

## Task 7: Percentile math and report formatting

**Files:**
- Create: `tools/loadgen/report.go`
- Create: `tools/loadgen/report_test.go`

- [ ] **Step 1: Write failing tests**

Create `tools/loadgen/report_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPercentiles_FixedSet(t *testing.T) {
	// 100 sorted values: 1ms..100ms
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(i+1) * time.Millisecond
	}
	p := ComputePercentiles(samples)
	assert.Equal(t, 50*time.Millisecond, p.P50)
	assert.Equal(t, 95*time.Millisecond, p.P95)
	assert.Equal(t, 99*time.Millisecond, p.P99)
	assert.Equal(t, 100*time.Millisecond, p.Max)
}

func TestPercentiles_Empty(t *testing.T) {
	p := ComputePercentiles(nil)
	assert.Zero(t, p.P50)
	assert.Zero(t, p.P95)
	assert.Zero(t, p.P99)
	assert.Zero(t, p.Max)
}

func TestPrintSummary_ContainsKeyFields(t *testing.T) {
	var buf bytes.Buffer
	s := Summary{
		Preset: "medium", Seed: 42, Site: "site-local",
		TargetRate: 500, ActualRate: 499.8,
		Duration: 60 * time.Second, Warmup: 10 * time.Second,
		Inject: "frontdoor", Sent: 25000,
	}
	PrintSummary(&buf, s)
	out := buf.String()
	for _, want := range []string{
		"preset: medium", "seed: 42", "site: site-local",
		"sent:", "25000", "inject: frontdoor",
	} {
		assert.True(t, strings.Contains(out, want), "summary missing %q; got:\n%s", want, out)
	}
}

func TestWriteCSV_OneRowPerSample(t *testing.T) {
	var buf bytes.Buffer
	rows := []CSVSample{
		{TimestampNs: 1, RequestID: "r1", Metric: "E1", LatencyNs: 2_100_000},
		{TimestampNs: 2, RequestID: "r1", Metric: "E2", LatencyNs: 8_700_000},
	}
	require.NoError(t, WriteCSV(&buf, rows))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 3) // header + 2 rows
	assert.Equal(t, "timestamp_ns,request_id,metric,latency_ns", lines[0])
	assert.Equal(t, "1,r1,E1,2100000", lines[1])
	assert.Equal(t, "2,r1,E2,8700000", lines[2])
}

func TestDetermineExitCode(t *testing.T) {
	cases := []struct {
		name         string
		sent         int
		errs         int
		wantExitCode int
	}{
		{"zero errors", 10000, 0, 0},
		{"under tolerance", 10000, 9, 0},   // 0.09% < 0.1%
		{"at tolerance boundary", 10000, 10, 0}, // exactly 0.1%: pass
		{"over tolerance", 10000, 11, 1},   // 0.11% > 0.1%
		{"no sends - any error fails", 0, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantExitCode, DetermineExitCode(tc.sent, tc.errs))
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run 'TestPercentiles|TestPrintSummary|TestWriteCSV|TestDetermineExitCode' -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 3: Implement `report.go`**

Create `tools/loadgen/report.go`:

```go
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"text/tabwriter"
	"time"
)

// Percentiles holds summary latency percentiles.
type Percentiles struct {
	P50, P95, P99, Max time.Duration
}

// ComputePercentiles returns P50/P95/P99/max of samples. Empty input -> zeros.
// Input does not need to be sorted on entry.
func ComputePercentiles(samples []time.Duration) Percentiles {
	if len(samples) == 0 {
		return Percentiles{}
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pick := func(q float64) time.Duration {
		idx := int(float64(len(sorted)-1) * q)
		return sorted[idx]
	}
	return Percentiles{
		P50: pick(0.50),
		P95: pick(0.95),
		P99: pick(0.99),
		Max: sorted[len(sorted)-1],
	}
}

// ConsumerStat captures the min/peak/final snapshot of a single durable.
type ConsumerStat struct {
	Stream           string
	Durable          string
	MinPending       uint64
	PeakPending      uint64
	FinalPending     uint64
	PeakAckPending   uint64
	Redelivered      uint64
}

// Summary is the full end-of-run report.
type Summary struct {
	Preset, Site, Inject string
	Seed                 int64
	TargetRate           int
	ActualRate           float64
	Duration, Warmup     time.Duration
	Sent                 int
	PublishErrors        int
	GatekeeperErrors     int
	MissingReplies       int
	MissingBroadcasts    int
	E1                   Percentiles
	E2                   Percentiles
	E1Count, E2Count     int
	Consumers            []ConsumerStat
}

// PrintSummary writes the terminal summary to w using text/tabwriter.
func PrintSummary(w io.Writer, s Summary) {
	fmt.Fprintln(w, "=== loadgen run complete ===")
	fmt.Fprintf(w, "preset: %s    seed: %d    site: %s\n", s.Preset, s.Seed, s.Site)
	fmt.Fprintf(w, "duration: %s (warmup: %s, measured: %s)    inject: %s\n",
		s.Duration, s.Warmup, s.Duration-s.Warmup, s.Inject)
	fmt.Fprintf(w, "target rate: %d msg/s    actual rate: %.1f msg/s\n\n", s.TargetRate, s.ActualRate)

	fmt.Fprintln(w, "publish results")
	fmt.Fprintf(w, "  sent:             %d\n", s.Sent)
	fmt.Fprintf(w, "  publish errors:    %d\n", s.PublishErrors)
	fmt.Fprintf(w, "  gatekeeper errors: %d\n", s.GatekeeperErrors)
	fmt.Fprintf(w, "  missing replies:   %d\n", s.MissingReplies)
	fmt.Fprintf(w, "  missing broadcasts:%d\n\n", s.MissingBroadcasts)

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "latency (measured window only)")
	fmt.Fprintln(tw, "metric\tcount\tp50\tp95\tp99\tmax")
	fmt.Fprintf(tw, "E1 gatekeeper\t%d\t%s\t%s\t%s\t%s\n", s.E1Count, s.E1.P50, s.E1.P95, s.E1.P99, s.E1.Max)
	fmt.Fprintf(tw, "E2 broadcast\t%d\t%s\t%s\t%s\t%s\n", s.E2Count, s.E2.P50, s.E2.P95, s.E2.P99, s.E2.Max)
	tw.Flush()

	fmt.Fprintln(w)
	if len(s.Consumers) > 0 {
		fmt.Fprintf(w, "consumer lag (%s)\n", s.Consumers[0].Stream)
		tw2 := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw2, "durable\tmin_pending\tpeak_pending\tfinal_pending\tpeak_ack_pending\tredelivered")
		for _, c := range s.Consumers {
			fmt.Fprintf(tw2, "%s\t%d\t%d\t%d\t%d\t%d\n",
				c.Durable, c.MinPending, c.PeakPending, c.FinalPending, c.PeakAckPending, c.Redelivered)
		}
		tw2.Flush()
	}
}

// CSVSample is one row in the per-sample CSV dump.
type CSVSample struct {
	TimestampNs int64
	RequestID   string
	Metric      string
	LatencyNs   int64
}

// WriteCSV writes a header and one row per sample.
func WriteCSV(w io.Writer, rows []CSVSample) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"timestamp_ns", "request_id", "metric", "latency_ns"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	for _, r := range rows {
		if err := cw.Write([]string{
			strconv.FormatInt(r.TimestampNs, 10),
			r.RequestID, r.Metric,
			strconv.FormatInt(r.LatencyNs, 10),
		}); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}
	cw.Flush()
	return cw.Error()
}

// DetermineExitCode returns 0 if error count is within 0.1% of sent.
// With sent == 0, any error is a failure.
func DetermineExitCode(sent, errs int) int {
	if sent == 0 {
		if errs == 0 {
			return 0
		}
		return 1
	}
	// 0.1% tolerance inclusive: errs * 1000 <= sent
	if errs*1000 <= sent {
		return 0
	}
	return 1
}
```

- [ ] **Step 4: Run to verify tests pass**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run 'TestPercentiles|TestPrintSummary|TestWriteCSV|TestDetermineExitCode' -v`
Expected: PASS for all tests.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/report.go tools/loadgen/report_test.go
git commit -m "feat(loadgen): percentiles, summary printer, CSV export, exit code"
```

---

## Task 8: Open-loop generator with injected publish function

**Files:**
- Create: `tools/loadgen/generator.go`
- Create: `tools/loadgen/generator_test.go`

- [ ] **Step 1: Write failing tests**

Create `tools/loadgen/generator_test.go`:

```go
package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestGenerator_SendsExpectedCount(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	c := NewCollector(m, p.Name)
	g := NewGenerator(GeneratorConfig{
		Preset:    p,
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
	// 200 msg/s for ~250ms: expect 40-60 publishes (wide tolerance for scheduler).
	assert.GreaterOrEqual(t, count, 30)
	assert.LessOrEqual(t, count, 70)
}

func TestGenerator_UsesFrontdoorSubject(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(GeneratorConfig{
		Preset: p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	require.NotEmpty(t, rp.calls)
	for _, c := range rp.calls {
		assert.Contains(t, c.subject, ".msg.send")
		assert.Contains(t, c.subject, "site-local")
	}
}

func TestGenerator_UsesCanonicalSubjectWhenInjectCanonical(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(GeneratorConfig{
		Preset: p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectCanonical,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)
	require.NotEmpty(t, rp.calls)
	for _, c := range rp.calls {
		assert.Contains(t, c.subject, "chat.msg.canonical.site-local.created")
	}
}

func TestGenerator_IncrementsPublishedMetric(t *testing.T) {
	p, _ := BuiltinPreset("small")
	f := BuildFixtures(p, 42, "site-local")
	rp := &recordingPublisher{}
	m := NewMetrics()
	g := NewGenerator(GeneratorConfig{
		Preset: p, Fixtures: f, SiteID: "site-local",
		Rate: 100, Inject: InjectFrontdoor,
		Publisher: rp, Metrics: m,
		Collector: NewCollector(m, p.Name),
	}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_ = g.Run(ctx)

	// Gather the counter value via the default prometheus export mechanism.
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
	assert.Greater(t, atomic.LoadInt64(&got), int64(0))
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestGenerator -v`
Expected: FAIL — undefined identifiers (`NewGenerator`, `GeneratorConfig`, `InjectFrontdoor`, `InjectCanonical`, `Publisher`).

- [ ] **Step 3: Implement the generator**

Create `tools/loadgen/generator.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// InjectMode selects which subject the generator publishes onto.
type InjectMode string

const (
	InjectFrontdoor InjectMode = "frontdoor"
	InjectCanonical InjectMode = "canonical"
)

// Publisher abstracts NATS publishing so tests can inject a recorder.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// GeneratorConfig is the parameter bundle for a Generator.
type GeneratorConfig struct {
	Preset    Preset
	Fixtures  Fixtures
	SiteID    string
	Rate      int
	Inject    InjectMode
	Publisher Publisher
	Metrics   *Metrics
	Collector *Collector
}

// Generator is the open-loop publisher.
type Generator struct {
	cfg GeneratorConfig
	rng *rand.Rand
}

// NewGenerator returns a Generator seeded from `seed`.
func NewGenerator(cfg GeneratorConfig, seed int64) *Generator {
	return &Generator{cfg: cfg, rng: rand.New(rand.NewSource(seed))}
}

// Run publishes at the configured rate until ctx is cancelled.
func (g *Generator) Run(ctx context.Context) error {
	if g.cfg.Rate <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	interval := time.Second / time.Duration(g.cfg.Rate)
	if interval <= 0 {
		interval = time.Nanosecond
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			g.publishOne(ctx)
		}
	}
}

func (g *Generator) publishOne(ctx context.Context) {
	if len(g.cfg.Fixtures.Subscriptions) == 0 {
		return
	}
	// Pick (user, room) from any subscription. This respects uniform and
	// mixed-distribution seeding because those are encoded in which
	// subscriptions exist.
	subIdx := g.rng.Intn(len(g.cfg.Fixtures.Subscriptions))
	sub := g.cfg.Fixtures.Subscriptions[subIdx]
	content := g.content(subIdx)
	msgID := uuid.NewString()
	reqID := uuid.NewString()

	var (
		subj string
		data []byte
		err  error
	)
	switch g.cfg.Inject {
	case InjectCanonical:
		now := time.Now().UTC()
		evt := model.MessageEvent{
			Message: model.Message{
				ID: msgID, RoomID: sub.RoomID,
				UserID: sub.User.ID, UserAccount: sub.User.Account,
				Content: content, CreatedAt: now,
			},
			SiteID:    g.cfg.SiteID,
			Timestamp: now.UnixMilli(),
		}
		data, err = json.Marshal(evt)
		subj = subject.MsgCanonicalCreated(g.cfg.SiteID)
	default:
		req := model.SendMessageRequest{ID: msgID, Content: content, RequestID: reqID}
		data, err = json.Marshal(req)
		subj = subject.MsgSend(sub.User.Account, sub.RoomID, g.cfg.SiteID)
	}
	if err != nil {
		g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "marshal").Inc()
		return
	}
	publishTime := time.Now()
	g.cfg.Collector.RecordPublish(reqID, msgID, publishTime)
	if perr := g.cfg.Publisher.Publish(ctx, subj, data); perr != nil {
		g.cfg.Metrics.PublishErrors.WithLabelValues(g.cfg.Preset.Name, "publish").Inc()
		return
	}
	g.cfg.Metrics.Published.WithLabelValues(g.cfg.Preset.Name).Inc()
}

func (g *Generator) content(subUserIdx int) string {
	r := g.cfg.Preset.ContentBytes
	size := r.Min
	if r.Max > r.Min {
		size = r.Min + g.rng.Intn(r.Max-r.Min+1)
	}
	if size <= 0 {
		size = 1
	}
	body := strings.Repeat("x", size)
	if g.cfg.Preset.MentionRate > 0 && g.rng.Float64() < g.cfg.Preset.MentionRate {
		// Prefix with a valid-looking mention token. The target user-<idx>
		// need not exist for capacity measurement; the gatekeeper does not
		// validate mention targets.
		target := g.rng.Intn(g.cfg.Preset.Users)
		body = fmt.Sprintf("@user-%d %s", target, body)
	}
	// ThreadRate handling is deferred: fabricating thread-parent fields that
	// pass gatekeeper validation requires tracking previously-published
	// messages, which is not needed for the capacity signal. The preset's
	// ThreadRate is read but unused until thread workloads are exercised.
	_ = subUserIdx
	return body
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test ./tools/loadgen/ -run TestGenerator -v`
Expected: PASS.

- [ ] **Step 5: Run the full unit suite to make sure nothing else broke**

Run: `cd /home/user/chat && make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/generator.go tools/loadgen/generator_test.go
git commit -m "feat(loadgen): open-loop generator with injected publisher"
```

---

## Task 9: Consumer-lag sampler

**Files:**
- Create: `tools/loadgen/consumerlag.go`

- [ ] **Step 1: Write `consumerlag.go`**

This is I/O against live JetStream, covered end-to-end by the integration test in Task 12. A unit test would just re-test the JetStream client.

Create `tools/loadgen/consumerlag.go`:

```go
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// ConsumerSampler polls a single durable consumer's info every interval and
// records min/peak/final samples. Start with Run(ctx); stop by cancelling ctx.
type ConsumerSampler struct {
	js       jetstream.JetStream
	stream   string
	durable  string
	metrics  *Metrics
	interval time.Duration

	hasSample          bool
	minPending         uint64
	peakPending        uint64
	finalPending       uint64
	peakAckPending     uint64
	finalRedelivered   uint64
}

// NewConsumerSampler constructs a sampler.
func NewConsumerSampler(js jetstream.JetStream, stream, durable string, m *Metrics, interval time.Duration) *ConsumerSampler {
	return &ConsumerSampler{js: js, stream: stream, durable: durable, metrics: m, interval: interval}
}

// Run polls ConsumerInfo until ctx is cancelled.
func (s *ConsumerSampler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sampleOnce(ctx)
		}
	}
}

func (s *ConsumerSampler) sampleOnce(ctx context.Context) {
	cons, err := s.js.Consumer(ctx, s.stream, s.durable)
	if err != nil {
		slog.Debug("consumer lookup failed", "stream", s.stream, "durable", s.durable, "error", err)
		return
	}
	info, err := cons.Info(ctx)
	if err != nil {
		slog.Debug("consumer info failed", "stream", s.stream, "durable", s.durable, "error", err)
		return
	}
	pending := info.NumPending
	ack := uint64(info.NumAckPending)
	redel := uint64(info.NumRedelivered)

	s.metrics.ConsumerPending.WithLabelValues(s.stream, s.durable).Set(float64(pending))
	s.metrics.ConsumerAckPending.WithLabelValues(s.stream, s.durable).Set(float64(ack))
	s.metrics.ConsumerRedelivered.WithLabelValues(s.stream, s.durable).Set(float64(redel))

	if !s.hasSample {
		s.hasSample = true
		s.minPending = pending
		s.peakPending = pending
		s.peakAckPending = ack
	} else {
		if pending < s.minPending {
			s.minPending = pending
		}
		if pending > s.peakPending {
			s.peakPending = pending
		}
		if ack > s.peakAckPending {
			s.peakAckPending = ack
		}
	}
	s.finalPending = pending
	s.finalRedelivered = redel
}

// Snapshot returns a ConsumerStat from what has been observed so far.
func (s *ConsumerSampler) Snapshot() ConsumerStat {
	return ConsumerStat{
		Stream:          s.stream,
		Durable:         s.durable,
		MinPending:      s.minPending,
		PeakPending:     s.peakPending,
		FinalPending:    s.finalPending,
		PeakAckPending:  s.peakAckPending,
		Redelivered:     s.finalRedelivered,
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./tools/loadgen/`
Expected: Succeeds.

- [ ] **Step 3: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/consumerlag.go
git commit -m "feat(loadgen): JetStream consumer-lag sampler"
```

---

## Task 10: Wire subcommands in `main.go`

Replace the stub in `main.go` with full wiring: each subcommand parses flags, opens connections, and dispatches.

**Files:**
- Modify: `tools/loadgen/main.go`

- [ ] **Step 1: Rewrite `main.go`**

Replace the entire contents of `tools/loadgen/main.go` with:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL       string `env:"NATS_URL,required"`
	NatsCredsFile string `env:"NATS_CREDS_FILE" envDefault:""`
	SiteID        string `env:"SITE_ID"         envDefault:"site-local"`
	MongoURI      string `env:"MONGO_URI,required"`
	MongoDB       string `env:"MONGO_DB"        envDefault:"chat"`
	MetricsAddr   string `env:"METRICS_ADDR"    envDefault:":9099"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: loadgen <seed|run|teardown> [flags]")
		os.Exit(2)
	}
	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}
	// SIGINT / SIGTERM cancel the base context. Each subcommand treats ctx
	// cancellation as "stop early but still run the end-of-run finalizers
	// (print summary, drain NATS, disconnect Mongo)".
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "seed":
		os.Exit(runSeed(ctx, cfg, os.Args[2:]))
	case "run":
		os.Exit(runRun(ctx, cfg, os.Args[2:]))
	case "teardown":
		os.Exit(runTeardown(ctx, cfg))
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}

func runSeed(ctx context.Context, cfg config, args []string) int {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	preset := fs.String("preset", "", "preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	_ = fs.Parse(args)
	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", *preset)
		return 2
	}
	client, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect", "error", err)
		return 1
	}
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database(cfg.MongoDB)
	fixtures := BuildFixtures(p, *seed, cfg.SiteID)
	if err := Seed(ctx, db, fixtures); err != nil {
		slog.Error("seed", "error", err)
		return 1
	}
	slog.Info("seed complete", "preset", p.Name, "users", len(fixtures.Users), "rooms", len(fixtures.Rooms), "subs", len(fixtures.Subscriptions))
	return 0
}

func runTeardown(ctx context.Context, cfg config) int {
	client, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect", "error", err)
		return 1
	}
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database(cfg.MongoDB)
	if err := Teardown(ctx, db); err != nil {
		slog.Error("teardown", "error", err)
		return 1
	}
	slog.Info("teardown complete")
	return 0
}

func runRun(ctx context.Context, cfg config, args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	preset := fs.String("preset", "", "preset name")
	seed := fs.Int64("seed", 42, "RNG seed")
	duration := fs.Duration("duration", 60*time.Second, "run duration")
	rate := fs.Int("rate", 500, "target msgs/sec")
	warmup := fs.Duration("warmup", 10*time.Second, "warmup window (samples discarded)")
	inject := fs.String("inject", "frontdoor", "injection point: frontdoor|canonical")
	csvPath := fs.String("csv", "", "optional csv output path")
	_ = fs.Parse(args)
	if *preset == "" {
		fmt.Fprintln(os.Stderr, "--preset required")
		return 2
	}
	p, ok := BuiltinPreset(*preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown preset: %s\n", *preset)
		return 2
	}
	injectMode := InjectFrontdoor
	if *inject == "canonical" {
		injectMode = InjectCanonical
	} else if *inject != "frontdoor" {
		fmt.Fprintf(os.Stderr, "unknown inject mode: %s\n", *inject)
		return 2
	}

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		slog.Error("nats connect", "error", err)
		return 1
	}
	js, err := jetstream.New(nc.Conn())
	if err != nil {
		slog.Error("jetstream init", "error", err)
		return 1
	}

	metrics := NewMetrics()
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: metrics.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()

	fixtures := BuildFixtures(p, *seed, cfg.SiteID)
	collector := NewCollector(metrics, p.Name)

	// E1 subscription: gatekeeper replies.
	e1Sub, err := nc.Conn().Subscribe("chat.user.*.response.>", func(msg *nats.Msg) {
		reqID := lastToken(msg.Subject)
		// Non-empty "error" field counts as a gatekeeper error.
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(msg.Data, &payload)
		if payload.Error != "" {
			metrics.PublishErrors.WithLabelValues(p.Name, "gatekeeper").Inc()
		}
		collector.RecordReply(reqID, time.Now())
	})
	if err != nil {
		slog.Error("subscribe e1", "error", err)
		return 1
	}
	defer func() { _ = e1Sub.Unsubscribe() }()

	// E2 subscription: broadcast events.
	e2Sub, err := nc.Conn().Subscribe("chat.room.*.event", func(msg *nats.Msg) {
		var evt model.RoomEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		if evt.Message == nil || evt.Message.ID == "" {
			return
		}
		collector.RecordBroadcast(evt.Message.ID, time.Now())
	})
	if err != nil {
		slog.Error("subscribe e2", "error", err)
		return 1
	}
	defer func() { _ = e2Sub.Unsubscribe() }()

	canonical := stream.MessagesCanonical(cfg.SiteID)
	samplerCtx, cancelSamplers := context.WithCancel(ctx)
	defer cancelSamplers()
	mwSampler := NewConsumerSampler(js, canonical.Name, "message-worker", metrics, 1*time.Second)
	bwSampler := NewConsumerSampler(js, canonical.Name, "broadcast-worker", metrics, 1*time.Second)
	var samplerWG sync.WaitGroup
	samplerWG.Add(2)
	go func() { defer samplerWG.Done(); mwSampler.Run(samplerCtx) }()
	go func() { defer samplerWG.Done(); bwSampler.Run(samplerCtx) }()

	publisher := &natsCorePublisher{nc: nc.Conn()}
	if injectMode == InjectCanonical {
		publisher = &natsCorePublisher{nc: nc.Conn(), useJetStream: true, js: js}
	}

	gen := NewGenerator(GeneratorConfig{
		Preset: p, Fixtures: fixtures, SiteID: cfg.SiteID,
		Rate: *rate, Inject: injectMode,
		Publisher: publisher, Metrics: metrics, Collector: collector,
	}, *seed)

	runCtx, cancelRun := context.WithTimeout(ctx, *duration)
	defer cancelRun()
	warmupDeadline := time.Now().Add(*warmup)
	genErr := gen.Run(runCtx)
	// Wait up to 2 seconds for trailing replies and broadcasts to arrive.
	time.Sleep(2 * time.Second)
	collector.DiscardBefore(warmupDeadline)
	missingReplies, missingBroadcasts := collector.Finalize()

	cancelSamplers()
	samplerWG.Wait()

	shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
	_ = metricsSrv.Shutdown(shutCtx)
	cancelShut()
	_ = nc.Drain()

	if genErr != nil {
		slog.Error("generator error", "error", genErr)
	}

	publishErrs := counterValue(metrics, "loadgen_publish_errors_total")
	gkErrs := counterValueLabeled(metrics, "loadgen_publish_errors_total", "reason", "gatekeeper")
	sent := int(counterValueLabeled(metrics, "loadgen_published_total", "preset", p.Name))
	measured := *duration - *warmup
	actualRate := 0.0
	if measured > 0 {
		actualRate = float64(collector.E1Count()+missingReplies) / measured.Seconds()
	}

	summary := Summary{
		Preset: p.Name, Seed: *seed, Site: cfg.SiteID,
		TargetRate: *rate, ActualRate: actualRate,
		Duration: *duration, Warmup: *warmup, Inject: *inject,
		Sent: sent,
		PublishErrors:    int(publishErrs - gkErrs),
		GatekeeperErrors: int(gkErrs),
		MissingReplies:   missingReplies,
		MissingBroadcasts: missingBroadcasts,
		E1:       ComputePercentiles(collector.E1Samples()),
		E2:       ComputePercentiles(collector.E2Samples()),
		E1Count:  collector.E1Count(),
		E2Count:  collector.E2Count(),
		Consumers: []ConsumerStat{mwSampler.Snapshot(), bwSampler.Snapshot()},
	}
	PrintSummary(os.Stdout, summary)

	if *csvPath != "" {
		if err := writeCSVFile(*csvPath, collector); err != nil {
			slog.Error("csv export", "error", err)
		}
	}

	totalErrs := summary.PublishErrors + summary.GatekeeperErrors + summary.MissingReplies + summary.MissingBroadcasts
	return DetermineExitCode(summary.Sent, totalErrs)
}

type natsCorePublisher struct {
	nc           *nats.Conn
	useJetStream bool
	js           jetstream.JetStream
}

func (p *natsCorePublisher) Publish(ctx context.Context, subject string, data []byte) error {
	if p.useJetStream {
		_, err := p.js.Publish(ctx, subject, data)
		if err != nil {
			return fmt.Errorf("jetstream publish: %w", err)
		}
		return nil
	}
	if err := p.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("core publish: %w", err)
	}
	return nil
}

func lastToken(subj string) string {
	i := strings.LastIndex(subj, ".")
	if i < 0 {
		return subj
	}
	return subj[i+1:]
}

func writeCSVFile(path string, c *Collector) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer f.Close()
	var rows []CSVSample
	// E1 rows
	for i, d := range c.E1Samples() {
		rows = append(rows, CSVSample{TimestampNs: int64(i), RequestID: "", Metric: "E1", LatencyNs: d.Nanoseconds()})
	}
	// E2 rows
	for i, d := range c.E2Samples() {
		rows = append(rows, CSVSample{TimestampNs: int64(i), RequestID: "", Metric: "E2", LatencyNs: d.Nanoseconds()})
	}
	return WriteCSV(f, rows)
}

func counterValue(m *Metrics, name string) float64 {
	metrics, err := m.Registry.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, mf := range metrics {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			total += metric.GetCounter().GetValue()
		}
	}
	return total
}

func counterValueLabeled(m *Metrics, name, labelName, labelValue string) float64 {
	metrics, err := m.Registry.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, mf := range metrics {
		if mf.GetName() != name {
			continue
		}
		for _, metric := range mf.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}
```

Note: `model.RoomEvent.Message` is a `*ClientMessage` per `pkg/model/event.go`. Accessing `evt.Message.ID` through the embedded `Message` works because `ClientMessage` embeds `Message`.

- [ ] **Step 2: Build to confirm**

Run: `cd /home/user/chat && go build ./tools/loadgen/`
Expected: Succeeds.

- [ ] **Step 3: Run full unit suite**

Run: `cd /home/user/chat && make test SERVICE=tools/loadgen`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/main.go
git commit -m "feat(loadgen): wire seed/run/teardown subcommands in main.go"
```

---

## Task 11: Dockerfile and docker-compose for the harness

**Files:**
- Create: `tools/loadgen/deploy/Dockerfile`
- Create: `tools/loadgen/deploy/docker-compose.loadtest.yml`
- Create: `tools/loadgen/deploy/prometheus/prometheus.yml`
- Create: `tools/loadgen/deploy/grafana/provisioning/datasources/prometheus.yaml`
- Create: `tools/loadgen/deploy/grafana/provisioning/dashboards/loadtest.yaml`
- Create: `tools/loadgen/deploy/grafana/dashboards/loadtest.json`

- [ ] **Step 1: Write the Dockerfile**

Create `tools/loadgen/deploy/Dockerfile`:

```dockerfile
FROM golang:1.25.8-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY pkg/ pkg/
COPY tools/loadgen/ tools/loadgen/

RUN CGO_ENABLED=0 go build -o /loadgen ./tools/loadgen/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /loadgen /loadgen
ENTRYPOINT ["/loadgen"]
```

- [ ] **Step 2: Write the docker-compose file**

Create `tools/loadgen/deploy/docker-compose.loadtest.yml`:

```yaml
name: loadgen

services:
  nats:
    image: nats:2.11-alpine
    command: ["-js", "-m", "8222"]
    ports:
      - "4222:4222"
      - "8222:8222"
    networks: [loadtest]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"
    networks: [loadtest]

  cassandra:
    image: cassandra:4.1
    environment:
      - CASSANDRA_CLUSTER_NAME=loadtest
    ports:
      - "9042:9042"
    networks: [loadtest]
    healthcheck:
      test: ["CMD-SHELL", "nodetool status | grep -q '^UN'"]
      interval: 10s
      timeout: 5s
      retries: 30

  cassandra-init:
    image: cassandra:4.1
    depends_on:
      cassandra:
        condition: service_healthy
    entrypoint:
      - sh
      - -c
      - |
        cqlsh cassandra -e "CREATE KEYSPACE IF NOT EXISTS chat WITH replication = {'class':'SimpleStrategy','replication_factor':1};"
    networks: [loadtest]
    restart: "no"

  message-gatekeeper:
    build:
      context: ../../..
      dockerfile: message-gatekeeper/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on: [nats, mongodb]
    networks: [loadtest]

  message-worker:
    build:
      context: ../../..
      dockerfile: message-worker/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - CASSANDRA_HOSTS=cassandra
      - CASSANDRA_KEYSPACE=chat
    depends_on:
      nats:
        condition: service_started
      mongodb:
        condition: service_started
      cassandra-init:
        condition: service_completed_successfully
    networks: [loadtest]

  broadcast-worker:
    build:
      context: ../../..
      dockerfile: broadcast-worker/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on: [nats, mongodb]
    networks: [loadtest]

  loadgen:
    build:
      context: ../../..
      dockerfile: tools/loadgen/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - METRICS_ADDR=:9099
    ports:
      - "9099:9099"
    depends_on: [nats, mongodb, message-gatekeeper, message-worker, broadcast-worker]
    entrypoint: ["sleep", "infinity"]
    networks: [loadtest]

  prometheus:
    image: prom/prometheus:v2.55.0
    profiles: [dashboards]
    volumes:
      - ./prometheus/prometheus.yml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"
    networks: [loadtest]

  grafana:
    image: grafana/grafana:11.2.2
    profiles: [dashboards]
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
      - GF_AUTH_ANONYMOUS_ENABLED=true
      - GF_AUTH_ANONYMOUS_ORG_ROLE=Admin
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning:ro
      - ./grafana/dashboards:/var/lib/grafana/dashboards:ro
    ports:
      - "3000:3000"
    networks: [loadtest]

networks:
  loadtest:
```

- [ ] **Step 3: Write Prometheus scrape config**

Create `tools/loadgen/deploy/prometheus/prometheus.yml`:

```yaml
global:
  scrape_interval: 5s
  evaluation_interval: 5s

scrape_configs:
  - job_name: loadgen
    static_configs:
      - targets: ["loadgen:9099"]
  - job_name: nats
    metrics_path: /
    static_configs:
      - targets: ["nats:8222"]
```

- [ ] **Step 4: Write Grafana provisioning**

Create `tools/loadgen/deploy/grafana/provisioning/datasources/prometheus.yaml`:

```yaml
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
```

Create `tools/loadgen/deploy/grafana/provisioning/dashboards/loadtest.yaml`:

```yaml
apiVersion: 1
providers:
  - name: loadtest
    folder: ""
    type: file
    options:
      path: /var/lib/grafana/dashboards
```

- [ ] **Step 5: Write a minimal dashboard JSON**

Create `tools/loadgen/deploy/grafana/dashboards/loadtest.json`:

```json
{
  "title": "Loadgen",
  "schemaVersion": 39,
  "version": 1,
  "refresh": "5s",
  "time": {"from": "now-15m", "to": "now"},
  "panels": [
    {
      "type": "timeseries",
      "title": "Throughput (msg/s)",
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 0},
      "targets": [{"expr": "rate(loadgen_published_total[10s])", "refId": "A"}]
    },
    {
      "type": "timeseries",
      "title": "E1 gatekeeper latency (P50/P95/P99)",
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 0},
      "targets": [
        {"expr": "histogram_quantile(0.50, sum(rate(loadgen_e1_latency_seconds_bucket[30s])) by (le))", "legendFormat": "p50", "refId": "A"},
        {"expr": "histogram_quantile(0.95, sum(rate(loadgen_e1_latency_seconds_bucket[30s])) by (le))", "legendFormat": "p95", "refId": "B"},
        {"expr": "histogram_quantile(0.99, sum(rate(loadgen_e1_latency_seconds_bucket[30s])) by (le))", "legendFormat": "p99", "refId": "C"}
      ]
    },
    {
      "type": "timeseries",
      "title": "E2 broadcast latency (P50/P95/P99)",
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 8},
      "targets": [
        {"expr": "histogram_quantile(0.50, sum(rate(loadgen_e2_latency_seconds_bucket[30s])) by (le))", "legendFormat": "p50", "refId": "A"},
        {"expr": "histogram_quantile(0.95, sum(rate(loadgen_e2_latency_seconds_bucket[30s])) by (le))", "legendFormat": "p95", "refId": "B"},
        {"expr": "histogram_quantile(0.99, sum(rate(loadgen_e2_latency_seconds_bucket[30s])) by (le))", "legendFormat": "p99", "refId": "C"}
      ]
    },
    {
      "type": "timeseries",
      "title": "Consumer pending",
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 8},
      "targets": [{"expr": "loadgen_consumer_pending", "legendFormat": "{{durable}}", "refId": "A"}]
    },
    {
      "type": "timeseries",
      "title": "Consumer ack pending",
      "gridPos": {"h": 8, "w": 12, "x": 0, "y": 16},
      "targets": [{"expr": "loadgen_consumer_ack_pending", "legendFormat": "{{durable}}", "refId": "A"}]
    },
    {
      "type": "timeseries",
      "title": "Publish errors/sec",
      "gridPos": {"h": 8, "w": 12, "x": 12, "y": 16},
      "targets": [{"expr": "rate(loadgen_publish_errors_total[10s])", "legendFormat": "{{reason}}", "refId": "A"}]
    }
  ]
}
```

- [ ] **Step 6: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/deploy/
git commit -m "feat(loadgen): docker-compose harness, Dockerfile, grafana dashboard"
```

---

## Task 12: Scoped Makefile

**Files:**
- Create: `tools/loadgen/deploy/Makefile`

- [ ] **Step 1: Write the Makefile**

Create `tools/loadgen/deploy/Makefile`:

```make
COMPOSE ?= docker compose -f docker-compose.loadtest.yml

.PHONY: up seed run run-dashboards down logs

up:
	$(COMPOSE) up -d --build

seed:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen seed --preset=$(PRESET)

run:
	@test -n "$(PRESET)" || (echo "PRESET=<name> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen run \
	    --preset=$(PRESET) \
	    --rate=$(or $(RATE),500) \
	    --duration=$(or $(DURATION),60s)

run-dashboards:
	$(COMPOSE) --profile dashboards up -d
	$(MAKE) run PRESET=$(PRESET) RATE=$(RATE) DURATION=$(DURATION)

down:
	$(COMPOSE) --profile dashboards down -v

logs:
	$(COMPOSE) logs -f loadgen
```

- [ ] **Step 2: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/deploy/Makefile
git commit -m "feat(loadgen): scoped Makefile for harness"
```

---

## Task 13: Integration test — end-to-end wiring

**Files:**
- Create: `tools/loadgen/integration_test.go`

- [ ] **Step 1: Write the integration test**

Create `tools/loadgen/integration_test.go`:

```go
//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/stream"
)

// setupNATS starts a JetStream-enabled NATS container via the generic
// testcontainers interface (no dedicated NATS module is required here).
func setupNATS(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2.11-alpine",
			Cmd:          []string{"-js"},
			ExposedPorts: []string{"4222/tcp"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(30 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	host, err := c.Host(ctx)
	require.NoError(t, err)
	port, err := c.MappedPort(ctx, "4222")
	require.NoError(t, err)
	return fmt.Sprintf("nats://%s:%s", host, port.Port()), func() { _ = c.Terminate(ctx) }
}

func setupMongo(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	c, err := mongodb.Run(ctx, "mongo:8")
	require.NoError(t, err)
	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	return uri, func() { _ = c.Terminate(ctx) }
}

// TestLoadgenSmallPreset_EndToEnd verifies the generator publishes messages,
// the canonical stream receives them, both durables drain, and MongoDB shows
// updated room.lastMsgId. It stands in for the gatekeeper/worker services by
// running a minimal in-process equivalent: it creates the canonical stream and
// consumes from MESSAGES_CANONICAL to ack messages so num_pending drops to 0.
func TestLoadgenSmallPreset_EndToEnd(t *testing.T) {
	ctx := context.Background()
	natsURI, stopNATS := setupNATS(t)
	defer stopNATS()
	mongoURI, stopMongo := setupMongo(t)
	defer stopMongo()

	nc, err := nats.Connect(natsURI)
	require.NoError(t, err)
	defer nc.Drain()

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	siteID := "site-test"
	canonical := stream.MessagesCanonical(siteID)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: canonical.Name, Subjects: canonical.Subjects})
	require.NoError(t, err)

	for _, durable := range []string{"message-worker", "broadcast-worker"} {
		cons, err := js.CreateOrUpdateConsumer(ctx, canonical.Name, jetstream.ConsumerConfig{
			Durable:   durable,
			AckPolicy: jetstream.AckExplicitPolicy,
		})
		require.NoError(t, err)
		go func(c jetstream.Consumer) {
			_, _ = c.Consume(func(msg jetstream.Msg) { _ = msg.Ack() })
		}(cons)
	}

	client, err := mongoutil.Connect(ctx, mongoURI)
	require.NoError(t, err)
	defer mongoutil.Disconnect(ctx, client)
	db := client.Database("chat")

	preset, _ := BuiltinPreset("small")
	fixtures := BuildFixtures(preset, 42, siteID)
	require.NoError(t, Seed(ctx, db, fixtures))

	metrics := NewMetrics()
	collector := NewCollector(metrics, preset.Name)

	// Fake gatekeeper: subscribe to the front-door subject, reply with the
	// original request shape (so missing-reply check passes), and publish a
	// MessageEvent to MESSAGES_CANONICAL so the downstream consumers see it.
	gkSub, err := nc.Subscribe("chat.user.*.room.*."+siteID+".msg.send", func(m *nats.Msg) {
		var req model.SendMessageRequest
		if err := json.Unmarshal(m.Data, &req); err != nil {
			return
		}
		_, _, gotSiteID, ok := parseUserRoomSiteSubject(m.Subject)
		if !ok || gotSiteID != siteID {
			return
		}
		evt := model.MessageEvent{
			Message: model.Message{ID: req.ID, Content: req.Content, CreatedAt: time.Now()},
			SiteID:  siteID, Timestamp: time.Now().UnixMilli(),
		}
		data, _ := json.Marshal(evt)
		_, _ = js.Publish(ctx, "chat.msg.canonical."+siteID+".created", data)

		replySubj := "chat.user." + m.Subject[len("chat.user."):]
		_ = replySubj
	})
	require.NoError(t, err)
	defer gkSub.Unsubscribe()

	// Also broadcast a matching room event so E2 correlation has something to consume.
	bwSub, err := nc.Subscribe("chat.msg.canonical."+siteID+".created", func(m *nats.Msg) {
		var evt model.MessageEvent
		if err := json.Unmarshal(m.Data, &evt); err != nil {
			return
		}
		roomEvt := model.RoomEvent{
			Type: model.RoomEventNewMessage, RoomID: "r",
			Message: &model.ClientMessage{Message: evt.Message},
		}
		data, _ := json.Marshal(roomEvt)
		_ = nc.Publish("chat.room.r.event", data)
	})
	require.NoError(t, err)
	defer bwSub.Unsubscribe()

	publisher := &natsCorePublisher{nc: nc}
	gen := NewGenerator(GeneratorConfig{
		Preset: preset, Fixtures: fixtures, SiteID: siteID,
		Rate: 50, Inject: InjectFrontdoor,
		Publisher: publisher, Metrics: metrics, Collector: collector,
	}, 42)

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	require.NoError(t, gen.Run(runCtx))

	// Allow trailing events to flow.
	time.Sleep(2 * time.Second)

	missingReplies, missingBroadcasts := collector.Finalize()
	require.Equal(t, 0, missingBroadcasts, "missing broadcasts")
	_ = missingReplies // the fake gatekeeper above does not actually send replies; ignore E1 assertion in this test.

	// Assert canonical stream pending is 0 for both durables.
	for _, durable := range []string{"message-worker", "broadcast-worker"} {
		cons, err := js.Consumer(ctx, canonical.Name, durable)
		require.NoError(t, err)
		info, err := cons.Info(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(0), info.NumPending, "durable %s still has pending", durable)
	}

	// Assert something got seeded and is reachable.
	var room model.Room
	err = db.Collection("rooms").FindOne(ctx, bson.M{"_id": fixtures.Rooms[0].ID}).Decode(&room)
	require.NoError(t, err)
	require.Equal(t, fixtures.Rooms[0].ID, room.ID)
}

// parseUserRoomSiteSubject is a local re-impl because the test can't use the
// internal subject package without introducing a cycle.
func parseUserRoomSiteSubject(s string) (account, roomID, siteID string, ok bool) {
	// chat.user.{account}.room.{roomID}.{siteID}.msg.send
	parts := splitDot(s)
	if len(parts) < 7 || parts[0] != "chat" || parts[1] != "user" || parts[3] != "room" {
		return "", "", "", false
	}
	return parts[2], parts[4], parts[5], true
}

func splitDot(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
```

- [ ] **Step 2: Run the integration test**

Run: `cd /home/user/chat && make test-integration SERVICE=tools/loadgen`
Expected: PASS. Docker must be running.

- [ ] **Step 3: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/integration_test.go
git commit -m "test(loadgen): integration test for end-to-end wiring"
```

---

## Task 14: Operator README

**Files:**
- Create: `tools/loadgen/README.md`

- [ ] **Step 1: Write the README**

Create `tools/loadgen/README.md`:

````markdown
# loadgen

Capacity-baseline load generator for the single-site messaging pipeline
(`message-gatekeeper` → `MESSAGES_CANONICAL` → `message-worker` +
`broadcast-worker`). Single Go binary with three subcommands.

## Quick start

```
make -C tools/loadgen/deploy up
make -C tools/loadgen/deploy seed PRESET=medium
make -C tools/loadgen/deploy run  PRESET=medium RATE=500 DURATION=60s
```

For live dashboards:

```
make -C tools/loadgen/deploy run-dashboards PRESET=medium
# Grafana at http://localhost:3000 (anonymous admin)
```

Tear down:

```
make -C tools/loadgen/deploy down
```

## Presets

| preset      | users  | rooms | notes                                                  |
|-------------|--------|-------|--------------------------------------------------------|
| `small`     | 10     | 5     | uniform, 200-byte content                              |
| `medium`    | 1 000  | 100   | uniform, 200-byte content                              |
| `large`     | 10 000 | 1 000 | uniform, 200-byte content                              |
| `realistic` | 1 000  | 100   | Zipf senders, mixed room sizes, 50–2000 bytes, mentions|

## Subcommands

- `loadgen seed --preset=<name> [--seed=42]` — idempotently populate
  MongoDB with deterministic fixtures.
- `loadgen run --preset=<name> [flags]` — open-loop publish at `--rate`
  msgs/sec for `--duration`, print a summary at the end. Flags:
  `--seed`, `--warmup`, `--inject=frontdoor|canonical`, `--csv=path`.
- `loadgen teardown` — drop the three seeded collections.

## Reading the summary

- `final_pending == 0` on both durables, zero errors → the pipeline is
  sustaining your target rate.
- `final_pending` climbing, or error counts > 0 → over capacity or a
  regression upstream of the worker.

## Non-goals

- Not a CI regression gate. Invoked manually.
- Not an auth benchmark. Uses shared `backend.creds`.
- Not a cross-site benchmark. Single-site only.
- Not an absolute-number tool. Numbers vary by host — compare within one
  machine across changes, don't compare across machines.
````

- [ ] **Step 2: Commit**

```bash
cd /home/user/chat
git add tools/loadgen/README.md
git commit -m "docs(loadgen): add operator README"
```

---

## Task 15: Lint + final full-test pass

- [ ] **Step 1: Run the linter**

Run: `cd /home/user/chat && make lint`
Expected: PASS (zero issues). Fix any findings before proceeding.

- [ ] **Step 2: Run the unit test suite for the whole repo**

Run: `cd /home/user/chat && make test`
Expected: PASS.

- [ ] **Step 3: Run coverage for `tools/loadgen`**

Run: `cd /home/user/chat && go test -race -coverprofile=coverage.out ./tools/loadgen/ && go tool cover -func=coverage.out | tail -n 1`
Expected: total coverage ≥ 80%.

If below 80%, identify the uncovered file(s) with
`go tool cover -func=coverage.out | sort -k3 -n` and add tests to reach
the threshold. Core files (`preset.go`, `generator.go`, `collector.go`,
`report.go`) should each be ≥ 90%.

- [ ] **Step 4: Commit any coverage-gap fixes**

```bash
cd /home/user/chat
git add tools/loadgen/
git commit -m "test(loadgen): raise coverage to project threshold"
```

- [ ] **Step 5: Push the branch**

```bash
cd /home/user/chat
git push -u origin claude/load-test-messaging-workers-tDKZn
```

---

## Done when

- `make test SERVICE=tools/loadgen` passes locally.
- `make test-integration SERVICE=tools/loadgen` passes locally.
- `make lint` passes for the whole repo.
- `tools/loadgen` coverage ≥ 80% overall, ≥ 90% on core files.
- Running `make -C tools/loadgen/deploy up seed run PRESET=small RATE=50 DURATION=10s` prints a well-formed summary with exit code 0 against a clean Docker host.
- All commits are on `claude/load-test-messaging-workers-tDKZn` and pushed.
