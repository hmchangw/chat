# Integration Test Suite — Design

## Purpose

A scenario-driven black-box integration test suite that exercises the
chat backend as deployed — drives the public surfaces of the services
and observes downstream effects on NATS, MongoDB, and Cassandra. Unlike
load testing (`tools/loadgen/`), which repeats the same operation to
measure capacity, this suite walks distinct scenarios to find
behavioral defects.

The suite answers one question per scenario: **for this trigger, did
the system do what the design says it must?** It produces a pass/fail
score per run, surfaces undocumented behaviors as first-class
"blindspots," and grows incrementally as new flows are added.

## Goals

- Encode the system's documented behaviors as executable scenarios.
- Make each scenario an **unconditional truth** — independently true
  regardless of other scenarios, execution order, or pre-existing state.
- Cover six scopes: single-service, single-pipeline, end-to-end user
  journey, multi-site federation, single-site fault resilience, and
  multi-region fault resilience.
- Allow the suite to grow service-by-service and flow-by-flow without
  rewrites.
- Treat undocumented expected behavior as a **blindspot** that lowers
  the conformance score until the design pins down the contract.
- Run against the existing local development topology (services in
  Docker Compose, infrastructure in a kind cluster).

## Non-goals

- Owning or provisioning infrastructure. The suite connects to whatever
  NATS, MongoDB, and Cassandra URLs are provided.
- Owning the docker-compose definitions for services-under-test. Those
  remain with each service's `deploy/` directory or with a shared
  harness compose file that lives outside this tool.
- Measuring throughput or latency. Capacity/perf is `tools/loadgen/`.
- Replacing the per-service unit tests, handler tests, or
  `integration_test.go` files. Those test code; this suite tests the
  assembled system.
- Generating production traffic. All scenario data is namespaced so it
  can never be confused with real data.

## Design principles

### Unconditional truth

Every scenario states an invariant that holds in isolation. Concretely:

- Each scenario creates its own fixtures with unique IDs derived from a
  per-run prefix (`it-<runID>-<scenarioID>-<entity>`). No shared "test
  alice"; every scenario gets its own alice.
- Each scenario sets up its own preconditions in `Given` steps. Never
  assume seeded state.
- Assertions describe behavior, not snapshots. *"After I add member X,
  the room MUST list X as a member"* — not *"the rooms collection has
  three documents."*
- Scenarios are runnable in any order, in parallel, against a stack
  that already contains unrelated data.

### Architecture as source of truth

A scenario's expected outcome must come from a documented design
(`docs/`, `CLAUDE.md`, per-service design notes, README). If no
documented expectation exists, the scenario is tagged `@blindspot:<reason>`
and counts as a failure until the design is filled in.

The scenario author **must not invent expected behavior**. Authoring
rules:

1. Find the documented expected behavior.
2. If none exists: stop, tag `@blindspot`, log the open question in
   `docs/integration-suite/blindspots.md`.
3. Only assert behaviors with a documented source.

### Blindspots as zeros

The run score is:

```
total     = passed + failed + blindspot
score (%) = 100 * passed / total
```

Blindspots are not skipped, pending, or filtered out. They are failures
with a special reason. Adding a blindspot lowers the conformance score;
filling in the design (and removing the tag, or codifying the
expectation) raises it.

## Tooling

| Component | Choice | Why |
|---|---|---|
| Scenario language | **Gherkin** (`.feature` files) | Human-readable, table-driven outlines, tag-based filtering |
| Test runner | **godog** (Cucumber for Go) | Native Go, reuses `pkg/natsutil`, `pkg/mongoutil`, `pkg/cassutil`; runs via `go test`; no new language in CI |
| Cluster fault injection | **chaos-mesh** | kind-native, declarative CRDs, can target specific namespaces/pods (essential for "kill NATS node 2 in TW") |
| Service fault injection | **`docker compose pause/kill`** | Service-side faults (pause room-worker mid-message); no extra dependency |
| Result format | **cucumber JSON + JUnit XML** | godog emits both natively; CI ingest is free |

The suite does not depend on `testcontainers-go` — it does not own
infrastructure lifecycle.

## Topology

```
[kind cluster]                        shared infra (per region)
  ├── namespace chat-tw
  │     ├── NATS supercluster (3 nodes)
  │     ├── MongoDB
  │     └── Cassandra
  └── namespace chat-us
        ├── NATS supercluster (3 nodes)
        ├── MongoDB
        └── Cassandra

[docker-compose on host]              services-under-test (per region)
  ├── compose-tw: auth, room, room-worker, … → kind namespace chat-tw
  └── compose-us: auth, room, room-worker, … → kind namespace chat-us

[integration-suite on host]           the tester
  ├── HTTP / NATS-request → docker-compose services (drive)
  ├── NATS subscribe / Mongo find / Cassandra select → kind (observe)
  └── chaos-mesh CRDs via kubectl     → kind (inject regional faults)
  └── docker compose CLI              → docker-compose (inject service faults)
```

The suite holds **two URL groups** in config:

- **Service URLs**, keyed by site: `ROOM_SERVICE_URL_TW`,
  `AUTH_SERVICE_URL_US`, etc. Points at docker-compose host ports.
- **Infra URLs**, keyed by site: `NATS_URL_TW`, `MONGO_URI_TW`,
  `CASSANDRA_HOSTS_TW`, etc. Points at kind (ingress, NodePort, or
  port-forwards).

A single-site scenario only consumes the `_TW` keys. Multi-site
scenarios consume both.

### Site-to-namespace mapping

The chaos driver needs to know which kind namespace backs each
`SITE_ID`. Configured via env:

```
SITES=tw,us
SITE_TW_NAMESPACE=chat-tw
SITE_US_NAMESPACE=chat-us
```

This mapping is the only thing tying the suite to a Kubernetes
deployment. If a site is later moved to a different cluster or to bare
docker, the chaos driver swaps implementations; scenarios are
unchanged.

## File layout

```
tools/integration-suite/
├── README.md
├── main_test.go                 godog entry point + cucumber/JUnit reporters
├── world.go                     shared test context (clients, run ID, fixtures)
├── config.go                    env-var parsing (caarlos0/env)
├── reporter.go                  last-run.md summary writer + blindspot tally
├── deploy/
│   └── azure-pipelines.yml      CI integration (later phase)
├── features/
│   ├── service/
│   │   └── room.feature
│   ├── pipeline/
│   │   └── room-member-ops.feature
│   ├── journey/                 (placeholder for v2)
│   ├── federation/
│   │   └── cross-site-add-member.feature
│   ├── resilience/
│   │   └── room-worker-restart.feature
│   └── regional-resilience/
│       └── partial-nats-tw.feature
└── steps/
    ├── auth_steps.go            user creation, JWT minting via auth-service
    ├── room_steps.go            room HTTP API verbs
    ├── nats_steps.go            publish, subscribe-and-wait
    ├── jetstream_steps.go       stream/consumer assertions
    ├── mongo_steps.go           collection queries
    ├── cassandra_steps.go       row queries
    ├── chaos_steps.go           chaos-mesh + docker-compose fault verbs
    ├── error_steps.go           error-class assertion steps
    ├── classifier.go            response → error-class mapping
    ├── tracing.go               traceparent propagation, trace-ID capture
    └── fixtures.go              ID generation, prefixing helpers
```

The location `tools/integration-suite/` matches the existing convention
established by `tools/loadgen/` and `tools/nats-debug/`.

## Scope organization

Six scopes, each a folder under `features/`:

| Scope | Surface | Example scenario |
|---|---|---|
| `service/` | One service, HTTP + its own DB | "Creating a channel room persists it with the requester as owner" |
| `pipeline/` | One event flow end-to-end | "Add-member request → ROOMS stream → room-worker → subscriptions written → reply" |
| `journey/` | Realistic multi-flow user paths | "New user signs up, creates room, invites friend, posts message" |
| `federation/` | Multi-site, happy path | "TW user adds US member; subscription appears in both sites" |
| `resilience/` | Single-site faults | "room-worker is killed mid-processing; the message is redelivered and processed exactly once" |
| `regional-resilience/` | Multi-site + regional faults | "Part of TW NATS dies; TW history returns a partial result with degraded-mode signal; US is unaffected" |

Folders run independently via tag filters:

```
make integration-suite SCOPE=service
make integration-suite SCOPE=pipeline
make integration-suite SCOPE=full
make integration-suite TAGS=@smoke
```

## Authoring model

### Step vocabulary

Steps are grouped by surface (`auth_steps.go`, `room_steps.go`,
`nats_steps.go`, ...) and shared across all scopes. A scenario in
`journey/` reuses the same `Given user "alice" is authenticated`
binding as `service/`.

Canonical step shapes (illustrative, not exhaustive):

```
Given user "<name>" is authenticated in site "<site>"
Given channel room "<id>" exists in site "<site>" with member "<name>"
When "<name>" requests to add member "<name>" to room "<id>"
When "<name>" fetches history for room "<id>"
Then within <duration> stream "<name>" in site "<site>" has <n> messages for room "<id>"
Then within <duration> "<name>" is a member of room "<id>"
Then "<name>" receives a success reply for request "<reqID>"
Then the response indicates degraded mode
```

`<site>` defaults to the primary site when omitted, so single-site
scenarios stay concise.

### Fixtures

Two patterns, in order of preference:

1. **Through the real API.** `Given user "alice" exists` calls
   `auth-service` to register and mint a JWT. Tests the production
   path while preparing state.
2. **Direct DB write.** Only when no API can produce the state, or
   when bypassing is necessary to test a specific edge condition
   (e.g., a corrupt subscription document). Marked clearly in the step
   name (`Given a malformed subscription document exists for room "<id>"`).

All scenario data carries the run-prefix:
`it-<runID>-<scenarioID>-<entity>`. The `runID` is set once per
`go test` invocation; the `scenarioID` is generated per scenario.
Examples:

- User account: `it-7a2c-room-create-01-alice`
- Room ID: `it-7a2c-room-create-01-general`
- JWT subject: same as account
- NATS request reply subject: standard
  `chat.user.<account>.response.<reqID>`, where `<reqID>` is also
  prefixed

The prefix makes collision impossible and cleanup trivial.

### Data lifecycle

- **No cleanup between scenarios.** Unique IDs sidestep collisions.
- **`make integration-suite-purge`** drops anything with the `it-`
  prefix from Mongo collections and Cassandra tables, and consumes any
  matching JetStream messages. Run on demand or nightly.
- Expected volume per 10,000 scenarios: ~50–100 MB across all stores.

### Authentication

Every scenario that touches a user-facing surface mints a real JWT via
`auth-service` in `Given user "<name>" is authenticated`. This tests
auth-service as part of every flow at no extra authoring cost.

If JWT minting becomes a measurable bottleneck, the step implementation
will cache JWTs per (run, account) inside the world struct. Caching is
an internal optimization — never visible in feature files.

## Traceability and error classification

`pkg/natsrouter` is the request-handling spine of every NATS service in
the system. Failures inside that mesh need to be **traceable** (so a
failing scenario points to exactly one trace in Jaeger) and
**classified** (so the report shows the *shape* of failures, not just
a count).

### Trace propagation

- Every outbound request from the suite — HTTP and NATS — carries a
  fresh `traceparent` header in W3C Trace Context format.
- The trace ID is captured in the scenario's world struct at request
  time, alongside the application request ID (`it-<runID>-<scenarioID>-<reqID>`).
- On scenario failure, the reporter prints both the request ID and the
  trace ID. A developer pastes the trace ID into Jaeger to see the
  full mesh of spans across services.
- The suite does **not** assert on trace structure in v1. Span-level
  contract testing is a deliberate v2 layer (likely Tracetest), once
  scenarios exist to motivate it.

### Error classification

Failed responses are classified into a fixed enum. Scenarios assert on
the **class**, not just on the existence of a failure:

| Class | Source | Example trigger |
|---|---|---|
| `RouteNotFound` | `pkg/natsrouter`: no handler registered for the subject | Request to an unknown subject pattern |
| `Validation` | Handler binding/validation rejected the request | Malformed body, missing required field |
| `Auth` | JWT missing, invalid, or unauthorized | Expired token, wrong NKey signature |
| `HandlerError` | Application logic returned a typed `model.ErrorResponse` | Room not found, member already exists |
| `Timeout` | NATS request timed out (no reply within `request.timeout`) | Worker stalled, consumer paused |
| `Unreachable` | NATS connection error, no responders | Cluster down, all instances dead |
| `Persistence` | Downstream store error surfaced through the handler | Mongo write conflict, Cassandra unavailable |
| `Downstream` | Handler called another service and got a non-success | Room-service called auth-service and got 5xx |

Classifier rules:

- HTTP responses are classified by status code + body shape
  (`model.ErrorResponse.Code`).
- NATS request/reply responses are classified by the
  `model.ErrorResponse.Code` field set by `natsutil.ReplyError`, plus
  the NATS-level error (`nats.ErrNoResponders`, `nats.ErrTimeout`).
- Anything that can't be classified is logged as
  `Unclassified` and counts as a failure — the suite is expected to
  evolve the classifier rather than absorb unclassified failures
  silently.

### Step shapes

```
Then the response is a <error-class> error
Then the response is a <error-class> error with code "<error-code>"
Then "<name>" receives an <error-class> reply for request "<reqID>"
```

`<error-class>` is one of the eight classes above.

### Reporting

The summary report breaks down failures by class so the failure shape
is visible at a glance:

```
Failures:           12
  HandlerError:      4
  Timeout:           5
  Persistence:       1
  Unreachable:       2
  Unclassified:      0     ← non-zero here is itself a problem

Blindspot:           8     treated as zeros
```

Per-failure entries in the summary include the trace ID:

```
  - pipeline/room-member-ops.feature:18 "Idempotent add member"
    class:    HandlerError
    code:     ROOM_NOT_FOUND
    trace:    4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7
    request:  it-7a2c-pipeline-add-member-04-req-3
```

### Why this matters

Since `pkg/natsrouter` underpins many services, errors propagate
through a mesh. A timeout on one request might be a downstream
service's `HandlerError`, which might be a Cassandra `Persistence`
error, which might be a `Unreachable` symptom of partial NATS outage.
Classified results make the shape of the cascade legible across many
scenarios; a count of "12 failures" does not.

## Fault injection model

Faults are expressed as ordinary Gherkin steps. The two drivers sit
behind `chaos_steps.go`.

### chaos-mesh (cluster-side)

Used for: NATS node loss, network partition between sites, packet
loss, DB unavailability, slow disk, time skew.

Each fault step creates a chaos-mesh CRD via `kubectl apply`, waits for
the chaos to take effect, runs the body of the scenario, and on
`After` removes the CRD. The CRDs are tagged with the run prefix so
that orphans from a crashed run can be reaped.

Example bindings:

```
When NATS is unavailable in site "<site>" for <duration>
When <n> of <m> NATS nodes in site "<site>" are killed
When the network between site "<src>" and site "<dst>" is partitioned
When MongoDB in site "<site>" has <duration> added latency
```

### docker compose (service-side)

Used for: killing or pausing a service-under-test container.

```
When room-worker in site "<site>" is paused
When room-worker in site "<site>" is killed
Then room-worker in site "<site>" is restarted
```

Wraps `docker compose -p <project> pause/kill/start <service>`.

### Targeting

All fault steps require a `<site>` (no implicit "all sites" — that
hides too much). For chaos-mesh, `<site>` maps to a kind namespace via
the `SITE_<X>_NAMESPACE` env var. For docker compose, `<site>` maps to
a project name via `SITE_<X>_COMPOSE_PROJECT`.

## Multi-site model

Site is a first-class dimension on every step that touches a
service-or-infra. Two patterns:

**Single-site scenario:**

```gherkin
Scenario: Creating a channel room persists it with the requester as owner
  Given user "alice" is authenticated
  When "alice" creates channel room "general"
  Then room "general" exists with owner "alice"
```

The primary site is implicit; everything runs in `site-tw`.

**Multi-site scenario:**

```gherkin
Scenario: Adding a US member to a TW room federates to US
  Given user "alice" is authenticated in site "tw"
  And user "bob" is authenticated in site "us"
  And channel room "general" exists in site "tw" with member "alice"
  When "alice" requests to add member "bob" to room "general"
  Then within 10s "bob" is a member of room "general" in site "tw"
  And within 10s "bob" has a subscription to room "general" in site "us"
```

Multi-room is naturally covered by scenarios that create several rooms;
no special folder is needed. Apply the tag `@multi-room` if filtering
is useful.

## Reporting and scoring

After each run the suite writes three artifacts:

1. **`docs/integration-suite/last-run.md`** — human summary:

   ```
   Run:        2026-05-12T14:22:11Z   (runID 7a2c)
   Total:      158
   Passed:     138
   Failed:      12
     HandlerError:    4
     Timeout:         5
     Persistence:     1
     Unreachable:     2
     Unclassified:    0
   Blindspot:    8         treated as zeros
   Score:    87.3%         (138 / 158)
   Duration:  4m12s

   Failures (behavior diverged from design)
     - service/room.feature:55 "DM room ID is deterministic"
       class: HandlerError  code: DM_ID_MISMATCH
       trace: 4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7
     - pipeline/room-member-ops.feature:18 "Idempotent add member"
       class: Timeout
       trace: 9f2b5c91a7e4d8f6b3a2c1e9d7b8a4f3
     - …

   Blindspots (undocumented behavior — design owes an answer)
     - regional-resilience/partial-nats-tw.feature: partial-history-contract
     - …
   ```

2. **`reports/cucumber.json`** + **`reports/junit.xml`** — machine-
   readable, picked up by CI.

3. **`docs/integration-suite/blindspots.md`** (living document) —
   running list of every `@blindspot:<reason>` tag in the suite, the
   feature file it appears in, and a target resolution. Each blindspot
   has an owner once the design discussion starts.

### Blindspot mechanics

A `BeforeScenario` hook inspects tags. If any tag matches
`@blindspot:<reason>`, the scenario is registered as a failure with
reason `"undocumented behavior: <reason>"` regardless of step outcomes.
This guarantees a blindspot scores zero until either:

- The tag is removed (design now exists; scenario asserts real
  expectations), or
- The reason is documented in `blindspots.md` *with a resolved
  expected behavior*, and the feature file is updated to assert it.

Workflow rule: the same PR that updates the design doc updates the
feature file. The score and the design move together.

## v1 implementation target

A first version that proves every scope works end-to-end against
`room-service` + `room-worker` + `auth-service`. One feature file per
scope (`journey/` deferred to v2 — it's a re-composition of existing
steps, not new infrastructure):

| Feature | Path | Highlights |
|---|---|---|
| Room HTTP basics | `features/service/room.feature` | Create channel room; create DM room is deterministic; read missing room returns 404; unauthenticated request returns 401 |
| Add-member pipeline | `features/pipeline/room-member-ops.feature` | Add member → ROOMS stream → worker → subscription persisted → success reply; idempotent re-add; add to missing room replies error |
| Federated add-member | `features/federation/cross-site-add-member.feature` | TW user adds US member; subscription appears in both sites |
| Worker restart | `features/resilience/room-worker-restart.feature` | Kill room-worker mid-process; message redelivers; subscription end-state correct |
| TW partial NATS outage | `features/regional-resilience/partial-nats-tw.feature` | Kill 1 of 3 NATS nodes in TW; observe history behavior; **likely contains `@blindspot:partial-history-contract`** |

Step files for v1: `auth_steps.go`, `room_steps.go`, `nats_steps.go`,
`jetstream_steps.go`, `mongo_steps.go`, `cassandra_steps.go`,
`chaos_steps.go`, `error_steps.go`, plus `world.go`, `config.go`,
`fixtures.go`, `classifier.go`, `tracing.go`, `reporter.go`,
`main_test.go`.

The full 8-class classifier is implemented in v1 even though early
scenarios will only exercise a few classes. The cost is small and it
locks the vocabulary before scenarios proliferate.

Makefile additions (in root `Makefile`):

```
make integration-suite [SCOPE=<scope>] [TAGS=<expr>] [SITE=<id>]
make integration-suite-purge
```

## Out of scope for v1

- `journey/` scope (will be added once step vocabulary stabilizes).
- Auto-generated permutation matrices beyond what Gherkin
  Scenario Outlines provide.
- Visual reporting dashboards (Markdown + JUnit XML is enough).
- CI integration (will be added in a follow-up; v1 is locally runnable).
- A pluggable cluster backend other than kind. The chaos driver hard-
  codes kind for v1; the abstraction is left for when we need it.

## Open questions

- **Site→namespace mapping in your kind setup**: spec assumes each site
  has its own namespace (`chat-tw`, `chat-us`). If a different
  convention is in use (labels, separate clusters), the chaos driver's
  targeting changes — implementation detail, not a design change.
- **History "degraded mode" signal**: HTTP header? response body
  field? Today this is `@blindspot:partial-history-contract`. The first
  resilience scenario will surface it.

## Risks

- **Step vocabulary drift.** Two authors write
  `Given user "alice" is logged in` and `Given user "alice" is
  authenticated`; godog treats them as different steps. Mitigation: a
  short style guide in `tools/integration-suite/README.md` listing the
  canonical phrasings, plus periodic review of the steps registered at
  startup.
- **Flakiness from async observation.** Every assertion that observes
  downstream effects is bounded by `within <duration>`. Default
  poll interval is 100ms; total wait is per-step. Steps that don't
  declare a wait are synchronous and fail immediately on mismatch.
- **Blindspot accumulation.** If blindspots grow unbounded, the score
  drops and motivation drops with it. Mitigation: blindspots are a PR
  blocker on the design side — adding a `@blindspot` to the suite
  obligates a follow-up issue against the design doc.
- **Chaos cleanup on crashed runs.** chaos-mesh CRDs survive a `go test`
  crash. Mitigation: every CRD is tagged with the run prefix; a
  `make integration-suite-purge` sweep deletes orphans.
