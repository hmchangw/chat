# Architecture — integration-suite

A scenario-driven, **black-box** conformance suite. It owns no
infrastructure: it authenticates as a real user and drives the
**already-running** chat services over the transports they actually
speak, then classifies the observable reply against the documented
design. Spec: `docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`
(predates the `internal/harness` split — see README "Layout").

## 0. Three layers, top to bottom

The whole tool decomposes into three orthogonal layers. Each layer
multiplies the value of the layer below; missing the bottom layer
caps how interesting the top layer's tests can be.

```
LAYER 3   Scenarios — input → output, no internals.
          ─────────────────────────────────────────────────────────
          Black-box assertions on what crosses the system boundary:
          immediate reply, persisted state, downstream events,
          broadcast deliveries. Authored in Gherkin, drafts by
          default, promoted to @status:approved by humans. Confusion
          matrix is over these verdicts (TP/TN/FP/FN).

LAYER 2   Test runner — the platform that executes Layer 3.
          ─────────────────────────────────────────────────────────
          godog + classifier + reporter + audit. Connects to the
          live stack as an authenticated NATS user (HTTP /auth →
          NATS JWT → request/reply). Captures responses, classifies
          them into 9 classes, writes the two-score + coverage +
          last-audit report.

LAYER 1   Infrastructure-control plane — what perturbs the system.
          ─────────────────────────────────────────────────────────
          Crash a pod, partition a network, slow a disk, freeze a
          subscription. Without this layer, Layer 3 tests only the
          "happy infra × various inputs" cross product. With it, the
          test space becomes "various inputs × various infra states"
          — which is where production actually breaks. Currently
          Part-2 work (chaos-mesh + docker compose pause/kill).
```

Implications:

- **What counts as "output" depends on Part-1 vs Part-2 primitives.**
  Part-1 sees immediate replies only. Part-2 adds state observation
  (Mongo, Cassandra, JetStream peek) and Layer-1 fault injection.
- **Tests are NOT a replacement for unit tests.** Internal logic is
  out of scope. Layer 3 asserts only what the system reveals at its
  boundary — value / error / warning × right / wrong.
- **The confusion matrix lives at Layer 3 verdicts, calibrated by
  Layer 2's audit loop.** Layer 1 expands the input axis; it does
  not change the verdict mechanism.

## 1. Context — the suite vs the system under test

```
        ┌──────────────────────────── tools/integration-suite ───────────────────────────┐
        │  go test -run TestFeatures   (godog)                                            │
        │     features/*.feature  ──►  *_steps_test.go  ──►  internal/harness              │
        └───────────────┬──────────────────────────────────────────────┬─────────────────┘
                        │ HTTP (auth only)                               │ NATS req/reply
                        ▼                                                ▼
                ┌───────────────┐                              ┌──────────────────┐
                │ auth-service  │  mints NATS JWT (dev mode)    │  chat-local-nats │
                │  :8080 (HTTP) │ ─────────────────────────────►│  :4222 (broker)  │
                └───────────────┘                              └────────┬─────────┘
                                                                        │ subject routing
                       live docker-compose stack (NOT kind):            ▼
            room-service · history-service · message-gatekeeper · *-worker · …
                       │            │                 │
                     Mongo       Cassandra        JetStream  (observed only in Part 2)
```

The suite connects to whatever `.env.local` points at
(`AUTH_SERVICE_URL_*`, `NATS_URL_*`). It never starts/stops containers.

## 2. Package architecture (the boundary)

```
tools/integration-suite/                 package integrationsuite  (TEST binary only)
│  main_test.go            godog entrypoint: load Config, build World, run, report
│  *_steps_test.go         step defs — the Given/When/Then vocabulary
│  http_helpers_test.go    newHTTPClient()   ─┐ runner-side transport helpers
│  nats_request_test.go    natsRequest()     ─┘ (test-only; call harness API)
│        │  dot-import (steps stay unqualified: suiteWorld, natsRequest…)
│        ▼
└─ internal/harness/                     package harness  (importable library)
     World            per-scenario state, ID prefixing, NATS conn pool, LastResponse
     classifier       reply  ──►  Class (None/Validation/Auth/HandlerError/…)
     nats_client      authenticated (JWT+nkey) connection pool, keyed account@site
     nats_response    parse {"error":…} / map transport errors (no-responders→RouteNotFound)
     credentials      nkey keypair + JWT plumbing
     tracing          W3C traceparent injected per request
     config           env → Config (SITES, PRIMARY_SITE, per-site URLs)
     cucumber_parse   reports/cucumber.json → summary + failures
     reporter         APPROVED vs DRAFT two-score render
     status/blindspot draft|approved tag logic; @blindspot enforcement
     audit/lint       calibration sampling; blindspot slug↔register check

cmd/{steps,lint,audit,audit-tally}        standalone CLIs (import internal/harness)
docs/integration-suite-v1/{last-run.md,blindspots.md,audit-log.md}   outputs & registers
```

Go forces `*_steps_test.go` + the runner to stay top-level (test files
must sit with the package they test); `internal/` makes the framework
non-importable by anything outside this tool and signals "authors don't
edit this."

## 3. Scenario lifecycle (one scenario, end to end)

```
godog reads features/<scope>/X.feature
   │
   ├─ Before hook ─ World.BeginScenario(name)
   │       resets credentials, installs IDPrefixer  it-<runID>-<scenarioID>-<entity>
   │
   ├─ Given user "alice" is authenticated
   │       GenerateUserNkey() → POST /auth {account,natsPublicKey} → JWT
   │       World.SetCredentials("alice", {Account,JWT,Seed})
   │
   ├─ When  "alice" <verb> "<entity>"
   │       step builds pkg/model request + pkg/subject subject
   │       natsRequest(): pooled NATS conn (UserJWTAndSeed) ─► live service ─► reply
   │       World.SetLastResponse({Transport, Body, ErrorText, TraceID, Err})
   │
   ├─ Then  the response is [a <Class>] ...
   │       LastResponse.Class() compared to the asserted Class
   │
   └─ After hook ─ if @blindspot:<slug> → force-fail "undocumented behavior"
```

Each scenario is independent: fresh user creds, fresh ID prefix, fresh
connection pool. No shared fixtures, no execution-order coupling.

## 4. Transport & classification dispatch

Authors never choose a transport — the **step verb** picks it via which
`pkg/subject` builder it calls; classification then dispatches on the
recorded transport:

```
            subject.RoomsCreate / MemberRoleUpdate / MsgThread …  → NATS req/reply
   step ─┤  (auth bootstrap only)                                 → HTTP POST /auth
            │
            ▼  LastResponse.Transport
        ┌── "nats" ──► ClassifyNATS: ErrNoResponders→RouteNotFound; ErrTimeout→Timeout;
        │               else substring of {"error":…}:
        │                 invalid/required/malformed → Validation
        │                 mongo/cassandra/db/database → Persistence
        │                 unauthorized/forbidden/permission → Auth
        │                 (none) → HandlerError ;  empty → None (success)
        └── "http" ──► ClassifyHTTP: 2xx→None 401/403→Auth 400→Validation
                        404/409/422→HandlerError 5xx→Timeout/Persistence/Downstream
```

Architectural consequence (discovered, documented): services sanitize
error text, so design-level *forbidden/not-found* often lands in
`HandlerError`. Asserting the design-true class then fails — correctly
surfaced as a **blindspot**, not fudged green.

## 5. Governance: two-score, blindspots, calibration

```
 cucumber.json ─► reporter ─► docs/integration-suite-v1/last-run.md
                                ├─ APPROVED  (@status:approved)  ← CI-gating
                                └─ DRAFT     (everything else)   ← informational

 @blindspot:<slug>  ──(After hook fails the scenario)──►  must have
        ▲                                                  ## <slug>
        └───────────  cmd/lint enforces slug ⇄ register  ──┘ in blindspots.md

 cmd/audit  → sample N scenarios → human labels → cmd/audit-tally
        → accuracy/FP/FN appended to audit-log.md  (classifier calibration)

 coverage.md (## <slug> + Source) ◄─@covers:<slug>─ scenarios
        → ScoreCoverage(cucumber.json) → COVERAGE block in last-run.md
          covered / known-gap / uncovered   (report-only)
```

Three orthogonal axes — keep them distinct:

| Axis | Question | Mechanism |
|---|---|---|
| Soundness | did behavior match design? | pass/fail two-score |
| Completeness | is every documented case tested? | coverage.md + `@covers` |
| Trust | is the verdict itself right? | audit confusion matrix |

- **Draft-by-default:** new scenarios never carry `@status:approved`;
  promotion is a separate, human-reviewed PR (AUTHORING RULE 5).
- **Blindspots-as-failures:** undocumented/unverifiable behavior is
  recorded and *fails*, never silently passes (RULE 2).
- **Coverage is report-only:** a documented case with no passing
  `@covers` scenario shows as *uncovered*; `Status: blindspot` cases
  show as *known-gap*. Low coverage never breaks CI (by design).

## 6. Part-1 boundary (what shapes coverage)

| Reachable in Part-1 | Deferred to Part-2 |
|---|---|
| HTTP (auth) + NATS request/reply | JetStream publish / consume / peek |
| Synchronous reply classification | Mongo / Cassandra state assertions |
| Pre-access validation (e.g. thread empty-id) | Anything behind a subscription/room fixture |
| `{"status":"accepted"}` acknowledgements | Post-condition (subscription/role actually changed) |

Why it matters architecturally: e.g. 7 of 8 history-service ops gate on
a subscription the suite can't establish in Part-1, and message
submission is fire-and-forget JetStream with a decoupled reply — so
those contracts are captured as registered blindspots that go green
automatically once the Part-2 primitives land. The boundary is a
property of the design, not a gap in the tests.
