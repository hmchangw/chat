# Integration suite multi-site — envelope + DAG + chaos engine design

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report," no flexibility for its
> own sake. If a feature isn't needed by a scenario, it doesn't get
> built. Failures are loud and obvious.

> 🎯 **In-place evolution, not a fork.** All changes land in
> `tools/integration-suite-multisite/` directly. No second tree. This
> is the future of the current tool, not a different tool.

**Status:** approved design / not yet implemented. Captured 2026-06-13
on top of `docs/integration-suite-plan-ahead.md` §§ 2.2, 2.3, 2.4,
2.6, 3. Supersedes the narrower "input-dag" draft (renamed from
`2026-06-13-integration-suite-input-dag.md`).

**Goal.** Land the **full convergence model** from plan-ahead §3 in one
coherent design: (1) `input:` as a DAG of tasks with named orderings,
(2) `expected:` as a positive/negative **envelope** evaluated as a
unit, and (3) a **chaos engine** that runs the whole DAG under
injected mishaps across many iterations with per-iteration fresh
state. One hand-authored scenario yields `|orderings| × iterations`
stress executions.

**Why all-at-once.** The three pieces interlock: the chaos engine
loops the DAG; the envelope is what a chaos iteration matches against
(positive = system did the right thing, negative = system correctly
refused, neither = UNDEFINED = bug). Building them separately means
re-touching the executor and matcher three times. The author's intent
("I want full coverage, not a slice") is explicit.

---

## 0. Hard dependency — read this first

**The chaos engine is unsound without per-iteration service-cache
reset (finding F-009).** Every chaos iteration reseeds identical Mongo
/ Cassandra state and reuses identical cache keys (same seed users,
same room ids). But chat-app services hold in-process caches (sub-cache,
room-meta-cache, user-cache; see F-009 in
`docs/integration-suite-multisite-findings.md`) that survive the whole
run — per-iteration fresh-state (§6.2) resets databases and JetStream,
NOT service heaps. So iteration N reads iteration N-1's cached
projection, and chaos verdicts are silently contaminated.

Consequences for sequencing:
- **DAG + envelope (§§ 4.1-4.3, 5, 8 positive/negative)** have **no**
  such dependency — a DAG runs as a single pass. These ship safely now.
- **Chaos engine (§4.4, §6.1 outer loop, §6.4, §7)** must NOT be
  trusted until one of:
  - **(a)** the F-009 chat-app fix lands — a test-only cache-flush hook
    (`chat.debug.cache.flush.*`) the sandbox calls in `fresh_state()`.
    This is the right fix; it is **chat-app work**, filed as F-009.
  - **(b)** per-iteration container restart of the 5 cache-bearing
    services. Correct but ~25s × iterations — prohibitive at
    `iterations: 20` (≈8 min/scenario). Rejected as a default.
- The spec below **specifies the chaos engine fully** so it's ready to
  implement, but §11 phases gate chaos execution behind the F-009
  capability. Authoring chaos scenarios is fine; trusting their
  verdicts is not, until (a).

This is the single most important architectural fact in the document.

---

## 1. What the tool does today (the starting point)

`Scenario.Input` is a single struct; `runScenario`
(`internal/runtime/runner_scenario.go:120`) fires it once via
`Dispatcher.Fire`, then loops a **flat** `Expected[]`, running each
entry through Gomega `Eventually`/`Consistently` with per-entry
`Should`/`ShouldNot`. Pollers collect events in the background. One
fire, one flat assertion list, no chaos, no iteration.

`Config` already carries stubs the chaos engine needs:
`MishapRegistry`, `DockerCLI`, `ChaosEngine` (`runner.go:69-85`) — the
multi-site fork dropped the mishap *implementations* during the
federation push but kept the wiring seams. This spec revives them.

The constraint: one fire per scenario; flat assertions; no temporal
(chaos) or structural (ordering) stress dimension.

---

## 2. The full model — worked example

```yaml
scenario: room-creation-survives-arbitrary-chaos
source: room-service/handler.go:300, room-worker/handler.go:1188
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
        bob:   { verified: true }

cassandra_data: []

input:                              # §4.1 — tasks defined once
  - id: create
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.site-a.create
    payload: { name: Engineering }
    credential: ${alice.credential}
  - id: join
    site: site-a
    verb: nats_request
    subject: chat.user.${bob.account}.request.room.site-a.join
    payload: { roomId: ${create.reply.body_json.roomId} }
    credential: ${bob.credential}
  - id: send_msg
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.room.${create.reply.body_json.roomId}.site-a.msg.send
    payload: { text: "hi" }
    credential: ${alice.credential}

input_order:                        # §4.2 — orderings to stress
  happy:            create >> join >> send_msg
  race_join_send:   create >> [join, send_msg]    # concurrency:>1 → real race
  send_before_join: create >> send_msg >> join    # negative-ish ordering

expected:                           # §4.3 — envelope, evaluated as a unit
  positive:
    - { location: reply,      match: { task: create, body_json: { status: accepted } } }
    - { location: reply,      match: { task: join,   body_json: { status: accepted } } }
    - { location: mongo_find, args: { collection: subscriptions },
        match: { task: join, account: ${bob.account}, roomId: ${create.reply.body_json.roomId} } }
  negative:
    - { location: reply,      match: { task: send_msg, body_json: { error: "*" } } }
    - { location: mongo_find, args: { collection: messages },
        match: { task: send_msg, text: "hi" }, not: true }

chaos:                              # §4.4 — generator, common across orderings
  iterations: 20
  kinds: any                        # picks from catalogs/mishaps/
  mode: random                      # random (default) | exhaustive | none
  concurrency: 1                    # 1 = sequential siblings (default); >1 = fan-out / races
  targeting: random                 # random (default) | smart (envelope-derived)
  seed: env:CHAOS_SEED              # reproducibility
```

Total executions: `|input_order| × chaos.iterations` =
`3 × 20 = 60` stress runs from one authored scenario.

---

## 3. Execution flow (the 2-D loop)

```
LOAD     parse input[] + input_order + envelope + chaos;
         infer data-deps; validate orderings; reject malformed (§5)
SETUP    Sandbox.Setup once — seed, mint JWTs, register pollers + mishaps

for ordering in input_order:                         ◄── outer: structural
   for iter in 1..chaos.iterations:                  ◄── inner: temporal
      fresh_state()         ◄── §6.2: drop+reseed Mongo, truncate+reseed
                                Cassandra, PurgeStream JS, reopen pollers,
                                [cache-flush via F-009 hook], reset Toxiproxy
      picks = chaos.select(seed, iter)               ◄── LIST per iteration (§4.4)
      targets = chaos.resolve_targets(picks, scenario, ordering)  ◄── §6.4
      for pick in picks: go mishap.Apply(pick, target)            ◄── parallel
      execute DAG in this ordering                   ◄── §6.3 (fire-anyway)
      Eventually(...).Should(
         Or( MatchEnvelope(positive), MatchEnvelope(negative) ))  ◄── §8
      record (ordering, iter, picks, outcome, raw events if UNDEFINED)  ◄── §9
```

`MatchEnvelope(positive)` holds iff **every** positive entry matches;
`MatchEnvelope(negative)` iff **every** negative entry matches.
Outcome ∈ { matched_positive, matched_negative, **UNDEFINED** }.
UNDEFINED (matched neither) is the bug signal — the system did
something that is neither the documented success nor the documented
graceful refusal.

---

## 4. Grammar reference

### 4.1 `input:` — list of tasks

```yaml
input:
  - id: <task-id>          # required; unique; [a-z][a-z0-9_-]*
    site: <site-id>        # required; site-a | site-b
    verb: <verb-name>      # required; closed catalog
    subject: <subject>     # required; ${alias.*} / ${task.reply.*} / ${task.events.*}
    payload: { ... }       # required for known verbs; ${...} resolves recursively
    credential: <cred>     # required for nats_request
```

```go
type Task struct {
    ID, Site, Verb, Subject string
    Payload    map[string]any
    Credential string
}
type Scenario struct {
    // …existing…
    Input      []Task            `yaml:"input"`
    InputOrder map[string]string `yaml:"input_order"`
    Expected   Envelope          `yaml:"expected"`   // CHANGED — was []Expected
    Chaos      ChaosSpec         `yaml:"chaos,omitempty"`
}
```

### 4.2 `input_order:` — orderings (Airflow `>>`)

Required. `map[name]expression`.

```
ordering ::= group ( ">>" group )*
group    ::= task-id | "[" task-id ( "," task-id )* "]"
```

- `a >> b` — a before b
- `a >> [b, c]` — a, then b & c as a sibling group
- `[a, b] >> c` — a & b sibling, then c
- Sibling-group execution is governed by `chaos.concurrency` (§4.4):
  `1` = declaration order, sequential; `>1` = concurrent fan-out.
- Nested brackets rejected.

### 4.3 `expected:` — the envelope

```yaml
expected:
  positive: [ <assertion>, ... ]    # ALL must hold → matched_positive
  negative: [ <assertion>, ... ]    # ALL must hold → matched_negative
```

Each `<assertion>` is the existing Expected shape plus the `task:`
selector inside `match:`:

```go
type Assertion struct {
    Location string
    Site     string
    Args     map[string]any
    Match    map[string]any   // may contain reserved `task: <id>`
    Timeout  Duration
    Polling  Duration
    Not      bool             // negate this single assertion within its set
}
type Envelope struct {
    Positive []Assertion `yaml:"positive"`
    Negative []Assertion `yaml:"negative"`
}
```

Semantics:
- An assertion with `not: true` inverts that one assertion (the event
  must NOT appear) — used inside `negative:` to say "and the message
  was NOT persisted."
- `match.task: <id>` filters candidate events to those attributed to
  task `<id>` before subset-matching (§8).
- `positive` and `negative` are each a **conjunction**. The envelope
  outcome is the Or of the two conjunctions (§8); neither ⇒ UNDEFINED.
- At least one of `positive`/`negative` must be non-empty. A scenario
  with only `positive` can still report UNDEFINED (positive failed,
  no negative to fall back to) — that's a plain failure.

### 4.4 `chaos:` — the generator

```yaml
chaos:
  iterations: <int>        # required if chaos: present; >=1
  kinds: any | [<kind>...] # which mishaps the picker draws from; `any` = whole catalog
  mode: random | exhaustive | none   # default random
  concurrency: <int>       # default 1; sibling-group fan-out + parallel mishaps
  targeting: random | smart           # default random (§6.4)
  seed: env:CHAOS_SEED | <int>        # default: time-seeded (non-reproducible)
```

```go
type ChaosSpec struct {
    Iterations  int
    Kinds       []string   // empty + Any → whole catalog
    Any         bool       // `kinds: any`
    Mode        string     // "random" | "exhaustive" | "none"
    Concurrency int        // default 1
    Targeting   string     // "random" | "smart"
    Seed        string     // "env:CHAOS_SEED" | literal int | ""
}
```

- **`chaos:` is optional.** Absent ⇒ implicit `{ iterations: 1, mode:
  none, concurrency: 1 }` — one clean pass per ordering, no mishaps.
  This is how DAG+envelope scenarios run before the chaos engine /
  F-009 fix exist (§11). The grammar is uniform; chaos is opt-in.
- **`mode: none`** — baseline sweep, no mishaps, still `iterations`
  times (flake detection).
- **`mode: random`** — each iteration draws a LIST of mishaps (size
  driven by catalog + a per-iteration count; v1: 0..2) from `kinds`.
- **`mode: exhaustive`** — iterate every single-mishap plus the
  no-mishap baseline once each; `iterations` becomes a cap, not a
  count.
- **`concurrency`** governs BOTH sibling-task fan-out (§6.3) and the
  number of mishaps applied concurrently (§6.4). `1` keeps everything
  deterministic and sequential (the safe default); `>1` is where real
  races live.

### 4.5 Substitution — `${task.reply.*}` and `${task.events.*}`

**Reply (always available):**

| Path | Resolves to |
|---|---|
| `${<t>.reply.body_json.<field>}` | decoded JSON reply field |
| `${<t>.reply.body_raw}` | reply body string |
| `${<t>.reply.status}` | chat-app status (`accepted`/`rejected`/…) |
| `${<t>.reply.header.<H>}` | first value of reply header H |
| `${<t>.reply.error}` | transport error string |

**Events:**

| Path | Resolves to |
|---|---|
| `${<t>.events.<location>[<i>].<field>}` | i-th event observed at location during t's window |
| `${<t>.events.<location>.length}` | count at that location |

Resolved at **task-fire time** against the running per-iteration
context. Out-of-bounds index / unresolved reference ⇒ hard halt of
that iteration with a precise error (never silent). Load-time
validation (§5) checks task existence + that the reference flows from
a later task; field paths are not type-checked.

---

## 5. Loader contract

Files: `internal/scenario/loader.go` (changes), `order_parse.go`,
`dag_validate.go`, `envelope_validate.go`, `chaos_validate.go` (new).

Pipeline:
1. YAML decode (`input: []Task`, `expected: Envelope`, `chaos:`).
2. **Shape preflight** — legacy map-shaped `input:` or flat-list
   `expected:` rejected with a migration error pointing here.
3. Per-task required fields; `rejectDeprecatedTokens` per task.
4. **DAG validation** (`dag_validate.go`): infer edges from
   `${t.reply.*}`/`${t.events.*}` in subject/payload/credential;
   reject cycles, unknown refs, duplicate ids.
5. **Ordering validation** (`order_parse.go`): tokenize `>>`/`[...]`;
   every task appears exactly once across the ordering; ordering
   respects inferred hard deps; reject malformed/nested/unknown.
6. **Envelope validation** (`envelope_validate.go`): at least one of
   positive/negative non-empty; each assertion's `location` is a
   known primitive; `match.task` (if present) names a declared task;
   `site` present/forbidden per primitive (existing rule).
7. **Chaos validation** (`chaos_validate.go`): `iterations>=1`;
   `mode`/`targeting` in enum; `kinds` entries exist in
   `catalogs/mishaps/`; `concurrency>=1`; `seed` parses.

Every error is precise, scenario-located, and fires before any I/O.
Representative messages enumerated inline in each new file's tests.

---

## 6. Executor contract

Files: `internal/runtime/dag_executor.go`, `chaos_loop.go` (new);
`runner_scenario.go` (rewired).

### 6.1 The two loops

`runScenario` becomes: Setup once → for each ordering → for each
iteration → fresh_state → chaos pick+apply → execute DAG → match
envelope → record. The outer/inner structure is §3.

### 6.2 `fresh_state()` per iteration (plan-ahead §3.1)

| Surface | Action |
|---|---|
| Mongo collections | drop + re-apply `seed.<site>.seed.{users,rooms,memberships}` + `mongo_data` |
| Cassandra tables | truncate + re-apply `cassandra_data` |
| JetStream streams | `js.PurgeStream()` per scenario-touched stream — NEW |
| Stateful poller buffers | `Close()` + reopen (each poller already has Close) |
| **Service in-process caches** | **flush via F-009 hook** (`chat.debug.cache.flush.<svc>`) — **REQUIRED for sound chaos; see §0** |
| JWTs / nkeys / Placeholders | keep cached (expensive to mint) |
| Toxiproxy | reset |
| Valkey room keys | keep cached |

Cost ~150-250ms/iteration sans cache-flush; the flush adds a handful
of fire-and-ack NATS round-trips (~10ms).

### 6.3 DAG execution within an iteration

Per ordering group, per task (concurrency-governed):
1. **Resolve** `${...}` against the running context (prior tasks'
   replies + events). Unresolved ⇒ halt iteration (precise error).
2. **Open** event collectors for locations this task's assertions
   reference AND locations downstream tasks substitute from.
3. **Tag** active-task = id (reply + observed events stamped).
4. **Fire** `Dispatcher.Fire(task)` — the existing primitive.
5. **Settle** the task's event window: run its task-scoped assertions
   / drain pollers to timeout (max of scoped `timeout`s, 2s floor).
6. **Store** `context[id] = { reply, events }`.

`chaos.concurrency`: `1` ⇒ groups run in declaration order,
sequentially (deterministic — the default). `>1` ⇒ sibling tasks in a
`[...]` group fire as concurrent goroutines, joined before the next
group. **Fire-anyway** (plan Q1/Q2): a failed reply or missing event
does NOT auto-skip downstream tasks; only an unresolved `${...}`
substitution halts. Behavioral failure flows through to envelope
evaluation.

### 6.4 Chaos application

Per iteration: `picks = select(seed, iter, mode, kinds)` returns a
LIST. `concurrency` caps simultaneous picks. Each pick →
`go mishap.Apply(pick, target)`.

**Targeting:**
- `random` (default) — pick from any backend in the relevant pool.
- `smart` — derive targets from what the scenario actually touches:
  walk the envelope's `site:`/`_site:` filters and assertion
  `location`s to a target set (e.g. envelope only mentions `site-a`
  Mongo ⇒ chaos targets site-a Mongo, not site-b/Cassandra). Reduces
  wasted iterations that chaos a backend the scenario never reads.
  Requires a scenario-walk at load time; specified here, flagged in
  §11 as the higher-complexity half of the chaos work.

Mishaps reset in `fresh_state()` (Toxiproxy reset; crashed containers
restarted) so each iteration starts clean.

---

## 7. Mishap framework (revival in multi-site)

The seams exist (`Config.MishapRegistry/DockerCLI/ChaosEngine`); the
implementations were dropped in the federation push. Revive under
`internal/mishap/` (the archived single-site tool at
`tools/archived/integration-suite/internal/mishap/` is the feature
reference — crib, don't import; it's a separate frozen module now).

v1 mishap kinds (catalog `catalogs/mishaps/`):
- **`crash`** — `DockerCLI` stops a target container mid-iteration;
  `fresh_state()` restarts it. Tests redelivery / restart recovery.
- **`mongo-partition-500ms`** — `ChaosEngine` (Toxiproxy) injects a
  timeout/latency toxic on a site's Mongo proxy for the iteration.
- **`cassandra-partition-500ms`** — same against Cassandra.
- **`nats-partition-500ms`** — same against a site's NATS proxy
  (new vs archived; federation makes this interesting).

Each kind is a `mishap.Factory` producing an `Apply(ctx, target)`
that the loop spawns and `fresh_state()` reverses. `chaos.kinds`
references these by catalog name; `ValidateForMishap` (exists)
checks references.

---

## 8. Matcher contract — envelope + `task:` selector

File: `internal/runtime/matchshape.go`, `matchenvelope.go` (new).

- **`task:` selector** — reserved key inside `match:`; pre-pass filters
  the event slice to `Task == <id>` before the existing
  `matches_shape` subset match. Forbidden inside `outbox_payload:`
  (nested-directive category error). Unscoped match accepts any task
  (backward-compatible).
- **`MatchEnvelope(set)`** — a Gomega matcher over the polled event
  slice that holds iff every assertion in `set` is satisfied
  (conjunction), honoring each assertion's `not:` and `task:`. Built
  from the existing `MatchShape` per assertion.
- The iteration assertion is
  `Eventually(poll).Should(Or(MatchEnvelope(positive),
  MatchEnvelope(negative)))`. On failure of both, Gomega's message
  carries the closest-miss from each set; the runner records
  **UNDEFINED** + the raw event dump (§9).

---

## 9. Reporter contract

File: `internal/runtime/reporter.go` (extended).

- **Per-iteration record**: `(ordering, iter, picks[], outcome)` where
  outcome ∈ {matched_positive, matched_negative, UNDEFINED}.
- **Confusion matrix** (plan Q17, now decided): rows =
  `(ordering × chaos_set)`, columns = `{matched +ve, matched -ve,
  UNDEFINED}`. UNDEFINED count is the headline bug signal.
- **UNDEFINED rows dump raw events** (plan Q18 minimal logging): exact
  event shapes, unprocessed, with location + task tag. No
  summarization.
- **Aggregation across iterations**: an ordering×chaos cell shows
  counts (e.g. `18 +ve / 2 -ve / 0 UNDEFINED`), mirroring plan-ahead
  §2.6's coverage table. Any non-zero UNDEFINED fails the scenario.
- Performance history (`performance.json`) keys per
  `(scenario, ordering)`; chaos iterations aggregate into the cell.

---

## 10. Migration — the existing scenarios

All current scenarios rewrite to the full shape:
- `input:` → single-task list (`id: t1`).
- `input_order:` → `{ only: t1 }`.
- flat `expected:` → `expected.positive` (assertions that asserted
  presence) and `expected.negative` (assertions that were `not: true`
  / asserted graceful refusal). This is a judgement split per
  scenario, NOT mechanical — `not: true` entries move to `negative`,
  presence entries to `positive`, and the author confirms the split
  reflects intent.
- no `chaos:` block initially ⇒ implicit single clean pass (§4.4), so
  migrated scenarios behave exactly as today (one fire, assert).
- `match.task: t1` added where an assertion refers to the fire.

Because the positive/negative split is a judgement call, migration is
**not** a blind script: a script proposes the split (presence→positive,
`not:true`→negative) and prints a per-file diff; the author reviews
each. Ships as one reviewed commit. Re-validated via `make validate` +
`make local`.

---

## 11. TDD plan & phasing (gated by the F-009 dependency)

Per `CLAUDE.md`: Red→Green→Refactor→Commit per slice. Phases ordered so
the **trustworthy-now** layers land first and the **F-009-gated** chaos
layer last.

**Phase A — grammar + loader** (input[], input_order, envelope, chaos
parse + all validators). Unit-tested; no infra.

**Phase B — envelope matcher** (`MatchEnvelope`, `task:` selector,
UNDEFINED outcome). Unit-tested against injected events.

**Phase C — DAG executor, no chaos** (single iteration, ordering loop,
reply+event substitution, event windows, `concurrency` knob). Unit
tests with mock dispatcher; this is the layer with **no F-009
dependency** and delivers deep multi-step authoring.

**Phase D — migration** (rewrite existing scenarios; positive/negative
split reviewed; one commit). After D, authoring deep scenarios is
fully unblocked.

**Phase E — integration proof (no chaos)** — one new multi-step
scenario, multiple orderings, `chaos: {mode: none}`, green end-to-end
under `USE_INFRA`.

**Phase F — mishap framework revival** (crash + 3 partitions; registry,
DockerCLI, ChaosEngine wiring; catalog entries). Unit + a single
mishap smoke.

**Phase G — chaos loop** (per-iteration fresh_state incl. JS purge +
poller reopen; pick/apply/reset; reporter confusion matrix). **GATED:
chaos verdicts are only trustworthy once the F-009 cache-flush hook
(§0a) exists. Until then, Phase G runs in `mode: none` only; multi-
mishap iterations are authored but not gated-on.**

**Phase H — smart targeting** (§6.4) — the higher-complexity half;
optional, after random-targeted chaos is proven.

**Phase I — docs** (`SCENARIO-REFERENCE.md`, `AUTHORING.md`, a
`DAG-CHAOS.md` cookbook).

No calendar estimate — phases are the unit of progress, each
committed and testable. Proof-of-life (deep multi-step authoring) at
end of Phase D; chaos value at Phase G once F-009 lands.

---

## 12. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| **Chaos verdicts contaminated by service caches (F-009)** | ⚠️ Critical | §0: chaos gated behind the F-009 flush hook; until then `mode: none` only |
| New wrong-verdict surface (executor/substitution/attribution/envelope) | High | Load-time hard-reject; per-task tagging; hard-halt on bad substitution; Phases A-C test-heavy by design |
| Envelope Or-semantics hide a real failure as "matched negative" | High | UNDEFINED is explicit; negative set authored deliberately; reporter dumps raw events on every UNDEFINED |
| Event-window settling races with poller timing | Medium | Mock-injected executor tests (Phase C); real-infra proof (Phase E) |
| Mishap revival drift vs archived reference | Medium | Crib not import; archived is a frozen separate module; re-test from scratch |
| Concurrency (`>1`) introduces nondeterministic flake | Medium | Default `concurrency:1`; `>1` opt-in; `CHAOS_SEED` for repro; tolerate residual timing flake |
| Smart targeting scenario-walk is complex/buggy | Medium | Phase H, after random chaos proven; random is the always-available fallback |
| Migration positive/negative split misclassifies intent | Medium | Author-reviewed per-file diff, not blind script |

---

## 13. Open questions — resolved for this spec

Plan-ahead §5 left several deferred. Resolved here (override on review):

- **Q4 substitution** — reply **and** events (`${t.events.*}`). [decided]
- **Q10 per-ordering chaos override** — no; scenario-level `chaos:`
  applies to all orderings. [YAGNI]
- **Q13 chaos targeting** — both modes specified; **default `random`**,
  `smart` opt-in (Phase H). Rationale: random is the honest baseline
  and unblocks chaos without the scenario-walk; smart is a proven-later
  optimization. [decided, was deferred]
- **Q14 reproducibility** — `seed: env:CHAOS_SEED` via
  `math/rand/v2.NewPCG`; tolerate residual poller-timing flake. [decided]
- **Q17 confusion-matrix dims** — `(ordering × chaos_set) ×
  {+ve, -ve, UNDEFINED}`. [decided, was deferred]
- **Q20 multi-site chaos targeting** — folds into Q13; `smart` uses
  `_site:` envelope filters to scope per-site. [decided]
- **Concurrency** — `chaos.concurrency` knob; default `1`
  (sequential/deterministic), `>1` enables sibling fan-out + parallel
  mishaps (real races). Reconciles "sequential default" with "full
  version." [decided]

Still genuinely open (do not block the spec; decide in-phase):
- Per-task `event_window:` override (Phase C).
- Default per-task window floor (2s; tune in Phase E).
- Mishap per-iteration count distribution for `mode: random` (0..2
  proposed; tune in Phase G).

---

## 14. Things explicitly NOT in scope

| Out | Why | Where |
|---|---|---|
| `for_each` ergonomic sugar over sibling tasks | sugar; underlying model identical | plan-ahead §2.4 |
| Cross-scenario DAGs (task in A refs task in B) | scenarios stay isolated, always | — |
| Reading substitutions from another scenario | same | — |
| The F-009 chat-app cache-flush hook itself | chat-app code; filed as F-009 | findings doc |
| Production-grade chaos (fault libraries, network emulation beyond Toxiproxy) | over-engineering for a test harness | — |

---

## 15. Glossary

- **Task** — one verb fire with an `id`; replaces the single `Input`.
- **Ordering** — a linearization of the task DAG under `input_order:`.
- **Envelope** — `expected: {positive, negative}`; each a conjunction;
  the iteration matches the Or of the two.
- **UNDEFINED** — iteration outcome matching neither set; the bug
  signal.
- **Chaos iteration** — one fresh-state run of the DAG under a picked
  mishap list.
- **fresh_state** — per-iteration reset of DBs + streams + pollers +
  (when available) service caches; keeps JWTs/Valkey.
- **Mishap / chaos** — interchangeable; an injected fault (crash,
  partition) applied for one iteration.
- **Substitution context** — per-iteration map of completed tasks'
  replies + events, feeding downstream `${...}` resolution.
- **`task:` selector** — reserved `match:` key filtering events by
  attributed task id.
- **Targeting** — which backends chaos hits: `random` (any) or `smart`
  (envelope-derived).
