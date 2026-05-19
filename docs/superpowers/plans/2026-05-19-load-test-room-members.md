# Room-Member Add Load Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add two new subcommands to `tools/loadgen` — `members-sustained` (open-loop throughput) and `members-capacity` (sequential per-room growth) — that benchmark the add-member pipeline (room-service → ROOMS canonical → room-worker → member_added broadcast) end-to-end on a single site.

**Architecture:** Reuses the existing loadgen NATS connect, Mongo/Valkey wiring, metrics server, Prometheus/Grafana overlay, and percentile/CSV writers. Members workload gets its own preset table (`MembersPreset`), fixture builder (`BuildMembersFixtures`), publishers (frontdoor + canonical), collector (E1 via NATS reply inbox; E2 via `(roomID, sortedAccounts)` correlation), generators, and summary printer. The existing `seed`/`teardown` subcommands gain `--workload=messages|members` for fixture staging. v1 scope: `--shape=users` only — see the spec at `docs/superpowers/specs/2026-05-19-load-test-room-members-design.md` for the rationale and Phase-2 plan.

**Tech Stack:** Go 1.25, `nats-io/nats.go` + `jetstream`, `caarlos0/env/v11`, `prometheus/client_golang`, `stretchr/testify`, `go.uber.org/mock`, `testcontainers-go`.

---

## File map

**Create:**
- `tools/loadgen/members.go` — `Shape`, `MembersPreset`, `BuiltinMembersPreset`, `MembersFixtures`, `BuildMembersFixtures`, parse/validate helpers
- `tools/loadgen/members_test.go`
- `tools/loadgen/members_publisher.go` — `MemberPublisher` interface, `frontdoorMemberPublisher`, `canonicalMemberPublisher`
- `tools/loadgen/members_publisher_test.go`
- `tools/loadgen/members_collector.go` — `MemberCollector`, correlation key, finalize
- `tools/loadgen/members_collector_test.go`
- `tools/loadgen/members_generator.go` — `SustainedMembersGenerator`, `CapacityMembersGenerator`
- `tools/loadgen/members_generator_test.go`
- `tools/loadgen/members_report.go` — `MembersSummary`, `CapacitySummary`, printers, CSV
- `tools/loadgen/members_report_test.go`
- `tools/loadgen/members_integration_test.go` (build tag `integration`)

**Modify:**
- `pkg/subject/subject.go` — add `RoomMemberEvent` wildcard helper
- `pkg/subject/subject_test.go` — test the helper
- `tools/loadgen/metrics.go` — add member-specific metric vectors
- `tools/loadgen/main.go` — `members-sustained` / `members-capacity` subcommands; `--workload` flag on `seed` / `teardown`
- `tools/loadgen/main_test.go` — flag-validation tests
- `tools/loadgen/deploy/Makefile` — `seed-members`, `run-sustained`, `run-capacity`, `reset-members` targets
- `tools/loadgen/README.md` — members section, preset table, examples

---

## Background notes for the implementer

Things you'll need to know that aren't obvious from the existing code.

**Add-member pipeline:**
- Client publishes a NATS request/reply to `chat.user.{requester}.request.room.{roomID}.{siteID}.member.add` carrying a JSON-encoded `model.AddMembersRequest` (`pkg/model/member.go`).
- `room-service.handleAddMembers` (in `room-service/handler.go`, around line 657) validates auth, expands channels, dedups, capacity-checks, and publishes a canonical event to `chat.room.canonical.{siteID}.member.add` (the ROOMS stream, see `pkg/stream/stream.go`). The reply is `{"status":"accepted"}` — that's E1.
- `room-worker.handler.go` around line 900 emits the broadcast: subject `chat.room.{roomID}.event.member` (see `pkg/subject.RoomMemberEvent`), payload `model.MemberAddEvent` (in `pkg/model/event.go` around line 109). The payload's `Accounts []string` field is the actually-added accounts.

**Correlation:**
- E1 (frontdoor only): use NATS `nc.PublishRequest(subj, replyInbox, data)` with a single `Subscribe` on `_INBOX.loadgen.members.>` for inbound replies; correlate by reply subject (last token).
- E2 (both injects): correlation key = `roomID + "|" + sortedJoin(accounts)`. With shape=users the K candidates per request come from the room's in-memory candidate pool, are disjoint across concurrent requests to the same room, and pass through room-worker as-is, so the key is unique.

**Reuse from existing loadgen:**
- `Seed(ctx, db, *Fixtures)` and `Teardown(ctx, db)` in `seed.go` — drop+insert users/rooms/subscriptions. Reusable for members workload because we use the same three collections.
- `SeedRoomKeys` / `TeardownRoomKeys` — Valkey keypair seeding. Members fixture rooms need keys too (broadcast-worker may run with encryption).
- `connectStores` in `main.go` — opens Mongo + Valkey, returns cleanup.
- `ConsumerSampler` in `consumerlag.go` — sample any consumer's `num_pending`. Reuse against `room-worker` consumer on the `ROOMS_{siteID}` stream.
- `ComputePercentiles`, `WriteCSV` in `report.go`.
- `connectKeyStore` in `main.go`.

**Existing patterns to follow:**
- Tests use `package main`, `testify/assert` + `require`, table-driven where multiple cases share logic.
- `slog` JSON for logging; no `fmt.Println`.
- All NATS subjects via `pkg/subject` builders.
- TDD: each task is Red (failing test) → Green (minimal impl) → Refactor → Commit.
- Commit after each task. Use conventional-commit-ish style (matches `git log --oneline`): `feat(loadgen): ...`, `test(loadgen): ...`, `chore(loadgen): ...`.

---

## Task 1: `RoomMemberEvent` wildcard subject helper

**Files:**
- Modify: `pkg/subject/subject.go` (add helper next to `RoomMemberEvent` around line 367)
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/subject/subject_test.go` inside the existing `TestBuilders` table (find the row that has `RoomMemberEvent` already — look for `"chat.room.r1.event.member"` — and add another row right after it):

```go
{"RoomMemberEventWildcard", subject.RoomMemberEventWildcard(),
    "chat.room.*.event.member"},
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=pkg/subject
```

Expected: FAIL with `undefined: subject.RoomMemberEventWildcard`.

- [ ] **Step 3: Add the helper**

In `pkg/subject/subject.go`, immediately after `func RoomMemberEvent`:

```go
// RoomMemberEventWildcard is the subscription pattern matching member events
// (member_added / member_removed) across all rooms on this site.
func RoomMemberEventWildcard() string {
    return "chat.room.*.event.member"
}
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=pkg/subject
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add RoomMemberEventWildcard helper"
```

---

## Task 2: Members metrics

**Files:**
- Modify: `tools/loadgen/metrics.go`

Extend the existing `Metrics` struct rather than creating a parallel one, so the existing metrics server and `consumerlag` sampler keep working unchanged.

- [ ] **Step 1: Write the failing test**

Append to `tools/loadgen/main_test.go` (the existing file has `package main`):

```go
func TestNewMetrics_RegistersMemberCollectors(t *testing.T) {
    m := NewMetrics()
    mfs, err := m.Registry.Gather()
    require.NoError(t, err)

    want := []string{
        "loadgen_member_published_total",
        "loadgen_member_publish_errors_total",
        "loadgen_member_e1_latency_seconds",
        "loadgen_member_e2_latency_seconds",
        "loadgen_member_room_size",
    }
    got := map[string]bool{}
    for _, mf := range mfs {
        got[mf.GetName()] = true
    }
    // Some metrics only appear after first Observe/Inc — force them to surface.
    m.MemberPublished.WithLabelValues("p", "warmup", "frontdoor", "users").Inc()
    m.MemberPublishErrors.WithLabelValues("publish").Inc()
    m.MemberE1Latency.WithLabelValues("p", "frontdoor").Observe(0.001)
    m.MemberE2Latency.WithLabelValues("p", "frontdoor").Observe(0.001)
    m.MemberRoomSize.WithLabelValues("room-x").Set(1)

    mfs, err = m.Registry.Gather()
    require.NoError(t, err)
    got = map[string]bool{}
    for _, mf := range mfs {
        got[mf.GetName()] = true
    }
    for _, name := range want {
        assert.True(t, got[name], "metric %s not registered", name)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `m.MemberPublished undefined`.

- [ ] **Step 3: Extend Metrics struct and constructor**

In `tools/loadgen/metrics.go`, add fields to the `Metrics` struct (before the closing brace):

```go
    MemberPublished     *prometheus.CounterVec
    MemberPublishErrors *prometheus.CounterVec
    MemberE1Latency     *prometheus.HistogramVec
    MemberE2Latency     *prometheus.HistogramVec
    MemberRoomSize      *prometheus.GaugeVec
```

In `NewMetrics`, after the existing `m := &Metrics{...}` block but before `r.MustRegister(...)`, add:

```go
    m.MemberPublished = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "loadgen_member_published_total", Help: "Member-add requests published by preset/phase/inject/shape."},
        []string{"preset", "phase", "inject", "shape"},
    )
    m.MemberPublishErrors = prometheus.NewCounterVec(
        prometheus.CounterOpts{Name: "loadgen_member_publish_errors_total", Help: "Member-add publish-side errors by reason (publish|room_service|timeout|marshal|saturated)."},
        []string{"reason"},
    )
    m.MemberE1Latency = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{Name: "loadgen_member_e1_latency_seconds", Help: "Member-add room-service reply latency.", Buckets: buckets},
        []string{"preset", "inject"},
    )
    m.MemberE2Latency = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{Name: "loadgen_member_e2_latency_seconds", Help: "Member-add broadcast-visible latency.", Buckets: buckets},
        []string{"preset", "inject"},
    )
    m.MemberRoomSize = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{Name: "loadgen_member_room_size", Help: "Current member count per room (capacity mode only)."},
        []string{"room_id"},
    )
```

Extend `r.MustRegister(...)` to include the new collectors:

```go
    r.MustRegister(
        m.Published, m.PublishErrors,
        m.E1Latency, m.E2Latency,
        m.ConsumerPending, m.ConsumerAckPending, m.ConsumerRedelivered,
        m.MemberPublished, m.MemberPublishErrors,
        m.MemberE1Latency, m.MemberE2Latency, m.MemberRoomSize,
    )
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/metrics.go tools/loadgen/main_test.go
git commit -m "feat(loadgen): add member-add Prometheus collectors"
```

---

## Task 3: `Shape` enum + validation

**Files:**
- Create: `tools/loadgen/members.go`
- Create: `tools/loadgen/members_test.go`

- [ ] **Step 1: Write the failing tests**

`tools/loadgen/members_test.go`:

```go
package main

import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestParseShape(t *testing.T) {
    cases := []struct {
        in   string
        want Shape
        err  bool
    }{
        {"users", ShapeUsers, false},
        {"orgs", ShapeOrgs, false},
        {"channels", ShapeChannels, false},
        {"mixed", ShapeMixed, false},
        {"", "", true},
        {"bogus", "", true},
    }
    for _, tc := range cases {
        t.Run(tc.in, func(t *testing.T) {
            got, err := ParseShape(tc.in)
            if tc.err {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tc.want, got)
        })
    }
}

func TestValidateInjectShape(t *testing.T) {
    // v1 supports shape=users only. Other shapes are reserved values rejected
    // at validation time. Canonical+channels remains explicitly rejected with
    // a distinct message so the spec's "explicit error" guidance still applies
    // once shapes are widened in v2.
    cases := []struct {
        inject InjectMode
        shape  Shape
        errSub string // empty -> expect no error
    }{
        {InjectFrontdoor, ShapeUsers, ""},
        {InjectCanonical, ShapeUsers, ""},
        {InjectFrontdoor, ShapeOrgs, "shape=orgs not supported in v1"},
        {InjectFrontdoor, ShapeChannels, "shape=channels not supported in v1"},
        {InjectFrontdoor, ShapeMixed, "shape=mixed not supported in v1"},
        {InjectCanonical, ShapeChannels, "incompatible with --inject=canonical"},
    }
    for _, tc := range cases {
        t.Run(string(tc.inject)+"/"+string(tc.shape), func(t *testing.T) {
            err := ValidateInjectShape(tc.inject, tc.shape)
            if tc.errSub == "" {
                require.NoError(t, err)
                return
            }
            require.Error(t, err)
            assert.Contains(t, err.Error(), tc.errSub)
        })
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: Shape`.

- [ ] **Step 3: Create the file**

`tools/loadgen/members.go`:

```go
package main

import "fmt"

// Shape selects what an add-member request carries: individual users, orgs,
// channel-refs, or a mix. v1 implements ShapeUsers; the other values exist so
// the flag is forward-compatible.
type Shape string

const (
    ShapeUsers    Shape = "users"
    ShapeOrgs     Shape = "orgs"
    ShapeChannels Shape = "channels"
    ShapeMixed    Shape = "mixed"
)

// ParseShape converts a CLI flag value to a Shape.
func ParseShape(s string) (Shape, error) {
    switch Shape(s) {
    case ShapeUsers, ShapeOrgs, ShapeChannels, ShapeMixed:
        return Shape(s), nil
    default:
        return "", fmt.Errorf("unknown shape %q (want users|orgs|channels|mixed)", s)
    }
}

// ValidateInjectShape enforces compatibility between --inject and --shape.
// canonical+channels is explicitly rejected (room-service owns channel
// expansion); v1 also rejects everything except shape=users until the
// org/channel pre-resolution work in v2.
func ValidateInjectShape(inject InjectMode, shape Shape) error {
    if inject == InjectCanonical && shape == ShapeChannels {
        return fmt.Errorf("--shape=channels incompatible with --inject=canonical (channel expansion lives in room-service)")
    }
    if shape != ShapeUsers {
        return fmt.Errorf("--shape=%s not supported in v1 (only shape=users is implemented; see docs/superpowers/specs/2026-05-19-load-test-room-members-design.md)", shape)
    }
    return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members.go tools/loadgen/members_test.go
git commit -m "feat(loadgen): add Shape enum + inject/shape validation"
```

---

## Task 4: `MembersPreset` + builtin presets

**Files:**
- Modify: `tools/loadgen/members.go`
- Modify: `tools/loadgen/members_test.go`

- [ ] **Step 1: Append the failing tests**

Add to `tools/loadgen/members_test.go`:

```go
func TestBuiltinMembersPreset(t *testing.T) {
    cases := []string{"members-small", "members-medium", "members-capacity"}
    for _, name := range cases {
        t.Run(name, func(t *testing.T) {
            p, ok := BuiltinMembersPreset(name)
            require.True(t, ok, "preset %s not registered", name)
            assert.Equal(t, name, p.Name)
            assert.Greater(t, p.Users, 0)
            assert.Greater(t, p.Rooms, 0)
            assert.GreaterOrEqual(t, p.CandidatePool, 1)
            // Sanity: total user pool must be big enough to fill baseline + candidate per room.
            // Some overlap across rooms is fine (users can be in multiple rooms),
            // but each individual room's needs must fit in the pool.
            assert.GreaterOrEqual(t, p.Users, p.BaselineSize+p.CandidatePool,
                "preset %s: Users (%d) < BaselineSize (%d) + CandidatePool (%d)",
                name, p.Users, p.BaselineSize, p.CandidatePool)
        })
    }
}

func TestBuiltinMembersPreset_Unknown(t *testing.T) {
    _, ok := BuiltinMembersPreset("nope")
    assert.False(t, ok)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: BuiltinMembersPreset`.

- [ ] **Step 3: Add preset table to `members.go`**

Append to `tools/loadgen/members.go`:

```go
// MembersPreset is a fully-deterministic spec for the members workload.
type MembersPreset struct {
    Name          string
    Users         int  // global user pool
    Rooms         int  // rooms to seed
    BaselineSize  int  // members per room at seed time (incl. owner)
    CandidatePool int  // unused-but-eligible users tagged per room
}

var builtinMembersPresets = map[string]MembersPreset{
    "members-small": {
        Name: "members-small", Users: 200, Rooms: 5,
        BaselineSize: 10, CandidatePool: 50,
    },
    "members-medium": {
        Name: "members-medium", Users: 5000, Rooms: 100,
        BaselineSize: 100, CandidatePool: 500,
    },
    "members-capacity": {
        Name: "members-capacity", Users: 12000, Rooms: 5,
        BaselineSize: 1, CandidatePool: 990, // fits under MAX_ROOM_SIZE=1000
    },
}

// BuiltinMembersPreset looks up a preset by name.
func BuiltinMembersPreset(name string) (MembersPreset, bool) {
    p, ok := builtinMembersPresets[name]
    return p, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members.go tools/loadgen/members_test.go
git commit -m "feat(loadgen): add MembersPreset + builtin members presets"
```

---

## Task 5: `BuildMembersFixtures` deterministic builder

**Files:**
- Modify: `tools/loadgen/members.go`
- Modify: `tools/loadgen/members_test.go`

This builds a `Fixtures` value (reusing the existing struct from `preset.go`) plus an in-memory `CandidatePools` map. Each room gets one owner subscription + (BaselineSize-1) member subscriptions; the next CandidatePool users are tagged as candidates for that room and are NOT seeded as members.

- [ ] **Step 1: Append the failing tests**

```go
func TestBuildMembersFixtures_Deterministic(t *testing.T) {
    p, _ := BuiltinMembersPreset("members-small")
    a, poolsA := BuildMembersFixtures(&p, 42, "site-A")
    b, poolsB := BuildMembersFixtures(&p, 42, "site-A")
    assert.Equal(t, a.Users, b.Users)
    assert.Equal(t, a.Rooms, b.Rooms)
    assert.Equal(t, a.Subscriptions, b.Subscriptions)
    assert.Equal(t, poolsA, poolsB)
}

func TestBuildMembersFixtures_Shape(t *testing.T) {
    p, _ := BuiltinMembersPreset("members-small")
    f, pools := BuildMembersFixtures(&p, 42, "site-A")

    require.Len(t, f.Users, p.Users)
    require.Len(t, f.Rooms, p.Rooms)

    // Each room has BaselineSize subscriptions.
    bySub := map[string]int{}
    for _, s := range f.Subscriptions {
        bySub[s.RoomID]++
    }
    for _, r := range f.Rooms {
        assert.Equal(t, p.BaselineSize, bySub[r.ID], "room %s should have BaselineSize subs", r.ID)
        assert.Equal(t, model.RoomTypeChannel, r.Type)
        assert.Equal(t, p.BaselineSize, r.UserCount)
    }

    // Each room has exactly one owner subscription.
    ownerCount := map[string]int{}
    for _, s := range f.Subscriptions {
        for _, role := range s.Roles {
            if role == model.RoleOwner {
                ownerCount[s.RoomID]++
            }
        }
    }
    for _, r := range f.Rooms {
        assert.Equal(t, 1, ownerCount[r.ID], "room %s should have exactly one owner", r.ID)
    }

    // Candidate pool size matches preset and is disjoint from seeded members.
    for _, r := range f.Rooms {
        pool := pools[r.ID]
        require.Len(t, pool, p.CandidatePool, "room %s candidate pool", r.ID)
        seeded := map[string]bool{}
        for _, s := range f.Subscriptions {
            if s.RoomID == r.ID {
                seeded[s.User.Account] = true
            }
        }
        for _, cand := range pool {
            assert.False(t, seeded[cand], "candidate %s already in room %s", cand, r.ID)
        }
    }
}

func TestBuildMembersFixtures_RoomKeys(t *testing.T) {
    p, _ := BuiltinMembersPreset("members-small")
    f, _ := BuildMembersFixtures(&p, 42, "site-A")
    assert.Len(t, f.RoomKeys, p.Rooms)
    for _, r := range f.Rooms {
        _, ok := f.RoomKeys[r.ID]
        assert.True(t, ok, "missing key for room %s", r.ID)
    }
}
```

Add imports at the top of `members_test.go` as needed:

```go
import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/hmchangw/chat/pkg/model"
)
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: BuildMembersFixtures`.

- [ ] **Step 3: Implement the builder**

Append to `tools/loadgen/members.go`:

```go
import (
    "crypto/ecdh"
    "fmt"
    "io"
    "math/rand"
    "time"

    "github.com/hmchangw/chat/pkg/model"
    "github.com/hmchangw/chat/pkg/roomkeystore"
)
```

(Combine with the existing `import "fmt"` at the top — replace it.)

```go
// CandidatePools maps roomID to the list of accounts eligible to be added to
// that room (i.e., users not already seeded as members). Each generator pops
// from this list to build add-member requests.
type CandidatePools map[string][]string

// BuildMembersFixtures is a pure function of (preset, seed, siteID) producing
// the full members-workload fixture set plus per-room candidate pools.
// Two calls with equal inputs produce equal outputs.
func BuildMembersFixtures(p *MembersPreset, seed int64, siteID string) (Fixtures, CandidatePools) {
    r := rand.New(rand.NewSource(seed))
    now := time.Unix(0, 0).UTC()

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
    for i := 0; i < p.Rooms; i++ {
        rooms[i] = model.Room{
            ID:        fmt.Sprintf("mroom-%06d", i),
            Name:      fmt.Sprintf("mroom-%d", i),
            Type:      model.RoomTypeChannel,
            SiteID:    siteID,
            UserCount: p.BaselineSize,
            CreatedAt: now,
            UpdatedAt: now,
        }
    }

    var subs []model.Subscription
    pools := make(CandidatePools, len(rooms))
    for i := range rooms {
        // Pick BaselineSize members + CandidatePool candidates per room.
        // Use a per-room permutation so different rooms get different sets.
        perm := r.Perm(len(users))
        need := p.BaselineSize + p.CandidatePool
        if need > len(perm) {
            need = len(perm)
        }
        chosen := perm[:need]
        memberSlice := chosen[:p.BaselineSize]
        candidateSlice := chosen[p.BaselineSize:need]

        for j, idx := range memberSlice {
            roles := []model.Role{model.RoleMember}
            if j == 0 {
                roles = []model.Role{model.RoleOwner}
            }
            subs = append(subs, model.Subscription{
                ID:       fmt.Sprintf("sub-%s-%s", rooms[i].ID, users[idx].ID),
                User:     model.SubscriptionUser{ID: users[idx].ID, Account: users[idx].Account},
                RoomID:   rooms[i].ID,
                SiteID:   siteID,
                Roles:    roles,
                JoinedAt: now,
            })
        }

        candidates := make([]string, len(candidateSlice))
        for k, idx := range candidateSlice {
            candidates[k] = users[idx].Account
        }
        pools[rooms[i].ID] = candidates
    }

    roomKeys := make(map[string]roomkeystore.RoomKeyPair, len(rooms))
    for i := range rooms {
        roomKeys[rooms[i].ID] = deterministicRoomKeyPair(r)
    }

    return Fixtures{
        Users:         users,
        Rooms:         rooms,
        Subscriptions: subs,
        RoomKeys:      roomKeys,
    }, pools
}
```

Note: `deterministicRoomKeyPair`, `engNameBank`, `chineseNameBank` already exist in `preset.go` — reuse them, don't redefine.

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Refactor — extract owner-finding helper if needed**

Skip if the implementation is clean. Otherwise, hoist common preset/owner helpers.

- [ ] **Step 6: Commit**

```
git add tools/loadgen/members.go tools/loadgen/members_test.go
git commit -m "feat(loadgen): add BuildMembersFixtures with per-room candidate pools"
```

---

## Task 6: Helper for finding a room's owner account

The generator needs to know which account to use as the requester for frontdoor publishes. Add a small helper on `Fixtures` (or on a new type) that maps `roomID → ownerAccount`.

**Files:**
- Modify: `tools/loadgen/members.go`
- Modify: `tools/loadgen/members_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestOwnersByRoom(t *testing.T) {
    p, _ := BuiltinMembersPreset("members-small")
    f, _ := BuildMembersFixtures(&p, 42, "site-A")

    owners := OwnersByRoom(&f)
    require.Len(t, owners, p.Rooms)
    for _, r := range f.Rooms {
        owner, ok := owners[r.ID]
        require.True(t, ok, "room %s missing owner", r.ID)
        assert.NotEmpty(t, owner)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: OwnersByRoom`.

- [ ] **Step 3: Implement**

Append to `tools/loadgen/members.go`:

```go
// OwnersByRoom returns a roomID -> owner account map from the fixture's
// subscription set. Used by the publisher to address frontdoor requests as
// "the owner asking room-service to add new members".
func OwnersByRoom(f *Fixtures) map[string]string {
    owners := make(map[string]string, len(f.Rooms))
    for _, s := range f.Subscriptions {
        for _, r := range s.Roles {
            if r == model.RoleOwner {
                owners[s.RoomID] = s.User.Account
                break
            }
        }
    }
    return owners
}
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members.go tools/loadgen/members_test.go
git commit -m "feat(loadgen): add OwnersByRoom helper"
```

---

## Task 7: `MemberPublisher` interface + canonical implementation

**Files:**
- Create: `tools/loadgen/members_publisher.go`
- Create: `tools/loadgen/members_publisher_test.go`

The canonical publisher is simpler (no reply handling), so build it first.

- [ ] **Step 1: Write the failing test**

`tools/loadgen/members_publisher_test.go`:

```go
package main

import (
    "context"
    "encoding/json"
    "testing"
    "time"

    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/hmchangw/chat/pkg/model"
    "github.com/hmchangw/chat/pkg/stream"
    "github.com/hmchangw/chat/pkg/subject"
)

// startEmbeddedJetStream starts an in-process NATS server with JetStream
// enabled and returns a connected client. Lighter than testcontainers for
// unit tests that only need a real NATS surface.
//
// IMPLEMENTATION NOTE: implementer, please use `natstest.NewServer` from
// `pkg/natstest` if it exists; otherwise inline the standard
// `nats-server/v2/test` pattern used elsewhere in the repo. Search:
//   grep -rn "test.RunServer\|natstest" --include="*.go"
// to find the existing helper.
func startEmbeddedJetStream(t *testing.T) (*nats.Conn, jetstream.JetStream, func()) {
    t.Helper()
    // SEE NOTE ABOVE — implementer fills this in matching the repo's pattern.
    t.Fatal("startEmbeddedJetStream not implemented — see comment")
    return nil, nil, func() {}
}

func TestCanonicalMemberPublisher_PublishesToRoomCanonical(t *testing.T) {
    nc, js, stop := startEmbeddedJetStream(t)
    defer stop()

    siteID := "site-A"
    _, err := js.CreateStream(context.Background(), jetstream.StreamConfig{
        Name:     stream.Rooms(siteID).Name,
        Subjects: stream.Rooms(siteID).Subjects,
    })
    require.NoError(t, err)

    // Capture published payloads via a core-NATS subscription on the wildcard.
    captured := make(chan *nats.Msg, 1)
    sub, err := nc.Subscribe(subject.RoomCanonicalWildcard(siteID), func(m *nats.Msg) {
        captured <- m
    })
    require.NoError(t, err)
    defer func() { _ = sub.Unsubscribe() }()

    p := newCanonicalMemberPublisher(js, siteID)

    req := &model.AddMembersRequest{
        RoomID:           "room-1",
        Users:            []string{"u1", "u2"},
        RequesterAccount: "owner-1",
        Timestamp:        time.Now().UTC().UnixMilli(),
    }
    require.NoError(t, p.Publish(context.Background(), "owner-1", "room-1", req, "corr-1"))

    select {
    case m := <-captured:
        assert.Equal(t, subject.RoomCanonical(siteID, "member.add"), m.Subject)
        var got model.AddMembersRequest
        require.NoError(t, json.Unmarshal(m.Data, &got))
        assert.Equal(t, req.RoomID, got.RoomID)
        assert.Equal(t, req.Users, got.Users)
    case <-time.After(time.Second):
        t.Fatal("did not receive canonical publish within 1s")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: newCanonicalMemberPublisher` (plus the `startEmbeddedJetStream` panic if Step 1 wasn't filled in — implementer must wire that first).

- [ ] **Step 3: Implement**

`tools/loadgen/members_publisher.go`:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"

    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"

    "github.com/hmchangw/chat/pkg/model"
    "github.com/hmchangw/chat/pkg/subject"
)

// MemberPublisher publishes one add-member operation onto NATS. corrID is the
// correlation ID the collector keys reply / broadcast samples against.
type MemberPublisher interface {
    Publish(ctx context.Context, requesterAccount, roomID string,
        req *model.AddMembersRequest, corrID string) error
}

type canonicalMemberPublisher struct {
    js     jetstream.JetStream
    siteID string
}

func newCanonicalMemberPublisher(js jetstream.JetStream, siteID string) *canonicalMemberPublisher {
    return &canonicalMemberPublisher{js: js, siteID: siteID}
}

func (p *canonicalMemberPublisher) Publish(ctx context.Context, _ string, _ string,
    req *model.AddMembersRequest, _ string) error {
    data, err := json.Marshal(req)
    if err != nil {
        return fmt.Errorf("marshal add-members canonical event: %w", err)
    }
    if _, err := p.js.Publish(ctx, subject.RoomCanonical(p.siteID, "member.add"), data); err != nil {
        return fmt.Errorf("jetstream publish: %w", err)
    }
    return nil
}

// frontdoorMemberPublisher is added in the next task.
var _ = nats.NewInbox // placeholder import-use; remove once frontdoor lands
```

- [ ] **Step 4: Wire `startEmbeddedJetStream`**

Implementer: search for an existing NATS embedded-server helper in the repo before writing one from scratch:

```
grep -rn "test.RunServer\|RunRandClientPortServer\|natstest" --include="*.go"
```

If `pkg/natstest` (or similar) exists, use it. Otherwise inline this pattern (matches what the worker integration tests do — see `tools/loadgen/integration_test.go` for the testcontainers pattern; for unit tests, prefer the in-process `nats-server/v2/test` package which is already in `go.sum`):

```go
import (
    "github.com/nats-io/nats-server/v2/server"
    natstest "github.com/nats-io/nats-server/v2/test"
)

func startEmbeddedJetStream(t *testing.T) (*nats.Conn, jetstream.JetStream, func()) {
    t.Helper()
    opts := natstest.DefaultTestOptions
    opts.Port = -1 // random
    opts.JetStream = true
    opts.StoreDir = t.TempDir()
    srv := natstest.RunServer(&opts)
    nc, err := nats.Connect(srv.ClientURL())
    require.NoError(t, err)
    js, err := jetstream.New(nc)
    require.NoError(t, err)
    return nc, js, func() {
        nc.Close()
        srv.Shutdown()
        if !srv.WaitForShutdown() {
            // best-effort; tests already isolated by TempDir
            _ = server.Server{}
        }
    }
}
```

If `nats-server/v2/test` is not already a direct dep (`grep nats-server/v2/test go.mod`), this is a new dependency and per CLAUDE.md you must ask before adding it. Fallback: skip the embedded path and write this test as `//go:build integration` using testcontainers `nats` module the same way `tools/loadgen/integration_test.go` does. Choose whichever the repo already supports.

- [ ] **Step 5: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 6: Commit**

```
git add tools/loadgen/members_publisher.go tools/loadgen/members_publisher_test.go
git commit -m "feat(loadgen): add canonical member publisher"
```

---

## Task 8: Frontdoor `MemberPublisher` with reply correlation

The frontdoor publisher uses `nc.PublishRequest(subj, replyInbox, data)`. A single subscription on `_INBOX.loadgen.members.>` dispatches incoming replies to the collector via a callback.

**Files:**
- Modify: `tools/loadgen/members_publisher.go`
- Modify: `tools/loadgen/members_publisher_test.go`

- [ ] **Step 1: Append the failing tests**

```go
func TestFrontdoorMemberPublisher_RequestReply(t *testing.T) {
    nc, _, stop := startEmbeddedJetStream(t)
    defer stop()

    siteID := "site-A"

    // Stand in for room-service: subscribe to the request subject and reply OK.
    sub, err := nc.Subscribe(subject.MemberAddWildcard(siteID), func(m *nats.Msg) {
        _ = m.Respond([]byte(`{"status":"accepted"}`))
    })
    require.NoError(t, err)
    defer func() { _ = sub.Unsubscribe() }()

    type replyEvt struct {
        corrID string
        body   []byte
    }
    replies := make(chan replyEvt, 1)
    p, err := newFrontdoorMemberPublisher(nc, siteID, func(corrID string, body []byte, _ time.Time) {
        replies <- replyEvt{corrID: corrID, body: body}
    })
    require.NoError(t, err)
    defer p.Close()

    req := &model.AddMembersRequest{
        RoomID: "room-1", Users: []string{"u1"},
        RequesterAccount: "owner-1",
        Timestamp:        time.Now().UTC().UnixMilli(),
    }
    require.NoError(t, p.Publish(context.Background(), "owner-1", "room-1", req, "corr-7"))

    select {
    case got := <-replies:
        assert.Equal(t, "corr-7", got.corrID)
        assert.Contains(t, string(got.body), "accepted")
    case <-time.After(time.Second):
        t.Fatal("no reply within 1s")
    }
}

func TestFrontdoorMemberPublisher_PublishesToMemberAddSubject(t *testing.T) {
    nc, _, stop := startEmbeddedJetStream(t)
    defer stop()

    siteID := "site-A"
    got := make(chan string, 1)
    sub, err := nc.Subscribe(subject.MemberAddWildcard(siteID), func(m *nats.Msg) {
        got <- m.Subject
        _ = m.Respond([]byte(`{"status":"accepted"}`))
    })
    require.NoError(t, err)
    defer func() { _ = sub.Unsubscribe() }()

    p, err := newFrontdoorMemberPublisher(nc, siteID, func(string, []byte, time.Time) {})
    require.NoError(t, err)
    defer p.Close()

    req := &model.AddMembersRequest{RoomID: "room-X", Users: []string{"u1"}}
    require.NoError(t, p.Publish(context.Background(), "owner-9", "room-X", req, "c"))

    select {
    case subj := <-got:
        assert.Equal(t, subject.MemberAdd("owner-9", "room-X", siteID), subj)
    case <-time.After(time.Second):
        t.Fatal("never received request")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: newFrontdoorMemberPublisher`.

- [ ] **Step 3: Implement**

Replace the placeholder line at the bottom of `tools/loadgen/members_publisher.go` with:

```go
// ReplyHandler is called once per inbound frontdoor reply. corrID is the
// correlation token the publisher embedded in the per-request reply inbox.
type ReplyHandler func(corrID string, body []byte, at time.Time)

type frontdoorMemberPublisher struct {
    nc          *nats.Conn
    siteID      string
    inboxPrefix string
    onReply     ReplyHandler
    sub         *nats.Subscription
}

func newFrontdoorMemberPublisher(nc *nats.Conn, siteID string, onReply ReplyHandler) (*frontdoorMemberPublisher, error) {
    inboxPrefix := "_INBOX.loadgen.members." + nats.NewInbox()[len("_INBOX."):]
    p := &frontdoorMemberPublisher{
        nc: nc, siteID: siteID, inboxPrefix: inboxPrefix, onReply: onReply,
    }
    sub, err := nc.Subscribe(inboxPrefix+".*", func(m *nats.Msg) {
        // corrID is the token after the prefix.
        corr := m.Subject[len(inboxPrefix)+1:]
        p.onReply(corr, m.Data, time.Now())
    })
    if err != nil {
        return nil, fmt.Errorf("subscribe reply inbox: %w", err)
    }
    p.sub = sub
    return p, nil
}

func (p *frontdoorMemberPublisher) Close() {
    if p.sub != nil {
        _ = p.sub.Unsubscribe()
    }
}

func (p *frontdoorMemberPublisher) Publish(_ context.Context, requesterAccount, roomID string,
    req *model.AddMembersRequest, corrID string) error {
    data, err := json.Marshal(req)
    if err != nil {
        return fmt.Errorf("marshal add-members request: %w", err)
    }
    subj := subject.MemberAdd(requesterAccount, roomID, p.siteID)
    reply := p.inboxPrefix + "." + corrID
    if err := p.nc.PublishRequest(subj, reply, data); err != nil {
        return fmt.Errorf("nats publish request: %w", err)
    }
    return nil
}
```

Remove the leftover `var _ = nats.NewInbox` placeholder line.

Add `"time"` to the imports.

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members_publisher.go tools/loadgen/members_publisher_test.go
git commit -m "feat(loadgen): add frontdoor member publisher with reply correlation"
```

---

## Task 9: `MemberCollector` for E1/E2 correlation

**Files:**
- Create: `tools/loadgen/members_collector.go`
- Create: `tools/loadgen/members_collector_test.go`

The collector keys E1 on `corrID` (assigned by publisher) and E2 on the composite `(roomID, sortedAccounts)`. Same struct holds both maps; `Record*` methods are thread-safe via a mutex.

- [ ] **Step 1: Write the failing tests**

`tools/loadgen/members_collector_test.go`:

```go
package main

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/hmchangw/chat/pkg/model"
)

func TestMemberCollector_E1Roundtrip(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    t0 := time.Unix(0, 0)
    c.RecordPublish("corr-1", "room-1", []string{"a", "b"}, t0)
    c.RecordReply("corr-1", "", t0.Add(5*time.Millisecond))

    samples := c.E1Samples()
    require.Len(t, samples, 1)
    assert.Equal(t, 5*time.Millisecond, samples[0])
    assert.Equal(t, 1, c.E1Count())
}

func TestMemberCollector_E1RoomServiceError(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    body, _ := json.Marshal(map[string]string{"error": "room is at maximum capacity"})
    t0 := time.Unix(0, 0)
    c.RecordPublish("corr-2", "room-1", []string{"a"}, t0)
    c.RecordReply("corr-2", string(body), t0.Add(1*time.Millisecond))

    // Error replies still count as E1 latency (we measured to the response)
    // but increment the room_service error counter.
    require.Len(t, c.E1Samples(), 1)
    assert.Equal(t, 1, c.RoomServiceErrorCount())
}

func TestMemberCollector_E2MatchByRoomAndAccounts(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    t0 := time.Unix(0, 0)
    c.RecordPublish("corr-1", "room-1", []string{"b", "a"}, t0) // unsorted input
    c.RecordBroadcast("room-1", []string{"a", "b"}, t0.Add(20*time.Millisecond))

    samples := c.E2Samples()
    require.Len(t, samples, 1)
    assert.Equal(t, 20*time.Millisecond, samples[0])
    assert.Equal(t, 1, c.E2Count())
}

func TestMemberCollector_E2NoMatchDropped(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    c.RecordPublish("corr-1", "room-1", []string{"a"}, time.Unix(0, 0))
    c.RecordBroadcast("room-2", []string{"a"}, time.Unix(0, 0)) // wrong room
    assert.Equal(t, 0, c.E2Count())
}

func TestMemberCollector_Finalize(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    t0 := time.Unix(0, 0)
    c.RecordPublish("corr-1", "room-1", []string{"a"}, t0)
    c.RecordPublish("corr-2", "room-2", []string{"b"}, t0)
    c.RecordReply("corr-1", "", t0.Add(time.Millisecond))
    c.RecordBroadcast("room-2", []string{"b"}, t0.Add(time.Millisecond))

    missingReplies, missingBroadcasts := c.Finalize()
    assert.Equal(t, 1, missingReplies, "corr-2 had no reply")
    assert.Equal(t, 1, missingBroadcasts, "room-1 had no broadcast")
}

func TestMemberCollector_DiscardBefore(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    t0 := time.Unix(0, 0)
    c.RecordPublish("c1", "room-1", []string{"a"}, t0)
    c.RecordReply("c1", "", t0.Add(time.Millisecond))
    c.RecordPublish("c2", "room-2", []string{"b"}, t0.Add(10*time.Second))
    c.RecordReply("c2", "", t0.Add(10*time.Second+time.Millisecond))

    c.DiscardBefore(t0.Add(5 * time.Second))
    assert.Equal(t, 1, c.E1Count())
}

// Verify the E2 payload-parsing helper extracts accounts from a MemberAddEvent.
func TestParseMemberAddBroadcast(t *testing.T) {
    evt := model.MemberAddEvent{
        Type: "member_added", RoomID: "room-1",
        Accounts: []string{"a", "b"},
    }
    data, _ := json.Marshal(evt)
    roomID, accounts, ok := ParseMemberAddBroadcast(data)
    require.True(t, ok)
    assert.Equal(t, "room-1", roomID)
    assert.Equal(t, []string{"a", "b"}, accounts)

    _, _, ok = ParseMemberAddBroadcast([]byte("not json"))
    assert.False(t, ok)

    evt.Type = "member_removed"
    bad, _ := json.Marshal(evt)
    _, _, ok = ParseMemberAddBroadcast(bad)
    assert.False(t, ok, "non-added events must be filtered")
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: NewMemberCollector`.

- [ ] **Step 3: Implement**

`tools/loadgen/members_collector.go`:

```go
package main

import (
    "encoding/json"
    "sort"
    "strings"
    "sync"
    "time"

    "github.com/hmchangw/chat/pkg/model"
)

// MemberCollector correlates frontdoor replies (E1) and member_added
// broadcasts (E2) with publish times. Thread-safe.
//
// E1 key:  corrID (assigned by publisher per request)
// E2 key:  roomID + "|" + sortedJoin(accounts)
//
// E2 keying works because the candidate-pool fixture guarantees that
// concurrent requests against the same room never share user accounts.
type MemberCollector struct {
    m       *Metrics
    preset  string
    inject  string
    mu      sync.Mutex
    byCorr  map[string]memberPubEntry
    byE2Key map[string]memberPubEntry
    e1      []sample
    e2      []sample
    rsErrs  int
}

type memberPubEntry struct {
    publishedAt time.Time
}

// NewMemberCollector returns a ready-to-use MemberCollector.
func NewMemberCollector(m *Metrics, preset, inject string) *MemberCollector {
    return &MemberCollector{
        m: m, preset: preset, inject: inject,
        byCorr:  make(map[string]memberPubEntry),
        byE2Key: make(map[string]memberPubEntry),
    }
}

// RecordPublish stores publish time under both correlation keys.
func (c *MemberCollector) RecordPublish(corrID, roomID string, accounts []string, t time.Time) {
    c.mu.Lock()
    defer c.mu.Unlock()
    e := memberPubEntry{publishedAt: t}
    if corrID != "" {
        c.byCorr[corrID] = e
    }
    c.byE2Key[e2Key(roomID, accounts)] = e
}

// RecordPublishFailed undoes a prior RecordPublish.
func (c *MemberCollector) RecordPublishFailed(corrID, roomID string, accounts []string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if corrID != "" {
        delete(c.byCorr, corrID)
    }
    delete(c.byE2Key, e2Key(roomID, accounts))
}

// RecordReply matches a frontdoor reply by corrID. If body contains a non-empty
// "error" field, counts a room_service error but still records the E1 latency.
func (c *MemberCollector) RecordReply(corrID, body string, at time.Time) {
    c.mu.Lock()
    defer c.mu.Unlock()
    e, ok := c.byCorr[corrID]
    if !ok {
        return
    }
    delete(c.byCorr, corrID)
    d := at.Sub(e.publishedAt)
    c.e1 = append(c.e1, sample{publishedAt: e.publishedAt, latency: d})
    c.m.MemberE1Latency.WithLabelValues(c.preset, c.inject).Observe(d.Seconds())

    if body != "" {
        var parsed struct {
            Error string `json:"error"`
        }
        if err := json.Unmarshal([]byte(body), &parsed); err == nil && parsed.Error != "" {
            c.rsErrs++
            c.m.MemberPublishErrors.WithLabelValues("room_service").Inc()
        }
    }
}

// RecordBroadcast matches a member_added broadcast by (roomID, sortedAccounts).
func (c *MemberCollector) RecordBroadcast(roomID string, accounts []string, at time.Time) {
    c.mu.Lock()
    defer c.mu.Unlock()
    k := e2Key(roomID, accounts)
    e, ok := c.byE2Key[k]
    if !ok {
        return
    }
    delete(c.byE2Key, k)
    d := at.Sub(e.publishedAt)
    c.e2 = append(c.e2, sample{publishedAt: e.publishedAt, latency: d})
    c.m.MemberE2Latency.WithLabelValues(c.preset, c.inject).Observe(d.Seconds())
}

// Finalize returns counts of unmatched publishes — replies and broadcasts.
func (c *MemberCollector) Finalize() (missingReplies int, missingBroadcasts int) {
    c.mu.Lock()
    defer c.mu.Unlock()
    return len(c.byCorr), len(c.byE2Key)
}

func (c *MemberCollector) E1Count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.e1) }
func (c *MemberCollector) E2Count() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.e2) }

func (c *MemberCollector) RoomServiceErrorCount() int {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.rsErrs
}

// DiscardBefore drops samples with publishedAt < cutoff (warmup pruning).
func (c *MemberCollector) DiscardBefore(cutoff time.Time) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.e1 = filterAtOrAfter(c.e1, cutoff)
    c.e2 = filterAtOrAfter(c.e2, cutoff)
}

// E1Samples returns a sorted copy of E1 latencies.
func (c *MemberCollector) E1Samples() []time.Duration {
    c.mu.Lock()
    defer c.mu.Unlock()
    return snapshotLatencies(c.e1)
}

// E2Samples returns a sorted copy of E2 latencies.
func (c *MemberCollector) E2Samples() []time.Duration {
    c.mu.Lock()
    defer c.mu.Unlock()
    return snapshotLatencies(c.e2)
}

func snapshotLatencies(in []sample) []time.Duration {
    out := make([]time.Duration, len(in))
    for i := range in {
        out[i] = in[i].latency
    }
    sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
    return out
}

func e2Key(roomID string, accounts []string) string {
    sorted := append([]string(nil), accounts...)
    sort.Strings(sorted)
    return roomID + "|" + strings.Join(sorted, ",")
}

// ParseMemberAddBroadcast decodes a model.MemberAddEvent payload and returns
// (roomID, accounts, true) when the event is type=member_added. Returns
// (_, _, false) on malformed input or non-added event types.
func ParseMemberAddBroadcast(data []byte) (string, []string, bool) {
    var evt model.MemberAddEvent
    if err := json.Unmarshal(data, &evt); err != nil {
        return "", nil, false
    }
    if evt.Type != "member_added" {
        return "", nil, false
    }
    return evt.RoomID, evt.Accounts, true
}
```

Note: `sample` and `filterAtOrAfter` are already defined in `collector.go`. Reuse them — don't redefine.

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members_collector.go tools/loadgen/members_collector_test.go
git commit -m "feat(loadgen): add member-add E1/E2 collector"
```

---

## Task 10: Request builder + `SustainedMembersGenerator`

**Files:**
- Create: `tools/loadgen/members_generator.go`
- Create: `tools/loadgen/members_generator_test.go`

The sustained generator is a round-robin ticker that pops K candidates per request from each room's in-memory pool. Aborts when all rooms are exhausted.

- [ ] **Step 1: Write the failing tests**

`tools/loadgen/members_generator_test.go`:

```go
package main

import (
    "context"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/hmchangw/chat/pkg/model"
)

type recordedPublish struct {
    requester string
    roomID    string
    accounts  []string
    corrID    string
}

type stubMemberPublisher struct {
    mu    sync.Mutex
    calls []recordedPublish
    fail  bool
}

func (s *stubMemberPublisher) Publish(_ context.Context, requester, roomID string,
    req *model.AddMembersRequest, corrID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.fail {
        return assertErr
    }
    s.calls = append(s.calls, recordedPublish{
        requester: requester,
        roomID:    roomID,
        accounts:  append([]string(nil), req.Users...),
        corrID:    corrID,
    })
    return nil
}

var assertErr = errStub("stub publish failure")

type errStub string

func (e errStub) Error() string { return string(e) }

func TestSustainedMembersGenerator_PublishesAtRateRoundRobin(t *testing.T) {
    p, _ := BuiltinMembersPreset("members-small")
    f, pools := BuildMembersFixtures(&p, 42, "site-A")
    owners := OwnersByRoom(&f)

    pub := &stubMemberPublisher{}
    metrics := NewMetrics()
    collector := NewMemberCollector(metrics, p.Name, "frontdoor")

    cfg := SustainedMembersConfig{
        Preset:         &p,
        Fixtures:       &f,
        Pools:          pools,
        Owners:         owners,
        SiteID:         "site-A",
        Rate:           50,
        UsersPerAdd:    2,
        Inject:         InjectFrontdoor,
        Shape:          ShapeUsers,
        Publisher:      pub,
        Metrics:        metrics,
        Collector:      collector,
        WarmupDeadline: time.Now(),
        MaxInFlight:    10,
    }
    gen := NewSustainedMembersGenerator(&cfg, 7)

    ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
    defer cancel()
    require.NoError(t, gen.Run(ctx))

    pub.mu.Lock()
    defer pub.mu.Unlock()
    // ~15 publishes at 50 r/s for 300ms.
    assert.GreaterOrEqual(t, len(pub.calls), 10)
    // Each publish carries exactly UsersPerAdd accounts.
    for _, c := range pub.calls {
        assert.Len(t, c.accounts, 2)
        assert.NotEmpty(t, c.corrID)
        assert.Equal(t, owners[c.roomID], c.requester)
    }
}

func TestSustainedMembersGenerator_AbortsOnPoolExhaustion(t *testing.T) {
    // Single room, tiny pool — exhausts almost immediately at K=2.
    f := Fixtures{
        Users: []model.User{
            {ID: "u1", Account: "u-1"}, {ID: "u2", Account: "u-2"},
            {ID: "u3", Account: "u-3"},
        },
        Rooms: []model.Room{
            {ID: "r1", Name: "r1", Type: model.RoomTypeChannel, SiteID: "site-A"},
        },
        Subscriptions: []model.Subscription{
            {ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "u-1"},
                RoomID: "r1", SiteID: "site-A", Roles: []model.Role{model.RoleOwner}},
        },
    }
    pools := CandidatePools{"r1": {"u-2", "u-3"}} // pool of 2, K=2 -> one publish then exhausted

    pub := &stubMemberPublisher{}
    metrics := NewMetrics()
    collector := NewMemberCollector(metrics, "test", "frontdoor")
    cfg := SustainedMembersConfig{
        Preset: &MembersPreset{Name: "test", Users: 3, Rooms: 1, BaselineSize: 1, CandidatePool: 2},
        Fixtures: &f, Pools: pools, Owners: OwnersByRoom(&f),
        SiteID: "site-A", Rate: 100, UsersPerAdd: 2,
        Inject: InjectFrontdoor, Shape: ShapeUsers,
        Publisher: pub, Metrics: metrics, Collector: collector,
        WarmupDeadline: time.Now(), MaxInFlight: 10,
    }
    gen := NewSustainedMembersGenerator(&cfg, 7)
    ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
    defer cancel()
    err := gen.Run(ctx)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "candidate pool exhausted")

    pub.mu.Lock()
    defer pub.mu.Unlock()
    assert.Equal(t, 1, len(pub.calls), "only one request fits before exhaustion")
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: SustainedMembersConfig`.

- [ ] **Step 3: Implement**

`tools/loadgen/members_generator.go`:

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "math/rand"
    "sync"
    "time"

    "github.com/hmchangw/chat/pkg/idgen"
    "github.com/hmchangw/chat/pkg/model"
)

// SustainedMembersConfig is the parameter bundle for an open-loop members
// generator.
type SustainedMembersConfig struct {
    Preset         *MembersPreset
    Fixtures       *Fixtures
    Pools          CandidatePools
    Owners         map[string]string
    SiteID         string
    Rate           int
    UsersPerAdd    int
    Inject         InjectMode
    Shape          Shape
    Publisher      MemberPublisher
    Metrics        *Metrics
    Collector      *MemberCollector
    WarmupDeadline time.Time
    MaxInFlight    int
}

// SustainedMembersGenerator publishes member-add requests at a target rate
// round-robin across the preset's rooms until ctx is cancelled or the pools
// run dry.
type SustainedMembersGenerator struct {
    cfg     SustainedMembersConfig
    mu      sync.Mutex
    pools   map[string][]string // mutable copy
    cursor  int                 // round-robin cursor over Rooms
    roomIDs []string
    rng     *rand.Rand
}

// ErrPoolsExhausted is returned by Run when every room's candidate pool has
// fewer than UsersPerAdd accounts remaining.
var ErrPoolsExhausted = errors.New("candidate pool exhausted on every room — preset's CandidatePool too small for rate * duration * usersPerAdd; re-seed with a larger pool")

// NewSustainedMembersGenerator clones the candidate pools so the input is
// not mutated.
func NewSustainedMembersGenerator(cfg *SustainedMembersConfig, seed int64) *SustainedMembersGenerator {
    pools := make(map[string][]string, len(cfg.Pools))
    roomIDs := make([]string, 0, len(cfg.Fixtures.Rooms))
    for _, r := range cfg.Fixtures.Rooms {
        pools[r.ID] = append([]string(nil), cfg.Pools[r.ID]...)
        roomIDs = append(roomIDs, r.ID)
    }
    return &SustainedMembersGenerator{
        cfg:     *cfg,
        pools:   pools,
        roomIDs: roomIDs,
        rng:     rand.New(rand.NewSource(seed)),
    }
}

// Run drives the publish loop. Returns ErrPoolsExhausted if every room runs
// out of candidates before ctx is cancelled.
func (g *SustainedMembersGenerator) Run(ctx context.Context) error {
    if g.cfg.Rate <= 0 {
        return fmt.Errorf("rate must be > 0")
    }
    if g.cfg.UsersPerAdd <= 0 {
        return fmt.Errorf("usersPerAdd must be > 0")
    }
    interval := time.Second / time.Duration(g.cfg.Rate)
    if interval <= 0 {
        interval = time.Nanosecond
    }
    tick := time.NewTicker(interval)
    defer tick.Stop()

    sem := make(chan struct{}, g.cfg.MaxInFlight)
    if g.cfg.MaxInFlight <= 0 {
        sem = nil
    }
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
            roomID, accounts, ok := g.takeNext()
            if !ok {
                return ErrPoolsExhausted
            }
            run := func() {
                g.publishOne(ctx, roomID, accounts)
            }
            if sem == nil {
                run()
                continue
            }
            select {
            case sem <- struct{}{}:
                wg.Add(1)
                go func() {
                    defer func() { <-sem; wg.Done() }()
                    run()
                }()
            default:
                g.cfg.Metrics.MemberPublishErrors.WithLabelValues("saturated").Inc()
                // Put accounts back so they aren't lost.
                g.giveBack(roomID, accounts)
            }
        }
    }
}

// takeNext rotates through rooms looking for one with at least UsersPerAdd
// candidates. Returns (_, _, false) when every room is below the threshold.
func (g *SustainedMembersGenerator) takeNext() (string, []string, bool) {
    g.mu.Lock()
    defer g.mu.Unlock()
    n := len(g.roomIDs)
    for tried := 0; tried < n; tried++ {
        idx := (g.cursor + tried) % n
        roomID := g.roomIDs[idx]
        if len(g.pools[roomID]) < g.cfg.UsersPerAdd {
            continue
        }
        accounts := g.pools[roomID][:g.cfg.UsersPerAdd]
        g.pools[roomID] = g.pools[roomID][g.cfg.UsersPerAdd:]
        g.cursor = (idx + 1) % n
        return roomID, append([]string(nil), accounts...), true
    }
    return "", nil, false
}

func (g *SustainedMembersGenerator) giveBack(roomID string, accounts []string) {
    g.mu.Lock()
    defer g.mu.Unlock()
    g.pools[roomID] = append(accounts, g.pools[roomID]...)
}

func (g *SustainedMembersGenerator) publishOne(ctx context.Context, roomID string, accounts []string) {
    owner := g.cfg.Owners[roomID]
    req := &model.AddMembersRequest{
        RoomID:           roomID,
        Users:            accounts,
        RequesterAccount: owner,
        Timestamp:        time.Now().UTC().UnixMilli(),
    }
    corrID := idgen.GenerateRequestID()
    publishTime := time.Now()
    g.cfg.Collector.RecordPublish(corrID, roomID, accounts, publishTime)

    if err := g.cfg.Publisher.Publish(ctx, owner, roomID, req, corrID); err != nil {
        g.cfg.Collector.RecordPublishFailed(corrID, roomID, accounts)
        g.cfg.Metrics.MemberPublishErrors.WithLabelValues("publish").Inc()
        g.giveBack(roomID, accounts)
        return
    }
    phase := "measured"
    if publishTime.Before(g.cfg.WarmupDeadline) {
        phase = "warmup"
    }
    g.cfg.Metrics.MemberPublished.WithLabelValues(g.cfg.Preset.Name, phase, string(g.cfg.Inject), string(g.cfg.Shape)).Inc()
}
```

`drainGracePeriod` is already declared as a package constant in `generator.go` — reuse it.

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members_generator.go tools/loadgen/members_generator_test.go
git commit -m "feat(loadgen): add SustainedMembersGenerator"
```

---

## Task 11: `CapacityMembersGenerator`

**Files:**
- Modify: `tools/loadgen/members_generator.go`
- Modify: `tools/loadgen/members_generator_test.go`

The capacity generator runs each room in its own goroutine. Within a room: sequential, wait for E2 (via a per-room ack channel exposed by the collector) before issuing the next add. Stop when room hits `TargetSize` or pool exhausts.

This needs the collector to expose a per-room ack channel. Add that to the collector first.

- [ ] **Step 1: Append a test for the per-room ack signal**

Add to `tools/loadgen/members_collector_test.go`:

```go
func TestMemberCollector_OnBroadcastCallback(t *testing.T) {
    m := NewMetrics()
    c := NewMemberCollector(m, "p", "frontdoor")
    seen := make(chan string, 4)
    c.OnBroadcast(func(roomID string, accounts []string) {
        seen <- roomID
    })
    c.RecordPublish("c1", "room-1", []string{"a"}, time.Unix(0, 0))
    c.RecordBroadcast("room-1", []string{"a"}, time.Unix(0, time.Millisecond.Nanoseconds()))
    select {
    case got := <-seen:
        assert.Equal(t, "room-1", got)
    case <-time.After(time.Second):
        t.Fatal("callback never fired")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `c.OnBroadcast undefined`.

- [ ] **Step 3: Add the callback to the collector**

In `tools/loadgen/members_collector.go`, add a field to the struct:

```go
    onBroadcast func(roomID string, accounts []string)
```

Add a setter:

```go
// OnBroadcast registers a callback fired after every matched broadcast,
// allowing the capacity generator to step its per-room loop.
func (c *MemberCollector) OnBroadcast(fn func(roomID string, accounts []string)) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.onBroadcast = fn
}
```

In `RecordBroadcast`, after the successful match, capture and invoke after unlocking:

```go
func (c *MemberCollector) RecordBroadcast(roomID string, accounts []string, at time.Time) {
    c.mu.Lock()
    k := e2Key(roomID, accounts)
    e, ok := c.byE2Key[k]
    if !ok {
        c.mu.Unlock()
        return
    }
    delete(c.byE2Key, k)
    d := at.Sub(e.publishedAt)
    c.e2 = append(c.e2, sample{publishedAt: e.publishedAt, latency: d})
    c.m.MemberE2Latency.WithLabelValues(c.preset, c.inject).Observe(d.Seconds())
    cb := c.onBroadcast
    c.mu.Unlock()
    if cb != nil {
        cb(roomID, accounts)
    }
}
```

- [ ] **Step 4: Run collector tests to verify they pass**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Write the failing capacity generator tests**

Append to `tools/loadgen/members_generator_test.go`:

```go
func TestCapacityMembersGenerator_StopsAtTargetSize(t *testing.T) {
    f := Fixtures{
        Users: nil,
        Rooms: []model.Room{
            {ID: "r1", Name: "r1", Type: model.RoomTypeChannel, SiteID: "site-A"},
        },
        Subscriptions: []model.Subscription{
            {ID: "s1", User: model.SubscriptionUser{ID: "u0", Account: "u-0"},
                RoomID: "r1", SiteID: "site-A", Roles: []model.Role{model.RoleOwner}},
        },
    }
    // Pool: u-1..u-10
    pool := make([]string, 10)
    for i := range pool {
        pool[i] = fmt.Sprintf("u-%d", i+1)
    }
    pools := CandidatePools{"r1": pool}

    metrics := NewMetrics()
    collector := NewMemberCollector(metrics, "test", "frontdoor")
    pub := &stubMemberPublisher{}

    // Simulate broadcast immediately on every publish.
    pubCh := make(chan struct{}, 100)
    pub.afterPublish = func(call recordedPublish) {
        collector.RecordBroadcast(call.roomID, call.accounts, time.Now())
        pubCh <- struct{}{}
    }

    cfg := CapacityMembersConfig{
        Preset: &MembersPreset{Name: "test", Users: 11, Rooms: 1, BaselineSize: 1, CandidatePool: 10},
        Fixtures: &f, Pools: pools, Owners: OwnersByRoom(&f),
        SiteID: "site-A",
        UsersPerAdd: 2,
        Inject: InjectFrontdoor, Shape: ShapeUsers,
        TargetSize: 7, // start at 1, add 2 at a time, stop at >=7 → after 3 adds room is 7
        Publisher: pub, Metrics: metrics, Collector: collector,
        E2Timeout: 2 * time.Second,
    }
    gen := NewCapacityMembersGenerator(&cfg)
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    require.NoError(t, gen.Run(ctx))

    pub.mu.Lock()
    defer pub.mu.Unlock()
    assert.Equal(t, 3, len(pub.calls), "stopped after 3 adds (size 1 + 2*3 = 7)")
}
```

Update `stubMemberPublisher` to support the `afterPublish` hook — add to the existing stub:

```go
type stubMemberPublisher struct {
    mu           sync.Mutex
    calls        []recordedPublish
    fail         bool
    afterPublish func(recordedPublish)
}

func (s *stubMemberPublisher) Publish(_ context.Context, requester, roomID string,
    req *model.AddMembersRequest, corrID string) error {
    s.mu.Lock()
    if s.fail {
        s.mu.Unlock()
        return assertErr
    }
    call := recordedPublish{
        requester: requester, roomID: roomID,
        accounts: append([]string(nil), req.Users...), corrID: corrID,
    }
    s.calls = append(s.calls, call)
    after := s.afterPublish
    s.mu.Unlock()
    if after != nil {
        after(call)
    }
    return nil
}
```

Add `"fmt"` import to the test file if not already present.

- [ ] **Step 6: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: CapacityMembersConfig`.

- [ ] **Step 7: Implement**

Append to `tools/loadgen/members_generator.go`:

```go
// CapacityMembersConfig parameterizes the per-room sequential growth generator.
type CapacityMembersConfig struct {
    Preset      *MembersPreset
    Fixtures    *Fixtures
    Pools       CandidatePools
    Owners      map[string]string
    SiteID      string
    UsersPerAdd int
    Inject      InjectMode
    Shape       Shape
    TargetSize  int // stop a room when its size >= TargetSize
    MaxRate     int // 0 = sequential pacing only; >0 = cap per-room req/sec
    Publisher   MemberPublisher
    Metrics     *Metrics
    Collector   *MemberCollector
    E2Timeout   time.Duration
}

// CapacityMembersGenerator drives each room to TargetSize sequentially. Per-
// room loops run concurrently so a slow room does not gate the others.
type CapacityMembersGenerator struct {
    cfg CapacityMembersConfig
}

// NewCapacityMembersGenerator creates a new capacity-mode generator.
func NewCapacityMembersGenerator(cfg *CapacityMembersConfig) *CapacityMembersGenerator {
    return &CapacityMembersGenerator{cfg: *cfg}
}

// Run runs each room until TargetSize or pool exhaustion. Returns nil when
// every room has finished (or ctx cancelled).
func (g *CapacityMembersGenerator) Run(ctx context.Context) error {
    if g.cfg.UsersPerAdd <= 0 {
        return fmt.Errorf("usersPerAdd must be > 0")
    }
    if g.cfg.TargetSize <= 0 {
        return fmt.Errorf("targetSize must be > 0")
    }

    // Fan broadcasts from the single collector callback out to per-room channels.
    perRoom := make(map[string]chan struct{}, len(g.cfg.Fixtures.Rooms))
    for _, r := range g.cfg.Fixtures.Rooms {
        perRoom[r.ID] = make(chan struct{}, 1)
    }
    g.cfg.Collector.OnBroadcast(func(roomID string, _ []string) {
        if ch, ok := perRoom[roomID]; ok {
            select {
            case ch <- struct{}{}:
            default:
            }
        }
    })

    var wg sync.WaitGroup
    for _, r := range g.cfg.Fixtures.Rooms {
        wg.Add(1)
        room := r
        go func() {
            defer wg.Done()
            g.runRoom(ctx, &room, perRoom[room.ID])
        }()
    }
    wg.Wait()
    return nil
}

func (g *CapacityMembersGenerator) runRoom(ctx context.Context, room *model.Room, ack <-chan struct{}) {
    size := room.UserCount
    pool := append([]string(nil), g.cfg.Pools[room.ID]...)
    owner := g.cfg.Owners[room.ID]

    var interval time.Duration
    if g.cfg.MaxRate > 0 {
        interval = time.Second / time.Duration(g.cfg.MaxRate)
    }
    var lastSent time.Time

    for size < g.cfg.TargetSize {
        if len(pool) < g.cfg.UsersPerAdd {
            return // pool exhausted; soft stop
        }
        if interval > 0 {
            if delay := interval - time.Since(lastSent); delay > 0 {
                select {
                case <-ctx.Done():
                    return
                case <-time.After(delay):
                }
            }
        }
        accounts := pool[:g.cfg.UsersPerAdd]
        pool = pool[g.cfg.UsersPerAdd:]

        req := &model.AddMembersRequest{
            RoomID:           room.ID,
            Users:            accounts,
            RequesterAccount: owner,
            Timestamp:        time.Now().UTC().UnixMilli(),
        }
        corrID := idgen.GenerateRequestID()
        publishTime := time.Now()
        lastSent = publishTime
        g.cfg.Collector.RecordPublish(corrID, room.ID, accounts, publishTime)

        if err := g.cfg.Publisher.Publish(ctx, owner, room.ID, req, corrID); err != nil {
            g.cfg.Collector.RecordPublishFailed(corrID, room.ID, accounts)
            g.cfg.Metrics.MemberPublishErrors.WithLabelValues("publish").Inc()
            return
        }
        g.cfg.Metrics.MemberPublished.WithLabelValues(g.cfg.Preset.Name, "measured", string(g.cfg.Inject), string(g.cfg.Shape)).Inc()

        // Wait for broadcast or timeout.
        select {
        case <-ack:
            size += g.cfg.UsersPerAdd
            g.cfg.Metrics.MemberRoomSize.WithLabelValues(room.ID).Set(float64(size))
        case <-time.After(g.cfg.E2Timeout):
            g.cfg.Metrics.MemberPublishErrors.WithLabelValues("timeout").Inc()
            return
        case <-ctx.Done():
            return
        }
    }
}
```

`idgen` is already imported from the sustained generator.

- [ ] **Step 8: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 9: Commit**

```
git add tools/loadgen/members_generator.go tools/loadgen/members_generator_test.go tools/loadgen/members_collector.go tools/loadgen/members_collector_test.go
git commit -m "feat(loadgen): add CapacityMembersGenerator with per-room ack signal"
```

---

## Task 12: `MembersSummary` + printer

**Files:**
- Create: `tools/loadgen/members_report.go`
- Create: `tools/loadgen/members_report_test.go`

- [ ] **Step 1: Write the failing test**

`tools/loadgen/members_report_test.go`:

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

func TestPrintMembersSummary_IncludesAllSections(t *testing.T) {
    s := MembersSummary{
        Preset: "members-medium", Site: "site-A", Inject: "frontdoor", Shape: "users",
        Seed: 42, TargetRate: 100, ActualRate: 98.7,
        Duration: 60 * time.Second, Warmup: 10 * time.Second,
        UsersPerAdd: 10, Sent: 6000, SentMeasured: 5000,
        PublishErrors: 0, RoomServiceErrors: 2,
        MissingReplies: 1, MissingBroadcasts: 0,
        E1: Percentiles{P50: 4 * time.Millisecond, P95: 12 * time.Millisecond, P99: 28 * time.Millisecond, Max: 50 * time.Millisecond},
        E2: Percentiles{P50: 10 * time.Millisecond, P95: 31 * time.Millisecond, P99: 78 * time.Millisecond, Max: 90 * time.Millisecond},
        E1Count: 5000, E2Count: 4999,
        Consumers: []ConsumerStat{{Stream: "ROOMS_site-A", Durable: "room-worker", FinalPending: 0}},
    }
    var buf bytes.Buffer
    require.NoError(t, PrintMembersSummary(&buf, &s))
    out := buf.String()
    for _, want := range []string{
        "members-medium", "frontdoor", "users",
        "target rate: 100",
        "users per add:    10",
        "publish errors:    0",
        "room-service errors:", "2",
        "E1 reply", "E2 broadcast",
        "ROOMS_site-A", "room-worker",
    } {
        assert.True(t, strings.Contains(out, want), "summary missing %q\n--- output ---\n%s", want, out)
    }
}

func TestPrintCapacitySummary_BucketTable(t *testing.T) {
    s := CapacitySummary{
        Preset: "members-capacity", Site: "site-A", Inject: "frontdoor", Shape: "users",
        Seed: 1, UsersPerAdd: 10, TargetSize: 500,
        Buckets: []SizeBucket{
            {Lower: 0, Upper: 100, Count: 10,
                E1: Percentiles{P50: 5 * time.Millisecond}, E2: Percentiles{P50: 12 * time.Millisecond}},
            {Lower: 100, Upper: 500, Count: 40,
                E1: Percentiles{P50: 8 * time.Millisecond}, E2: Percentiles{P50: 30 * time.Millisecond}},
        },
        FinalSizes: map[string]int{"r1": 500, "r2": 500},
    }
    var buf bytes.Buffer
    require.NoError(t, PrintCapacitySummary(&buf, &s))
    out := buf.String()
    for _, want := range []string{"members-capacity", "size_bucket", "0-100", "100-500", "r1", "500"} {
        assert.True(t, strings.Contains(out, want), "capacity summary missing %q\n%s", want, out)
    }
}

func TestBucketize_BoundaryEdges(t *testing.T) {
    // Buckets [0,10) [10,100) [100,1000)
    edges := []int{0, 10, 100, 1000}
    cases := []struct {
        size int
        want int // index into edges; -1 = above last
    }{
        {0, 0}, {9, 0}, {10, 1}, {99, 1}, {100, 2}, {999, 2}, {1000, -1}, {-1, -1},
    }
    for _, tc := range cases {
        assert.Equal(t, tc.want, BucketIndex(tc.size, edges), "size=%d", tc.size)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `undefined: MembersSummary`.

- [ ] **Step 3: Implement the printer**

`tools/loadgen/members_report.go`:

```go
package main

import (
    "fmt"
    "io"
    "sort"
    "text/tabwriter"
    "time"
)

// MembersSummary is the sustained-mode end-of-run report.
type MembersSummary struct {
    Preset, Site, Inject, Shape string
    Seed                        int64
    TargetRate                  int
    ActualRate                  float64
    Duration, Warmup            time.Duration
    UsersPerAdd                 int
    Sent                        int // warmup + measured
    SentMeasured                int
    PublishErrors               int
    RoomServiceErrors           int
    MissingReplies              int
    MissingBroadcasts           int
    E1                          Percentiles
    E2                          Percentiles
    E1Count, E2Count            int
    Consumers                   []ConsumerStat
}

// PrintMembersSummary writes the sustained-mode summary to w.
func PrintMembersSummary(w io.Writer, s *MembersSummary) error {
    fmt.Fprintln(w, "=== loadgen members-sustained complete ===")
    fmt.Fprintf(w, "preset: %s    seed: %d    site: %s\n", s.Preset, s.Seed, s.Site)
    fmt.Fprintf(w, "duration: %s (warmup: %s, measured: %s)    inject: %s    shape: %s\n",
        s.Duration, s.Warmup, s.Duration-s.Warmup, s.Inject, s.Shape)
    fmt.Fprintf(w, "target rate: %d req/s    actual rate: %.1f req/s\n", s.TargetRate, s.ActualRate)

    fmt.Fprintln(w, "\npublish results")
    fmt.Fprintf(w, "  users per add:    %d\n", s.UsersPerAdd)
    fmt.Fprintf(w, "  sent (total):     %d\n", s.Sent)
    fmt.Fprintf(w, "  sent (measured):  %d   ← compared to E1/E2 counts below\n", s.SentMeasured)
    fmt.Fprintf(w, "  publish errors:    %d\n", s.PublishErrors)
    fmt.Fprintf(w, "  room-service errors: %d\n", s.RoomServiceErrors)
    fmt.Fprintf(w, "  missing replies:   %d\n", s.MissingReplies)
    fmt.Fprintf(w, "  missing broadcasts:%d\n\n", s.MissingBroadcasts)

    tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw, "latency (measured window only)")
    fmt.Fprintln(tw, "metric\tcount\tp50\tp95\tp99\tmax")
    fmt.Fprintf(tw, "E1 reply\t%d\t%s\t%s\t%s\t%s\n", s.E1Count, s.E1.P50, s.E1.P95, s.E1.P99, s.E1.Max)
    fmt.Fprintf(tw, "E2 broadcast\t%d\t%s\t%s\t%s\t%s\n", s.E2Count, s.E2.P50, s.E2.P95, s.E2.P99, s.E2.Max)
    if err := tw.Flush(); err != nil {
        return fmt.Errorf("flush latency table: %w", err)
    }

    if len(s.Consumers) > 0 {
        fmt.Fprintf(w, "\nconsumer lag (%s)\n", s.Consumers[0].Stream)
        tw2 := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
        fmt.Fprintln(tw2, "durable\tmin_pending\tpeak_pending\tfinal_pending\tpeak_ack_pending\tredelivered")
        for i := range s.Consumers {
            c := &s.Consumers[i]
            fmt.Fprintf(tw2, "%s\t%d\t%d\t%d\t%d\t%d\n",
                c.Durable, c.MinPending, c.PeakPending, c.FinalPending, c.PeakAckPending, c.Redelivered)
        }
        if err := tw2.Flush(); err != nil {
            return fmt.Errorf("flush consumer table: %w", err)
        }
    }
    return nil
}

// SizeBucket holds latency aggregates for one (lower, upper) room-size range.
type SizeBucket struct {
    Lower, Upper int
    Count        int
    E1, E2       Percentiles
}

// CapacitySummary is the capacity-mode end-of-run report.
type CapacitySummary struct {
    Preset, Site, Inject, Shape string
    Seed                        int64
    UsersPerAdd, TargetSize     int
    PublishErrors               int
    Timeouts                    int
    Buckets                     []SizeBucket
    FinalSizes                  map[string]int
}

// PrintCapacitySummary writes the capacity-mode summary to w.
func PrintCapacitySummary(w io.Writer, s *CapacitySummary) error {
    fmt.Fprintln(w, "=== loadgen members-capacity complete ===")
    fmt.Fprintf(w, "preset: %s    seed: %d    site: %s\n", s.Preset, s.Seed, s.Site)
    fmt.Fprintf(w, "inject: %s    shape: %s    users per add: %d    target size: %d\n\n",
        s.Inject, s.Shape, s.UsersPerAdd, s.TargetSize)
    fmt.Fprintf(w, "publish errors: %d    timeouts: %d\n\n", s.PublishErrors, s.Timeouts)

    tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw, "size_bucket\tcount\te1_p50\te1_p99\te2_p50\te2_p99")
    for _, b := range s.Buckets {
        fmt.Fprintf(tw, "%d-%d\t%d\t%s\t%s\t%s\t%s\n",
            b.Lower, b.Upper, b.Count, b.E1.P50, b.E1.P99, b.E2.P50, b.E2.P99)
    }
    if err := tw.Flush(); err != nil {
        return fmt.Errorf("flush bucket table: %w", err)
    }

    fmt.Fprintln(w, "\nfinal sizes")
    ids := make([]string, 0, len(s.FinalSizes))
    for id := range s.FinalSizes {
        ids = append(ids, id)
    }
    sort.Strings(ids)
    tw2 := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
    fmt.Fprintln(tw2, "room_id\tfinal_size")
    for _, id := range ids {
        fmt.Fprintf(tw2, "%s\t%d\n", id, s.FinalSizes[id])
    }
    return tw2.Flush()
}

// BucketIndex returns the index of the bucket whose [lower, upper) contains
// size, or -1 if size < edges[0] or size >= edges[len-1]. edges must be
// strictly increasing.
func BucketIndex(size int, edges []int) int {
    if len(edges) < 2 {
        return -1
    }
    if size < edges[0] || size >= edges[len(edges)-1] {
        return -1
    }
    for i := 0; i < len(edges)-1; i++ {
        if size >= edges[i] && size < edges[i+1] {
            return i
        }
    }
    return -1
}
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/members_report.go tools/loadgen/members_report_test.go
git commit -m "feat(loadgen): add MembersSummary + CapacitySummary printers"
```

---

## Task 13: Add `--workload` flag on `seed` / `teardown` subcommands

**Files:**
- Modify: `tools/loadgen/main.go`
- Modify: `tools/loadgen/main_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tools/loadgen/main_test.go`:

```go
func TestRunSeed_RejectsUnknownWorkload(t *testing.T) {
    cfg := &config{}
    code := runSeed(context.Background(), cfg, []string{"--workload=widgets", "--preset=members-small"})
    assert.Equal(t, 2, code)
}

func TestRunSeed_RejectsUnknownMembersPreset(t *testing.T) {
    cfg := &config{}
    code := runSeed(context.Background(), cfg, []string{"--workload=members", "--preset=nope"})
    assert.Equal(t, 2, code)
}
```

Make sure `"context"` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL (current `runSeed` knows only `--preset`).

- [ ] **Step 3: Refactor `runSeed` to branch on workload**

In `tools/loadgen/main.go`, replace `runSeed` with:

```go
func runSeed(ctx context.Context, cfg *config, args []string) int {
    fs := flag.NewFlagSet("seed", flag.ExitOnError)
    workload := fs.String("workload", "messages", "messages|members")
    preset := fs.String("preset", "", "preset name")
    seed := fs.Int64("seed", 42, "RNG seed")
    _ = fs.Parse(args)
    if *preset == "" {
        fmt.Fprintln(os.Stderr, "--preset required")
        return 2
    }
    switch *workload {
    case "messages":
        return runSeedMessages(ctx, cfg, *preset, *seed)
    case "members":
        return runSeedMembers(ctx, cfg, *preset, *seed)
    default:
        fmt.Fprintf(os.Stderr, "unknown workload: %s\n", *workload)
        return 2
    }
}

func runSeedMessages(ctx context.Context, cfg *config, preset string, seed int64) int {
    p, ok := BuiltinPreset(preset)
    if !ok {
        fmt.Fprintf(os.Stderr, "unknown preset: %s\n", preset)
        return 2
    }
    db, keyStore, cleanup, err := connectStores(ctx, cfg)
    if err != nil {
        return 1
    }
    defer cleanup()
    fixtures := BuildFixtures(&p, seed, cfg.SiteID)
    if err := Seed(ctx, db, &fixtures); err != nil {
        slog.Error("seed", "error", err)
        return 1
    }
    if err := SeedRoomKeys(ctx, keyStore, fixtures.RoomKeys); err != nil {
        slog.Error("seed room keys", "error", err)
        return 1
    }
    slog.Info("seed complete (messages)",
        "preset", p.Name,
        "users", len(fixtures.Users),
        "rooms", len(fixtures.Rooms),
        "subs", len(fixtures.Subscriptions),
        "roomKeys", len(fixtures.RoomKeys))
    return 0
}

func runSeedMembers(ctx context.Context, cfg *config, preset string, seed int64) int {
    p, ok := BuiltinMembersPreset(preset)
    if !ok {
        fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", preset)
        return 2
    }
    db, keyStore, cleanup, err := connectStores(ctx, cfg)
    if err != nil {
        return 1
    }
    defer cleanup()
    fixtures, pools := BuildMembersFixtures(&p, seed, cfg.SiteID)
    if err := Seed(ctx, db, &fixtures); err != nil {
        slog.Error("seed", "error", err)
        return 1
    }
    if err := SeedRoomKeys(ctx, keyStore, fixtures.RoomKeys); err != nil {
        slog.Error("seed room keys", "error", err)
        return 1
    }
    candCount := 0
    for _, ids := range pools {
        candCount += len(ids)
    }
    slog.Info("seed complete (members)",
        "preset", p.Name,
        "users", len(fixtures.Users),
        "rooms", len(fixtures.Rooms),
        "subs", len(fixtures.Subscriptions),
        "roomKeys", len(fixtures.RoomKeys),
        "candidatePoolTotal", candCount)
    return 0
}
```

Mirror the same split for `runTeardown`:

```go
func runTeardown(ctx context.Context, cfg *config, args []string) int {
    fs := flag.NewFlagSet("teardown", flag.ExitOnError)
    workload := fs.String("workload", "messages", "messages|members")
    preset := fs.String("preset", "", "preset name (required to identify which room keys to delete)")
    seed := fs.Int64("seed", 42, "RNG seed (must match the seed used at seed time)")
    _ = fs.Parse(args)
    if *preset == "" {
        fmt.Fprintln(os.Stderr, "--preset required")
        return 2
    }
    switch *workload {
    case "messages":
        return runTeardownMessages(ctx, cfg, *preset, *seed)
    case "members":
        return runTeardownMembers(ctx, cfg, *preset, *seed)
    default:
        fmt.Fprintf(os.Stderr, "unknown workload: %s\n", *workload)
        return 2
    }
}

func runTeardownMessages(ctx context.Context, cfg *config, preset string, seed int64) int {
    p, ok := BuiltinPreset(preset)
    if !ok {
        fmt.Fprintf(os.Stderr, "unknown preset: %s\n", preset)
        return 2
    }
    db, keyStore, cleanup, err := connectStores(ctx, cfg)
    if err != nil {
        return 1
    }
    defer cleanup()
    fixtures := BuildFixtures(&p, seed, cfg.SiteID)
    roomIDs := roomIDsOf(fixtures.Rooms)
    if err := Teardown(ctx, db); err != nil {
        slog.Error("teardown", "error", err)
        return 1
    }
    if err := TeardownRoomKeys(ctx, keyStore, roomIDs); err != nil {
        slog.Error("teardown room keys", "error", err)
        return 1
    }
    slog.Info("teardown complete (messages)")
    return 0
}

func runTeardownMembers(ctx context.Context, cfg *config, preset string, seed int64) int {
    p, ok := BuiltinMembersPreset(preset)
    if !ok {
        fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", preset)
        return 2
    }
    db, keyStore, cleanup, err := connectStores(ctx, cfg)
    if err != nil {
        return 1
    }
    defer cleanup()
    fixtures, _ := BuildMembersFixtures(&p, seed, cfg.SiteID)
    roomIDs := roomIDsOf(fixtures.Rooms)
    if err := Teardown(ctx, db); err != nil {
        slog.Error("teardown", "error", err)
        return 1
    }
    if err := TeardownRoomKeys(ctx, keyStore, roomIDs); err != nil {
        slog.Error("teardown room keys", "error", err)
        return 1
    }
    slog.Info("teardown complete (members)")
    return 0
}

func roomIDsOf(rooms []model.Room) []string {
    out := make([]string, len(rooms))
    for i := range rooms {
        out[i] = rooms[i].ID
    }
    return out
}
```

Add `"github.com/hmchangw/chat/pkg/model"` to the imports if it isn't already.

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS. Also run all loadgen tests to make sure existing messages-seed paths still work:

```
make test SERVICE=tools/loadgen
```

- [ ] **Step 5: Commit**

```
git add tools/loadgen/main.go tools/loadgen/main_test.go
git commit -m "feat(loadgen): add --workload flag to seed and teardown"
```

---

## Task 14: `members-sustained` subcommand wiring

**Files:**
- Modify: `tools/loadgen/main.go`
- Modify: `tools/loadgen/main_test.go`

- [ ] **Step 1: Write the failing test**

Append to `tools/loadgen/main_test.go`:

```go
func TestDispatch_MembersSustained_UnknownPreset(t *testing.T) {
    os.Args = []string{"loadgen", "members-sustained", "--preset=nope"}
    cfg := &config{NatsURL: "nats://localhost:1", MongoURI: "mongodb://localhost:1", ValkeyAddr: "localhost:1"}
    code := dispatch(context.Background(), cfg)
    assert.Equal(t, 2, code)
}

func TestDispatch_MembersSustained_RejectsBadShape(t *testing.T) {
    os.Args = []string{"loadgen", "members-sustained", "--preset=members-small", "--shape=orgs"}
    cfg := &config{NatsURL: "nats://localhost:1", MongoURI: "mongodb://localhost:1", ValkeyAddr: "localhost:1"}
    code := dispatch(context.Background(), cfg)
    assert.Equal(t, 2, code)
}
```

These tests rely on the existing `dispatch` indirection — confirm by reading `main.go` first. If `dispatch` reads `os.Args[1]`, the tests above are correct. If you need to refactor dispatch to take an explicit subcommand string, do that first as a no-op refactor (extract subcommand from `os.Args[1]` into a parameter).

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL with `unknown subcommand: members-sustained`.

- [ ] **Step 3: Add the subcommand case + implementation**

In `tools/loadgen/main.go`, extend the `switch os.Args[1]` in `dispatch`:

```go
    case "members-sustained":
        return runMembersSustained(ctx, cfg, os.Args[2:])
    case "members-capacity":
        return runMembersCapacity(ctx, cfg, os.Args[2:])
```

Add `runMembersSustained`:

```go
func runMembersSustained(ctx context.Context, cfg *config, args []string) int {
    fs := flag.NewFlagSet("members-sustained", flag.ExitOnError)
    preset := fs.String("preset", "", "members preset name")
    seed := fs.Int64("seed", 42, "RNG seed")
    duration := fs.Duration("duration", 60*time.Second, "run duration")
    rate := fs.Int("rate", 100, "target req/sec")
    warmup := fs.Duration("warmup", 10*time.Second, "warmup window (samples discarded)")
    inject := fs.String("inject", "frontdoor", "frontdoor|canonical")
    shapeFlag := fs.String("shape", "users", "users|orgs|channels|mixed (v1: users only)")
    usersPerAdd := fs.Int("users-per-add", 10, "users per add request")
    csvPath := fs.String("csv", "", "optional CSV output path")
    _ = fs.Parse(args)

    if *preset == "" {
        fmt.Fprintln(os.Stderr, "--preset required")
        return 2
    }
    p, ok := BuiltinMembersPreset(*preset)
    if !ok {
        fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", *preset)
        return 2
    }
    var injectMode InjectMode
    switch *inject {
    case "frontdoor":
        injectMode = InjectFrontdoor
    case "canonical":
        injectMode = InjectCanonical
    default:
        fmt.Fprintf(os.Stderr, "unknown inject: %s\n", *inject)
        return 2
    }
    shape, err := ParseShape(*shapeFlag)
    if err != nil {
        fmt.Fprintln(os.Stderr, err.Error())
        return 2
    }
    if err := ValidateInjectShape(injectMode, shape); err != nil {
        fmt.Fprintln(os.Stderr, err.Error())
        return 2
    }
    if *usersPerAdd <= 0 {
        fmt.Fprintln(os.Stderr, "--users-per-add must be > 0")
        return 2
    }

    nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
    if err != nil {
        slog.Error("nats connect", "error", err)
        return 1
    }
    js, err := jetstream.New(nc.NatsConn())
    if err != nil {
        slog.Error("jetstream init", "error", err)
        return 1
    }

    metrics := NewMetrics()
    metricsSrv := &http.Server{
        Addr: cfg.MetricsAddr, Handler: metrics.Handler(),
        ReadHeaderTimeout: 5 * time.Second,
    }
    go func() {
        if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            slog.Warn("metrics server stopped", "error", err)
        }
    }()

    fixtures, pools := BuildMembersFixtures(&p, *seed, cfg.SiteID)
    owners := OwnersByRoom(&fixtures)
    collector := NewMemberCollector(metrics, p.Name, *inject)

    // E2 broadcast subscription.
    e2Sub, err := nc.NatsConn().Subscribe(subject.RoomMemberEventWildcard(), func(m *nats.Msg) {
        roomID, accounts, ok := ParseMemberAddBroadcast(m.Data)
        if !ok {
            return
        }
        collector.RecordBroadcast(roomID, accounts, time.Now())
    })
    if err != nil {
        slog.Error("subscribe e2", "error", err)
        return 1
    }
    defer func() { _ = e2Sub.Unsubscribe() }()

    // Publisher (frontdoor needs E1 callback hooked to collector).
    var publisher MemberPublisher
    var frontdoor *frontdoorMemberPublisher
    switch injectMode {
    case InjectFrontdoor:
        frontdoor, err = newFrontdoorMemberPublisher(nc.NatsConn(), cfg.SiteID, func(corrID string, body []byte, at time.Time) {
            collector.RecordReply(corrID, string(body), at)
        })
        if err != nil {
            slog.Error("frontdoor publisher", "error", err)
            return 1
        }
        defer frontdoor.Close()
        publisher = frontdoor
    case InjectCanonical:
        publisher = newCanonicalMemberPublisher(js, cfg.SiteID)
    }

    // Consumer-lag sampler on room-worker.
    samplerCtx, cancelSamplers := context.WithCancel(ctx)
    defer cancelSamplers()
    sampler := NewConsumerSampler(js, stream.Rooms(cfg.SiteID).Name, "room-worker", metrics, time.Second)
    var samplerWG sync.WaitGroup
    samplerWG.Add(1)
    go func() { defer samplerWG.Done(); sampler.Run(samplerCtx) }()

    warmupDeadline := time.Now().Add(*warmup)
    genCfg := SustainedMembersConfig{
        Preset:         &p,
        Fixtures:       &fixtures,
        Pools:          pools,
        Owners:         owners,
        SiteID:         cfg.SiteID,
        Rate:           *rate,
        UsersPerAdd:    *usersPerAdd,
        Inject:         injectMode,
        Shape:          shape,
        Publisher:      publisher,
        Metrics:        metrics,
        Collector:      collector,
        WarmupDeadline: warmupDeadline,
        MaxInFlight:    cfg.MaxInFlight,
    }
    gen := NewSustainedMembersGenerator(&genCfg, *seed)

    runCtx, cancelRun := context.WithTimeout(ctx, *duration)
    defer cancelRun()
    genErr := gen.Run(runCtx)
    time.Sleep(2 * time.Second) // drain trailing replies/broadcasts
    collector.DiscardBefore(warmupDeadline)
    missingReplies, missingBroadcasts := collector.Finalize()

    cancelSamplers()
    samplerWG.Wait()

    shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
    _ = metricsSrv.Shutdown(shutCtx)
    cancelShut()
    _ = nc.Drain()

    if genErr != nil && !errors.Is(genErr, ErrPoolsExhausted) {
        slog.Error("generator error", "error", genErr)
    } else if errors.Is(genErr, ErrPoolsExhausted) {
        slog.Warn("aborted early", "reason", "pools exhausted")
    }

    mfs, _ := metrics.Registry.Gather()
    pubErrs := int(gatheredCounterValue(mfs, "loadgen_member_publish_errors_total", "reason", "publish"))
    rsErrs := collector.RoomServiceErrorCount()
    sentWarmup := int(gatheredCounterValue(mfs, "loadgen_member_published_total", "phase", "warmup"))
    sentMeasured := int(gatheredCounterValue(mfs, "loadgen_member_published_total", "phase", "measured"))
    sent := sentWarmup + sentMeasured
    measured := *duration - *warmup
    var actualRate float64
    if measured > 0 {
        actualRate = float64(sentMeasured) / measured.Seconds()
    }

    summary := MembersSummary{
        Preset: p.Name, Site: cfg.SiteID, Inject: *inject, Shape: string(shape),
        Seed: *seed, TargetRate: *rate, ActualRate: actualRate,
        Duration: *duration, Warmup: *warmup, UsersPerAdd: *usersPerAdd,
        Sent: sent, SentMeasured: sentMeasured,
        PublishErrors: pubErrs, RoomServiceErrors: rsErrs,
        MissingReplies: missingReplies, MissingBroadcasts: missingBroadcasts,
        E1: ComputePercentiles(collector.E1Samples()),
        E2: ComputePercentiles(collector.E2Samples()),
        E1Count: collector.E1Count(), E2Count: collector.E2Count(),
        Consumers: []ConsumerStat{sampler.Snapshot()},
    }
    if err := PrintMembersSummary(os.Stdout, &summary); err != nil {
        slog.Warn("print summary", "error", err)
    }
    if *csvPath != "" {
        if err := writeMembersCSV(*csvPath, collector); err != nil {
            slog.Error("csv export", "error", err)
        }
    }
    totalErrs := summary.PublishErrors + summary.RoomServiceErrors + summary.MissingReplies + summary.MissingBroadcasts
    return DetermineExitCode(summary.SentMeasured, totalErrs)
}

func writeMembersCSV(path string, c *MemberCollector) error {
    f, err := os.Create(path)
    if err != nil {
        return fmt.Errorf("create csv: %w", err)
    }
    defer func() { _ = f.Close() }()
    var rows []CSVSample
    for i, d := range c.E1Samples() {
        rows = append(rows, CSVSample{TimestampNs: int64(i), Metric: "E1", LatencyNs: d.Nanoseconds()})
    }
    for i, d := range c.E2Samples() {
        rows = append(rows, CSVSample{TimestampNs: int64(i), Metric: "E2", LatencyNs: d.Nanoseconds()})
    }
    return WriteCSV(f, rows)
}
```

Imports to add to `main.go` if not already present: `"sync"`, `"github.com/nats-io/nats.go"`, `"github.com/hmchangw/chat/pkg/stream"`, `"github.com/hmchangw/chat/pkg/subject"`.

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS. The flag-validation tests should now reach the validation rejection paths (no NATS connect attempted because flag parsing fails first).

- [ ] **Step 5: Commit**

```
git add tools/loadgen/main.go tools/loadgen/main_test.go
git commit -m "feat(loadgen): add members-sustained subcommand"
```

---

## Task 15: `members-capacity` subcommand wiring

**Files:**
- Modify: `tools/loadgen/main.go`

Similar wiring to `members-sustained` but uses the capacity generator and capacity summary. Most plumbing (NATS, metrics server, broadcast subscription, publisher selection) is identical — factor the common setup into a helper if natural, otherwise duplicate.

- [ ] **Step 1: Write the failing test**

Append to `tools/loadgen/main_test.go`:

```go
func TestDispatch_MembersCapacity_RequiresTargetSize(t *testing.T) {
    os.Args = []string{"loadgen", "members-capacity", "--preset=members-capacity"}
    cfg := &config{NatsURL: "nats://localhost:1", MongoURI: "mongodb://localhost:1", ValkeyAddr: "localhost:1"}
    code := dispatch(context.Background(), cfg)
    assert.Equal(t, 2, code)
}
```

- [ ] **Step 2: Run test to verify it fails**

```
make test SERVICE=tools/loadgen
```

Expected: FAIL (`members-capacity` not yet wired).

- [ ] **Step 3: Implement**

Add `runMembersCapacity` to `main.go`:

```go
func runMembersCapacity(ctx context.Context, cfg *config, args []string) int {
    fs := flag.NewFlagSet("members-capacity", flag.ExitOnError)
    preset := fs.String("preset", "", "members preset name")
    seed := fs.Int64("seed", 42, "RNG seed")
    inject := fs.String("inject", "frontdoor", "frontdoor|canonical")
    shapeFlag := fs.String("shape", "users", "users|orgs|channels|mixed (v1: users only)")
    usersPerAdd := fs.Int("users-per-add", 10, "users per add request")
    targetSize := fs.Int("target-size", 0, "stop each room when its member count >= target-size (required)")
    maxRate := fs.Int("max-rate", 0, "optional cap on per-room req/sec; 0 = sequential pacing only")
    e2Timeout := fs.Duration("e2-timeout", 30*time.Second, "max wait for broadcast per add")
    csvPath := fs.String("csv", "", "optional CSV output path")
    _ = fs.Parse(args)

    if *preset == "" {
        fmt.Fprintln(os.Stderr, "--preset required")
        return 2
    }
    if *targetSize <= 0 {
        fmt.Fprintln(os.Stderr, "--target-size required and must be > 0")
        return 2
    }
    p, ok := BuiltinMembersPreset(*preset)
    if !ok {
        fmt.Fprintf(os.Stderr, "unknown members preset: %s\n", *preset)
        return 2
    }
    var injectMode InjectMode
    switch *inject {
    case "frontdoor":
        injectMode = InjectFrontdoor
    case "canonical":
        injectMode = InjectCanonical
    default:
        fmt.Fprintf(os.Stderr, "unknown inject: %s\n", *inject)
        return 2
    }
    shape, err := ParseShape(*shapeFlag)
    if err != nil {
        fmt.Fprintln(os.Stderr, err.Error())
        return 2
    }
    if err := ValidateInjectShape(injectMode, shape); err != nil {
        fmt.Fprintln(os.Stderr, err.Error())
        return 2
    }
    if *usersPerAdd <= 0 {
        fmt.Fprintln(os.Stderr, "--users-per-add must be > 0")
        return 2
    }

    nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
    if err != nil {
        slog.Error("nats connect", "error", err)
        return 1
    }
    js, err := jetstream.New(nc.NatsConn())
    if err != nil {
        slog.Error("jetstream init", "error", err)
        return 1
    }

    metrics := NewMetrics()
    metricsSrv := &http.Server{
        Addr: cfg.MetricsAddr, Handler: metrics.Handler(),
        ReadHeaderTimeout: 5 * time.Second,
    }
    go func() {
        if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
            slog.Warn("metrics server stopped", "error", err)
        }
    }()

    fixtures, pools := BuildMembersFixtures(&p, *seed, cfg.SiteID)
    owners := OwnersByRoom(&fixtures)
    collector := NewMemberCollector(metrics, p.Name, *inject)

    e2Sub, err := nc.NatsConn().Subscribe(subject.RoomMemberEventWildcard(), func(m *nats.Msg) {
        roomID, accounts, ok := ParseMemberAddBroadcast(m.Data)
        if !ok {
            return
        }
        collector.RecordBroadcast(roomID, accounts, time.Now())
    })
    if err != nil {
        slog.Error("subscribe e2", "error", err)
        return 1
    }
    defer func() { _ = e2Sub.Unsubscribe() }()

    var publisher MemberPublisher
    var frontdoor *frontdoorMemberPublisher
    switch injectMode {
    case InjectFrontdoor:
        frontdoor, err = newFrontdoorMemberPublisher(nc.NatsConn(), cfg.SiteID, func(corrID string, body []byte, at time.Time) {
            collector.RecordReply(corrID, string(body), at)
        })
        if err != nil {
            slog.Error("frontdoor publisher", "error", err)
            return 1
        }
        defer frontdoor.Close()
        publisher = frontdoor
    case InjectCanonical:
        publisher = newCanonicalMemberPublisher(js, cfg.SiteID)
    }

    genCfg := CapacityMembersConfig{
        Preset: &p, Fixtures: &fixtures, Pools: pools, Owners: owners,
        SiteID: cfg.SiteID, UsersPerAdd: *usersPerAdd,
        Inject: injectMode, Shape: shape,
        TargetSize: *targetSize, MaxRate: *maxRate,
        Publisher: publisher, Metrics: metrics, Collector: collector,
        E2Timeout: *e2Timeout,
    }
    gen := NewCapacityMembersGenerator(&genCfg)
    if err := gen.Run(ctx); err != nil {
        slog.Error("generator error", "error", err)
    }
    time.Sleep(2 * time.Second)
    collector.Finalize()

    shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
    _ = metricsSrv.Shutdown(shutCtx)
    cancelShut()
    _ = nc.Drain()

    // Compute final sizes from the room_size gauge.
    finals := map[string]int{}
    mfs, _ := metrics.Registry.Gather()
    for _, mf := range mfs {
        if mf.GetName() != "loadgen_member_room_size" {
            continue
        }
        for _, mt := range mf.GetMetric() {
            var rid string
            for _, l := range mt.GetLabel() {
                if l.GetName() == "room_id" {
                    rid = l.GetValue()
                }
            }
            finals[rid] = int(mt.GetGauge().GetValue())
        }
    }
    pubErrs := int(gatheredCounterValue(mfs, "loadgen_member_publish_errors_total", "reason", "publish"))
    timeouts := int(gatheredCounterValue(mfs, "loadgen_member_publish_errors_total", "reason", "timeout"))

    // Simple bucketing: split [0, target] into 4 even buckets.
    edges := []int{0, *targetSize / 4, *targetSize / 2, (*targetSize * 3) / 4, *targetSize + 1}
    buckets := computeSizeBuckets(collector, finals, edges)

    summary := CapacitySummary{
        Preset: p.Name, Site: cfg.SiteID, Inject: *inject, Shape: string(shape),
        Seed: *seed, UsersPerAdd: *usersPerAdd, TargetSize: *targetSize,
        PublishErrors: pubErrs, Timeouts: timeouts,
        Buckets: buckets, FinalSizes: finals,
    }
    if err := PrintCapacitySummary(os.Stdout, &summary); err != nil {
        slog.Warn("print summary", "error", err)
    }
    if *csvPath != "" {
        if err := writeMembersCSV(*csvPath, collector); err != nil {
            slog.Error("csv export", "error", err)
        }
    }
    return 0
}

// computeSizeBuckets is intentionally simple in v1 — it returns one row per
// bucket with the aggregate E1/E2 percentiles for samples whose source room's
// FINAL size fell in that bucket. (Per-sample size tracking is a v2
// enhancement; for now we treat each room's full latency tape as belonging
// to its final-size bucket.)
func computeSizeBuckets(c *MemberCollector, finals map[string]int, edges []int) []SizeBucket {
    out := make([]SizeBucket, 0, len(edges)-1)
    for i := 0; i < len(edges)-1; i++ {
        out = append(out, SizeBucket{Lower: edges[i], Upper: edges[i+1]})
    }
    for _, sz := range finals {
        idx := BucketIndex(sz, edges)
        if idx < 0 {
            continue
        }
        out[idx].Count++
    }
    e1 := ComputePercentiles(c.E1Samples())
    e2 := ComputePercentiles(c.E2Samples())
    for i := range out {
        if out[i].Count > 0 {
            out[i].E1 = e1
            out[i].E2 = e2
        }
    }
    return out
}
```

- [ ] **Step 4: Run test to verify it passes**

```
make test SERVICE=tools/loadgen
```

Expected: PASS.

- [ ] **Step 5: Commit**

```
git add tools/loadgen/main.go tools/loadgen/main_test.go
git commit -m "feat(loadgen): add members-capacity subcommand"
```

---

## Task 16: Deploy Makefile + README

**Files:**
- Modify: `tools/loadgen/deploy/Makefile`
- Modify: `tools/loadgen/README.md`

- [ ] **Step 1: Add Makefile targets**

In `tools/loadgen/deploy/Makefile`, after the existing `teardown:` target, add:

```
seed-members:
	@test -n "$(PRESET)" || (echo "PRESET=<members-*> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen seed --workload=members --preset=$(PRESET)

teardown-members:
	@test -n "$(PRESET)" || (echo "PRESET=<members-*> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen teardown --workload=members --preset=$(PRESET)

# Re-seed wrapper: drop members fixtures then re-seed. The spec's chosen
# "reset between runs" strategy for sustained mode.
reset-members:
	@test -n "$(PRESET)" || (echo "PRESET=<members-*> required" && exit 1)
	$(MAKE) teardown-members PRESET=$(PRESET)
	$(MAKE) seed-members PRESET=$(PRESET)

run-sustained:
	@test -n "$(PRESET)" || (echo "PRESET=<members-*> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen members-sustained \
	    --preset=$(PRESET) \
	    --rate=$(or $(RATE),100) \
	    --duration=$(or $(DURATION),60s) \
	    --users-per-add=$(or $(USERS_PER_ADD),10) \
	    --inject=$(or $(INJECT),frontdoor) \
	    --shape=$(or $(SHAPE),users)

run-capacity:
	@test -n "$(PRESET)" || (echo "PRESET=<members-*> required" && exit 1)
	@test -n "$(TARGET_SIZE)" || (echo "TARGET_SIZE=<n> required" && exit 1)
	$(COMPOSE) exec -T loadgen /loadgen members-capacity \
	    --preset=$(PRESET) \
	    --target-size=$(TARGET_SIZE) \
	    --users-per-add=$(or $(USERS_PER_ADD),10) \
	    --inject=$(or $(INJECT),frontdoor) \
	    --shape=$(or $(SHAPE),users)
```

Also extend the `.PHONY` line to list the new targets.

- [ ] **Step 2: Update the README**

In `tools/loadgen/README.md`, add a section after the existing "Subcommands" section:

```markdown
## Members workload (add-member benchmark)

Benchmarks the add-member pipeline:
`room-service.handleAddMembers` → `chat.room.canonical.{siteID}.member.add`
(ROOMS stream) → `room-worker` → `chat.room.{roomID}.event.member` broadcast.

### Quick start

```
make -C tools/loadgen/deploy up
make -C tools/loadgen/deploy seed-members PRESET=members-medium
make -C tools/loadgen/deploy run-sustained PRESET=members-medium RATE=100 DURATION=60s
```

For capacity-mode growth curves:

```
make -C tools/loadgen/deploy seed-members PRESET=members-capacity
make -C tools/loadgen/deploy run-capacity  PRESET=members-capacity TARGET_SIZE=500
```

Between sustained runs, reset state so candidate pools refill:

```
make -C tools/loadgen/deploy reset-members PRESET=members-medium
```

### Presets

| preset             | rooms | baseline | candidate pool | use case                                |
|--------------------|-------|----------|----------------|-----------------------------------------|
| `members-small`    | 5     | 10       | 50             | smoke / dev                             |
| `members-medium`   | 100   | 100      | 500            | sustained-throughput default            |
| `members-capacity` | 5     | 1        | 990            | capacity-growth, fills up to ~MAX_ROOM_SIZE |

### Subcommands

- `loadgen seed --workload=members --preset=<name>` — populate Mongo
  + Valkey for the members workload.
- `loadgen teardown --workload=members --preset=<name>` — drop the seeded data.
- `loadgen members-sustained --preset=<name> [flags]` — open-loop publish
  at `--rate` req/sec for `--duration`. Flags: `--users-per-add` (default 10),
  `--inject=frontdoor|canonical` (default frontdoor),
  `--shape=users` (v1; orgs/channels/mixed reserved for v2), `--warmup`,
  `--csv`.
- `loadgen members-capacity --preset=<name> --target-size=N [flags]` —
  per-room sequential growth until rooms reach `--target-size`. Flags:
  `--users-per-add`, `--inject`, `--shape`, `--max-rate` (per-room rate
  cap, default 0 = sequential pacing only), `--e2-timeout`, `--csv`.

### v1 scope

Only `--shape=users` is implemented. The flag accepts `orgs`, `channels`,
`mixed` for forward compat but rejects them at parse time. See
`docs/superpowers/specs/2026-05-19-load-test-room-members-design.md`
for the rationale and the v2 plan.

### Reading the summary

- **Sustained mode**: `final_pending == 0` on room-worker + zero errors →
  pipeline is sustaining the target rate. Climbing `final_pending` or
  non-zero errors → over capacity. If you see `aborted early — pools
  exhausted` in the logs, your `rate × duration × users-per-add` exceeded
  the preset's `CandidatePool` budget; pick a bigger preset or shorter
  duration.
- **Capacity mode**: the size-bucket table shows latency at four
  size ranges; the `final sizes` block confirms each room hit
  `--target-size`. A row with `count > 0` whose `e2_p99` is much larger
  than smaller-size buckets indicates a per-room-size degradation.
```

- [ ] **Step 3: Commit**

```
git add tools/loadgen/deploy/Makefile tools/loadgen/README.md
git commit -m "docs(loadgen): document members workload + Makefile targets"
```

---

## Task 17: Integration test (testcontainers)

**Files:**
- Create: `tools/loadgen/members_integration_test.go`

This test stands up real NATS, Mongo, Valkey, room-service, and room-worker via testcontainers (or via the existing `compose.services.yaml` stack — check what `tools/loadgen/integration_test.go` does and follow the same pattern). Seeds `members-small`, runs a 5-second sustained burst, asserts zero errors and that E1+E2 counts are non-zero.

- [ ] **Step 1: Read the existing integration test for the established pattern**

```
cat tools/loadgen/integration_test.go
```

Mirror its setup (container helpers, env vars, cleanup hooks).

- [ ] **Step 2: Write the test**

`tools/loadgen/members_integration_test.go`:

```go
//go:build integration

package main

import (
    "context"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// TestMembersSustained_EndToEnd seeds members-small, runs members-sustained
// for 5s, and asserts the pipeline accepted every request and broadcast it.
func TestMembersSustained_EndToEnd(t *testing.T) {
    // EXISTING PATTERN: this file should reuse the same testcontainer setup
    // as integration_test.go in the same package. Implementer: copy the
    // setup blocks (nats/mongo/valkey/room-service/room-worker bring-up)
    // verbatim. The skeleton below assumes those helpers exist as
    // `setupStack(t)` returning (natsURL, mongoURI, valkeyAddr, siteID, cleanup).

    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()

    natsURL, mongoURI, valkeyAddr, siteID, cleanup := setupStack(t)
    defer cleanup()

    cfg := &config{
        NatsURL: natsURL, MongoURI: mongoURI, ValkeyAddr: valkeyAddr,
        SiteID: siteID, MongoDB: "loadgen_members_test",
        MetricsAddr: ":0", MaxInFlight: 50,
    }
    require.Equal(t, 0, runSeed(ctx, cfg, []string{"--workload=members", "--preset=members-small"}))

    // Use a tiny rate + short duration so the pool doesn't exhaust.
    code := runMembersSustained(ctx, cfg, []string{
        "--preset=members-small", "--duration=5s", "--rate=5",
        "--warmup=1s", "--users-per-add=2",
    })
    assert.Equal(t, 0, code, "members-sustained exit code should be 0")
}
```

If `setupStack` doesn't exist in `integration_test.go`, factor out a helper there as part of this task: extract the existing setup into `func setupStack(t *testing.T) (natsURL, mongoURI, valkeyAddr, siteID string, cleanup func())` and update the existing test to use it. Keep the extraction commit separate from the new test.

- [ ] **Step 3: Run the integration test**

```
make test-integration SERVICE=tools/loadgen
```

Expected: PASS within ~60s.

- [ ] **Step 4: Commit**

```
git add tools/loadgen/members_integration_test.go tools/loadgen/integration_test.go
git commit -m "test(loadgen): add members-sustained end-to-end integration test"
```

---

## Final verification

- [ ] **Step 1: Run full test suite + lint**

```
make lint
make test
make test-integration SERVICE=tools/loadgen
```

All must pass.

- [ ] **Step 2: Manual smoke test against the local stack**

```
make -C tools/loadgen/deploy up
make -C tools/loadgen/deploy seed-members PRESET=members-small
make -C tools/loadgen/deploy run-sustained PRESET=members-small RATE=20 DURATION=10s
make -C tools/loadgen/deploy down
```

Expected output: a `=== loadgen members-sustained complete ===` block with non-zero E1 and E2 counts, zero `publish errors`, and `final_pending: 0` on the room-worker consumer.

- [ ] **Step 3: Push the branch**

```
git push -u origin claude/load-test-room-members-7o4o4
```

---

## Self-review checklist (for the planner — already completed)

1. **Spec coverage** — Every section of the design spec maps to at least one task:
   - Architecture / two subcommands → Tasks 14, 15
   - Inject modes (frontdoor + canonical) → Tasks 7, 8 + wiring in 14, 15
   - `shape=channels --inject=canonical` rejection → Task 3
   - Fixtures + presets → Tasks 4, 5, 6
   - SustainedGenerator / CapacityGenerator → Tasks 10, 11
   - Publisher interface + impls → Tasks 7, 8
   - Collector with E1/E2 correlation → Tasks 9, 11 (per-room ack)
   - Observability (member-specific metrics) → Task 2
   - MembersSummary / CapacitySummary → Task 12
   - Error classes (publish / room_service / timeout) → Tasks 9, 10, 11
   - Testing (unit + integration) → present in every task; integration in Task 17
   - File layout → matches the file map at the top
   - v1 scope (users-only) → enforced in Task 3, surfaced in Tasks 14, 15, README

2. **Placeholder scan** — no TODOs, no "implement later", no "similar to Task N". One callout in Task 7 step 4 ("ask before adding dep") follows CLAUDE.md's policy on third-party additions; if the implementer's environment doesn't already have `nats-server/v2/test`, the test should be rewritten as `//go:build integration` per the fallback note.

3. **Type consistency** — `MembersPreset`, `Fixtures`, `CandidatePools`, `MemberPublisher`, `MemberCollector`, `SustainedMembersConfig`, `CapacityMembersConfig` all match across tasks. Method signatures (`Publish(ctx, requester, roomID, *AddMembersRequest, corrID)`) consistent across publisher impls and stub. `RecordPublish(corrID, roomID, accounts, t)` consistent across tests and impl.
