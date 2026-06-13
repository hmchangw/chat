# Integration suite multi-site — `flow:` scenario design

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report." If a feature isn't
> needed by a scenario, it doesn't get built. Failures are loud.

> 🎯 **In-place evolution.** All changes land in
> `tools/integration-suite-multisite/`. No second tree.

**Status:** approved design / not yet implemented. Captured 2026-06-13
after a brainstorm that converged on a unified-flow model in
preference to the narrower `${task.events.*}` + `await:` slice (which
this spec **supersedes** as the next forward-agenda item — see
`docs/integration-suite-plan-ahead.md` §4.1 #1). Builds on the shipped
multi-input slice (`2026-06-13-integration-suite-multi-input.md`).

**Goal.** Replace today's two-section shape (`input:` list of tasks
+ `expected:` list of assertions) with a single ordered `flow:` that
interleaves **fires** (verb calls) and **observations** (poller
waits). Causal relationships become positional: an observation between
two fires is the gate that the second fire waits on; an observation
at the tail is a cumulative end-state check. The model unifies what
were previously three concepts — task ordering, per-task assertion
scoping (`match.task:`), and event-window waits — into one.

**Decisions baked in (from the brainstorm critique + tester review):**
- **`[]` is homogeneous-only.** Mixed-type groups (fire + observe) are
  rejected at load time; the semantics are too ambiguous to be safe.
- **Parallel-fire groups are reserved-rejected in v1.** `[fire_a,
  fire_b]` parses but errors with "parallel fire not yet supported"
  — that's the future concurrency slice's grammar, declared here so
  it doesn't shift later. Parallel **observation** groups (`[obs_a,
  obs_b]`) work in v1.
- **`flow:` replaces `input:` + `expected:`** for new scenarios.
  Legacy shape stays valid via dual-shape unmarshal (same pattern as
  multi-input's `TaskList`); the 23+ existing scenarios stay
  untouched.
- **Substitution is per-step.** `${<step-id>.reply.body_json.x}`
  reads a fire step's captured reply; `${<obs-id>.body_json.x}` reads
  an observation step's matched event. No `events[i]` indexing — the
  observation step IS the index.
- **`match.task:` directive is gone from the flow shape.** Position
  is the default scope for reply observations (most-recent preceding
  fire). For the unusual shape where multiple fires sit between
  reply observations, an `observe: reply` step gets an explicit
  step-level **`of: <fire-id>`** field — scoping lives at the step
  level alongside `site:`, never inside the `match:` shape. The
  legacy directive remains valid in the legacy shape only.
- **Adjacent fires retain zero added inter-fire latency.** The flow
  executor must not introduce scheduling delay between two adjacent
  fire-barriers beyond what the legacy multi-input path has, or
  every existing race-finding repro (F-006, F-011, F-012, F-013)
  silently weakens. Asserted as a Phase F benchmark gate.
- **Negative observe steps are window-scoped, not whole-scenario.**
  This is a deliberate change from today's `Consistently().ShouldNot()`
  against the full accumulated buffer — and a soundness trap if an
  author places `not: true` mid-flow expecting whole-window absence.
  The loader warns (not rejects) on `not: true` outside the final
  barrier; AUTHORING.md leads with the rule.

**Out of scope (separate specs):**
- Parallel **fires** (`[fire_a, fire_b]`) — grammar reserved here,
  semantics deferred to the concurrency slice.
- Named orderings — the flow model has one ordering per scenario;
  multiple orderings are a future loop wrapper.
- Chaos engine (still gated on F-009).

---

## 1. What the tool does today (the starting point)

`Scenario.Input` is a `TaskList` (multi-input, shipped 2026-06-13):
ordered list of tasks fired sequentially. `Scenario.Expected` is a
flat list of assertions evaluated against the accumulated event slice
after all fires complete. Reply substitution `${<id>.reply.body_json.x}`
threads earlier-task replies into later tasks. Reply assertions scope
to one task via `match.task: <id>`.

**The constraint this spec removes.** The two-section shape forces
the author to mentally interleave a sequence (the task list) with a
conjunction (the expected list) that has no temporal structure of
its own. Assertions that gate a fire — "wait for the federation
event before firing the next task" — have no clean expression. The
F-011 reproducer's own comment names this limitation; ordering-
dependent, sequential-durability, and cross-site federation bug
classes are structurally invisible to it.

---

## 2. The new shape — worked example

A cross-site rename scenario where the second fire (on the remote
site) MUST see the federated rename. The wait is implicit in the
flow position; the observation captures the value the next fire
substitutes from.

```yaml
scenario: cross-site-rename-bob-on-remote-reads-new-name
source: room-service/handler.go (rename), inbox-worker/handler.go (room_renamed apply)
status: draft
tag: positive

sites:
  site-a:
    seed:
      users: { alice: { verified: true } }
      rooms:
        - { id: r-shared, name: Original, type: channel }
      memberships:
        alice: [{ room: r-shared, roles: [owner] }]
  site-b:
    seed:
      users: { bob: { verified: true } }
      rooms:
        - { id: r-shared, name: Original, type: channel }
      memberships:
        bob: [{ room: r-shared, roles: [member] }]

steps:                                        # NEW — declarative step bodies
  rename:
    fire: nats_request
    site: site-a
    subject: chat.user.${alice.account}.request.room.r-shared.site-a.rename
    payload: { newName: "Engineering" }
    credential: ${alice.credential}

  rename_accepted:
    observe: reply
    # No of: needed — position-default scopes to the immediately
    # preceding fire (rename). Explicit `of: rename` would be equivalent.
    match: { body_json: { status: accepted } }

  federation_landed:
    observe: jetstream_consume
    site: site-b
    args:
      stream: INBOX_site-b
      filter_subject: chat.inbox.site-b.>
    match:
      body_json:
        type: room_renamed
        roomId: r-shared
        newName: "Engineering"
    timeout: 10s

  bob_lists:
    fire: nats_request
    site: site-b
    subject: chat.user.${bob.account}.request.room.list
    payload: {}
    credential: ${bob.credential}

  bob_sees_renamed:
    observe: reply
    match:
      body_json:
        rooms:
          - id: r-shared
            name: ${federation_landed.body_json.newName}   # cross-step substitution

flow: rename >> rename_accepted >> federation_landed >> bob_lists >> bob_sees_renamed
```

`${federation_landed.body_json.newName}` resolves to `"Engineering"`
**because** the executor reached `federation_landed` and matched the
event before firing `bob_lists`. Without the gate, `bob_lists` would
race the federation; with it, the read-after-write is deterministic.

---

## 3. Grammar reference

### 3.1 `steps:` — declarative step definitions

Map of `<step-id>` → step body. Every step has exactly one of `fire:`
or `observe:`.

```yaml
steps:
  <step-id>:
    fire: <verb>           # OR observe: <location>
    site: <site>
    …
```

| Field | When | Type | Notes |
|---|---|---|---|
| `fire` | fire step | string | Verb name from the closed catalog (`nats_request`, `jetstream_publish`). Mutually exclusive with `observe`. |
| `observe` | observe step | string | Location name from the closed poller catalog (`reply`, `mongo_find`, `cassandra_select`, `jetstream_consume`, `nats_subscribe`, `logs_tail`). Mutually exclusive with `fire`. |
| `site` | both | string | `site-a` / `site-b`. Required per the existing site-rules table; `observe: reply` and `observe: cassandra_select` forbid it. |
| `subject` | fire | string | NATS subject template. Substitution resolved at fire time. |
| `payload` | fire | map | JSON payload. Substitution resolved. |
| `credential` | fire | string | `${<alias>.credential}`. |
| `args` | observe | map | Per-poller args (mirrors today's `expected[].args`). |
| `match` | observe | map | Subset shape; the existing `MatchShape` semantics. Purely shape; no scoping directives in the flow shape. |
| `of` | observe: reply only | string | Explicit fire-id whose reply this observation matches against. Defaults to the immediately-preceding fire step in the linearized flow. Required by the loader only when there is ambiguity (multiple fires between two reply observations); rejected on any non-reply observe step. |
| `timeout` | observe | duration | Default 5s; how long the gate waits for a match before failing the scenario. For `not: true`, also the window over which absence is checked. |
| `polling` | observe | duration | Default 100ms. |
| `not` | observe | bool | `true` ⇒ `Consistently().ShouldNot()` for the full `timeout` window. **Window-scoped**: the step proves absence only during its execution window, not whole-scenario. End-state absence checks belong in the final barrier — the loader warns on `not: true` steps placed elsewhere (see §5.3, §10). |

Step ids are `[a-z][a-z0-9_-]*`, unique within the scenario.

### 3.2 `flow:` — ordering expression

The `flow:` field is the executor's input: a string in the compact
`>>` / `[]` notation OR an equivalent YAML list (both accepted by the
loader). The compact form is canonical for readability; the YAML form
is escape for long flows.

**Compact (canonical):**

```yaml
flow: rename >> rename_accepted >> federation_landed >> bob_lists >> bob_sees_renamed
```

**EBNF:**

```
flow      ::= group ( ">>" group )*
group     ::= step-id | "[" step-id ( "," step-id )* "]"
step-id   ::= [a-z][a-z0-9_-]*
```

**Equivalent YAML list:**

```yaml
flow:
  - rename
  - rename_accepted
  - federation_landed
  - bob_lists
  - bob_sees_renamed
```

**Parallel observation group:**

```yaml
flow: rename >> [rename_accepted, federation_landed] >> bob_lists
```

Means: fire `rename`; the two observations may match in any order; the
barrier is satisfied when **both** have matched (or any has timed
out → scenario fails). The next step (`bob_lists`) fires when the
barrier closes.

**Constraints:**
- Every step id in `flow:` must be declared in `steps:`. No silent
  skips: every step in `steps:` must appear in `flow:` exactly once.
- Nested brackets rejected (`a >> [b, [c, d]]`). Use sequential
  composition for nesting needs.
- **Parallel-fire group** (`[fire_a, fire_b]`) parses but is rejected
  with `parallel fire not yet supported in v1; deferred to the
  concurrency slice`. The grammar reserves the shape so it doesn't
  shift later.
- **Mixed-type group** (`[fire_a, obs_b]`) rejected with `parallel
  group must be all fire or all observe`.

### 3.3 Substitution — `${<step-id>.…}`

Per-step substitution, available in fire-step `subject` / `payload` /
`credential` and observe-step `args` / `match`.

| Token | Resolves to |
|---|---|
| `${<fire-id>.reply.body_json.<field>}` | The fire's decoded JSON reply field. Available immediately after the fire executes (every `nats_request` fire captures its reply automatically; `jetstream_publish` is fire-and-forget — no reply, substitution hard-fails). |
| `${<fire-id>.reply.status}` | Sugar for `body_json.status`. |
| `${<observe-id>.body_json.<field>}` | The matched event's decoded body field. Available only after the observe step has matched. |
| `${<observe-id>.<payload-field>}` | Other event payload fields per the poller's emitted shape (e.g. `_id`, `name`, `traceparent`). |

**Resolution timing.** Substitutions are resolved at the **step's
execution moment** against the running flow context (every previously-
completed step's captured value).

**Load-time validation.** A `${<id>.…}` reference must point at a step
declared in `steps:` AND positioned earlier in the linearized flow.
Reference to a later-flow step ⇒ load error. Reference to a step in a
parallel group from inside the same group ⇒ load error.

### 3.4 Backward compatibility — dual shape

Existing scenarios that use `input:` + `expected:` continue to work
unchanged. The loader picks shape by which top-level keys are present:

| `input:` present | `expected:` present | `steps:` / `flow:` present | Interpretation |
|---|---|---|---|
| ✓ | ✓ | — | Legacy multi-input shape. Decode and run as today. |
| — | — | ✓ | New flow shape. Decode `steps:` + `flow:`; route to the flow executor. |
| ✓ | — | ✓ | **Error** — cannot mix shapes in one scenario. |
| — | ✓ | ✓ | **Error** — same. |

No migration of the 23+ existing scenarios is required. New deep
scenarios opt into the flow shape.

---

## 4. Loader contract

Files: `internal/scenario/loader.go` (small change),
`internal/scenario/flow_parse.go` (new — `>>` / `[]` parser),
`internal/scenario/flow_validate.go` (new — referential integrity).

Pipeline:
1. YAML decode (`steps:` as `map[string]Step`, `flow:` as
   `FlowExpression`).
2. **Shape preflight.** Detect mixed legacy + new shape; reject with a
   migration error pointing at this spec.
3. **Step body validation.** Each step has exactly one of `fire:` /
   `observe:`. Required fields per type (site rules from
   `ValidateSiteFields` extended to step types).
4. **Flow parsing** (`flow_parse.go`). Tokenize `>>` and `[...]`
   groups; produce a `FlowPlan` = ordered list of `FlowBarrier`s
   where each barrier is a non-empty `[]Step`.
5. **Flow validation** (`flow_validate.go`):
   - every step in `steps:` appears in `flow:` exactly once
   - every step id in `flow:` is declared in `steps:`
   - no nested brackets (rejected by the tokenizer)
   - parallel-fire group: rejected at load time with the
     "deferred to concurrency slice" message
   - mixed-type group: rejected
   - `${<id>.*}` references in step bodies point at earlier steps,
     and don't reference siblings inside the same parallel group
   - **`of:` validation** — on an observe-reply step, `of:` must
     name a declared fire step that fires earlier in the flow.
     Rejected on any non-reply observe step. The loader REQUIRES
     `of:` whenever a reply observation has more than one
     preceding-but-unobserved fire (i.e., ambiguity); otherwise the
     default scope (most-recent preceding fire) applies and `of:` is
     optional.
   - **negative-step placement warning** — for every `not: true`
     observe step NOT in the final barrier of the flow, emit a
     loader warning (not error): `step %q: not: true observe steps
     prove absence only during their own window; place end-state
     absence checks in the final barrier for whole-scenario
     coverage (see AUTHORING.md §Negative-observe semantics)`. Warn,
     don't reject — mid-flow absence is sometimes legitimate (proving
     X did NOT happen during a specific phase).
6. Existing `rejectDeprecatedTokens` runs against step bodies too.

Precise error messages, all scenario-located, fire **before** any
I/O. Representative examples:

```
scenario %q: step %q must declare exactly one of fire: or observe:
scenario %q: flow group [%s, %s] contains both fire and observe — must be homogeneous
scenario %q: flow group [%s, %s] is a parallel fire group — not supported in v1 (deferred to the concurrency slice)
scenario %q: step %q is declared but missing from flow:
scenario %q: flow references unknown step %q
scenario %q: step %q references ${%s.body_json.x} but %q is positioned after %q in the flow
scenario %q: step %q references ${%s.…} from inside the same parallel group [%s, %s]
scenario %q: nested brackets not supported
scenario %q: step %q: of: is valid only on observe: reply steps
scenario %q: step %q: of: %q is unknown / positioned after this step
scenario %q: step %q: ambiguous reply scope — multiple fires precede without an intervening reply observation; declare of: <fire-id> explicitly
```

Warnings (printed by the validator, not failure):

```
scenario %q: step %q: not: true observe steps prove absence only during their own window; place end-state absence checks in the final barrier
```

---

## 5. Executor contract

Files: `internal/runtime/flow_executor.go` (new),
`internal/runtime/runner_scenario.go` (re-routed by shape detection),
`internal/runtime/substitute.go` (extended).

### 5.1 Walking the barrier sequence

The loader produces a `FlowPlan` — an ordered list of barriers, each
containing one or more steps. Sequential `a >> b >> c` is three
one-step barriers. `a >> [b, c] >> d` is three barriers of sizes 1, 2,
1.

```go
type FlowPlan struct {
    Barriers []FlowBarrier
}

type FlowBarrier struct {
    Steps  []Step     // size 1 = sequential; size N>1 = parallel observation group
    Kind   string     // "fire" | "observe" — homogeneous within a barrier
}
```

Execution loop:

```
ctx := newFlowContext()    // map[step-id]CapturedValue
for each barrier in plan.Barriers:
    spawn N goroutines (1 per step in the barrier)
    each goroutine:
        if step is fire:
            executeFire(step, ctx)        // §5.2
        else:
            executeObserve(step, ctx)     // §5.3
    wait for all N goroutines to complete OR any to fail-hard
    if any step failed:
        record scenario-fail with the precise step + reason; halt
    else:
        update ctx with all N captured values; advance to next barrier
```

For sequential barriers (the common case), N=1 — no goroutines, no
concurrency, deterministic.

### 5.2 Fire-step execution

```go
func executeFire(step Step, ctx *FlowContext) error {
    // 1. resolve substitution against ctx
    resolved := substitute(step, ctx)
    if err != nil { return halt("step %q: %w", step.ID, err) }

    // 2. fire via existing Dispatcher (reused unchanged)
    cred := resolveCredential(resolved.Credential, ...)
    inSpec := &InputSpec{Site: resolved.Site, Verb: resolved.Verb,
                          Subject: resolved.Subject, Payload: resolved.Payload,
                          TaskID: step.ID}    // step id flows through as task tag
    err := dispatcher.Fire(ctx.NATSCtx, inSpec, ctx.subCtx, cred, "")

    // 3. capture reply (nats_request only) into flow context
    if verbProducesReply(step.Verb) {
        ctx.Captured[step.ID] = ReplyValue{...}     // body_json, status
    }

    // 4. fire-anyway: rejected/error reply does NOT halt
    return nil
}
```

Fire-anyway preserved from multi-input: a `rejected` reply or
transport error stores its captured value and proceeds; only an
unresolved `${...}` substitution halts.

### 5.3 Observe-step execution

```go
func executeObserve(step Step, ctx *FlowContext) error {
    resolved := substitute(step, ctx)
    if err != nil { return halt("step %q: %w", step.ID, err) }

    poller := registry.Get(resolved.Location)
    pollFn := poller.PollFn(resolved.Site, resolved.Args, "")
    matcher := MatchShape(resolved.Match, matcherReg)

    if step.Not {
        // 5.3.1 negative gate — must NOT match for the full timeout
        ok := Consistently(pollFn, step.Timeout, step.Polling).ShouldNot(matcher)
        if !ok { return halt("step %q (not): event matched within %s", step.ID, step.Timeout) }
        // negative steps capture nothing — there is no event to expose
        return nil
    }

    // 5.3.2 positive gate — wait until match
    matched, matchedEvent := Eventually(pollFn, step.Timeout, step.Polling).Should(matcher)
    if !matched {
        return halt("step %q: no event matched shape %v within %s (closest miss: %s)",
            step.ID, resolved.Match, step.Timeout, matcher.bestReason)
    }

    // 5.3.3 capture the matched event into the flow context
    ctx.Captured[step.ID] = EventValue{Payload: matchedEvent.Payload, …}
    return nil
}
```

**Negative steps cost wall-clock time.** A `not: true` observe step
waits its full `timeout` to be confident the event didn't arrive.
Documented; authors set `timeout` to the smallest plausible window.

**Negative steps are WINDOW-scoped, not whole-scenario** — soundness
trap. Today's flat `expected[]` runs `Consistently().ShouldNot()`
against the accumulated buffer AFTER all fires complete, proving
absence across the entire scenario. In the flow shape, a `not: true`
observe step proves absence ONLY during its own `timeout` window:
events arriving after the step completes are not its concern.

Concrete failure mode: an early `not: true` step proving "no
canonical for this message" passes if the dropped-then-delivered
message's canonical arrives during a LATER barrier. The author got a
false-negative finding. The F-006-class drops (gatekeeper-empty-
content-rejected, quote-nonexistent-parent-drops-message) all
depend on whole-scenario absence and must sit in the final barrier
to retain semantic parity with the legacy shape.

The loader warns on `not: true` steps placed anywhere other than the
final barrier (§4). AUTHORING.md leads with the rule.

**Per-step timeout.** Default 5s (matches today's `expected[].timeout`
default). Override per step in `steps:`.

**Zero added inter-fire latency** — performance gate. Two adjacent
fire-barriers in the flow must execute back-to-back with no added
scheduling delay beyond what the legacy multi-input path has, or
every existing race-finding repro silently weakens (F-006-fresh,
F-011 worker-pool concurrency, F-012 create-room race, F-013 edit
race). Phase F includes an explicit benchmark gate (§9 Phase F).

### 5.4 Substitution context shape

```go
type FlowContext struct {
    Site         string
    Placeholders map[string]map[string]any
    Services     map[string]Credential
    Captured     map[string]CapturedValue   // step-id → reply or event
}

type CapturedValue interface{ isCapture() }
type ReplyValue struct{ BodyJSON map[string]any; Status string }  // fire-step reply
type EventValue struct{ Payload map[string]any }                  // observe-step matched event
```

Resolution rules:
- `${<id>.reply.body_json.<f>}` requires `ctx.Captured[id]` to be a
  `ReplyValue`. Type error otherwise.
- `${<id>.body_json.<f>}` and `${<id>.<f>}` resolve against an
  `EventValue.Payload`. Type error if `ctx.Captured[id]` is a
  `ReplyValue` (the author should write `.reply.body_json.<f>` for
  fires).
- Existing forms (`${<alias>.account}`, `${now}`, `$auto`, …) continue
  to work unchanged.

### 5.5 Sandbox + poller setup

Unchanged. Pollers start at `Sandbox.Setup` and run throughout the
scenario, accumulating events in their buffers. Observe steps consult
the buffer at execution time — no per-step poller spin-up.

---

## 6. Matcher contract

File: `internal/runtime/matchshape.go` — **no change for the flow
shape**. The `match.task:` directive is gone from the flow grammar;
`match:` is purely shape. Scoping lives at the step level. The
matcher itself is called once per observe step against the poller's
accumulated event slice and is otherwise identical to today.

**Reply scoping in the flow model — step-level, not match-level.**
Each fire's reply event is tagged with the fire's step id
(`Event.Task` — the field multi-input added, reused here). An
`observe: reply` step resolves its target fire as follows:

1. If the step body has `of: <fire-id>`, scope to that fire's
   tagged reply event. (Loader has already validated the reference
   points at an earlier fire.)
2. Otherwise, scope to the **most-recent preceding fire** in the
   linearized flow (the position-default).
3. The loader rejects the ambiguous shape at load time (§4: "ambiguous
   reply scope — multiple fires precede without an intervening reply
   observation"), so the executor never encounters an undeclared
   ambiguity at runtime.

The flow executor passes the resolved fire-id into a
`flow-scoped MatchShape` constructor that filters the event slice by
`Event.Task == fireID` before the subset-match runs — the same
mechanical operation `match.task:` does in the legacy shape, just
driven by a step field instead of a match-key directive. One
machinery; cleaner grammar at the YAML surface.

**`match.task:` in the legacy shape stays valid** for backward compat.
Loader routes by scenario shape (§3.4); the directive only fires on
legacy scenarios.

---

## 7. Reporter contract

File: `internal/runtime/reporter.go` (extended).

Per-step verdict instead of today's per-expected[i]. Each step in the
flow gets a row: step id, kind (fire/observe), outcome (pass / fail /
halted-upstream / timed-out), captured value (truncated), wall-clock
duration. Failure cause names the step:

```
scenario: cross-site-rename-bob-on-remote-reads-new-name  FAIL
  step rename               fire    pass    12ms     reply: {status: accepted}
  step rename_accepted      obs     pass    3ms      —
  step federation_landed    obs     FAIL    10s      no event matched shape {…} within 10s (closest miss: site-a OUTBOX_site-a observed type=room_renamed but newName="Original")
  step bob_lists            —       halted-upstream
  step bob_sees_renamed     —       halted-upstream
```

Performance.json keys per `(scenario, step-id)` for the flow shape;
legacy `(scenario, case)` rows continue unchanged.

---

## 8. Migration

**No mandatory migration.** Dual-shape (§3.4) keeps the 23+ existing
scenarios untouched. New deep scenarios opt into the flow shape on
their merits.

For a scenario the author chooses to migrate:

```diff
- input:
-   - id: t1
-     site: site-a
-     verb: nats_request
-     subject: ...
-     payload: ...
-     credential: ...
- expected:
-   - location: reply
-     match: { task: t1, body_json: { status: accepted } }
-   - location: mongo_find
-     site: site-a
-     args: ...
-     match: { name: SanityRoomA }
+ steps:
+   t1:
+     fire: nats_request
+     site: site-a
+     subject: ...
+     payload: ...
+     credential: ...
+   t1_accepted:
+     observe: reply
+     match: { body_json: { status: accepted } }
+   room_in_mongo:
+     observe: mongo_find
+     site: site-a
+     args: ...
+     match: { name: SanityRoomA }
+ flow: t1 >> t1_accepted >> room_in_mongo
```

Mechanical for the simple cases; an author-reviewed translation for
the cases where the original `expected[]` row order implies a causal
gate that wasn't expressible.

---

## 9. TDD plan & phasing

Per `CLAUDE.md`: Red → Green → Refactor → Commit per slice. Phases
ordered so each is independently committable and the shape detection
ships first.

**Phase A — shape detection in the loader.**
- RED: tests verifying the loader picks the right shape; legacy
  scenarios continue to parse; mixed-shape rejected.
- GREEN: shape preflight + routing in `loader.go`.

**Phase B — `steps:` decoding + step-body validation.**
- RED: per-step required-field tests, exactly-one-of-fire-or-observe,
  site rules per step type, deprecated-token rejection.
- GREEN: `Step` type + `loadFlowScenarioBody`.

**Phase C — `flow:` parser.**
- RED: parser tests — sequential, single parallel group, malformed
  expressions, nested brackets, mixed-type groups, parallel-fire
  group rejected.
- GREEN: `flow_parse.go` producing a `FlowPlan`.

**Phase D — flow validation.**
- RED: tests for unknown step refs, missing-from-flow, duplicate
  references, forward-ref substitution, same-group sibling
  substitution.
- GREEN: `flow_validate.go`.

**Phase E — substitution extension.**
- RED: tests for `${<obs-id>.body_json.x}`, `${<fire-id>.reply.…}`,
  type-mismatch errors (reading `.reply.…` on an observe step, etc.).
- GREEN: `substitute.go` extended; `FlowContext` type.

**Phase F — flow executor.**
- RED: mock-poller + mock-dispatcher tests for sequential barriers,
  parallel-observation barriers, fire-anyway, halt-on-unresolved-
  substitution, halt-on-observe-timeout, negative-step pass + fail,
  `of:` scope override, position-default reply scope.
- GREEN: `flow_executor.go` + `runner_scenario.go` shape-routing.
- **Race-repro soundness gate** (required, blocking): a benchmark
  test fires N adjacent fire-only steps (`fire_a >> fire_b`, no
  intervening observe) and compares wall-clock latency to the legacy
  multi-input path executing the same N tasks back-to-back.
  Tolerance: the flow path adds no more than 1ms per inter-fire gap
  (essentially measurement noise). A regression here silently weakens
  every existing race-finding repro across the suite — the F-006,
  F-011, F-012, F-013 reproducers all depend on tight fire spacing.
  Phase F does NOT merge until this benchmark is green.

**Phase G — reporter rewrite.**
- RED: reporter tests for per-step verdict rows; halted-upstream
  marking; performance.json key shape.
- GREEN: `reporter.go` extended; `last-run.md` template updated.

**Phase H — integration proof.**
- Author one new scenario in the flow shape (the cross-site rename
  from §2). Runs green end-to-end under `USE_INFRA`.

**Phase I — docs.**
- `SCENARIO-REFERENCE.md` — new §N "Flow shape" (steps + flow + EBNF
  + substitution + `of:` scoping + dual-shape note).
- `AUTHORING.md` — when to choose flow vs legacy (one-line guideline:
  "use flow if you need a gate between fires; legacy is fine for
  simple 1-fire/N-assert validation scenarios"). MUST include a
  prominent `## Negative-observe semantics` section with the
  window-vs-whole-scenario distinction and the **end-state negatives
  belong in the final barrier** rule. Without this section authors
  will silently weaken absence findings; this is the highest-leverage
  doc requirement in the spec.
- `FLOW.md` cookbook. The first pattern documented MUST be the
  **tail parallel-observe barrier** for cumulative end-state checks
  (`>> [neg_a, neg_b, pos_c]`), not a serial chain — both for speed
  (one window vs N) and for parity with legacy `expected[]`
  semantics. Subsequent patterns: cross-site federation gate,
  read-after-write chain, value-propagation via `${obs.body_json.x}`.

No calendar estimate — phases are the unit of progress. Proof-of-life
at end of Phase F; authoring unblocked end of Phase H.

---

## 10. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| **Negative observe is window-scoped (semantic change vs legacy)** — authors place `not: true` mid-flow expecting whole-scenario absence and get false-negative findings | ⚠️ Critical | Loader warns on `not: true` outside the final barrier (§4). AUTHORING.md leads with the rule (Phase I doc gate). The F-006-class drop assertions are the worst-case consumer; their migrations must keep absence in the tail barrier. |
| **Race-repro soundness — added inter-fire latency in the flow executor** silently weakens every existing race finding | ⚠️ Critical | Phase F benchmark gate: adjacent fires through the flow path match legacy multi-input wall-clock within 1ms tolerance. Blocking; Phase F does not merge without it. |
| `>>` / `[]` parser bugs ship subtle scenario-meaning errors | High | Hand-rolled parser with exhaustive Phase C tests; ambiguous shapes rejected loudly (`nested brackets`, `mixed-type group`, `parallel-fire`). Reject is cheap; quiet wrong-meaning is not. |
| Negative observe steps balloon wall-clock time when serialized | Medium | Document the trade-off; tail parallel-observe barrier is the cookbook default (§9 Phase I) — one 5s window covers all tail checks, not N×5s serial. Per-step `timeout` tunable. |
| Parallel-observation barrier sees one obs match and one timeout — what verdict? | Medium | Spec'd: barrier fails if **any** member step fails. Timed-out observation = step fail = scenario fail with that step named. Same loudness as sequential. |
| `of:`-required ambiguity surfaces only when an author writes the rare multi-fire-batched-reply shape; the error must be clear | Low | Loader error names the specific shape, the candidate fire ids, and points at the field: "ambiguous reply scope — fires [A, B] precede without intervening reply observation; add of: A or of: B to step X" (§4). |
| Forward-reference substitution slips past load validation | Medium | Phase D tests enumerate every reference path (subject, payload deep, credential, args, match deep). |
| Reporter rewrite breaks existing `performance.json` consumers | Low | Legacy `(scenario, case)` rows continue; new `(scenario, step-id)` rows added; consumers key on the union. Documented in the multi-site RUNBOOK. |
| Migration appetite — authors might convert scenarios sub-optimally | Low | Migration is optional; no pressure. A flow shape that mechanically mirrors a legacy `expected[]` is fine; the gate-pattern is only added when the author wants the new capability. |
| Author confusion about which shape to use | Medium | Single guideline in AUTHORING.md: "use flow if you need a gate between fires; legacy is fine for simple validation scenarios." Examples in FLOW.md. |
| Substitution forward-error UX (resolution fails inside a deep payload) | Low | Existing substitute error format already names the offending token + the resolution context; reuse. |

---

## 11. What this spec EXPLICITLY does NOT cover

| Out of scope | Why deferred | Future home |
|---|---|---|
| Parallel **fires** — `[fire_a, fire_b]` | Concurrency is a stress dimension distinct from causality; nondeterminism reintroduced; needs `CHAOS_SEED` repro hook. Grammar reserved here so the shape doesn't shift later. | concurrency slice |
| Predicate-based observation indexing (`observe.body_json.x where: y`) | Sugar; not needed for v1. Authors can write multiple observe steps with distinct `match:` shapes for the same effect. | matcher-rigor slice |
| Named orderings (`flow_orderings: { case_a: …, case_b: … }`) | One scenario, one flow in v1. Multiple orderings as a stress dimension is a wrapper around the flow executor, layers in cleanly later. | named-orderings slice |
| Chaos engine, iteration loop, mishap framework | Gated on F-009; entirely separate topic. The flow executor is the natural inner loop for chaos when it lands. | chaos slice |
| Cross-scenario step references | Scenarios stay isolated, always. | — |
| Inline-substitution of MULTIPLE matches from one observe step | An observe step captures **the matched event**; if multiple satisfy the shape, the captured one is the first (today's Eventually-stop-on-first-match). Use multiple observe steps for explicit multi-match semantics. | matcher-rigor slice |

---

## 12. Glossary

- **Step** — one unit of execution in `steps:`. Has exactly one of
  `fire:` or `observe:`.
- **Fire step** — a verb call. Captures its reply (for `nats_request`).
- **Observe step** — a poller wait. Matches an event and captures it.
- **Flow** — the ordered expression in `flow:` linking step ids with
  `>>` (sequence) and `[]` (parallel).
- **Barrier** — one position in the flow that must complete before the
  next position starts. Sequential: barrier of size 1. Parallel
  observation group: barrier of size N.
- **Captured value** — the per-step value stored in the flow context
  for later substitution (a `ReplyValue` for fires, an `EventValue`
  for observes).
- **Gate** — an observe step positioned between two fire steps, by
  function. The flow grammar has no special "gate" keyword; gating is
  implicit in position.
- **Fire-anyway** — fire steps with rejected/error replies do NOT halt
  the flow; only unresolved `${...}` substitution and observe
  timeouts halt.
- **Dual-shape** — legacy `input:` + `expected:` scenarios continue
  to load and run; new scenarios opt into `steps:` + `flow:`.
