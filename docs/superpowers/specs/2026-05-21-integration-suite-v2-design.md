# Integration Test Suite — Design (v2)

## Purpose

A scenario-driven black-box conformance suite for the chat backend. It
authenticates as a real user, drives the assembled services through their
native transports, observes the system's reactions through a bounded set of
reader locations, and classifies each scenario's outcome against a
documented expected behavior. It is not a unit-test replacement: it tests
the assembled system at its boundaries, not internal logic.

The suite is organized as a **four-layer architecture**. Scenarios — what
the AI authors and humans approve — are minimal typed templates. The
building blocks the scenarios compose from live in deliberately-curated
catalogs. The runtime engine resolves templates against the catalogs,
fires the action, and computes a verdict using time-frame-bounded
observation. Chaos is a perturbation layer applied on top.

## Goals

- Each scenario is ~10 lines of typed YAML, authored by AI, reviewed and
  approved by a human in a separate PR.
- The catalogs are bounded and grow deliberately, with explicit reviewer
  sign-off when expanded.
- One authored scenario expands into a family of test cases at runtime
  (one happy case + perturbation cases + concurrency cases). The author
  writes only the happy version.
- Negative coverage — "the system must NOT do anything else" — is
  automatic, not author-declared. The bounded reader catalog plus trace
  propagation plus service ability profiles make it work.
- Pass/fail is per-test-case, classified into precise outcome categories
  (missing-positive, unexpected-cascade, anomaly, timeframe-never-closed).
  The classification falls out of the verdict mechanism itself; no
  external calibration loop is needed for first-cut runs.
- The platform connects to the running system from outside; it owns no
  infrastructure.

## Non-goals

- Replacing per-service unit tests. Internal logic is out of scope.
- Owning infrastructure. The suite expects a running stack provided via
  env-var-configured URLs.
- Measuring throughput or capacity. That belongs in load-testing tools.
- Asserting on implementation details (function names, internal data
  structures). Black-box only.

---

## The four-layer model

```
LAYER 4   Scenarios
          ─────────────────────────────────────────────────────────
          YAML templates. AI submits as draft; human approves in a
          separate PR. Each scenario describes (verb, placeholders,
          ordered sequence of expected reads). Happy-path expectations
          only; "happy" means actual behavior equals expected for this
          input, regardless of whether expected is success or error.

LAYER 3   Building-block catalogs
          ─────────────────────────────────────────────────────────
          Six catalogs that hold every reusable thing scenarios
          compose from: verbs, fixture cast, reader locations,
          matchers, mishaps, service ability profiles. Bounded.

LAYER 2   Test runner
          ─────────────────────────────────────────────────────────
          Loads scenarios, resolves placeholders against the cast,
          fires verbs, observes via readers within the timeframe,
          classifies each test case, produces the run report
          (verdict counts + per-service attribution).

LAYER 1   Infrastructure-control plane (chaos)
          ─────────────────────────────────────────────────────────
          Pod kill, network partition, slow disk. Applied as
          environmental perturbations across scenarios at runtime.
```

Each layer is independent. Layer 4 templates are agnostic to Layer 1
chaos — the platform applies chaos across whatever scenarios exist, the
scenarios don't change.

---

## Layer 4 — Scenarios

### Storage format

Scenarios are YAML files. Drafts live at
`scenarios/drafts/<scope>/<name>.yaml`; approved scenarios live at
`scenarios/approved/<scope>/<name>.yaml`. Promotion is a file move + a
`# Status: approved` annotation, done by a human in a PR. Approved
scenarios feed the authoritative CI-gating score; drafts are
informational only.

### Scenario shape

```yaml
scenario: <unique snake_case name>
source: <relative path to design doc that backs this expectation>

input:
  verb: <primitive verb name from catalog>
  subject: <NATS subject or HTTP url, may contain $placeholder refs>
  payload: <typed payload matching the verb's input shape>
  credential: <reference to a cast member's credential>
  placeholders:
    <name>:
      type: <fixture type from catalog: user, room, message, ...>
      predicate:
        <tag>: <value | $other_placeholder.field>
        ...

sequence:                                    # ordered list of in-scope services
  - service: <service name>
    reads:                                   # POSITIVE expected reads
      - location: <reader location from catalog>
        predicate: <typed matcher invocation>
        within: <duration>                   # OPTIONAL — SLA assertion only
        optional: <true|false>               # OPTIONAL — defaults to false (required)
      - ...
  - service: <next service, optional>
    reads: [...]

mishaps:                                     # OPTIONAL — control rods
  ignore:                                    # mishaps that don't apply to this scenario
    - mishap: <mishap name from catalog>
      reason: <one-sentence rationale; required on every ignored entry>
    - mishap: <another>
      reason: <...>
```

Most scenarios are 10–20 lines total. The reads are written by the AI by
binding placeholder values into known reader-location patterns from the
catalog. The `mishaps.ignore` block is similarly populated by the AI based
on the scenario's source design doc and which mishaps are clearly
irrelevant.

#### Per-read flags

- `within:` (optional) — explicit SLA. If declared, the read MUST match
  within `within` seconds of the verb firing. Used for latency-critical
  observations like synchronous replies. If omitted, the read just needs
  to match before timeframe close (see Verdict mechanism).
- `optional:` (optional, default `false`) — if `true`, the read is
  expected only when it happens. Used for retry log lines, cache misses,
  recovery messages that may or may not occur depending on conditions.

#### The mishaps blacklist (control rods)

Scenarios default to running under **all subsets** of all applicable
mishaps. Authors (AI in the draft, human in review) declare a `mishaps.ignore`
list with mishaps that don't make sense for this scenario, each justified by
a one-sentence `reason:`. The reviewer's job is to skim the reasons and
confirm each exclusion is sensible. Without a reason, the lint rejects the
scenario.

Example:

```yaml
mishaps:
  ignore:
    - mishap: notification-worker-pod-kill
      reason: this scenario doesn't trigger notifications
    - mishap: cross-site-partition
      reason: single-site scenario; cross-site is out of scope
```


### Happy path includes both true-positive and true-negative

A "happy path" means actual behavior equals expected behavior for the
given input. Two flavors both qualify:

| Input shape | Expected behavior |
|---|---|
| authorized user, valid request | success reply + DB change |
| unauthorized user, valid request | auth error in the expected place |

Different input shapes produce different scenarios, each its own happy
path. Both are authored as drafts and reviewed independently.

### Authoring rules

- All scenarios start as draft. Promotion requires a separate human-
  reviewed PR adding `# Status: approved`.
- Every scenario MUST cite a `source:` design doc; lint enforces this.
- A scenario references catalog items by name; the platform resolves
  them. No prose, no English step phrasings, no regex matching.
- If the design is silent on what a scenario should assert, **do not
  write the scenario.** Either fill in the design first, or accept that
  silent behavior will surface at runtime as an `anomaly` event
  (something happened we don't know how to classify) — which is the
  signal that the design needs filling in. The platform handles silent
  behavior via the verdict mechanism, not via author-declared tags.

### What scenarios do NOT declare

Scenarios do not declare:

- Negative checks (handled automatically; see §"Verdict mechanism").
- Mishap-induced expected behavior — scenarios only declare WHICH
  mishaps to skip via `mishaps.ignore`; the platform applies any
  remaining mishap from the catalog, and the same `reads:` block is
  the contract under every mishap subset.
- Background activity expected from other services (handled by ability
  profiles in §3.6).

The scenario surface is intentionally minimal: input + happy expectations
+ control-rod exclusions. Everything else is platform-supplied.

---

## Layer 3 — Building-block catalogs

Six catalogs. All bounded. Each grows deliberately, with explicit reviewer
sign-off when expanded. The catalog is the contract between Layer 4
scenarios and the system under test.

### 3.1 Verbs

The verb catalog holds **interface-level primitives**, not domain
operations. Approximately six entries, rarely growing:

```
nats_request       (subject, payload, credential)   → reply or transport error
nats_publish       (subject, payload, credential)   → ack or error
jetstream_publish  (stream, payload, credential)    → seq or error
http_request       (method, url, headers, body, c)  → response
nats_subscribe     (subject, credential, deadline)  → captured messages
jetstream_consume  (stream, consumer, deadline)     → captured messages
```

Each verb has a **transport-level effect template** that describes only
its wire-level behavior: "nats_request returns a reply within a timeout
or a transport-level error class (RouteNotFound, Timeout, Unreachable)."

Domain-level expectations (a row appears in a Mongo collection, a stream
gains a message) are NOT on the verb. They belong on the scenario's
sequence reads. Verbs stay primitive; scenarios are explicit about what
their action causes in the system.

**Storage:** the verb's executor is a Go function in
`internal/harness/verbs/<name>.go`. The verb's metadata (name, input
shape, transport effect template) is YAML in `catalogs/verbs/<name>.yaml`
that references the executor by name. A startup validator cross-checks
the two.

### 3.2 Fixture cast

The fixture cast is the test universe at t=0: a fixed, multi-typed
roster of seeded users, rooms, messages, subscriptions, etc. Seeded
ONCE at environment bring-up, idempotently. **Read-only to verbs** at
scenario runtime; verbs may only create new `it-<runID>-...`-prefixed
temporary data, which is cleaned up between runs.

```yaml
# catalogs/fixture-cast.yaml
users:
  - id: alice
    tags: [verified, owner_of:general, member_of:private]
    credentials: { account: alice, nkey_seed: <...>, jwt: <...> }
  - id: bob
    tags: [verified, no_rooms]
    credentials: { ... }
  - id: dave
    tags: [unregistered]
    credentials: null
  ...

rooms:
  - id: general
    tags: [type:channel, owned_by:alice]
  ...

messages: ...
subscriptions: ...
```

Scenarios select cast members by predicate, not by name:

```yaml
placeholders:
  requester:
    type: user
    predicate: { verified: true, owner_of: $target_room.id }
```

The platform resolves the predicate against the cast and picks one
matching fixture. Cross-placeholder references like `$target_room.id`
keep multi-placeholder scenarios consistent.

**Cast sizing.** For each predicate that any concurrency mishap targets,
the cast must contain ≥ 3 fixtures matching the predicate (the standard
concurrency level — see §3.5). The cast designer ensures this when
adding scenarios.

**Cast growth.** When a scenario introduces a predicate combo no
existing fixture covers, the cast file gets a new entry as a one-time
reviewed addition. Not per-scenario growth.

### 3.3 Reader locations

A bounded enumeration of every place the platform can observe state or
events. Each entry declares its metadata:

```yaml
# catalogs/readers/<location>.yaml
location: mongo.rooms
owners:    [room-service]                    # services that write here
timestamp_source: createdAt or updatedAt
shape:    model.Room

location: jetstream.OUTBOX_<site>
owners:    [room-worker, message-gatekeeper, room-service]
timestamp_source: metadata.timestamp + seq
shape:    varies by subject pattern

location: cassandra.messages_by_room
owners:    [message-worker]
timestamp_source: created_at_unix_ms
shape:    model.cassandra.Message

location: logs.<service>                     # one entry per service
owners:    [<service>]
timestamp_source: time field on each JSON log line (slog format)
shape:    structured log entry
implementation: reads the container's stdout via `docker logs <container>`
                or `kubectl logs <pod>`, filters by traceparent + run-prefix
                + the scenario's timeframe
```

The bounded catalog is the mechanism that makes negative coverage
automatic. The platform watches every location; the scenario lists only
its positive expectations; anything observed but not expected is flagged
according to the verdict rules (§"Verdict mechanism").

A reader entry MAY include a `consumers:` field listing services that
read or trigger off the location (e.g., `inbox-worker` consumes from
`jetstream.OUTBOX_<site>` via cross-site sourcing). This field is
**documentation only** — the verdict mechanism does not use it. It exists
so humans browsing the catalog can understand cascade topology.

The catalog grows only when a new physical location is added to the
system. A catalog validator at suite startup cross-checks declared
owners against the actual service code (NATS subscription declarations,
JetStream consumer registrations, MongoDB / Cassandra write call sites)
and fails on drift.

### 3.4 Matchers

A small library of typed predicate matchers used inside `reads:`:

```
equals(expected)
contains(substring | item)
count_eq(N)
count_at_least(N)
matches_shape(Type, field=value, ...)
within(duration)              # may compose with any matcher above
```

Each matcher is a Go function. Adding a new matcher is rare and requires
a one-line YAML entry plus a Go function definition.

### 3.5 Mishaps

Mishaps are **perturbation actions only.** They do not declare expected
behavior; the scenario's sequence reads remain authoritative under any
mishap. A mishap-applied test passes if the system still produces the
scenario's expected reads (with nothing extra) — the resilience signal.
It fails if it doesn't.

Two axes:

```yaml
# catalogs/mishaps/env/<name>.yaml
mishap: kill_pod
axis: environment
parameters:
  target_service: <name>
  duration: <e.g., "5s">
executor: ExecuteKillPod

# catalogs/mishaps/env/<name>.yaml
mishap: partition_network
axis: environment
parameters:
  src_site: <site>
  dst_site: <site>
  duration: <duration>
executor: ExecutePartitionNetwork

# (Other environment mishaps: slow_disk, drop_packets, …)
```

There is **exactly one concurrency mishap** in the catalog:

```yaml
# catalogs/mishaps/concurrency/concurrent.yaml
mishap: concurrent
axis: concurrency
parameters:
  N: 3                              # FIXED — no other levels
notes: |
  Resolves the scenario's placeholder predicate against the cast, picks
  3 distinct fixtures matching the predicate, fires the verb in parallel
  for each. Per-actor expected reads must still match for each actor.

  The system is either designed to handle concurrent requests or it
  isn't. Testing higher N adds no information beyond "can it handle
  multiple at once," so we don't generate N=10 / N=100 variants.
```

The catalog grows when a new perturbation kind is added.

### 3.6 Service ability profiles

A per-service catalog of triggers and write locations for **background
activity** — activity not driven by a request the suite issues. Used to
filter background events out of "anomaly" classification during verdict.

```yaml
# catalogs/services/broadcast-worker.yaml
service: broadcast-worker
triggers:
  - source: jetstream.MESSAGES_CANONICAL
    pattern: "*.created"
    on_trigger:
      - writes: chat.room.<roomID>.event
      - writes: logs.broadcast-worker (INFO)

  - source: scheduled
    pattern: cleanup-stale-broadcast-state
    cron: every-5min
    on_trigger:
      - writes: mongo.broadcast_state (deletes stale rows)
      - writes: logs.broadcast-worker (INFO cleanup)
```

A startup validator cross-checks the profile against the service's
declared subscriptions, scheduled jobs, and write paths.

### Catalog storage policy — operations as Go, data as YAML

Each catalog has two halves. Things that EXECUTE (verbs, readers,
matchers, mishaps) are Go code. Things that DESCRIBE (the cast, the
service ability profiles, the metadata that ties a YAML name to its Go
executor) are YAML.

```
internal/harness/catalog/
  verbs/<name>.go               executor functions
  readers/<location>.go         query functions
  matchers.go                   matcher implementations
  mishaps/<name>.go             perturbation executor functions
  fixture-seeder.go             reads cast YAML, seeds env idempotently
  validator.go                  cross-checks catalog YAML vs Go symbols
                                + service code at startup

catalogs/                       all YAML
  verbs/<name>.yaml             metadata + transport effect template
  readers/<location>.yaml       metadata + ownership + timestamp source
  fixture-cast.yaml             the cast roster
  matchers.yaml                 matcher names + signatures
  mishaps/<axis>/<name>.yaml    metadata + executor reference
  services/<name>.yaml          ability profiles

scenarios/
  drafts/<scope>/<name>.yaml
  approved/<scope>/<name>.yaml
```

A YAML name (e.g., `executor: ExecuteCreateRoomViaNATSRequest`)
references a Go symbol; the startup validator confirms the symbol
exists and has the expected signature. A renamed Go function therefore
forces an explicit YAML update — drift surfaces immediately.

---

## Test case expansion

Each authored scenario expands into a **family of test cases** at runtime,
across the **full subset** of mishaps the scenario does NOT exclude.

```
Applicable mishaps for scenario S = (all mishaps in catalog) − S.mishaps.ignore

Test cases generated for S = the power set of Applicable mishaps:
  - empty set (no mishaps applied) → "happy" case
  - every single mishap            → individual stress
  - every pair of mishaps          → cross-axis
  - every triple                   → ...
  - up to: every mishap together   → maximum stress

Typical numbers:
  Catalog has ~10 mishaps. A scenario's `mishaps.ignore` typically lists
  ~3-4 irrelevant ones. Applicable set = ~6-7 mishaps.
  Subsets = 2^7 = 128 test cases per scenario.
```

The `mishaps.ignore` list on the scenario (see Layer 4) is the **control
rod**: it bounds the test-case explosion AND documents which mishaps the
scenario doesn't apply to. Without it, every scenario would run the full
catalog and many test cases would be nonsensical (e.g., "kill the
notification-worker pod during a room-create scenario that doesn't even
trigger notifications").

Input variations are NOT a mishap axis. A different input shape is a
**different scenario** with its own placeholder predicate, its own
happy path, and its own `mishaps.ignore` list. Authors write one happy
version per scenario; the platform multiplies it.

Mishaps do NOT change the scenario's expected reads. The same `reads:`
block is the contract whether the case is happy or any subset of mishaps
is applied. If the system meets the reads under perturbation → resilience
verified. If not → resilience defect surfaces as test failure.

### Concurrency uses distinct cast fixtures

The concurrency mishap fires the scenario's verb in parallel from **3
distinct cast fixtures** matching the placeholder predicate (not the
same actor three times). Three is fixed in the catalog because "the
system can handle concurrent requests" is a property the system either
has or doesn't — testing more actors past the first concurrent-success
doesn't add information.

Cast sizing must guarantee ≥ 3 fixtures matching every concurrency-
applicable predicate.

---

## Verdict mechanism

### Time-frame scope

The platform watches every reader location continuously, but only
events with timestamps within the scenario's **time frame** count toward
verdict. The time frame is **event-driven**, not budget-driven:

```
T_open  = the moment the verb fires.

T_close = (timestamp of the LAST observed in-scope event)
          + small quiet grace period (e.g., 200ms)

  where in-scope event = any event carrying our scenario trace from a
                         service listed in the sequence.

  The quiet grace period is just "make sure no more events are coming."
  It is much smaller and more stable than any per-read budget would be.

Timeframe = [T_open, T_close]
```

Why event-driven and not budget-driven: per-read time budgets are hard
to estimate accurately for distributed cascades. A bad budget either
closes the timeframe too early (missing in-scope events the platform
should have seen) or too late (catching legitimate downstream
out-of-scope activity and flagging it as unexpected). The actual event
stream from in-scope services is a better signal of "we're done" than
any pre-declared duration.

**Safety cap.** If the quiet period never triggers (no events arrive,
or events keep arriving indefinitely from in-scope services for some
pathological reason), a hard timeout (e.g., 30s by default,
configurable) closes the timeframe and the scenario fails with
"timeframe never closed."

**Per-read `within:` (optional)** is decoupled from T_close. When a read
declares `within: N`, the read MUST match within N seconds of T_open or
it's a missing-positive failure — even if T_close is later. This is for
SLA-style assertions ("the synchronous reply must arrive within 1s").
When a read omits `within:`, it's checked at T_close — match any time
before close = pass.

### Three-way event classification

Every change observed at any reader location within the timeframe is
classified into exactly one of three categories:

```
1. E.trace == scenario_trace
   The event is part of the cascade triggered by this scenario's verb.
   Check it against the sequence's expected reads:
     matched         → ✓ (expected read satisfied)
     unmatched       → ✗ unexpected-cascade (cascade did something extra)

2. E.trace ≠ scenario_trace
   AND E's owner service's ability profile matches this event
   The event is legitimate background activity for the owner service.
   → filter out, ignore

3. E.trace ≠ scenario_trace
   AND no ability profile matches
   The event is unattributable — neither this scenario's cascade nor
   any documented background pattern.
   → ✗ anomaly (possibly a trace-propagation gap, possibly a real bug)
```

This three-way model lets the platform accommodate legitimate background
activity (scheduled jobs, leader elections) without false-positive
flagging, while still surfacing genuine anomalies.

### Pass criteria

```
PASS iff:
  every REQUIRED expected read matched (either within its `within:` SLA,
    if declared, or before T_close otherwise)
  AND zero unexpected-cascade events
  AND zero anomalies

OPTIONAL reads contribute neutrally:
  matched      → ✓ (event is "expected," not unexpected-cascade)
  not matched  → ✓ (the optional event simply did not happen; not a failure)

FAIL classes:
  missing-positive    a required read didn't find its match (within SLA
                      or before T_close)
  unexpected-cascade  an event with our trace matched no expected read
                      (neither required nor optional)
  anomaly             an event without our trace and not matching any
                      service's ability profile
  timeframe-never-closed  safety-cap timeout reached without quiet period
                      triggering — typically a system hang or runaway
                      cascade
```

### Per-service attribution

Each failure report names the **owner service** of the failing read or
event (derived from the reader-location catalog). So a failure says
exactly which service deviated and at which location.

---

## Layer 2 — Test runner

The runner loads scenarios, resolves placeholders, fires verbs, observes,
classifies, and reports.

### Components

- **YAML scenario loader + schema validator.** Reads
  `scenarios/drafts/**/*.yaml` and `scenarios/approved/**/*.yaml`,
  validates against the schema, fails fast on malformed input.
- **Catalog validator.** At suite startup, validates that every
  reference between catalog YAML and Go code resolves correctly, and
  that service ability profiles match the running services' declared
  behavior.
- **Predicate resolver.** Resolves scenario placeholder predicates
  against the cast, including cross-placeholder references. Confirms at
  least one valid binding exists per scenario; errors with "no cast
  member matches predicate" otherwise.
- **Mishap expander.** For each scenario, generates the runtime family
  of test cases as the power set of applicable mishaps (catalog mishaps
  minus the scenario's `mishaps.ignore` list).
- **Verb dispatcher.** Invokes the verb's Go executor with the
  resolved input, propagating traceparent across the cascade.
- **Reader infrastructure.** Continuously watches every catalog
  location during the timeframe, captures observed changes with their
  metadata (timestamp, trace, owner).
- **Timeframe closer.** Computes T_close from observed in-scope event
  flow + quiet grace period, with a safety cap.
- **Verdict + reporter.** Applies the three-way classification to each
  observed event, decides per-test-case pass/fail, writes the run
  summary.

### Reporting

The report is intentionally minimal. Each invocation writes:

- **Verdict counts**: total test cases run, passed, failed.
- **Failure breakdown by class** (the four v2 verdict classes):
  - `missing-positive` — a required read didn't match
  - `unexpected-cascade` — an event with our trace had no matching expected read
  - `anomaly` — an event without our trace, not explained by any service ability profile
  - `timeframe-never-closed` — safety cap fired without quiet period
- **Per-service attribution** for each failed test case: which service's read or owned location was implicated.
- **Two-score split**: APPROVED scenarios' verdict counts (the CI-gating number) vs DRAFT scenarios' verdict counts (informational).

The verdict mechanism itself produces the per-test-case classification
automatically. Richer reporting (per-mishap-subset breakdown, trend
analysis, etc.) is anticipated and will be designed once real run data
motivates it.

### Invocation

The runner is invoked via Makefile targets that pre-fill env vars for
the docker-local stack:

```
make -C tools/integration-suite local       run scenarios against local stack
make -C tools/integration-suite run         run scenarios; caller supplies env
make -C tools/integration-suite lint        catalog reference consistency
```

---

## Layer 1 — Infrastructure-control plane

Layer 1 is the **chaos perturbation layer.** It is the implementation
of the environment-mishap axis in §3.5 and corresponds to physical
events the platform applies to the running stack: kill a pod, partition
a network, slow a disk, freeze a subscription.

### Mechanism

Each environment mishap entry in `catalogs/mishaps/env/<name>.yaml`
declares its parameters and references a Go executor. The executor
talks to whatever fault-injection mechanism the stack provides:

- **kind clusters** → chaos-mesh CRDs (`kubectl apply` / `kubectl
  delete`); CRDs are tagged with the run prefix for safe orphan reaping.
- **Docker Compose services** → `docker compose pause/kill/start
  <service>`.
- **TCP-level latency / loss** (optional) → a toxiproxy sidecar between
  service and dependency.

The chaos executor knows nothing about scenarios. It just applies the
named perturbation. The platform composes "apply mishap + run scenario +
observe" into a single test case.

### Scoping

Each environment mishap targets a specific region/site/service. So a
mishap can perturb one site's NATS while leaving another untouched —
useful for federation/regional-resilience scenarios.

### Status

Layer 1 is the natural extension of the design once Layers 4–2 are
operational. It is not required for first runnable scenarios; scenarios
without mishap expansion still produce a valid two-score report. Chaos
adds to the runtime test-case multiplier; it does not change the
authoring or verdict surfaces.

---

## Open questions

These are deliberately left for the implementation plan to refine:

- **Exact YAML schema for scenarios and catalogs.** Field names, nesting
  style, optional/required markings. The shapes in this spec are
  normative for structure; specific field names may be refined.
- **Concurrency cast-sizing policy.** Auto-detect max needed N across
  all scenarios and pre-validate, vs accept underspec and fail loud at
  first run.
- **Depth of catalog validator semantic checks.** Start with shallow
  reference resolution; expand to semantic comparison against service
  code iteratively.
- **AI authoring prompt format.** A living document that grows as new
  failure modes are discovered. Maps to the scenario YAML schema +
  catalog references.

## Risks

- **YAML schema can become a moving target if undisciplined.** Mitigated
  by a schema validator + a short, version-controlled schema doc that
  grows in lockstep with reviewed PRs.
- **Service ability profiles drift behind service changes.** Mitigated
  by the validator that compares profiles against service code at
  startup, plus PR-template rules that require profile updates alongside
  service-code changes.
- **Trace propagation gaps cause false anomalies.** Mitigated by the
  three-way classification surfacing anomalies explicitly rather than
  silently filtering them — gaps become visible rather than mask real
  bugs.
- **Cast roster size grows in unexpected ways.** Mitigated by the
  catalog-growth discipline (every cast change is a PR) and the sizing
  rule (deliberate, not implicit).
- **Catalog growth becomes a bottleneck.** Mitigated by the catalogs
  being explicitly small (six total) and bounded by system topology;
  growth events are rare and align with system changes.
