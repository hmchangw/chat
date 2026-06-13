# Integration suite multi-site — input DAG design

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report," no flexibility for its
> own sake. If a feature isn't needed by a scenario, it doesn't get
> built.

> 🎯 **In-place evolution, not a fork.** The DAG model replaces the
> single-fire `input:` shape in `tools/integration-suite-multisite/`
> directly. No second tree. All grammar/loader/executor/matcher
> changes land in the existing package layout.

**Status:** approved design / not yet implemented. Captured 2026-06-13
during a brainstorming round on top of `docs/integration-suite-plan-ahead.md`
§§ 2.3, 2.4, 2.6, 3.

**Goal.** Replace the single-fire `input:` shape with a list of named
tasks plus an `input_order:` block declaring orderings to stress.
Authors can express multi-step flows (`alice creates room → bob joins
→ alice sends`), per-task assertions, and reply-or-event substitution
into downstream tasks. Sequential execution, every-task-waits-for-its-
events semantics. Backward-incompatible: the 23 existing scenarios
migrate as a single one-line sweep.

**Scope — DAG-only.** This spec covers §2.3 (multi-step) + §2.6
(ordering notation). It does NOT cover §2.2 (envelope positive/
negative restructure), §3 chaos engine, fresh-state iteration loop,
or reporter rework. Those layer cleanly on top of DAG later — the
chaos engine just loops the DAG; the envelope is an `expected:` shape
change orthogonal to `input:`.

---

## 1. What the tool does today (the starting point)

`internal/scenario/types.go`'s `Scenario.Input` is a single struct:

```go
type Input struct {
    Site       string
    Verb       string
    Subject    string
    Payload    map[string]any
    Credential string
}
```

`runScenario` (`internal/runtime/runner_scenario.go:120`) calls
`Dispatcher.Fire` exactly once, then loops `s.Expected` and runs each
through Gomega's `Eventually`/`Consistently`. The dispatcher captures
the synchronous reply (if any) into the `reply` reader so a
`location: reply` assertion observes it. Pollers run in the background
collecting events from Mongo/Cassandra/NATS/JetStream/logs.

**The constraint that gates everything downstream:** one fire per
scenario. JetStream redelivery sequences, dedup tests, multi-step
thread flows, and `tcount` CAS races can't be expressed.

---

## 2. The new shape — worked example

```yaml
scenario: room-create-then-join-then-send
source: room-service/handler.go:300, room-worker/handler.go:1188
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
        bob:   { verified: true }

input:                              # NEW — list of tasks
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

input_order:                        # NEW — mandatory; map of named orderings
  happy_path: create >> join >> send_msg

expected:
  - location: reply
    match: { task: create, body_json: { status: accepted } }   # NEW — task: selector

  - location: reply
    match: { task: join, body_json: { status: accepted } }

  - site: site-a
    location: mongo_find
    args: { collection: subscriptions }
    match: { task: join, account: ${bob.account}, roomId: ${create.reply.body_json.roomId} }

  - site: site-a
    location: cassandra_select
    args: { query: "SELECT JSON * FROM messages_by_room WHERE room_id = ? AND bucket = ?",
            params: [${create.reply.body_json.roomId}, ${bucket(now)}] }
    match: { task: send_msg, msg: "hi" }
```

Three orderings on the same task definitions:

```yaml
input_order:
  happy_path:     create >> join >> send_msg     # serial, expected to pass
  send_before_join: create >> send_msg >> join   # negative ordering
  parallel:       create >> [join, send_msg]     # PARSED but rejected at validate
                                                  # — send_msg has a data dep on create
                                                  # (passes), but firing join+send concurrently
                                                  # is forbidden under "sequential always" (§6)
```

---

## 3. Grammar reference

### 3.1 `input:` — list of tasks

```yaml
input:
  - id: <task-id>            # required; unique within scenario; [a-z][a-z0-9_-]*
    site: <site-id>          # required; "site-a" or "site-b"
    verb: <verb-name>        # required; closed catalog (today: nats_request, jetstream_publish)
    subject: <subject>       # required; ${alice.account} / ${task.reply.*} / ${task.events.*} OK
    payload: { ... }         # required for nats_request/jetstream_publish; ${...} resolves recursively
    credential: <cred>       # required for nats_request; ${alice.credential} pattern
```

Field semantics are identical to today's `Input` struct; the only
shape change is wrapping it in a list and adding `id:`. The Go type
becomes:

```go
type Task struct {
    ID         string         `yaml:"id"`
    Site       string         `yaml:"site"`
    Verb       string         `yaml:"verb"`
    Subject    string         `yaml:"subject"`
    Payload    map[string]any `yaml:"payload"`
    Credential string         `yaml:"credential,omitempty"`
}

type Scenario struct {
    // …existing fields…
    Input       []Task              `yaml:"input"`
    InputOrder  map[string]string   `yaml:"input_order"`
    // …
}
```

### 3.2 `input_order:` — orderings to stress

Required block. Map of `<ordering-name> → <ordering-expression>`. At
least one entry; ordering names are `[a-z][a-z0-9_]*`.

**Ordering expression EBNF:**

```
ordering   ::= group ( ">>" group )*
group      ::= task-id | "[" task-id ( "," task-id )* "]"
task-id    ::= [a-z][a-z0-9_-]*
```

**Notation:**
- `a >> b` — a finishes before b starts
- `a >> b >> c` — chain (left-to-right execution)
- `a >> [b, c]` — a finishes; b and c both ready (sequential under
  §6 — they run in declaration order, since v1 has no concurrency)
- `[a, b] >> c` — a, b both ready in declaration order; c after both
- single task: `a`

Whitespace around `>>` and `,` is ignored. Nested brackets (e.g.
`a >> [b, [c, d]]`) are rejected by the parser.

### 3.3 `expected[].match.task:` — per-task assertion scoping

The reserved key `task` inside any `match:` shape filters events by
the task that produced them.

```yaml
expected:
  - location: reply
    match: { task: create, body_json: { status: accepted } }

  - location: mongo_find
    args: { collection: rooms }
    match: { task: send_msg, name: Engineering }
```

Filter semantics:
- For `location: reply`, the reply event is tagged with the firing
  task's id at injection time. Matcher filters by `task == <id>`,
  then subset-matches the remainder of `match:` against the payload.
- For polled locations (`mongo_find`, `cassandra_select`,
  `jetstream_consume`, `nats_subscribe`, `logs_tail`), the event
  carries the **active task at the time it was observed**, i.e. the
  task that was firing OR the last-completed task when the event
  arrived. Matcher filters on this.

A `match:` without `task:` is unscoped — accepts events from any
task. Backward-compatible with the migrated single-task scenarios.

Reserved key collision: a chat-app payload that genuinely uses
`task` as a top-level field would clash. Mitigation: `task:` is a
shape-level directive consumed by the matcher BEFORE the subset
match runs (mirrors `matches_shape` / `outbox_payload` precedent).

### 3.4 Substitution — `${task.reply.*}` and `${task.events.*}`

**Reply addressing (always available):**

| Path | Resolves to |
|---|---|
| `${<task>.reply.body_json.<field>}` | Decoded JSON reply body field |
| `${<task>.reply.body_raw}` | Reply body as string |
| `${<task>.reply.status}` | `accepted` / `rejected` / `error` (set by chat-app) |
| `${<task>.reply.header.<HeaderName>}` | First value of NATS reply header |
| `${<task>.reply.error}` | Transport-level error string (timeout / no responders) |

**Event addressing:**

| Path | Resolves to |
|---|---|
| `${<task>.events.<location>[<index>].<field>}` | The `<index>`th event observed at `<location>` during `<task>`'s window |
| `${<task>.events.<location>.length}` | Number of events observed at that location |

`<location>` is exactly as it appears in `expected[].location` —
`reply`, `mongo_find`, `cassandra_select`, `jetstream_consume`,
`nats_subscribe`, `logs_tail`. `<index>` is 0-based; out-of-bounds is
a runtime error (the executor halts with `${...} index out of range`).
`<field>` uses dot-walk against the event's `Payload` (the shape the
poller emits — see `internal/readers/`).

**Resolution timing.** Substitutions are resolved at **task-fire
time**, NOT at scenario-load time. The executor maintains a running
context with reply payloads and event buffers indexed by task id.
Load-time validation (§4.3) verifies that referenced tasks EXIST and
the reference comes from a later-firing task; field paths are not
type-checked.

### 3.5 Backward compatibility — none

The single-fire shape (`input:` as a map, not a list) is **removed**.
All 23 existing scenarios migrate to the new shape with a one-line
addition (see §7 Migration). Loader rejects the old shape with a
clear migration error pointing at this spec.

---

## 4. Loader contract

### 4.1 Parse pipeline (`internal/scenario/loader.go`)

1. YAML decode into `Scenario` struct (`input: []Task`).
2. **Shape preflight** — peek the decoded `input:` node. If it's a
   mapping (legacy shape), reject with
   `scenario %q: legacy single-fire input: shape removed; migrate to list-of-tasks + input_order: (see specs/2026-06-13-integration-suite-input-dag.md §7)`.
3. Required-field validation per task (id, site, verb, subject;
   payload required for known verbs).
4. Run `rejectDeprecatedTokens` per task (existing function — covers
   `${site}`, `${siteA}`, `${service.*}`).
5. Run **DAG validation** (§4.2).
6. Run **input_order validation** (§4.3).

### 4.2 DAG validation (`internal/scenario/dag_validate.go` — new)

Walks every task's `subject`, `payload` (recursively into maps and
slices), and `credential` for `${<task>.reply.*}` and
`${<task>.events.*}` tokens. Builds a directed graph: edge `a → b`
means task `b` references task `a`'s reply or events.

Validation rules:
- Every referenced `<task>` must be declared in `input:`.
- No cycles.
- Task `id` values unique.
- Reserved task id `_` (underscore) forbidden (reserved for future
  ordering-only placeholder, not used in v1).

Errors are precise:

```
scenario %q: task %q references ${%s.reply.*} but no task with id %q is declared
scenario %q: cycle in task dependencies: a → b → a
scenario %q: duplicate task id %q at input[%d] and input[%d]
```

### 4.3 input_order validation (`internal/scenario/order_parse.go` — new)

Parser produces an `OrderingPlan` per named ordering:

```go
type OrderingPlan struct {
    Name   string
    Groups [][]string  // outer = sequence, inner = group fired in declaration order
}
```

For each ordering:
1. Tokenize on `>>`; recognise `[...]` groups (regex / hand-rolled
   parser — small enough for hand-rolled).
2. Every task id mentioned in the ordering must be declared in
   `input:`.
3. Every task declared in `input:` must appear in the ordering
   (no silently-skipped tasks).
4. Hard data deps (from §4.2) must be respected: if `b → a` (b
   references a), then `a` must appear BEFORE `b` in the linearized
   ordering.

Errors:

```
scenario %q: input_order %q: unknown task id %q (declared tasks: [...])
scenario %q: input_order %q: task %q is declared in input: but missing from the ordering
scenario %q: input_order %q: ordering violates data dep — task %q references ${%s.reply.*} but %q fires before %q
scenario %q: input_order %q: malformed expression at %q (expected task-id, '>>', '[', ']', or ',')
scenario %q: input_order %q: nested brackets not supported
scenario %q: input_order %q is empty
```

### 4.4 Cross-scenario uniqueness (existing, unchanged)

`CheckScenarioNameUniqueness` and `CrossScenarioCheck` continue to
work — they key on `Scenario.Name` and on seed aliases. The DAG
shape doesn't affect either.

---

## 5. Executor contract (`internal/runtime/dag_executor.go` — new)

Replaces the single-`Dispatcher.Fire` call in `runScenario`. The
existing `Dispatcher` stays as the per-task primitive — the executor
calls `Fire` once per task, in ordering sequence, capturing each
task's reply + observed events into the substitution context.

### 5.1 State machine

For each `(ordering, task)` pair in declaration order:

```
1. Resolve task's subject/payload/credential against the running
   substitution context (replies + events of all previously-completed
   tasks). If any ${...} reference is unresolved, halt the ordering
   with a precise error.

2. Open per-task event collectors for every `location` listed in
   expected[] that mentions this task (via match.task: <id>) AND
   for every location referenced by a downstream task's
   ${<id>.events.<location>.*} substitution. Use existing pollers
   (mongo_find / cassandra_select / jetstream_consume /
   nats_subscribe / logs_tail) — no new readers required.

3. Mark Dispatcher's current-task context to <id> so the reply
   reader's Inject() tags the captured reply with task=<id>.

4. Call Dispatcher.Fire(ctx, taskInputSpec, ...). Reply is captured
   into the reply reader as today.

5. Wait for the per-task event-collection window to settle (§5.2).

6. Store the task's reply payload (or reply-error) and the
   collected event slices in the substitution context under key
   <id>. The context shape is:

       map[task-id]TaskResult{ Reply ReplyPayload, Events map[location][]Event }

7. Per Q1 fire-anyway: a failed reply or a missing event the
   downstream task substitutes from does NOT skip downstream tasks
   automatically — only an unresolved substitution does (step 1).
   That's a clean shape: explicit data dep failure halts; behavioral
   failure flows through to assertion time.
```

### 5.2 Event-window settling — "always wait for events"

Every task waits for its event-collection window to settle before
the next task fires. "Settle" means: the union of `expected[]` rows
scoped to this task (via `match.task: <id>`) has either fired its
positive Eventually() OR exhausted its timeout. Pollers keep
collecting throughout — events that arrive late get attributed to
the next task (the "active task at observation time" rule from §3.3).

**Per-task timeout default:** the maximum of (a) the longest
`expected[].timeout` for rows scoped to this task, and (b) 2s
floor. Authors can override per-task via `task.event_window:
<duration>` (a small grammar addition added late if needed; v1 uses
the inferred default).

The executor signals "this task is done" by:

1. Computing the set of `expected[]` rows with `match.task: <id>`.
2. For each, running its `Eventually()` (positive) or `Consistently()`
   (negative) loop against the per-task event collectors.
3. Once all rows in the set have a verdict (pass or timeout), the
   task's events buffer is frozen for substitution purposes — late
   events get attributed to the next task.
4. Move to next task.

**Substitution timing edge case.** If task `n+1` substitutes from
`${task-n.events.<location>[0].*}` but task `n`'s window collected
zero events at `<location>`, the substitution fails at step 1 of
the next iteration with `${...} index out of range — task n
observed 0 events at location X during its window`. Hard halt; no
silent empty.

### 5.3 Sequential — no concurrency

Even when `input_order` has `[a, b]` (parallel-shaped group), the
executor runs `a` then `b` in declaration order. The grammar admits
the brackets so the chaos lift can later add a `chaos.concurrency:
N` config to fan them out without grammar changes. v1: brackets are
informational, no concurrency.

### 5.4 Per-task reply tagging

`internal/readers/nats_reply.go` adds a `task` field to the injected
event (mirrors the existing `OwnerSvc` field). `internal/runtime/
dispatcher.go`'s `Inject` callsite is updated to thread the active
task id. The matcher's `task:` selector reads it.

### 5.5 Cross-task event attribution

Polled events get a `task` attribution at observation time:
- If `t.events.<location>` is currently being collected and an
  event arrives, attribute to `t`.
- After `t`'s window settles, events at `<location>` attribute to
  the next task (or remain unattributed if no next task).

Implementation: pollers don't change — they emit events with
their existing fields. The executor wraps each poller's output
channel with a tagger that stamps `event.Task` based on the
executor's "currently firing task" pointer at receive time. Adds
~20 lines of code, no per-poller changes.

---

## 6. Matcher contract — `task:` selector

`internal/runtime/matchshape.go` adds `task` to the recognised
shape-level directives (alongside `outbox_payload`). The handler:

1. Pre-pass: extract `task` from the `match:` map.
2. Filter the event slice — keep only events whose `Task` field
   equals the extracted value.
3. Pass the rest of `match:` (minus `task:`) to the existing
   `matches_shape` machinery.

`task:` is forbidden inside `outbox_payload:` (a nested directive
inside another directive is a category error). The matcher rejects
it at the shape-validation step with a precise error.

Unscoped `match:` (no `task:`) accepts events from any task —
default behavior, backward-compatible with the migrated
single-task scenarios.

---

## 7. Migration — the 23 scenarios

Every existing scenario gets a single-task rewrite. Mechanically:

```diff
- input:
-   site: site-a
-   verb: nats_request
-   subject: chat.user.${alice.account}.request.room.site-a.create
-   payload: { name: $auto }
-   credential: ${alice.credential}
+ input:
+   - id: t1
+     site: site-a
+     verb: nats_request
+     subject: chat.user.${alice.account}.request.room.site-a.create
+     payload: { name: $auto }
+     credential: ${alice.credential}
+ input_order:
+   happy_path: t1
```

`expected[]` entries gain a `match.task: t1` field where they refer
to that fire (most do, but the matcher accepts unscoped match
shapes, so they don't all have to). A migration script
(`tools/integration-suite-multisite/cmd/migrate_v3_to_dag/main.go`,
delete after sweep) generates these edits mechanically and prints
a diff per file for review.

The sweep ships as ONE commit with all 23 scenarios + the script
deletion + a `RUNBOOK.md` note. The runner gate is the same
`make validate` + `make local` set we use today.

---

## 8. TDD plan

Per `CLAUDE.md` § Testing: Red → Green → Refactor → Commit for every
slice. Phase order (each lands as its own commit batch):

**Phase 1 — grammar + loader (~3 days)**
- RED: tests in `internal/scenario/` covering Task struct decode,
  unknown-shape rejection, every error path enumerated in §4.2 + §4.3.
- GREEN: `Task` type, list-shape loader, DAG validator, input_order
  parser.
- Cover: empty input, single task, three-task chain, parallel
  brackets, malformed expressions, unknown task ref, cycle, data-dep
  violation, missing task in ordering.

**Phase 2 — executor (~5 days)**
- RED: tests in `internal/runtime/` covering executor state machine
  with a mock dispatcher + injected events. Covers: single-task
  pass-through, two-task chain with reply substitution, two-task
  chain with event substitution (lazy window), failed-task does not
  halt downstream, unresolved substitution halts, per-task timeout
  default.
- GREEN: `dag_executor.go` + `Inject` wiring + event-tagger wrapper.

**Phase 3 — matcher `task:` selector (~1 day)**
- RED: `matchshape_test.go` cases: filter-pass, filter-mismatch,
  unscoped backward compat, `task:` in outbox_payload rejected.
- GREEN: directive handler.

**Phase 4 — migration sweep (~2 days)**
- RED: every migrated scenario passes `make validate`.
- Migration script. One commit, all 23 + RUNBOOK note + script
  delete.

**Phase 5 — integration proof (~2 days)**
- Author one new scenario that REQUIRES the DAG model (e.g.
  "create room → bob joins → alice sends → assert message persisted
  in Cassandra"), single ordering. Runs green end-to-end via
  `USE_INFRA=true make -C tools/integration-suite-multisite local`.

**Phase 6 — second ordering on the same scenario (~½ day)**
- Add a `send_msg >> join` negative ordering to the new scenario;
  assert it correctly produces an error reply on `send_msg`.
- Proves the multi-ordering loop works end-to-end against real
  infra.

**Phase 7 — docs (~1 day)**
- Update `SCENARIO-REFERENCE.md` (new top-level `input:` shape +
  `input_order:` + `task:` selector + `${task.events.*}`
  substitution).
- Update `AUTHORING.md` (when to use a DAG, the >>  notation, common
  patterns).
- Add a one-pager `DAG.md` cookbook.

**Total: ~3 weeks** (15 working days), with proof-of-life by end of
phase 2 (executor + tests pass).

---

## 9. Risks and mitigations

| Risk | Likelihood | Mitigation |
|---|---|---|
| Event-window-settling logic interacts badly with the existing pollers' Eventually timing | Medium | Phase 2 executor tests use a mock dispatcher + handcrafted event injection; phase 5 catches real-infra timing |
| Per-task event attribution races (event arrives between executor's "task done" decision and the next task's "task firing" mark) | Medium | The executor's task-pointer flip happens BEFORE marking the previous task's events frozen. Late events attribute to next-task by design. Document. |
| Migration sweep accidentally changes a scenario's behavior | Low | Script-generated; reviewer scans the diff; `make local` re-runs the full set before commit |
| `${task.events.<loc>[<idx>].*}` field-path bugs surface late | Medium | Phase 2 tests cover dot-walk against every poller's emitted Payload shape; spec § Substitution lists each |
| Reserved `task:` collides with a chat-app payload using `task` as a field name | Low | Existing precedent: `outbox_payload:` is reserved. Spec calls it out; matcher rejects collision at shape-validate |
| `>>` notation feels too "framework-y" for some authors | Low | Documented escape: a single-task scenario uses `input_order: { happy: t1 }`, no `>>` needed |
| Sequential-only is restrictive for race tests | High (by design) | Bracket grammar reserved; chaos engine lift adds `concurrency:` knob. Document explicitly in DAG.md |

---

## 10. What this spec EXPLICITLY does NOT cover

| Out of scope | Why deferred | Where it lives |
|---|---|---|
| `expected: { positive: [...], negative: [...] }` envelope restructure | Orthogonal to `input:` shape; touches all 23 scenarios on its own | Plan-ahead §2.2 / §3 |
| Chaos engine (per-iteration fresh-state loop, mishap picks) | Layers cleanly on top of DAG (just loops the DAG) | Plan-ahead §3, deferred per scope choice |
| Sibling concurrency | Bracket grammar reserved; v1 sequential. Folds in with chaos's `concurrency:` knob | Plan-ahead §3.1 + this spec §5.3 |
| Per-ordering chaos override (`input_order.case_a.chaos:`) | Plan Q10 🟡 YAGNI for v1 | Plan-ahead §5 Q10 |
| Auto-enumeration of orderings (`input_order: auto`) | Plan Q9 ✅ rejected | Plan-ahead §2.6 + §5 Q9 |
| Cross-scenario DAGs (one task in scenario A references a task in scenario B) | Explicitly never. Scenarios remain isolated. | — |
| Smart-targeted chaos (Q13 ⏸) | Pairs with chaos engine, not DAG | Plan-ahead §5 Q13 |

---

## 11. Open questions deferred to implementation

These do not block the spec but need a focused micro-decision when
the code lands. They are NOT the kind that re-shape the spec.

1. **`task.event_window: <duration>` per-task override** — useful?
   Decide in phase 2 when the default heuristic is in code.
2. **Default per-task event window** — 2s floor mentioned in §5.2;
   verify on real-infra in phase 5; tune if needed.
3. **Migration script as `internal/cmd/` vs `tools/`** — small
   process question, decide in phase 4.

---

## 12. Glossary (terms this spec introduces or repurposes)

- **Task** — One verb fire. Replaces today's single `Input` struct.
  Has an `id`, fires via the existing dispatcher.
- **Ordering** — A linearization of the task DAG that the executor
  fires in. Authors declare named orderings under `input_order:`.
- **Hard data dep** — Edge in the task DAG inferred from
  `${<task>.reply.*}` or `${<task>.events.*}` substitutions in
  another task's payload/subject/credential.
- **Event-collection window** — The period between a task's fire
  and the executor's decision that the task is "done" (its scoped
  assertions have settled or timed out). Events arriving in this
  window attribute to the task.
- **Substitution context** — Per-execution map keyed by task id,
  carrying each completed task's `Reply` + `Events`. Used to
  resolve `${...}` tokens at fire-time of each downstream task.
- **`task:` selector** — Reserved key inside a `match:` shape that
  filters events by their attributed task id before subset-matching.
