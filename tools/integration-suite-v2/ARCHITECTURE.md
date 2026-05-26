# Architecture — integration-suite v2

A scenario-driven, **black-box** conformance suite for the chat
backend. It owns no infrastructure: the runtime authenticates as a real
user, drives the **already-running** services over the transports they
actually speak, watches the locations where their effects land, then
classifies what it observed against what the scenario asked for.

Spec: `docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md`.
Part-1 plan: `docs/superpowers/plans/2026-05-22-integration-suite-v2-part1.md`.

## 0. Four layers, top to bottom

The whole tool decomposes into four orthogonal layers. Each layer
multiplies the value of the layer below it.

```
LAYER 4   Scenarios — what to test.
          ─────────────────────────────────────────────────────────
          One YAML file per scenario. Names a verb, a credential,
          payload, the sequence of reads we expect, and the mishap
          subsets to skip. The author never picks a transport, never
          writes Go, never invents step phrasing.

LAYER 3   Catalogs — the building blocks.
          ─────────────────────────────────────────────────────────
          YAML data + Go executors, curated. Six catalogs: verbs,
          matchers, readers, services, fixture-cast, (mishaps are
          declared by the runtime). The validator cross-checks every
          name a scenario references against the registered set.

LAYER 2   Runtime — the platform that executes Layer 4 over Layer 3.
          ─────────────────────────────────────────────────────────
          Loads catalogs, seeds the cast, resolves placeholders,
          fires the verb with a fresh traceparent, multiplexes every
          reader's events into one stream, closes the timeframe when
          the cascade goes quiet, classifies events three ways
          (in-cascade / background / anomaly), writes the verdict.

LAYER 1   Chaos — what perturbs the system. (Part-2/3 work.)
          ─────────────────────────────────────────────────────────
          Kill a pod, partition a network, slow a disk, run N
          requests concurrently. Mishap framework is wired in v2
          (subset expansion + per-scenario blacklist), but only the
          empty subset ("happy") runs in Part-1.
```

Implications:

- **What counts as "output" depends on which readers are registered.**
  Part-1 has three: NATS reply, `mongo.rooms`, and `logs.room-service`.
  Adding a reader (e.g. `cassandra.messages_by_room`) automatically
  expands what every scenario observes — no per-scenario change.
- **Negative coverage is automatic.** Every event from a registered
  reader is classified. If a service emits an effect the scenario
  didn't ask for, it lands in *unexpected-cascade* (in-trace) or
  *anomaly* (out-of-trace), both of which fail the verdict. There is
  no "anything else can happen" hole.
- **Mishaps never change expectations.** A scenario's reads are
  authoritative under any mishap subset. Either the system handles the
  perturbation and the reads still match, or it doesn't and the
  verdict fails.

## 1. Context — the suite vs the system under test

```
        ┌──────────────────── tools/integration-suite-v2 ─────────────────────┐
        │  cmd/runner                                                          │
        │     scenarios/**.yaml + catalogs/**.yaml ──► internal/runtime        │
        │     ──► writes docs/integration-suite-v2/last-run.md                 │
        └─────────────────┬───────────────────────────────────┬───────────────┘
                          │ HTTP (auth only)                   │ NATS req/reply
                          ▼                                    ▼
                  ┌───────────────┐                  ┌──────────────────┐
                  │ auth-service  │  mints NATS JWT  │  chat-local-nats │
                  │  :8080 (HTTP) │ ────────────────►│  :4222 (broker)  │
                  └───────────────┘                  └────────┬─────────┘
                                                              │ subject routing
                            live docker-compose stack:        ▼
            room-service · room-worker · message-gatekeeper · *-worker · …
                       │            │                 │
                     Mongo       Cassandra        JetStream
                       │                                                  ⌃
                       └─ observed by mongo.* readers; docker logs ─ container_logs
```

The runner connects to whatever the env vars point at
(`AUTH_SERVICE_URL`, `NATS_URL`, `MONGO_URI`, `MONGO_DB`, `SITE_ID`).
It never starts or stops containers; preflight in the Makefile fails
fast if the local stack isn't up.

## 2. Package architecture (the boundary)

```
tools/integration-suite-v2/
│  Makefile                  validate / local / run / preflight
│  README.md                 quick start
│  ARCHITECTURE.md           this doc
│
├─ catalogs/                 YAML data — Layer 3
│    verbs/                  one file per verb (nats_request.yaml, …)
│    readers/                one file per reader location
│    services/               one file per service (ability profile)
│    matchers.yaml           matcher registry
│    fixture-cast.yaml       seeded user cast
│
├─ scenarios/                YAML scenarios — Layer 4
│    drafts/                 AI-authored, not CI-gating
│    approved/               human-reviewed, authoritative
│
├─ cmd/
│    runner/main.go          loads catalogs + scenarios → runs → writes report
│    validator/main.go       cross-checks catalog YAML against Go executors
│
└─ internal/
     catalog/                Catalog struct + YAML loader + Validate
     scenario/               Scenario types + LoadFile + Resolve (predicate → fixture)
     verbs/                  Executor interface + NATSRequest + Registry
     matchers/               Matcher interface + 4 matchers + Registry
     readers/                Reader interface + mongo.rooms / reply / container_logs + Registry
     fixtures/               Cast + CastSpec + Seeder (nkey + /auth POST)
     mishap/                 PowerSet + Expand + ConcurrentExecutor stub
     runtime/                tracing · dispatcher · observer · timeframe ·
                             verdict · reporter · runner (orchestrator)
```

`internal/` makes the framework non-importable from outside the tool —
scenario authors edit YAML, not Go.

## 3. Scenario lifecycle (one case, end to end)

```
runner.Run(cfg)
  │
  ├─ catalog.Load(cfg.CatalogsDir)              ── load every catalogs/**.yaml
  │
  ├─ fixtures.Seeder.Seed(ctx, castSpec)        ── for each "generate-and-auth"
  │     GenerateNkey → POST /auth → store {Account, JWT, NkeySeed}
  │
  ├─ build registries: verbs · matchers · readers
  │     readerReg.Register("mongo.rooms",       NewMongoRoomsReader(mongo, db))
  │     readerReg.Register("reply",             NewNATSReplyReader())
  │     readerReg.Register("logs.room-service", NewContainerLogsReader(...))
  │
  └─ for every scenarios/**.yaml:
      scenario.LoadFile(path)
      runOneCase(ctx, s, cast, …):
        │
        ├─ scenario.Resolve(s, cast)
        │     placeholder "requester" + predicate {verified:true}
        │     → cast.FindByPredicate(["verified"], 1)[0] → CastUser
        │
        ├─ observer.Start(obsCtx, runID, startTime)
        │     fan in every reader's Watch() into one Event channel
        │     ── starts BEFORE the verb fires so we don't miss early events
        │
        ├─ dispatcher.Fire(ctx, s, res, site)
        │     tp := NewTraceparent()                            (32-hex W3C)
        │     subject = substitute("chat.user.${requester.account}.request.room.${site}.create", res, site)
        │     payload = JSON-encode { name: $auto, requesterAccount: ${…} }
        │     executor.Execute(ctx, &verbs.Input{Subject, Payload, Credential, Traceparent: tp})
        │     replyReader.Inject(out.Reply, tp, time.Now(), "")
        │
        ├─ GatherUntilQuiet(events, traceID, inScopeServices, 200ms, 5s)
        │     drain channel until: no in-scope event for 200ms, or 5s cap
        │
        ├─ Classify(s, gathered, traceID, matcherReg, profileLookup) → Verdict
        │     in-cascade (trace match) → try to satisfy an expected read
        │     out-of-trace + matches service ability profile → background, ignore
        │     anything else → anomaly
        │     unmatched required reads → missing-positive
        │
        └─ append to RunReport.Cases
      reporter.Write(cfg.OutputPath, report) → docs/integration-suite-v2/last-run.md
```

Each case is independent: fresh traceparent, fresh observer context,
auto-generated entity IDs prefixed `it-<runID>-…` so cross-case state
never collides.

## 4. Three-way event classification

The verdict mechanism replaces v1's 8-class string-matched classifier
with a structural three-way split:

```
                            event arrives at observer
                                       │
                  ┌────────────────────┴────────────────────┐
                  │ traceparent contains scenario's traceID?│
                  └────────────────┬───────────┬────────────┘
                                  yes         no
                                   │           │
              in-cascade            │           │            out-of-trace
        ┌──────────────────────────▼───┐  ┌───▼────────────────────────────┐
        │ does it match an EXPECTED    │  │ does OwnerSvc's ability profile │
        │ unsatisfied read at this     │  │ have a pattern that matches?    │
        │ location?                    │  │                                 │
        │   yes → mark satisfied       │  │   yes → background, ignore      │
        │   no  → unexpected-cascade   │  │   no  → anomaly                 │
        │         (fail)               │  │         (fail)                  │
        └──────────────────────────────┘  └─────────────────────────────────┘

        After draining: every required read still unsatisfied → missing-positive (fail)
```

A scenario passes only when: every required read matched, no
unexpected cascade, no anomaly. There is no "everything else can
happen" loophole — extra effects need either an expected read or an
ability-profile entry.

## 5. Event-driven timeframe (no budgets)

Instead of "wait N seconds and see," the runtime closes the timeframe
when the cascade goes quiet:

```
T_open  = observer.Start(...)          ── before the verb fires
fire verb
loop:
  on event in-scope (trace match AND owner in scenario.Sequence services):
      lastInScopeAt = ev.Timestamp
  on 50ms tick:
      if has-seen-events AND time.Since(lastInScopeAt) >= quietGrace (200ms):
          T_close = now ── done
  on safetyCap (5s):
          T_close = now ── pathological case
```

Consequence: fast scenarios close in ~250ms; slow cascades (federation,
JetStream republish) close when they actually finish; deadlocked
services hit the safety cap and fail with `timeframe-never-closed`.

## 6. Catalogs as the building-block surface

The six catalogs encode every primitive a scenario can use. Adding a
primitive is editing two files (YAML + Go); adding a scenario is
editing one (YAML).

| Catalog              | YAML lives at                | Go executor             | What it provides                           |
|----------------------|------------------------------|-------------------------|--------------------------------------------|
| `verbs`              | `catalogs/verbs/*.yaml`      | `internal/verbs/*.go`   | how to invoke an action (transport + send) |
| `matchers`           | `catalogs/matchers.yaml`     | `internal/matchers/*.go`| how to compare expected vs observed        |
| `readers`            | `catalogs/readers/*.yaml`    | `internal/readers/*.go` | where to watch for effects                 |
| `services`           | `catalogs/services/*.yaml`   | (data only)             | ability profiles → what's "background"     |
| `fixture-cast`       | `catalogs/fixture-cast.yaml` | `internal/fixtures/*.go`| seeded users with predicates (tags)        |
| mishaps (Part-2)     | (declared in runtime)        | `internal/mishap/*.go`  | perturbations to expand via power set      |

The `validator` CLI cross-checks every YAML executor reference against
the registered Go symbols and every reader owner against the service
list. `make validate` runs it without needing the live stack.

## 7. Part-1 boundary (what scopes coverage today)

| Reachable in Part-1                       | Deferred                              |
|-------------------------------------------|---------------------------------------|
| HTTP (auth) + NATS request/reply verbs    | JetStream publish + Cassandra readers |
| Mongo collection readers (mongo.rooms)    | JetStream peek / consume readers      |
| NATS reply reader                         | Cross-site (multi-site SITE list)     |
| Container log reader (`docker logs -f`)   | Chaos (kill_pod, partition, …)        |
| Mishap framework + empty-subset run       | Mishap subset expansion at runtime    |
| User placeholders (`type: user`)          | Room/message placeholders             |

Part-1 ships a working v2 platform end-to-end on the docker-local
single-site stack with one smoke scenario. Adding scenarios is a
YAML-only change; adding new building blocks (Cassandra reader,
JetStream verb, room placeholder) is the work of Part-2+.
