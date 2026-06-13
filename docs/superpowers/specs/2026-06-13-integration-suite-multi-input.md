# Integration suite multi-site — multi-input scenario design

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report." If a feature isn't
> needed by a scenario, it doesn't get built. Failures are loud.

> 🎯 **In-place evolution.** All changes land in
> `tools/integration-suite-multisite/`.

**Status:** approved design / not yet implemented. Captured
2026-06-13. Replaces the over-scoped "envelope + DAG + chaos" draft
(commits `17ecebba`, `59c3f14c`) — chaos and envelope are SEPARATE
TOPICS for separate specs. This document covers only what's needed
to author multi-step scenarios.

**Goal.** Today's tool fires exactly one verb per scenario, so causal
chains like "alice creates room → bob joins → alice sends message →
assert it persisted" cannot be expressed as a single scenario. This
spec adds **multi-fire**: `input:` becomes a list of tasks fired in
declaration order, downstream tasks read upstream replies via
`${task.reply.*}` substitution, and assertions scope to a single
task via a `match.task: <id>` selector. Nothing else changes.

**Out of scope (separate specs, separate sessions).** Named orderings
with Airflow `>>` notation, parallel sibling tasks, `${task.events.*}`
event substitution, envelope (positive/negative) match structure,
chaos engine with mishap iterations, per-iteration `fresh_state`,
F-009 cache-flush hook. These are stress / structural / robustness
features; they layer on top of multi-fire cleanly but they are not
required to author deep scenarios.

---

## 1. What the tool does today

`internal/scenario/types.go`: `Scenario.Input` is a single struct.
`internal/runtime/runner_scenario.go:120`: `runScenario` calls
`Dispatcher.Fire(input)` exactly once, then loops `s.Expected` and
runs each entry through Gomega `Eventually`/`Consistently`. Pollers
collect events in the background; the reply reader captures the
synchronous reply (if any).

The constraint: one fire per scenario. Multi-step causality, dedup
sequences, thread reply chains, `tcount` CAS races all require >1
fire and can't be expressed.

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

input:                                # NEW — list of tasks
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

expected:                             # SAME flat list as today
  - location: reply
    match: { task: create, body_json: { status: accepted } }   # NEW match.task: filter

  - location: reply
    match: { task: join, body_json: { status: accepted } }

  - location: reply
    match: { task: send, body_json: { status: accepted } }

  - site: site-a
    location: mongo_find
    args: { collection: subscriptions }
    match: { task: join, account: ${bob.account}, roomId: ${create.reply.body_json.roomId} }

  - site: site-a
    location: cassandra_select
    args: { query: "SELECT JSON * FROM messages_by_room WHERE room_id = ? AND bucket = ?",
            params: [${create.reply.body_json.roomId}, ${bucket(now)}] }
    match: { task: send, msg: "hi" }
```

One scenario file, three fires, full causal chain, assertions scoped
per step.

---

## 3. Grammar reference

### 3.1 `input:` — list of tasks

```yaml
input:
  - id: <task-id>          # required; unique within scenario; [a-z][a-z0-9_-]*
    site: <site-id>        # required; site-a | site-b
    verb: <verb-name>      # required; existing closed catalog
    subject: <subject>     # required; ${alias.*} / ${task.reply.*} OK
    payload: { ... }       # required per verb; ${...} resolves recursively
    credential: <cred>     # required for nats_request
```

Task fields are identical to today's `Input` struct. The only shape
change is wrapping in a list and adding `id:`. Go types:

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
    // …existing fields unchanged…
    Input []Task `yaml:"input"`            // CHANGED — was Input struct
    // Expected []Expected — UNCHANGED
}
```

**No `input_order:` block.** Tasks fire in YAML declaration order.
That's the entire ordering grammar.

### 3.2 `match.task: <id>` selector

The reserved key `task` inside any `match:` map filters candidate
events to those attributed to task `<id>` before the existing
subset-match runs.

```yaml
expected:
  - location: reply
    match: { task: create, body_json: { status: accepted } }

  - location: mongo_find
    args: { collection: rooms }
    match: { task: send, name: Engineering }
```

- `match:` without a `task:` key is unscoped — accepts events from
  any task (backward-compatible with migrated single-task scenarios).
- `task:` is forbidden inside nested directives (`outbox_payload:`);
  category error rejected by the matcher at shape-validate.
- Mirrors the `outbox_payload:` precedent: pre-pass extracts `task`,
  filters events, passes the rest of `match:` to existing
  `matches_shape`.

### 3.3 Substitution — `${task.reply.*}` only

Reply-only substitution. Events deferred to a later spec.

| Path | Resolves to |
|---|---|
| `${<task>.reply.body_json.<field>}` | decoded JSON reply field (dot-walk) |
| `${<task>.reply.body_raw}` | reply body as string |
| `${<task>.reply.status}` | chat-app status (`accepted`/`rejected`/…) |
| `${<task>.reply.header.<H>}` | first value of reply header H |
| `${<task>.reply.error}` | transport-level error string |

Resolved at **task-fire time** against the running context (replies
from prior tasks). Unresolved reference ⇒ hard halt of the scenario
with a precise error.

Load-time validation (§4) verifies the referenced task exists and is
declared **before** the referencing task; field paths are not
type-checked.

### 3.4 Backward compatibility — none

The single-fire shape (`input:` as a map, not a list) is **removed**.
All existing scenarios migrate to the list shape via a mechanical
sweep (§7). Loader rejects the old shape with a clear migration error.

---

## 4. Loader contract

Files: `internal/scenario/loader.go` (changes),
`internal/scenario/dag_validate.go` (new).

Pipeline:
1. YAML decode (`input: []Task`).
2. **Shape preflight** — legacy map-shaped `input:` rejected:
   ```
   scenario %q: legacy single-fire input: shape removed; wrap in a list with id: t1
   (see specs/2026-06-13-integration-suite-multi-input.md §7)
   ```
3. Per-task required fields (id, site, verb, subject; payload per
   verb; credential per verb).
4. Run existing `rejectDeprecatedTokens` per task (covers `${site}`,
   `${siteA}`, `${service.*}`).
5. **Multi-input validation** (`dag_validate.go`):
   - Task `id` values unique within scenario.
   - Walk every task's subject/payload/credential for
     `${<task>.reply.*}` tokens; the referenced task must exist AND
     appear earlier in declaration order.
   - `match.task: <id>` in any `expected[]` entry must name a
     declared task.

Errors are precise, scenario-located, and fire **before** any I/O:

```
scenario %q: duplicate task id %q at input[%d] and input[%d]
scenario %q: task %q references ${%s.reply.*} but no task with id %q is declared
scenario %q: task %q references ${%s.reply.*} but task %q fires after %q in declaration order
scenario %q: expected[%d].match.task references unknown task id %q (declared: [...])
```

No cycle check needed — declaration order is linear, the "earlier
than" rule already prevents cycles.

---

## 5. Executor contract

Files: `internal/runtime/dag_executor.go` (new, small);
`internal/runtime/runner_scenario.go` (rewired);
`internal/runtime/dispatcher.go` + `internal/readers/nats_reply.go`
(thread active-task id).

### 5.1 Single execution loop

`runScenario` replaces its single `Dispatcher.Fire(s.Input)` call
with:

```
ctx := newSubstitutionContext()      // map[task-id]ReplyPayload
for _, task := range s.Input {       // declaration order
   resolved, err := substitute(task, ctx)
   if err != nil { halt scenario with precise error }   // §3.3 unresolved

   markActiveTask(dispatcher, task.ID)                  // §5.3 tagging
   reply := dispatcher.Fire(resolved)                   // existing primitive

   ctx[task.ID] = reply              // stored even on rejected/error;
                                     // see §5.2 fire-anyway
}
// run existing Expected[] assertion loop, unchanged shape
```

Identical to today's flow with an outer for-loop and a context map.

### 5.2 Fire-anyway semantics

A task whose reply is `rejected` / `error` / timeout does NOT halt
the scenario — downstream tasks fire. Only an **unresolved
substitution** halts (data-dep failure). This matches today's
behavior: assertions are the source of truth for pass/fail, not the
fires themselves. A scenario can deliberately fire a doomed sequence
and assert that the doom occurred.

### 5.3 Reply tagging

`internal/readers/nats_reply.go` adds a `Task string` field to the
injected event (mirrors the existing `OwnerSvc` field).
`internal/runtime/dispatcher.go`'s `Inject` callsite threads the
active task id from the executor's `markActiveTask` pointer. The
matcher's `task:` selector reads it. ~15 LoC change.

### 5.4 No concurrency

Tasks fire one at a time. No goroutines, no sibling fan-out, no
parallel groups. Deterministic by construction. (Concurrency is a
separate spec — see §10.)

---

## 6. Matcher contract — `task:` selector

File: `internal/runtime/matchshape.go`.

`task` joins `outbox_payload` as a shape-level directive consumed
before the subset match:

```go
// pseudocode for matchShape entry point
if taskID, ok := matchMap["task"]; ok {
    events = filterByTask(events, taskID)   // new pre-filter
    delete(matchMap, "task")                // continue with the rest
}
// existing matches_shape logic, unchanged
```

- Unscoped `match:` (no `task:`) accepts events from any task —
  backward-compatible with migrated single-task scenarios.
- `task:` inside `outbox_payload:` rejected at shape-validate as
  nested-directive category error.

---

## 7. Migration — existing scenarios

Mechanical sweep, one commit. For each scenario file:

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
```

`expected[]` entries that referred to the fire **may** add
`match.task: t1` for clarity, but it's optional — unscoped `match:`
still accepts the single task's events.

A migration script
(`tools/integration-suite-multisite/cmd/migrate_to_multi_input/main.go`,
deleted after the sweep) generates the diff per file; reviewer scans;
single commit. Re-validated via `make validate` then
`USE_INFRA=true make -C tools/integration-suite-multisite local`.

---

## 8. TDD plan

Per `CLAUDE.md`: Red → Green → Refactor → Commit per slice.

**Phase 1 — grammar + loader**
- RED: tests in `internal/scenario/` covering list-shape decode,
  legacy-shape rejection, duplicate id, unknown ref, forward ref,
  unknown `match.task`.
- GREEN: `Task` type + list-shape loader + `dag_validate.go`.
- Unit-tested; no infra.

**Phase 2 — matcher `task:` selector**
- RED: `matchshape_test.go` cases — filter-pass, filter-mismatch,
  unscoped backward-compat, `task:` inside `outbox_payload:` rejected.
- GREEN: directive handler (~10 LoC).

**Phase 3 — executor + reply tagging**
- RED: tests in `internal/runtime/` with a mock dispatcher injecting
  scripted replies. Cases: single-task pass-through, two-task chain
  with reply substitution, three-task chain, failed-task does NOT
  halt downstream, unresolved substitution halts with precise error.
- GREEN: `dag_executor.go` + `Inject` task-tag wiring +
  `runner_scenario.go` rewire.

**Phase 4 — migration sweep**
- Migration script generates the diff for every existing scenario.
- One commit: 23 files + script deletion + brief RUNBOOK note.
- `make validate` + `make local` green before commit.

**Phase 5 — integration proof**
- Author one new scenario that REQUIRES multi-input (the
  `room-create-then-join-then-send` example from §2). Runs green
  end-to-end via `USE_INFRA=true make -C tools/integration-suite-multisite local`.

After Phase 5 you can author deep scenarios.

---

## 9. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Reply substitution fails late (unresolved at runtime) | Low | Hard-halt with precise error pointing at the task and the bad `${...}`; loader catches structural cases up-front |
| Reserved `task:` collides with chat-app payload using `task` as a field | Low | Existing precedent (`outbox_payload:`); spec calls it out; matcher rejects collision at shape-validate |
| Migration sweep accidentally changes a scenario's behavior | Low | Script-generated; reviewer scans diff; `make local` re-runs the full set before commit |
| Task tagging races for cross-task events that arrive late | Medium | A late-arriving event tags to whichever task is "active" at observation time. For multi-input v1 with sequential fires + scoped assertions, this is precise enough. Note in DAG.md once docs land. |

---

## 10. Explicitly NOT in scope

| Out | Why | Future spec |
|---|---|---|
| `input_order:` block with named orderings + Airflow `>>` | Stress-coverage feature, not "can we author multi-step." Folds into multi-input cleanly later. | Future "named orderings" spec |
| `[a, b]` sibling-group parallel grammar | Same — concurrency is stress, not authoring | Future "concurrency" spec |
| `${task.events.*}` event substitution | Adds executor-side event-window settling; not needed for reply chaining | Future "event substitution" spec |
| `expected: { positive: [...], negative: [...] }` envelope | Orthogonal restructure to what assertions mean; touches every scenario; deserves its own design | Future "envelope" spec |
| Chaos engine, iterations, mishap framework | Entirely separate topic, with its own hard dependency (F-009 cache-flush) | Future "chaos engine" spec |
| Cross-scenario task references | Scenarios stay isolated, always | — |

---

## 11. Glossary

- **Task** — one verb fire, with an `id`, declared in `input:`.
  Replaces today's single `Input` struct.
- **Substitution context** — per-scenario map keyed by task id,
  carrying each completed task's reply. Feeds downstream `${...}`
  resolution.
- **`task:` selector** — reserved key inside a `match:` shape that
  filters events by attributed task id before the subset match.
- **Fire-anyway** — failed tasks do not halt downstream fires; only
  an unresolved `${...}` substitution halts the scenario.
