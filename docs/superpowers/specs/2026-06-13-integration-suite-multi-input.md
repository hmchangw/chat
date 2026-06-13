# Integration suite multi-site — multi-input scenario design

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report." If a feature isn't
> needed by a scenario, it doesn't get built. Failures are loud.

> 🎯 **In-place evolution.** All changes land in
> `tools/integration-suite-multisite/`.

**Status:** approved design / not yet implemented. Captured
2026-06-13; refined same day after a review round (trims #1-#4 below).
Replaces the over-scoped "envelope + DAG + chaos" draft (commits
`17ecebba`, `59c3f14c`) — chaos and envelope are SEPARATE TOPICS for
separate specs. This document covers only what's needed to author
multi-step scenarios.

**Goal.** Today's tool fires exactly one verb per scenario, so causal
chains like "alice creates room → bob joins → alice sends message →
assert it persisted" cannot be expressed as a single scenario. This
spec adds **multi-fire**: `input:` accepts a list of tasks fired in
declaration order, downstream tasks read upstream replies via
`${task.reply.body_json.*}` substitution, and **reply** assertions
scope to a single task via a `match.task: <id>` selector. Nothing
else changes.

**Refinements from review (applied):**
- **#1** `match.task:` is **reply-only** — poller assertions
  disambiguate by content, not by task. (Correctness: pollers have no
  "active task" at poll time; see §3.2 / §5.3.)
- **#2** substitution is trimmed to `${task.reply.body_json.*}` +
  `${task.reply.status}` — speculative paths dropped (YAGNI).
- **#3** existing scenarios stay **untouched** via a dual-shape
  `UnmarshalYAML` (map-or-list) — no migration sweep.
- **#4** no "DAG" naming — this is a linear task list, not a DAG.

**Out of scope (separate specs, separate sessions).** Named orderings
with Airflow `>>` notation, parallel sibling tasks, `${task.events.*}`
event substitution, envelope (positive/negative) match structure,
chaos engine with mishap iterations, per-iteration `fresh_state`,
F-009 cache-flush hook. These layer on top of multi-fire cleanly but
are not required to author deep scenarios.

---

## 1. What the tool does today

`internal/scenario/types.go`: `Scenario.Input` is a single `Input`
struct (types.go:168). `internal/runtime/runner_scenario.go:120`:
`runScenario` calls `Dispatcher.Fire(input)` exactly once, then loops
`s.Expected` and runs each entry through Gomega
`Eventually`/`Consistently`. Pollers collect events in the background;
the reply reader (`NATSReplyReader.Inject`, nats_reply.go:54) captures
the synchronous reply at fire time.

The constraint: one fire per scenario. Multi-step causality, dedup
sequences, thread reply chains, `tcount` CAS all require >1 fire and
can't be expressed.

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

input:                                # list of tasks (new shape)
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

  - id: send
    site: site-a
    verb: nats_request
    subject: chat.user.${alice.account}.room.${create.reply.body_json.roomId}.site-a.msg.send
    payload: { text: "hi" }
    credential: ${alice.credential}

expected:                             # flat list, unchanged shape
  # reply assertions: scoped by task (the reply is captured at fire time)
  - location: reply
    match: { task: create, body_json: { status: accepted } }
  - location: reply
    match: { task: join, body_json: { status: accepted } }
  - location: reply
    match: { task: send, body_json: { status: accepted } }

  # poller assertions: disambiguated by CONTENT, never by task (#1)
  - site: site-a
    location: mongo_find
    args: { collection: subscriptions }
    match: { account: ${bob.account}, roomId: ${create.reply.body_json.roomId} }

  - site: site-a
    location: cassandra_select
    args: { query: "SELECT JSON * FROM messages_by_room WHERE room_id = ? AND bucket = ?",
            params: [${create.reply.body_json.roomId}, ${bucket(now)}] }
    match: { msg: "hi" }
```

One scenario file, three fires, full causal chain. Reply assertions
scope per step; DB-state assertions match on the content that already
uniquely identifies the row.

---

## 3. Grammar reference

### 3.1 `input:` — list of tasks (or, still, a single fire)

```yaml
input:
  - id: <task-id>          # required in list shape; unique; [a-z][a-z0-9_-]*
    site: <site-id>        # required; site-a | site-b
    verb: <verb-name>      # required; existing closed catalog
    subject: <subject>     # required; ${alias.*} / ${task.reply.*} OK
    payload: { ... }       # required per verb; ${...} resolves recursively
    credential: <cred>     # required for nats_request
```

Task fields are identical to today's `Input` struct plus `id:`. Go
types:

```go
type Task struct {
    ID         string         `yaml:"id"`
    Site       string         `yaml:"site"`
    Verb       string         `yaml:"verb"`
    Subject    string         `yaml:"subject"`
    Payload    map[string]any `yaml:"payload"`
    Credential string         `yaml:"credential,omitempty"`
}

// TaskList accepts EITHER a single mapping (legacy single-fire) OR a
// sequence of tasks. Dual-shape unmarshal — direct analog of
// SeedMembership.UnmarshalYAML (types.go:136).
type TaskList []Task

type Scenario struct {
    // …existing fields unchanged…
    Input TaskList `yaml:"input"`   // was: Input struct
    // Expected []Expected — UNCHANGED
}
```

`TaskList.UnmarshalYAML`:
- `MappingNode` → decode into one `Task` with empty `ID`, return
  `[]Task{t}`. A single-fire scenario needs no id (it can't reference
  itself), so existing scenarios decode unchanged.
- `SequenceNode` → decode into `[]Task`; each element requires `id:`.
- else → `scenario: input must be a single fire (map) or a list of tasks`.

**No `input_order:` block.** Tasks fire in YAML declaration order.
That is the entire ordering grammar. (Named orderings are a separate
spec — §10.)

### 3.2 `match.task: <id>` selector — reply assertions only (#1)

The reserved key `task` inside a `match:` map filters candidate
events to those attributed to task `<id>` before the existing
subset-match.

**It is valid only on `location: reply` assertions.** A reply is
captured synchronously by `NATSReplyReader.Inject` while its task is
firing, so it can be tagged unambiguously. Background pollers
(`mongo_find`, `cassandra_select`, `jetstream_consume`,
`nats_subscribe`, `logs_tail`) observe state during the later
assertion phase — no task is "active" at observation time, and a DB
row isn't "from" a task, it's state that exists after fires settle.
Poller assertions disambiguate by **content** (the fields that
already uniquely identify the row), exactly as they do today.

```yaml
- location: reply
  match: { task: create, body_json: { status: accepted } }   # OK

- location: mongo_find
  args: { collection: rooms }
  match: { task: create, name: Engineering }                 # REJECTED at load
```

- `match:` on a reply without `task:` is unscoped — accepts any
  reply (backward-compatible with single-fire scenarios).
- The loader rejects `match.task:` on any non-reply location (§4).
- `task:` is forbidden inside nested directives (`outbox_payload:`) —
  category error at shape-validate.
- Mechanism mirrors the `outbox_payload:` precedent: pre-pass extracts
  `task`, filters reply events, passes the rest of `match:` to
  existing `matches_shape`.

### 3.3 Substitution — `${task.reply.body_json.*}` and `.status` (#2)

Reply-only, trimmed to what scenarios actually need:

| Path | Resolves to |
|---|---|
| `${<task>.reply.body_json.<field>}` | decoded JSON reply field (dot-walk) |
| `${<task>.reply.status}` | chat-app status (`accepted` / `rejected` / …) |

Dropped until a scenario demands them: `body_raw`, `header.<H>`,
`error`. (Add back in a follow-up when a concrete scenario needs them
— same discipline as the rest of this spec.)

Resolved at **task-fire time** against the running context (replies
from prior tasks). Unresolved reference ⇒ hard halt of the scenario
with a precise error. Load-time validation (§4) verifies the
referenced task exists and is declared **before** the referencing
task; field paths are not type-checked.

### 3.4 Backward compatibility — full (#3)

Existing scenarios are **not migrated**. The dual-shape
`TaskList.UnmarshalYAML` (§3.1) accepts the current single-fire map
shape verbatim — a single-fire scenario decodes to a one-element task
list with empty id and behaves exactly as today. New multi-step
scenarios use the list shape with `id:` per task. No sweep, no
throwaway tool.

---

## 4. Loader contract

Files: `internal/scenario/loader.go` (small change),
`internal/scenario/multi_input_validate.go` (new — name per #4).

Pipeline:
1. YAML decode; `TaskList.UnmarshalYAML` normalizes both shapes into
   `[]Task` (§3.1).
2. Per-task required fields (site, verb, subject; payload per verb;
   credential per verb). List-shape tasks also require `id:`.
3. Run existing `rejectDeprecatedTokens` per task (`${site}`,
   `${siteA}`, `${service.*}`).
4. **Multi-input validation** (`multi_input_validate.go`):
   - Task `id` values unique within scenario.
   - Walk each task's subject/payload/credential for
     `${<task>.reply.*}` tokens; the referenced task must exist AND
     appear earlier in declaration order.
   - `match.task: <id>` may appear **only** on `location: reply`
     assertions, and must name a declared task.

Errors are precise, scenario-located, and fire **before** any I/O:

```
scenario %q: duplicate task id %q at input[%d] and input[%d]
scenario %q: list-shape input[%d] is missing required id:
scenario %q: task %q references ${%s.reply.*} but no task with id %q is declared
scenario %q: task %q references ${%s.reply.*} but task %q fires after %q in declaration order
scenario %q: expected[%d].match.task is only valid on location: reply (got %q)
scenario %q: expected[%d].match.task references unknown task id %q (declared: [...])
```

No cycle check needed — declaration order is linear; the
"earlier-than" rule already prevents cycles.

---

## 5. Executor contract

Files: `internal/runtime/task_executor.go` (new, small — name per #4);
`internal/runtime/runner_scenario.go` (rewired);
`internal/readers/nats_reply.go` (add task tag to reply Inject).

### 5.1 Single execution loop

`runScenario` replaces its single `Dispatcher.Fire(s.Input)` call
with:

```
ctx := newSubstitutionContext()      // map[task-id]ReplyPayload
for _, task := range s.Input {       // declaration order
   resolved, err := substitute(task, ctx)
   if err != nil { halt scenario with precise error }   // §3.3 unresolved

   reply := dispatcher.Fire(resolved, task.ID)          // reply tagged task=ID (§5.3)
   ctx[task.ID] = reply                                 // stored even on rejected/error (§5.2)
}
// run existing Expected[] assertion loop — unchanged
```

Identical to today's flow with an outer loop and a context map.

### 5.2 Fire-anyway semantics

A task whose reply is `rejected` / `error` / timeout does NOT halt
the scenario — downstream tasks still fire. Only an **unresolved
substitution** halts (data-dep failure). Assertions remain the source
of truth for pass/fail, not the fires. A scenario can deliberately
fire a doomed sequence and assert that the doom occurred.

### 5.3 Reply tagging — reply reader only (#1)

`internal/readers/types.go`'s `Event` gains a `Task string` field
(alongside `OwnerSvc`). `NATSReplyReader.Inject` (nats_reply.go:54)
takes the firing task's id and stamps it on the reply event — this
runs synchronously at fire time, so the tag is unambiguous.

**No dispatcher-side `markActiveTask` plumbing, and pollers are not
touched** — they never tag a task (§3.2). This is the simplification
#1 buys: tagging lives entirely in the one synchronous reply path.

### 5.4 No concurrency

Tasks fire one at a time. No goroutines, no sibling fan-out. (Parallel
groups are a separate spec — §10.)

---

## 6. Matcher contract — `task:` selector (reply only)

File: `internal/runtime/matchshape.go`.

`task` joins `outbox_payload` as a shape-level directive consumed
before the subset match, but applies only to reply events (the loader
guarantees `task:` appears only on reply assertions, so the matcher
only ever sees it there):

```go
if taskID, ok := matchMap["task"]; ok {
    events = filterByTask(events, taskID)   // reply events carry Task
    delete(matchMap, "task")                // continue with the rest
}
// existing matches_shape logic, unchanged
```

- Unscoped reply `match:` (no `task:`) accepts any reply —
  backward-compatible with single-fire scenarios.
- `task:` inside `outbox_payload:` rejected at shape-validate.

---

## 7. Migration — none (#3)

No migration. Dual-shape `UnmarshalYAML` (§3.1, §3.4) keeps every
existing scenario working untouched. New scenarios opt into the list
shape. (This section retained as an explicit "nothing to do" so the
absence is intentional, not an oversight.)

---

## 8. TDD plan

Per `CLAUDE.md`: Red → Green → Refactor → Commit per slice.

**Phase 1 — grammar + loader**
- RED: tests in `internal/scenario/` covering: single-map decode
  (legacy, unchanged), list decode, list element missing id, duplicate
  id, unknown reply ref, forward ref, `match.task` on a poller
  (rejected), `match.task` unknown id.
- GREEN: `Task` + `TaskList.UnmarshalYAML` + `multi_input_validate.go`.
- Unit-tested; no infra.

**Phase 2 — matcher `task:` selector**
- RED: `matchshape_test.go` — reply filter-pass, reply filter-mismatch,
  unscoped reply backward-compat, `task:` inside `outbox_payload:`
  rejected.
- GREEN: directive handler (~10 LoC).

**Phase 3 — executor + reply tagging**
- RED: tests in `internal/runtime/` with a mock dispatcher injecting
  scripted replies: single-task pass-through, two-task chain with
  reply substitution, three-task chain, failed-task does NOT halt
  downstream, unresolved substitution halts with precise error.
- GREEN: `task_executor.go` + `Event.Task` + reply-Inject tag +
  `runner_scenario.go` rewire.

**Phase 4 — integration proof**
- Author one new scenario that REQUIRES multi-input (the
  `room-create-then-join-then-send` example from §2). Runs green
  end-to-end via
  `USE_INFRA=true make -C tools/integration-suite-multisite local`.

After Phase 4 you can author deep scenarios.

---

## 9. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Reply substitution fails late (unresolved at runtime) | Low | Hard-halt with precise error naming the task and bad `${...}`; loader catches structural cases up-front |
| Reserved `task:` collides with a chat-app reply using `task` as a field | Low | Existing `outbox_payload:` precedent; matcher rejects collision at shape-validate; reply-only narrows the surface |
| Dual-shape unmarshal masks a malformed `input:` (neither map nor list) | Low | `UnmarshalYAML` default branch rejects with a precise kind error |

(The "task-tagging races on pollers" risk from the prior draft is
**deleted** — #1 confines tagging to the synchronous reply path, so
the race cannot occur.)

---

## 10. Explicitly NOT in scope

| Out | Why | Future spec |
|---|---|---|
| `input_order:` block + Airflow `>>` named orderings | Stress-coverage feature, not "author multi-step" | "named orderings" |
| `[a, b]` sibling-group parallel grammar / concurrency | Concurrency is stress, not authoring | "concurrency" |
| `${task.events.*}` event substitution | Adds executor-side event-window settling; reply chaining doesn't need it | "event substitution" |
| `${task.reply.body_raw / header / error}` | YAGNI — no scenario needs them yet (#2) | add on demand |
| `expected: { positive: [...], negative: [...] }` envelope | Orthogonal restructure of what assertions mean | "envelope" |
| Chaos engine, iterations, mishap framework | Separate topic with its own hard dependency (F-009) | "chaos engine" |
| Cross-scenario task references | Scenarios stay isolated, always | — |

---

## 11. Glossary

- **Task** — one verb fire, with an `id`, declared in the `input:`
  list. Replaces today's single `Input` struct (which still decodes
  via the dual-shape unmarshal).
- **Substitution context** — per-scenario map keyed by task id,
  carrying each completed task's reply. Feeds downstream
  `${task.reply.*}` resolution.
- **`task:` selector** — reserved key inside a **reply** `match:`
  shape that filters reply events by the firing task's id.
- **Fire-anyway** — failed tasks do not halt downstream fires; only an
  unresolved `${...}` substitution halts the scenario.
