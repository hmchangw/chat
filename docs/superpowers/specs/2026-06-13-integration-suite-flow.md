# Integration suite multi-site — `flow:` scenario design

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report." If a feature isn't
> needed by a scenario, it doesn't get built. Failures are loud.

> 🎯 **In-place evolution.** All changes land in
> `tools/integration-suite-multisite/`. No second tree.

**Status:** approved design / not yet implemented. Captured 2026-06-13
after brainstorm + tester review + author course-correction. Builds
on the shipped multi-input slice
(`2026-06-13-integration-suite-multi-input.md`).

**Goal.** Add an optional `flow:` ordering expression that
**orchestrates the existing `input:` (fires) and `expected:`
(observations) by reference**. An observation positioned between two
fires becomes a gate — its match is the precondition the next fire
waits on; cumulative end-state checks group in a tail parallel-observe
barrier. Causal correctness, cross-site federation gates, and
read-after-write all become expressible without changing what `input:`
or `expected:` look like.

**Decisions baked in (course-corrected from prior drafts):**
- **`flow:` orchestrates existing `input:` + `expected:` items by id.**
  No new `steps:` section, no discriminated-union step type. `input:`
  is unchanged from multi-input (TaskList); `expected:` gains a
  small optional surface (`id:`, `of:`).
- **`flow:` is opt-in and additive.** Without it, scenarios run
  exactly as today (multi-input executor, flat `expected[]`). With
  it, the flow executor runs only the ids referenced in the flow
  expression.
- **When `flow:` is present, it is the single source of truth.**
  Every `input[]` task and every `expected[]` entry must appear in
  the flow; un-referenced entries are a loader error. ("Mostly in
  flow, except for some other stuff" is the kind of grammar that
  compounds bugs over time — reject it at load.)
- **`[]` is homogeneous-only.** Mixed-type groups (fire id + observe
  id) rejected at load; ambiguous semantics.
- **Parallel-fire groups reserved-rejected in v1.** `[fire_a, fire_b]`
  parses but errors with "parallel fire not yet supported." Grammar
  shape fixed now so the concurrency slice doesn't shift it.
- **Substitution is per-id.** `${<input-id>.reply.body_json.x}` reads
  a fire's captured reply; `${<expected-id>.body_json.x}` reads an
  observation's matched event. No `events[i]` indexing — the
  observation IS the indexed value.
- **Reply scoping** — position is the default (most-recent preceding
  fire). For ambiguous shapes (multiple fires between reply
  observations, or reply observations inside a parallel group), the
  reply assertion uses an explicit step-level **`of: <input-id>`**
  field. `match:` stays purely shape; no scoping directives inside it.
- **Negative observe steps are window-scoped, not whole-scenario.**
  Deliberate change from today's `Consistently().ShouldNot()` against
  the full accumulated buffer. The loader warns on `not: true`
  observations placed anywhere other than the final barrier;
  AUTHORING.md leads with the rule.
- **Adjacent fires retain zero added inter-fire latency.** The flow
  executor must not introduce scheduling delay between adjacent
  fire-barriers beyond what the legacy multi-input path has, or every
  existing race-finding repro (F-006, F-011, F-012, F-013, F-014)
  silently weakens. Phase F benchmark gate.

**Out of scope (separate specs):**
- Parallel **fires** (`[fire_a, fire_b]`) — concurrency slice.
- Named orderings (multiple `flow:`s per scenario) — orderings slice.
- Chaos engine — chaos slice (gated on F-009).

---

## 1. What the tool does today (the starting point)

`Scenario.Input` is a `TaskList` (multi-input, shipped 2026-06-13):
ordered list of tasks fired sequentially. `Scenario.Expected` is a
flat list of assertions evaluated against the accumulated event slice
after all fires complete. Reply substitution `${<id>.reply.body_json.x}`
threads earlier-task replies into later tasks. Reply assertions scope
to one task via `match.task: <id>`.

**The constraint this spec removes.** The two-section shape forces
the author to mentally interleave a fires-sequence with an
assertions-conjunction that has no temporal structure of its own.
Assertions that gate a fire — "wait for the federation event before
firing the next task" — have no clean expression. The F-006-class
drops, F-011 tcount lost-update, F-012 create-room race, F-013 edit
race, and F-014 dedup-collision findings each named this gap in some
form. Ordering-dependent, sequential-durability, and cross-site
federation bug classes are structurally invisible without it.

---

## 2. The new shape — worked example

A scenario that contract-pairs F-012 (create-room "accepted" precedes
usability): prove that **once the room observably exists**, rename
works correctly. The Mongo observation step IS the precondition.

```yaml
scenario: create-then-rename-after-room-persists
source: room-service/handler.go (create + rename), room-worker/handler.go (canonical apply)
status: draft
tag: positive

sites:
  site-a:
    seed:
      users: { alice: { verified: true } }

input:                              # multi-input shape, unchanged
  - id: create
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.site-a.create
    payload: { name: Engineering }
    credential: ${alice.credential}

  - id: rename
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.${create.reply.body_json.roomId}.site-a.rename
    payload: { newName: Renamed }
    credential: ${alice.credential}

expected:                           # gains optional id: (+ of: where needed)
  - id: create_accepted
    location: reply
    match: { body_json: { status: accepted } }

  - id: room_persisted              # THE GATE — converts a race into determined order
    location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { _id: ${create.reply.body_json.roomId} } }
    match: { _id: ${create.reply.body_json.roomId}, name: Engineering, type: channel }
    timeout: 10s

  - id: rename_accepted
    location: reply
    match: { body_json: { status: accepted } }

  - id: room_renamed_in_mongo
    location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { _id: ${create.reply.body_json.roomId} } }
    match: { _id: ${create.reply.body_json.roomId}, name: Renamed }

  - id: rename_canonical
    location: jetstream_consume
    site: site-a
    args: { stream: ROOMS_site-a, filter_subject: chat.room.canonical.site-a.> }
    match: { body_json: { type: room_renamed, newName: Renamed } }

  - id: no_error_logs               # tail-only — window-scoped semantics
    location: logs_tail
    site: site-a
    args: { service: room-worker }
    match: { level: error }
    not: true
    timeout: 5s

flow: create >> create_accepted >> room_persisted >> rename >> rename_accepted >> [room_renamed_in_mongo, rename_canonical, no_error_logs]
```

Read the `flow:` line as the story:
1. `create` fires.
2. `create_accepted` matches its reply.
3. `room_persisted` waits for room-worker to materialize the doc — **the gate**.
4. `rename` fires (after the gate; the roomId resolves from `create`'s captured reply).
5. `rename_accepted` matches the rename reply.
6. **Tail parallel-observe** — three end-state checks share one 5s window: Mongo updated, canonical event published, no errors logged anywhere.

Without `flow:`, the scenario runs as today: fires sequentially, then evaluates the flat `expected[]` against the accumulated buffer. With `flow:`, the gate at step 3 is honored.

---

## 3. Grammar reference

### 3.1 `input:` — unchanged from multi-input

The multi-input grammar
(`docs/superpowers/specs/2026-06-13-integration-suite-multi-input.md`
§3.1) carries forward verbatim. Each task has an `id:` (required in
the list shape); fields are `site`, `verb`, `subject`, `payload`,
`credential`. No new fields.

### 3.2 `expected:` — adds optional `id:` and `of:`

Each entry remains an Assertion: `location`, `site`, `args`, `match`,
`timeout`, `polling`, `not`. Two additions:

| New field | Required when | Type | Notes |
|---|---|---|---|
| `id` | When the entry is referenced by `flow:` | string | `[a-z][a-z0-9_-]*`. Unique within the scenario. Required if `flow:` is present; optional otherwise (multi-input flat behavior). |
| `of` | Explicit reply scope (rare) | string | Names an `input[].id`. Valid only on `location: reply` entries. The reply observation matches against events tagged with that fire id. Defaults to the most-recent preceding fire in the linearized flow. Required only when the loader detects ambiguity. |

**Scoping rules for `location: reply`** (resolved at load time):
1. If `of:` is set, scope to that fire's tagged reply.
2. Otherwise, if exactly one fire precedes the reply observation in
   the flow since the last reply observation, scope to that fire
   (position-default).
3. Otherwise, the shape is **ambiguous** and the loader rejects with
   `scenario %q: expected %q: ambiguous reply scope — fires [A, B]
   precede without intervening reply observation; add of: A or of: B`.

`match.task:` directive remains valid in the legacy (no-`flow:`)
shape for backward compat. In a `flow:` scenario, `match.task:` is
rejected by the loader with `use of: <input-id> at the expected[]
level instead`.

### 3.3 `flow:` — ordering expression

Optional top-level field. When present, it is the executor's input
and the single source of truth for what runs.

```
flow ::= group ( ">>" group )*
group ::= ref | "[" ref ( "," ref )* "]"
ref   ::= input-id | expected-id          # ids resolve into one namespace
```

- `>>` is sequence.
- `[a, b]` is parallel — for observations only in v1. Parallel-fire
  groups parse but reject at load time.
- Nested brackets rejected.

**Constraints (enforced at load time):**
- Every `input[].id` must appear in `flow:` exactly once. Unreferenced
  fires are a hard error: "every input task must appear in flow:".
- Every `expected[].id` must appear in `flow:` exactly once.
  Unreferenced observations are a hard error.
- Every `flow:` reference must resolve to a declared id.
- Inputs without an `id` are rejected when `flow:` is present (every
  participant must be addressable).
- Expected entries without an `id` are rejected when `flow:` is
  present.
- Single-fire scenarios (legacy single-`input:` map) cannot use
  `flow:` — they have no task-id to reference. Use the list shape.

**Compact and verbose forms.** The compact `>>` / `[]` is canonical;
a YAML list form is accepted for long flows:

```yaml
flow:
  - create
  - create_accepted
  - room_persisted
  - rename
  - rename_accepted
  - [room_renamed_in_mongo, rename_canonical, no_error_logs]
```

### 3.4 Substitution

Per-id, available in `input[]` fields (subject/payload/credential)
and `expected[]` fields (args/match):

| Token | Resolves to |
|---|---|
| `${<input-id>.reply.body_json.<field>}` | The fire's decoded JSON reply field. `nats_request` only — `jetstream_publish` produces no reply. |
| `${<input-id>.reply.status}` | Sugar for `body_json.status`. |
| `${<expected-id>.body_json.<field>}` | The observation's matched event body field. |
| `${<expected-id>.<payload-field>}` | Other matched event payload fields (e.g. `_id`, `name`, `traceparent`). |

**Resolution timing.**
- `flow:` absent: substitution resolves at fire / assertion evaluation
  time as today (multi-input rules).
- `flow:` present: substitution resolves at the **referencing id's
  execution moment** against the running flow context.

**Load-time validation.** A `${<id>.…}` reference must point at an id
declared in the scenario AND positioned earlier in the linearized
flow (when `flow:` is present); not at a sibling inside the same
parallel group.

### 3.5 Backward compatibility — pure addition

| `flow:` | Behavior |
|---|---|
| absent | Today's multi-input executor: fires sequentially, then evaluates flat `expected[]`. No `id:` required on expected entries. `match.task:` directive valid. |
| present | Flow executor: fires + observations execute in flow order; gates honored. Every `input[].id` and `expected[].id` referenced. `match.task:` directive rejected (use `of:`). |

**No mandatory migration.** All 23+ existing scenarios continue to
work unchanged. New deep scenarios opt into `flow:` when they need a
gate.

---

## 4. Loader contract

Files: `internal/scenario/loader.go` (small change),
`internal/scenario/flow_parse.go` (new — `>>` / `[]` parser),
`internal/scenario/flow_validate.go` (new — referential integrity).

Pipeline:
1. YAML decode. `Scenario.Flow` is `*FlowExpression` (nil when absent).
2. Existing `rejectDeprecatedTokens` runs against input + expected
   bodies (no change for legacy scenarios).
3. Existing `ValidateMultiInput` runs.
4. If `Scenario.Flow == nil` → done (legacy path).
5. **Flow parsing** (`flow_parse.go`). Tokenize `>>` and `[...]`
   groups; produce a `FlowPlan` — ordered list of `FlowBarrier`s
   (each barrier is a non-empty `[]string` of ids and a homogeneity
   tag).
6. **Flow validation** (`flow_validate.go`):
   - every `input[].id` appears in `flow:` exactly once
   - every `expected[].id` appears in `flow:` exactly once
   - every flow id resolves to a declared input or expected
   - no inputs or expecteds missing an `id`
   - homogeneous groups: `[a, b]` resolves to all-inputs OR
     all-expecteds. Mixed rejected.
   - all-input parallel group rejected with the "deferred to
     concurrency slice" message.
   - nested brackets rejected (tokenizer).
   - **`of:` validation** — `of:` valid only on `location: reply`
     expected entries; must name a declared input that fires earlier;
     required when the position-default rule (§3.2) detects ambiguity.
   - **`match.task:` rejection** in flow scenarios.
   - **`${<id>.*}` references** in input/expected bodies must point
     at ids positioned earlier in the flow; not at siblings inside
     the same parallel group.
   - **negative-step placement warning** (not rejection): for every
     `not: true` expected entry NOT in the final barrier, emit a
     loader warning: `expected %q: not: true observe entries prove
     absence only during their own window; place end-state absence
     checks in the final barrier for whole-scenario coverage (see
     AUTHORING.md §Negative-observe semantics)`.

Errors are precise, scenario-located, and fire before any I/O:

```
scenario %q: flow present but input[%d] (id=%q) is not referenced
scenario %q: flow present but expected[%d] (id=%q) is not referenced
scenario %q: flow present but input[%d] is missing required id
scenario %q: flow present but expected[%d] is missing required id
scenario %q: flow references unknown id %q
scenario %q: flow group [%s, %s] contains both inputs and expecteds — must be homogeneous
scenario %q: flow group [%s, %s] is a parallel fire group — not supported in v1
scenario %q: nested brackets not supported
scenario %q: expected %q: of: is valid only on location: reply
scenario %q: expected %q: of: %q must name a declared input
scenario %q: expected %q: ambiguous reply scope — fires [%s, %s] precede without intervening reply observation; add of: <input-id>
scenario %q: expected %q: match.task: is not valid in flow scenarios; use expected[%d].of instead
scenario %q: expected %q references ${%s.…} but %q is positioned after this entry in the flow
scenario %q: expected %q references ${%s.…} from inside the same parallel group [%s, %s]
```

Warning (validator emits, does not fail):

```
scenario %q: expected %q: not: true observe entries prove absence only during their own window — place end-state absence checks in the final barrier
```

---

## 5. Executor contract

Files: `internal/runtime/flow_executor.go` (new),
`internal/runtime/runner_scenario.go` (routes by `Scenario.Flow != nil`),
`internal/runtime/substitute.go` (extended for `${expected-id.*}`).

### 5.1 Walking the barrier sequence

The loader produces a `FlowPlan` — an ordered list of barriers. Each
barrier holds 1+ ids of one kind (all inputs or all expecteds). For
the v1 grammar, fire barriers are size-1; observe barriers can be
size N.

```go
type FlowPlan struct{ Barriers []FlowBarrier }

type FlowBarrier struct {
    Kind string   // "fire" | "observe"
    IDs  []string // input-ids or expected-ids
}
```

Execution:

```
ctx := newFlowContext()    // map[id]CapturedValue
for each barrier in plan.Barriers:
    spawn N goroutines (1 per id in the barrier)   // N=1 for fire barriers
    each goroutine:
        if barrier.Kind == "fire":
            executeFire(input[id], ctx)
        else:
            executeObserve(expected[id], ctx)
    wait for all N to complete or fail-hard
    if any failed:
        record scenario-fail with the specific id and reason; halt
    else:
        merge captured values into ctx; advance
```

Sequential `a >> b >> c` is three size-1 barriers — no goroutines,
no concurrency, deterministic. The barrier loop is the same code
path as today's `for _, task := range s.Input` but generalized.

### 5.2 Fire-barrier execution

```go
func executeFire(task scenario.Task, ctx *FlowContext) error {
    resolved := substitute(task, ctx)
    if err != nil { return halt("input %q: %w", task.ID, err) }

    cred := resolveCredential(resolved.Credential, ...)
    inSpec := &InputSpec{Site: resolved.Site, Verb: resolved.Verb,
                          Subject: resolved.Subject, Payload: resolved.Payload,
                          TaskID: task.ID}
    err := dispatcher.Fire(natsCtx, inSpec, ctx.subCtx, cred, "")

    if verbProducesReply(task.Verb) {
        ctx.Captured[task.ID] = ReplyValue{BodyJSON: ...}
    }
    return nil   // fire-anyway: rejected reply does NOT halt
}
```

**Identical to multi-input's `fireTasks`** — except called per-barrier
instead of in a single sweep. Behavior preserved exactly. This is
how the zero-added-latency property is achieved: nothing between
barriers (in the sequential case) except the loop dispatch.

### 5.3 Observe-barrier execution

```go
func executeObserve(exp scenario.Expected, ctx *FlowContext) error {
    resolved := substitute(exp, ctx)
    if err != nil { return halt("expected %q: %w", exp.ID, err) }

    poller := registry.Get(resolved.Location)
    pollFn := poller.PollFn(resolved.Site, resolved.Args, "")
    scope := resolveReplyScope(exp, ctx)                  // for of: / position
    matcher := MatchShape(resolved.Match, matcherReg, scope)

    if exp.Not {
        ok := Consistently(pollFn, exp.Timeout, exp.Polling).ShouldNot(matcher)
        if !ok { return halt("expected %q (not): match observed within %s", exp.ID, exp.Timeout) }
        return nil   // negative captures nothing
    }

    matched, matchedEvent := Eventually(pollFn, exp.Timeout, exp.Polling).Should(matcher)
    if !matched {
        return halt("expected %q: no event matched within %s (closest: %s)",
            exp.ID, exp.Timeout, matcher.bestReason)
    }

    ctx.Captured[exp.ID] = EventValue{Payload: matchedEvent.Payload}
    return nil
}
```

**Negative steps are window-scoped, not whole-scenario** — soundness
trap. Today's flat `expected[]` runs `Consistently().ShouldNot()`
against the accumulated buffer after all fires complete, proving
absence across the entire scenario. In the flow shape, `not: true`
proves absence ONLY during its own `timeout` window: events arriving
after its execution don't count toward its verdict.

Concrete failure mode: an early `not: true` step proving "no canonical
for this message" passes if the dropped-then-delivered message's
canonical arrives during a LATER barrier. The author got a
false-negative finding. The F-006-class drop assertions
(gatekeeper-empty-content-rejected, quote-nonexistent-parent-drops-
message) all depend on whole-scenario absence and must sit in the
final barrier.

The loader warns on `not: true` outside the final barrier (§4).
AUTHORING.md leads with the rule.

### 5.4 Substitution context

```go
type FlowContext struct {
    Site         string
    Placeholders map[string]map[string]any
    Services     map[string]Credential
    Captured     map[string]CapturedValue   // id → reply or event
}

type CapturedValue interface{ isCapture() }
type ReplyValue struct{ BodyJSON map[string]any }   // from a fire
type EventValue struct{ Payload  map[string]any }   // from an observe match
```

Resolution rules:
- `${<id>.reply.body_json.<f>}` requires `ctx.Captured[id]` to be a
  `ReplyValue` (id refers to a fire). Type error otherwise.
- `${<id>.body_json.<f>}` and `${<id>.<f>}` require `ctx.Captured[id]`
  to be an `EventValue` (id refers to an observation).
- Existing forms (`${<alias>.account}`, `${now}`, `$auto`, …)
  unchanged.

### 5.5 Sandbox + poller setup

**Unchanged from today.** Pollers start at `Sandbox.Setup` and run
throughout the scenario, accumulating events into their per-poller
buffers. Observe steps consult the buffer at execution time — there
is no per-step poller spin-up.

### 5.6 Zero added inter-fire latency — performance gate

Adjacent fire-barriers must execute back-to-back with no scheduling
delay beyond what the legacy multi-input path has. Phase F includes
an explicit benchmark gate (§9 Phase F).

---

## 6. Matcher contract

`internal/runtime/matchshape.go` — **no change to `MatchShape` core**.
The flow path passes a reply-scope hint to the constructor:

```go
MatchShape(expected, reg, scope ReplyScope)
```

where `ReplyScope` is empty (no scoping) for non-reply observations
or carries the resolved input-id for reply observations. When set,
`Match` filters the event slice to `Event.Task == scope.InputID`
before the subset match — the same mechanical operation
`match.task:` does in the legacy shape, just driven by the
expected's `of:` field (or position default) instead of a directive
inside `match:`.

`match.task:` directive remains valid in the legacy shape. The loader
routes by `Scenario.Flow == nil`.

---

## 7. Reporter contract

File: `internal/runtime/reporter.go` (extended).

In flow scenarios, per-id verdicts replace today's per-`expected[i]`
rows. Each entry in the flow gets a row: kind (fire/observe), id,
outcome (pass / fail / halted-upstream / timed-out), captured value
(truncated), wall-clock duration. Failure cause names the id:

```
scenario: create-then-rename-after-room-persists   FAIL
  fire   create               pass    12ms     reply: {status: accepted, roomId: "01970a4f...Q"}
  obs    create_accepted      pass     5ms     —
  obs    room_persisted       FAIL  10000ms    no mongo doc matched {_id: ...} within 10s
  fire   rename               halted-upstream
  obs    rename_accepted      halted-upstream
  obs    [parallel-3]         halted-upstream
```

`performance.json` adds rows keyed by `(scenario, id)` for flow
scenarios; legacy `(scenario, case)` rows continue for non-flow
scenarios.

---

## 8. Migration

**No mandatory migration.** All 23+ existing scenarios continue to
work as today (no `flow:` present → multi-input executor). New deep
scenarios opt into `flow:` on their merits.

For an existing multi-input scenario the author chooses to gate,
the changes are purely additive:
1. Add `id:` to every `expected[]` entry.
2. Add `flow:` line listing input + expected ids in the desired
   order, including any gates and the tail barrier.

No body rewrites, no shape conversion, no migration tool.

---

## 9. TDD plan & phasing

Per `CLAUDE.md`: Red → Green → Refactor → Commit per phase. Phases
ordered so each is independently committable and the route-by-flow-
presence ships first.

**Phase A — `expected:` gains optional `id:` and `of:`.**
- RED: decode tests; legacy scenarios continue to parse unchanged;
  duplicate ids rejected; `of:` on non-reply rejected.
- GREEN: extend the `Expected` struct + decode validation.

**Phase B — `flow:` parser.**
- RED: parser tests — sequential, single parallel group, malformed
  expressions, nested brackets, mixed-type groups (caught later in
  validation since the parser doesn't know id kinds), parallel-fire
  group, YAML list form.
- GREEN: `flow_parse.go` producing a `FlowPlan`.

**Phase C — flow validation.**
- RED: tests for every error path in §4 (unknown id, missing
  inputs/expecteds, missing-from-flow, duplicates, mixed-type group,
  parallel-fire, forward-ref substitution, same-group sibling
  substitution, `of:` validation, `match.task:` rejection in flow,
  negative-placement warning).
- GREEN: `flow_validate.go`; wire into loader after
  `ValidateMultiInput`.

**Phase D — substitution extension.**
- RED: tests for `${<expected-id>.body_json.x}`, `${<input-id>.reply.…}`,
  type-mismatch errors (reading `.body_json.…` on a fire id, etc.).
- GREEN: extend `substitute.go`; introduce `FlowContext`.

**Phase E — flow executor.**
- RED: mock-poller + mock-dispatcher tests for sequential barriers,
  parallel-observation barriers, fire-anyway, halt-on-unresolved-
  substitution, halt-on-observe-timeout, negative-step pass + fail,
  `of:` scope override, position-default reply scope.
- GREEN: `flow_executor.go` + `runner_scenario.go` route-by-
  `Scenario.Flow != nil`.

**Phase F — race-repro soundness gate** (required, blocking).
A benchmark test fires N adjacent fire-only ids
(`fire_a >> fire_b`, no intervening observe) through the flow path
and compares wall-clock latency to the legacy multi-input path
executing the same N tasks back-to-back. Tolerance: the flow path
adds no more than 1ms per inter-fire gap. A regression here silently
weakens every existing race-finding repro across the suite (F-006,
F-011, F-012, F-013, F-014 all depend on tight fire spacing). The
flow executor does NOT merge to main without this benchmark green.

**Phase G — reporter rewrite.**
- RED: per-id verdict rows; halted-upstream marking; `performance.json`
  key shape for flow scenarios.
- GREEN: `reporter.go` extended; `last-run.md` template updated.

**Phase H — integration proof.**
- Author one new scenario in the flow shape (the
  create-then-rename-after-room-persists scenario from §2 or the
  cross-site rename pattern). Runs green end-to-end under
  `USE_INFRA`.

**Phase I — docs.**
- `SCENARIO-REFERENCE.md` — new §N "Flow shape" (when to add `flow:`,
  the EBNF, `id:` and `of:` rules, substitution semantics).
- `AUTHORING.md` — **MUST include a prominent `## Negative-observe
  semantics` section** with the window-vs-whole-scenario distinction
  and the **end-state negatives belong in the final barrier** rule.
  This is a gated doc requirement, not optional — without it authors
  will silently weaken absence findings. Also documents the
  decision rule: "use `flow:` if you need a gate between fires;
  leave it out for simple validation scenarios."
- `FLOW.md` cookbook. **First documented pattern MUST be the tail
  parallel-observe barrier** (`>> [neg_a, neg_b, pos_c]`) — for
  speed (one window vs N) and for parity with legacy `expected[]`
  semantics. Subsequent patterns: cross-site federation gate,
  read-after-write chain, value-propagation via `${obs-id.body_json.x}`.

No calendar estimate — phases are the unit of progress. Proof-of-life
at end of Phase E; race-soundness verified end of Phase F; authoring
unblocked end of Phase H.

---

## 10. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| **Negative observe is window-scoped (semantic change vs legacy)** — authors place `not: true` mid-flow expecting whole-scenario absence and get false-negative findings | ⚠️ Critical | Loader warns on `not: true` outside the final barrier. AUTHORING.md leads with the rule (Phase I doc gate). The F-006-class drop assertions are the worst-case consumer; their migrations must keep absence in the tail barrier. |
| **Race-repro soundness — added inter-fire latency in the flow executor** silently weakens every existing race finding | ⚠️ Critical | Phase F benchmark gate: adjacent fires through the flow path match legacy multi-input wall-clock within 1ms tolerance. Blocking; Phase F does not merge without it. |
| `>>` / `[]` parser bugs ship subtle scenario-meaning errors | High | Hand-rolled parser with exhaustive Phase B tests; ambiguous shapes rejected loudly. Reject is cheap; quiet wrong-meaning is not. |
| Negative observe steps balloon wall-clock time when serialized | Medium | Tail parallel-observe barrier is the cookbook default — one 5s window covers all tail checks, not N×5s serial. Per-step `timeout` tunable. |
| Parallel-observation barrier sees one obs match and one timeout — what verdict? | Medium | Spec'd: barrier fails if any member step fails. Timed-out observation = step fail = scenario fail with that id named. |
| `of:`-required ambiguity surfaces only when an author writes the rare multi-fire-batched-reply shape; the error must be clear | Low | Loader error names the specific shape, the candidate fire ids, and points at the field. |
| Forward-reference substitution slips past load validation | Medium | Phase C tests enumerate every reference path (subject, payload deep, credential, args, match deep). |
| Reporter rewrite breaks existing `performance.json` consumers | Low | Legacy `(scenario, case)` rows continue; new `(scenario, id)` rows added for flow scenarios; consumers key on the union. |
| Author confusion about which shape to use | Medium | Single guideline in AUTHORING.md: "use `flow:` if you need a gate; leave it out for simple validation scenarios." Examples in FLOW.md. |

---

## 11. What this spec EXPLICITLY does NOT cover

| Out of scope | Why deferred | Future home |
|---|---|---|
| Parallel **fires** (`[fire_a, fire_b]`) | Concurrency is a stress dimension distinct from causality; nondeterminism reintroduced; needs `CHAOS_SEED` repro hook. Grammar reserved here so the shape doesn't shift later. | concurrency slice |
| Predicate-based observation indexing (`match.<field> where: <pred>`) | Sugar; not needed for v1. Authors can write multiple observe steps with distinct `match:` shapes for the same effect. | matcher-rigor slice |
| Named orderings (multiple `flow:`s per scenario) | One scenario, one flow in v1. Multiple orderings as a stress dimension is a wrapper around the flow executor, layers in cleanly later. | named-orderings slice |
| Chaos engine, iteration loop, mishap framework | Gated on F-009; entirely separate topic. The flow executor is the natural inner loop for chaos when it lands. | chaos slice |
| Cross-scenario id references | Scenarios stay isolated, always. | — |
| New `steps:` section (a discriminated-union "step" type) | Over-abstraction — the existing `input:` and `expected:` sections already carry fire vs observe distinctions cleanly. Authors should not learn a new shape; they should learn `flow:` as a reference layer. | — |

---

## 12. Glossary

- **`flow:`** — top-level field carrying an ordering expression
  (`>>` / `[]`) that references input and expected ids.
- **Barrier** — one position in the flow that must complete before
  the next. Sequential: barrier of size 1. Parallel observation: size
  N>1.
- **Gate** — an expected observation positioned between two fires.
  Not a grammar keyword — gating is implicit in position.
- **Fire-anyway** — fires with rejected/error replies do NOT halt
  the flow; only unresolved `${...}` substitution and observe
  timeouts/Consistently-violations halt.
- **Captured value** — the per-id value stored in the flow context
  (a `ReplyValue` for fires, an `EventValue` for matched observations).
- **Window-scoped negative** — a `not: true` observation proves
  absence only during its own `timeout` window, not whole-scenario.
- **Tail parallel-observe barrier** — the cookbook default for
  cumulative end-state checks; one window covers N positive and
  negative tail observations.
- **`of:`** — explicit reply-scope field on a `location: reply`
  expected entry; names an input id whose tagged reply this
  observation matches against. Defaults to most-recent preceding
  fire.
