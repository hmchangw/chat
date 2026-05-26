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

## Blindspots

A **blindspot** is a scenario whose expected outcome is not documented
anywhere in the design. Blindspots are first-class artifacts of the
suite — the way the system surfaces design gaps as quantifiable
conformance failures.

### Definition

A scenario is a blindspot when the author cannot find a documented
expected behavior in any of:

- A spec in `docs/superpowers/specs/`
- `CLAUDE.md`
- A README in the service or `pkg/` package
- A design comment in the source code

If no source exists, the author must **not** invent behavior. They
mark the scenario as a blindspot and log the open question.

### What is and is not a blindspot

| Situation | Blindspot? |
|---|---|
| Design says X should happen; implementation does Y | No — regular failure |
| Design is silent on what should happen for this input | Yes |
| Author too lazy to find the design | No — not an escape hatch |
| Step library can't express the assertion (yet) | No — tooling gap |
| Design says "best effort" without specifics | Yes — needs specifics |
| Same scenario has documented and undocumented assertions | Yes — tag the undocumented part |

### Mechanics — three places, in lock-step

**1. The `.feature` file (the tag)**

```gherkin
@regional-resilience @blindspot:partial-history-contract
Scenario: TW NATS partial outage degrades history but does not corrupt it
  ...
```

Format: `@blindspot:<slug>`, where `<slug>` is kebab-case, unique
across the suite, and serves as a stable handle. A scenario may carry
multiple `@blindspot:` tags when it asks several open questions.

**2. The runtime hook (the marker)**

`main_test.go` registers a `BeforeScenario` hook:

```
For each tag on the scenario:
  if tag starts with "@blindspot:":
    mark scenario as FAILED
    failure reason = "undocumented behavior: <slug>"
    still execute all steps — the actual outcome is informative
```

Steps still execute. The hook only forces the result. Why run the
steps when the outcome is predetermined?

- If steps pass coincidentally, the report shows the implementation
  *did* produce some behavior — useful evidence for the design
  discussion (the current behavior could become the documented one).
- If steps fail, the report shows both the blindspot reason and the
  actual divergence — useful evidence the implementation already
  disagrees with itself.

**3. The register (`docs/integration-suite/blindspots.md`)**

Every unique slug has one entry. Format:

```markdown
## partial-history-contract

**Found in:** features/regional-resilience/partial-nats-tw.feature
**Question:** When part of the NATS supercluster in a region is
unavailable, what contract does history-service hold for partial reads?
**Candidates:**
  - Return whatever messages are reachable, no error
  - Return whatever messages are reachable + a degraded-mode header
  - Return 503 if any partition is unreachable
  - Return 200 with an explicit `partial: true` field
**Owner:** TBD
**Target spec:** docs/superpowers/specs/<future>-history-degraded-mode.md
**Status:** open
**Added:** 2026-05-12
```

### Resolution workflow

When the design is filled in, a **single PR** does all three:

1. Adds the new spec doc.
2. Updates `blindspots.md`: status → `resolved`, target spec links the
   new doc.
3. Updates the feature file: removes the `@blindspot:<slug>` tag,
   replaces vague assertions with the now-documented contract.

The score moves on the next run.

### Guardrails

- **Slug consistency check.** A `make integration-suite-lint` target
  (also run in CI) verifies every `@blindspot:<slug>` in any feature
  file has a matching entry in `blindspots.md`, and vice versa. PR
  fails if inconsistent.
- **Aging review.** Each entry has an `Added` date. A periodic
  review (monthly recommended) closes stale entries — either by
  codifying the behavior or by deleting the scenario as no longer
  relevant.
- **No blindspot filter.** The runner cannot skip blindspots — they
  are always counted in the score. You *can* filter scenarios *to*
  run only blindspots (useful for design sessions) but never *out of*
  the totals.

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

## Defining a test

This is the playbook for adding a scenario to the suite. Designed to
be followed by humans and by AI authoring agents. Every step is
mandatory.

### Step 1 — Pick a behavior

Behaviors come from one of:

- A user-facing flow the system supports ("a US member sees messages
  a TW user posted to a shared channel")
- An invariant ("DM room IDs are deterministic in the sorted
  concatenation of the two user IDs")
- A failure mode the architecture promises to handle ("a worker
  redelivers a message after restart")
- A regression motivated by a real bug

### Step 2 — Find the documented expected behavior

Sources, in order of authority:

1. `docs/superpowers/specs/` — design specs
2. `CLAUDE.md` — project conventions
3. Service or `pkg/` `README.md`
4. Design comments in source code

If none of these say what should happen for the case at hand, **stop**
and follow the blindspot workflow (see Blindspots section). Do not
invent behavior.

### Step 3 — Choose a scope

Pick the most specific scope that contains the behavior:

| Scope | Use when |
|---|---|
| `service/` | One service, no async pipeline, one site |
| `pipeline/` | One async flow end-to-end (request → stream → worker → store), one site |
| `journey/` | Multiple flows composed into a realistic user path, one site |
| `federation/` | Multi-site, no faults |
| `resilience/` | One site, faults included |
| `regional-resilience/` | Multi-site with regional faults |

### Step 4 — Reuse step vocabulary

Step definitions in `steps/*.go` are shared across all scopes. Before
writing a new step, search existing files for a matching phrase.
Adding a near-duplicate (`Given user "alice" is logged in` next to an
existing `Given user "alice" is authenticated`) fragments the
vocabulary; godog treats them as different steps.

If a new step is genuinely needed:

1. Pick the right surface file (`auth_steps.go`, `room_steps.go`, ...).
2. Follow the phrasing rules in Step 5.
3. Place it next to related steps. Do not create a new file for one
   step.

### Step 5 — Phrasing rules

- **Lowercase verbs.** `Given`, `When`, `Then`, `And` are Gherkin
  keywords. The body starts with a lowercase verb.
- **Quoted parameters.** Every variable goes in `"double quotes"` so
  the step reads naturally and the regex stays simple.
- **Site is explicit in multi-site scenarios.** When a step touches a
  service or infra resource and the scenario uses more than one site,
  append `in site "<id>"`. Single-site scenarios omit the suffix;
  the world struct uses the primary site by default.
- **Timed assertions declare a budget.** Any assertion observing an
  async effect uses `within <duration>`:
  `Then within 5s "<name>" is a member of room "<id>"`.
- **No implementation details.** A scenario reads as user-visible
  behavior, not as a description of code.
  Good: `Then alice receives a success reply`.
  Bad: `Then handler.PublishFunc was called`.

### Step 6 — Use Background and Scenario Outline

- **`Background`** for shared setup across all scenarios in one
  feature file. Fixtures only — no behavior under test.
- **`Scenario Outline`** for parameter permutations. Prefer one
  outline with a table over five near-duplicate scenarios.

### Step 7 — Tag the scenario

- The **scope tag is implicit from the folder.** Do not duplicate it.
- Optional tags:
  - `@smoke` — fast, included in CI smoke runs
  - `@slow` — run nightly only
  - `@multi-room` — uses multiple rooms in one scenario
  - `@blindspot:<slug>` — undocumented behavior (see Blindspots)
  - `@error-class:<class>` — flags the dominant expected error class
    when the scenario asserts on a failure mode

### Step 8 — Verify before commit

Run the new scenario locally:

- For normal scenarios: confirm it passes.
- For blindspots: confirm it fails with reason
  `"undocumented behavior: <slug>"`, and confirm `blindspots.md` has
  the matching entry.

### Authoring template

A complete scenario, annotated:

```gherkin
# features/pipeline/room-member-ops.feature   ← scope by folder
Feature: Room member operations
  Behavior driven by the design in docs/superpowers/specs/2026-04-14-add-member-design.md

  Background:
    Given user "alice" is authenticated                    # fixture, not behavior
    And channel room "general" exists with member "alice"  # fixture, not behavior

  @smoke
  Scenario: Adding a new member persists a subscription and replies success
    Given user "bob" is authenticated
    When "alice" requests to add member "bob" to room "general"
    Then within 3s "bob" is a member of room "general"     # async observation, bounded
    And "alice" receives a success reply

  Scenario Outline: Adding a member to a non-existent room replies <class>
    Given user "<requester>" is authenticated
    When "<requester>" requests to add member "<target>" to room "nonexistent"
    Then "<requester>" receives a <class> reply with code "<code>"

    Examples:
      | requester | target | class        | code           |
      | alice     | bob    | HandlerError | ROOM_NOT_FOUND |
```

### Adding tests with AI

The spec is the AI agent's authoring context. A prompt that produces
conformant scenarios includes, at minimum:

1. **The exact behavior to test** — one sentence or paragraph,
   unambiguous.
2. **The source where the expected behavior is documented** — file
   path and section, so the AI can verify rather than invent.
3. **The list of currently registered steps** — produced by
   `make integration-suite-steps` (prints every registered step regex
   on startup). The AI must prefer matching an existing step over
   adding a new one.
4. **The target scope folder.**

The AI follows the same rules as a human author: reuse vocabulary,
prefer outlines, tag blindspots, no invented behavior. If the
documented behavior pointed at does not actually exist, the AI must
register a blindspot — never fill the gap from training data.

A short reference prompt for AI authoring lives in
`tools/integration-suite/README.md` alongside the canonical phrasings.

## Test validity: status and confusion metric

The suite must distinguish behaviors the system **commits to** from
behaviors that have only been **proposed by an author** (AI or human)
and not yet ratified. Without this distinction, AI-authored scenarios
would pollute the conformance score from the moment they are
committed, regardless of whether their expectations are correct.

Two mechanisms working together:

- **Status tag** decides which scenarios count as authoritative.
- **Confusion-metric audit** calibrates how trustworthy the
  authoritative set actually is, by sampling outcomes and having a
  human classify them as TP / TN / FP / FN.

### Status

Every scenario has one of two statuses:

| State | Tag | Meaning |
|---|---|---|
| **draft** | (no `@status:` tag — the default) | An author proposed this as expected behavior. The system is not yet committed to it. |
| **approved** | `@status:approved` | The team has accepted this as a contract the system must honor. A failure here is a defect. |

AI-created scenarios always enter as **draft**. Promotion to
**approved** is a one-line edit (adding the tag) in a human-reviewed
PR. No automated process can promote a scenario.

The **draft list** is the natural review queue: as the AI adds
scenarios, the list grows. Humans walk it and decide which
expectations are real contracts and which are not.

### Two scores per run

The runner reports two scores side by side:

```
APPROVED score:   93.1%   (108 / 116)    ← authoritative; gates CI
DRAFT score:      71.4%   (30 / 42)      ← informational; what AI proposes
```

The **APPROVED score** is the system's measured conformance to its
accepted contracts. CI gates on this score (or on a configured
threshold).

The **DRAFT score** is informational only. Draft failures are visible
(authors iterate against them) but never block.

The blindspot rule applies within each status group: blindspots in
draft count as zeros toward the draft score; blindspots in approved
count as zeros toward the approved score.

### Confusion-metric audit

The status tag controls *which* scenarios count. The audit calibrates
*how reliable* those scenarios are as oracles.

For each approved scenario's outcome, a human classifies against
reality:

|  | Reality: system correct | Reality: system broken |
|---|---|---|
| **Scenario passed** | TP (good) | FP (false confidence) |
| **Scenario failed** | FN (false alarm) | TN (good) |

Workflow:

```bash
make integration-suite-audit SAMPLE=30
```

Produces a markdown checklist of 30 randomly-sampled approved
scenarios from the most recent run, with each scenario's outcome
pre-filled and two empty columns for the reviewer:

```markdown
# Suite Audit — 2026-05-12

Sampled 30 of 116 approved scenarios from run 7a2c.

| Scenario | Outcome | Reviewer says | Class |
|---|---|---|---|
| pipeline/room-member-ops.feature:18 | Passed | Behavior correct | TP |
| pipeline/room-member-ops.feature:42 | Passed | Assertion too loose; behavior was actually wrong | FP |
| resilience/worker-restart.feature:25 | Failed | Behavior correct; scenario expects wrong reply | FN |
| service/room.feature:12 | Failed | Behavior actually broken | TN |
| … |
```

The reviewer fills the last two columns in a markdown editor.
`make integration-suite-audit-tally` reads the file and produces:

```
Confusion matrix (n=30):
              | actually correct | actually broken
test passed   |       TP=22      |      FP=2
test failed   |       FN=1       |      TN=5

Accuracy:                 90.0%
False-positive rate:      28.6%   ← scenarios passing on broken behavior
False-negative rate:       4.3%   ← scenarios failing on correct behavior
```

The result is appended to `docs/integration-suite/audit-log.md`.
Tracking FP and FN rates over time tells you whether the suite is
becoming more or less reliable as it grows.

### Interpretation

- **High FP rate** → the approved set is too lenient. Review more
  strictly; sharpen assertions; tighten `within <duration>` bounds.
- **High FN rate** → the approved set is over-strict or points at the
  wrong source of truth. Revisit cited sources; relax unnecessary
  preconditions; or fix the cited design where the design itself is
  wrong.

### Recommended cadence

- Audit monthly, or after a large batch of new approvals.
- Sample size ≥ 30 for a useful matrix; sample more after big changes.
- An FP rate > 10% should trigger a focused review of the approved
  set before expanding it further.

### What is deliberately not done

- **No automated drift detection** when cited specs change. If FP
  rates show silent drift is a real problem, a `sources.lock`-style
  mechanism can be added later. Until then, the habit on a spec
  change is to `grep` for citing scenarios and re-review them.
- **No per-assertion source quoting.** The scenario-level `# Source:`
  comment plus human review is the v1 oracle. Per-assertion citation
  enforcement can be layered in if reviews prove insufficient.

## Reporting and scoring

After each run the suite writes three artifacts:

1. **`docs/integration-suite/last-run.md`** — human summary:

   ```
   Run:        2026-05-12T14:22:11Z   (runID 7a2c)
   Total:      158
   Duration:   4m12s

   APPROVED   116 scenarios       ← authoritative; gates CI
     Passed:        108
     Failed:          5
       HandlerError:    2
       Timeout:         2
       Persistence:     1
     Blindspot:       3
     Score:        93.1%   (108 / 116)

   DRAFT       42 scenarios       ← informational; never gates
     Passed:         30
     Failed:          7
       HandlerError:    2
       Timeout:         3
       Unreachable:     2
     Blindspot:       5
     Score:        71.4%   ( 30 /  42)

   Last audit: 2026-04-29 (n=30) — accuracy 90.0%, FP 6.7%, FN 3.3%

   Failures (behavior diverged from design)
     [APPROVED] service/room.feature:55 "DM room ID is deterministic"
       class: HandlerError  code: DM_ID_MISMATCH
       trace: 4e1c7a83e4d3b8a2f6c1d2e3f4a5b6c7
     [APPROVED] pipeline/room-member-ops.feature:18 "Idempotent add member"
       class: Timeout
       trace: 9f2b5c91a7e4d8f6b3a2c1e9d7b8a4f3
     [DRAFT]    federation/cross-site-add-member.feature:22 "US member ack"
       class: Unreachable
       trace: 1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d
     - …

   Blindspots (undocumented behavior — design owes an answer)
     [APPROVED] regional-resilience/partial-nats-tw.feature: partial-history-contract
     [DRAFT]    pipeline/room-member-ops.feature: retry-on-mongo-conflict
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
`status.go` (status-tag parsing + score split), `audit.go` (audit
checklist generation + tally), `main_test.go`.

The full 8-class classifier is implemented in v1 even though early
scenarios will only exercise a few classes. The cost is small and it
locks the vocabulary before scenarios proliferate.

The status / audit machinery is implemented in v1 because AI-authored
scenarios start landing immediately and the suite must not let them
pollute the authoritative score from day one.

Makefile targets added in v1:

```
make integration-suite [SCOPE=<scope>] [TAGS=<expr>] [SITE=<id>]
make integration-suite-purge
make integration-suite-lint
make integration-suite-steps
make integration-suite-audit       SAMPLE=<n>
make integration-suite-audit-tally
```

Documentation deliverables in v1:

- `tools/integration-suite/README.md` — user flow (setup, env vars,
  common commands, troubleshooting)
- `tools/integration-suite/AUTHORING.md` — AI + human authoring
  playbook (rules, step vocabulary discovery, scope decision tree,
  template, anti-patterns, checklist, AI prompt template)
- `docs/integration-suite/blindspots.md` — initial empty register
- `docs/integration-suite/audit-log.md` — initial empty log

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
