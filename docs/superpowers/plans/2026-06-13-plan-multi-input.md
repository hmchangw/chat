# Plan: Integration suite multi-site — multi-input scenarios

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:executing-plans` (review-checkpoint style) or
> `superpowers:subagent-driven-development` (in-session, no separate
> review) to implement this plan task-by-task. Every Task ends with
> Red → Green → Refactor → Commit.

**Spec:** `docs/superpowers/specs/2026-06-13-integration-suite-multi-input.md`
(canonical contract; this plan is the executable form).

**Goal.** Add multi-fire scenarios to
`tools/integration-suite-multisite/`. After this plan ships, a single
scenario can fire multiple verbs in declaration order, downstream
tasks read upstream replies via `${task.reply.body_json.*}` /
`.status`, and reply assertions scope per task via `match.task: <id>`.
Background pollers continue to disambiguate by content.

**Scope discipline.** Implement exactly what the spec describes —
nothing else. Specifically OUT (with future-spec homes per spec §10):
named orderings / `>>` notation, sibling concurrency, event
substitution, envelope, chaos, F-009. If a task wants to "while we're
here…" — stop.

**Hard constraints.**
- TDD, every task. Tests RED before implementation; never the inverse.
- Existing 23+ scenarios stay green at every commit (dual-shape
  `UnmarshalYAML` makes this free — verify per phase).
- No code in `chat/` outside `tools/integration-suite-multisite/`.
  This is suite-team work.
- Lint/test pre-commit hook stays passing.

---

## File structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Modify | `internal/scenario/types.go` | `Input` → `TaskList []Task`; `Task` struct; `TaskList.UnmarshalYAML` (map-or-list) |
| Create | `internal/scenario/multi_input_validate.go` | Per-scenario validation: id uniqueness, ref-before-use of `${t.reply.*}`, `match.task:` reply-only + known id |
| Create | `internal/scenario/multi_input_validate_test.go` | Unit tests for every validator error path |
| Modify | `internal/scenario/types_test.go` (or `loader_test.go` — find existing home) | Tests for `TaskList.UnmarshalYAML` (map shape, list shape, malformed) |
| Modify | `internal/readers/types.go` | Add `Task string` to `Event` |
| Modify | `internal/readers/nats_reply.go` | `Inject` takes task id; stamps `Event.Task` |
| Modify | `internal/readers/nats_reply_test.go` | Reply emits `Task` field |
| Modify | `internal/runtime/matchshape.go` | `task:` directive: extract-and-filter pre-pass on reply events |
| Modify | `internal/runtime/matchshape_test.go` | Filter-pass, filter-mismatch, unscoped reply, `task:` inside `outbox_payload:` rejected |
| Create | `internal/runtime/task_executor.go` | Sequential per-task loop with substitution context + fire-anyway |
| Create | `internal/runtime/task_executor_test.go` | Mock-dispatcher unit tests |
| Modify | `internal/runtime/runner_scenario.go` | Replace single `Dispatcher.Fire` with `taskExecutor.Run` |
| Modify | `internal/runtime/substitute.go` (or wherever `${...}` resolves) | Resolve `${<task>.reply.body_json.<field>}` + `.status` against context |
| Modify | `internal/runtime/substitute_test.go` | Substitution context tests |
| Modify | `internal/runtime/dispatcher.go` | `Fire` signature gets `taskID string`; thread into reply Inject |
| Modify | `internal/runtime/dispatcher_test.go` | Reply tagged with task id |
| Create | `scenarios/drafts/room-create-then-join-then-send.yaml` | Phase 4 integration proof |
| Modify | `SCENARIO-REFERENCE.md` | New `input:` list shape + `task:` selector + reply substitution (1 short section per topic) |
| Modify | `AUTHORING.md` | One-line note: reply-substitution chains room-lifecycle flows; message chains use known ids + seeded parents |

Exact filenames in `internal/runtime/` (substitute, dispatcher tests) — verify in Task 0 before assuming.

---

## Task 0 — Reconnaissance (no code; informs Task numbering only)

Don't skip this. The plan assumes file locations that need verifying
against the actual tree before edits.

- [ ] **Step 1.** Confirm the homes of: existing `Input` consumer
      sites, `Dispatcher.Fire` callers, `Substitute` /
      `resolvePlaceholders` (the `${...}` function — exact name TBD),
      and the matchshape directive-handling code (where
      `outbox_payload:` is consumed).
- [ ] **Step 2.** Run `make -C tools/integration-suite-multisite validate`
      and capture the baseline output — every phase must reproduce
      this clean state.
- [ ] **Step 3.** Confirm `SeedMembership.UnmarshalYAML` at
      `internal/scenario/types.go:136` is the precedent referenced
      in the spec (#3). The new `TaskList.UnmarshalYAML` is a direct
      analog.

**No commit.** Output the findings as a short note in the executor's
working scratch; refine the file-structure table above if any path
was wrong.

---

## Task 1 — Grammar: `Task`, `TaskList`, dual-shape unmarshal

**Outcome:** Existing scenarios decode unchanged. New list-shape
scenarios decode into `[]Task`. Malformed inputs reject precisely.

### Step 1 (RED) — write `internal/scenario/types_test.go` cases (or wherever the existing loader/types tests live; add a new file if necessary):

- [ ] `TestTaskList_UnmarshalYAML_LegacyMap` — given today's `input: { site, verb, subject, payload, credential }` shape, decodes to `[]Task` of length 1 with empty `ID`.
- [ ] `TestTaskList_UnmarshalYAML_List` — given `input: [{id: t1, …}, {id: t2, …}]`, decodes to `[]Task` of length 2 with both ids.
- [ ] `TestTaskList_UnmarshalYAML_ListElementMissingID` — given a list element with no `id:`, errors with a message that says "id required" and names the bad index. (Validation here OR in Task 2 — pick one. Recommend Task 2; this test then asserts decode succeeds, Task 2's validator rejects.)
- [ ] `TestTaskList_UnmarshalYAML_NeitherMapNorList` — given a scalar `input: foo`, errors with "input must be a single fire (map) or a list of tasks".
- [ ] `TestTaskList_UnmarshalYAML_EmptyList` — given `input: []`, errors with "input must have at least one task".

Run; expect all to fail (no `Task` type yet).

### Step 2 (GREEN) — `internal/scenario/types.go`:

- [ ] Add `Task` struct with the spec §3.1 fields and yaml tags.
- [ ] Add `TaskList []Task`.
- [ ] Implement `TaskList.UnmarshalYAML` as a direct analog of
      `SeedMembership.UnmarshalYAML` (types.go:136) — switch on
      `node.Kind`: `MappingNode` ⇒ one-task slice with empty id;
      `SequenceNode` ⇒ decode each element; default ⇒ kind error;
      empty sequence ⇒ explicit error.
- [ ] Change `Scenario.Input` from `Input` to `TaskList`. **Delete
      the old `Input` struct only if nothing else references it
      after the dust settles** (likely several call sites still
      reference `Input` fields — leave the struct, just stop using
      it on `Scenario`; rename to `legacyInput` if shadowing is
      annoying).
- [ ] Run the Step 1 tests — green.

### Step 3 (verify backward-compat) — run the full validate sweep:

- [ ] `make -C tools/integration-suite-multisite validate` — must be
      identical to the Task 0 baseline. (Compile errors at this
      point are likely from consumers of `Scenario.Input.Site`,
      `.Verb`, etc. — that's normal; minimally adapt the runner
      callsite to read `s.Input[0]` as a TEMPORARY shim. Mark with
      `// TODO(task3-rewire): replace with task executor` comment so
      Task 3 cleans it up.)

### Step 4 (commit):

- [ ] `git commit` — message: `scenario(multi-input): Task + TaskList with dual-shape unmarshal`. Note in body: "no behavior change — existing scenarios decode identically; new list shape parses but isn't validated/executed yet (Tasks 2-3)."

---

## Task 2 — Loader validation: `multi_input_validate.go`

**Outcome:** Malformed multi-input scenarios reject at load time with
precise errors; existing scenarios continue to load clean.

### Step 1 (RED) — `internal/scenario/multi_input_validate_test.go`:

For each spec §4 error path, one table-driven test case:

- [ ] `TestMultiInputValidate_DuplicateTaskID` — two tasks with `id: t1`, error names both indices.
- [ ] `TestMultiInputValidate_ListShape_MissingID` — list element with empty id (decoded by Task 1 with empty id), error names the index.
- [ ] `TestMultiInputValidate_LegacyShape_NoIDRequired` — single-fire decoded form (one task, empty id), validator passes silently.
- [ ] `TestMultiInputValidate_UnknownReplyRef` — task t2 references `${t99.reply.body_json.foo}`, error names t2 and t99.
- [ ] `TestMultiInputValidate_ForwardReplyRef` — task t1 references `${t2.reply.body_json.foo}`, error: "fires after %q in declaration order".
- [ ] `TestMultiInputValidate_ReplyRefInPayload` — reference inside a nested payload map resolves and validates correctly (no false negative).
- [ ] `TestMultiInputValidate_ReplyRefInCredential` — reference inside `credential:` resolves.
- [ ] `TestMultiInputValidate_MatchTaskOnPoller_Rejected` — `expected: [{location: mongo_find, match: {task: t1, …}}]`, error: "match.task is only valid on location: reply (got %q)".
- [ ] `TestMultiInputValidate_MatchTaskUnknownID` — `match: {task: t99, …}` on a reply, error names t99 and lists declared ids.
- [ ] `TestMultiInputValidate_MatchTaskOnReply_KnownID` — passes silently.
- [ ] `TestMultiInputValidate_UnscopedReplyMatch` — reply assertion with no `task:` key passes silently (backward-compat).

Run; all fail.

### Step 2 (GREEN) — `internal/scenario/multi_input_validate.go`:

- [ ] Walker for `${<id>.reply.<path>}` over `Task.Subject`, recursive
      walk of `Task.Payload` (map and slice nodes), `Task.Credential`.
      Returns `(referencedID, fullToken)` pairs per task.
- [ ] Per-scenario validation function — call from existing loader
      pipeline after `rejectDeprecatedTokens`. Implements §4 rules.
- [ ] Wire into `loader.go` (or `Scenario.Validate`, whichever the
      existing convention is — Task 0 told you).

Run Step 1 tests — green. Run the full validate sweep — every existing
scenario still passes (single-fire shape needs no id, no refs, no
`match.task:`).

### Step 3 (commit):

- [ ] `git commit` — `scenario(multi-input): validate id uniqueness, ref-before-use, match.task scoping`. Body: list error paths covered.

---

## Task 3 — Executor: substitution context + task loop + reply tagging

**Outcome:** Multi-task scenarios actually fire in order; replies feed
substitution; `match.task:` works end-to-end against reply events.

### Step 1 (RED) — three test files:

**A. `internal/runtime/substitute_test.go`** (or wherever the
substitution function lives):

- [ ] `TestResolve_TaskReplyBodyJSON` — context has t1's reply
      `{"roomId": "r1"}`; resolves `${t1.reply.body_json.roomId}` to `"r1"`.
- [ ] `TestResolve_TaskReplyStatus` — resolves `${t1.reply.status}` to
      `"accepted"`.
- [ ] `TestResolve_TaskReplyUnknownField` — `${t1.reply.body_json.nope}`,
      error names t1 and the path.
- [ ] `TestResolve_TaskReplyUnknownTask` — `${t99.reply.body_json.x}` with
      t99 absent from context, error names t99.
- [ ] `TestResolve_NestedInPayload` — `${...}` deep inside a payload map
      resolves correctly.

**B. `internal/readers/nats_reply_test.go`** (extend existing):

- [ ] `TestNATSReplyReader_Inject_StampsTaskID` — `Inject(outcome, …, taskID="t1")` produces an `Event` with `Task: "t1"`.
- [ ] Existing `TestNATSReplyReader_InjectEmitsEvent` continues to pass when called with an empty taskID (legacy / unscoped path).

**C. `internal/runtime/task_executor_test.go`** (new):

- [ ] `TestTaskExecutor_SinglePassThrough` — one task; dispatcher's `Fire` called once with the resolved input; reply stored in context.
- [ ] `TestTaskExecutor_TwoTaskReplyChain` — t1 returns `{roomId: "r1"}`; t2's payload references `${t1.reply.body_json.roomId}`; dispatcher receives `"r1"` in t2's resolved payload.
- [ ] `TestTaskExecutor_ThreeTaskChain` — sanity that the loop generalises.
- [ ] `TestTaskExecutor_FireAnyway_RejectedReplyDoesNotHalt` — t1 returns `{status: "rejected"}`; t2 still fires with its (non-substituted) payload.
- [ ] `TestTaskExecutor_FireAnyway_SubstitutingFromRejectedHalts` — t1 returns `{status: "rejected"}` with no `body_json.roomId`; t2 references it; halts with an error naming **both** t2 and `${t1.reply.body_json.roomId}`.
- [ ] `TestTaskExecutor_UnresolvedSubstitutionHalts` — t1 absent in context (impossible past validation, but executor must defend), halts with precise error.
- [ ] `TestTaskExecutor_TaskTagThreaded` — verify `dispatcher.Fire` receives the task id, and the captured reply event carries `Task: <id>`.

Run all three files; expect failures everywhere.

### Step 2 (GREEN) — implementations:

- [ ] **Substitution.** Extend the existing resolver to recognise the
      `${<id>.reply.body_json.<path>}` and `${<id>.reply.status}`
      forms. Both look up `(context[id]).Reply.<accessor>`. Failure
      modes per the RED tests. Other `${...}` forms (alias, $auto, …)
      stay intact.
- [ ] **Reply tagging.** Add `Task string` to `readers.Event`
      (`internal/readers/types.go`). Extend
      `NATSReplyReader.Inject` to take `taskID string` and stamp
      `Event.Task`. Update existing callers to pass `""` until
      Task 3 rewires the dispatcher.
- [ ] **Dispatcher.** `Dispatcher.Fire` gains a `taskID string`
      param; thread it into the reply reader's `Inject` call.
      Existing single-fire callers pass `""` (backward-compat; the
      runner rewires next).
- [ ] **Task executor.** New `internal/runtime/task_executor.go`:

```go
// Skeleton — adapt to existing struct conventions in this dir.
type ReplyPayload struct {
    Status   string         // "accepted" | "rejected" | …
    BodyJSON map[string]any // decoded reply body (or nil)
}

type SubstitutionContext map[string]ReplyPayload

func RunTasks(ctx context.Context, tasks []Task, disp *Dispatcher, resolve Resolver) error {
    sub := SubstitutionContext{}
    for _, t := range tasks {
        resolved, err := resolveTask(t, sub, resolve)
        if err != nil {
            return fmt.Errorf("task %q: %w", t.ID, err)   // halt on unresolved
        }
        reply := disp.Fire(ctx, resolved, t.ID)            // task id flows
        sub[t.ID] = toReplyPayload(reply)                  // store even rejected/error
    }
    return nil
}
```

- [ ] **Runner rewire.** `internal/runtime/runner_scenario.go`:
      replace the single `Dispatcher.Fire(s.Input)` (the Task 1
      `s.Input[0]` shim) with `RunTasks(ctx, s.Input, dispatcher,
      resolver)`. Delete the shim comment.

Run all three RED suites — green. Run the full validate sweep — must
remain clean.

### Step 3 (refactor pass):

- [ ] If the `Task` field on `readers.Event` is awkwardly shaped
      (e.g. you find yourself stamping `""` in many places), consider
      a small constructor `readers.NewReplyEvent(outcome, taskID)` —
      but do not add it speculatively. If there are 2-3 call sites
      and all read clearly, leave it alone.
- [ ] Confirm no `TODO(task3-rewire)` comments remain.

### Step 4 (commit):

- [ ] `git commit` — `scenario(multi-input): task executor + reply tagging + reply substitution`. Body: summarises the wiring (substitution context, fire-anyway, reply-only tagging) and confirms backward-compat.

---

## Task 4 — Matcher: `task:` selector

**Outcome:** A reply assertion's `match: {task: <id>, …}` filters to
that task's reply event before the existing subset match runs.

### Step 1 (RED) — extend `internal/runtime/matchshape_test.go`:

- [ ] `TestMatchShape_TaskSelector_FilterPass` — events include reply from t1 and t2; `match: {task: t1, body_json: {status: accepted}}` matches t1's reply only.
- [ ] `TestMatchShape_TaskSelector_FilterMismatch` — events include only t2's reply; `match: {task: t1, …}` does NOT match.
- [ ] `TestMatchShape_UnscopedReply_StillMatches` — reply assertion without `task:` matches any reply (back-compat).
- [ ] `TestMatchShape_TaskInsideOutboxPayload_Rejected` — `match: {outbox_payload: {task: t1, …}}` rejected with category error.

Run; expect failures.

### Step 2 (GREEN) — `internal/runtime/matchshape.go`:

- [ ] Extract `task` from the top-level `match:` map before the
      existing subset-match runs. If present, filter the candidate
      slice by `Event.Task == taskID`. Then delete the key and
      proceed.
- [ ] Inside `outbox_payload:` handler, if `task` appears nested,
      reject with a precise error.

Run Step 1 tests — green.

### Step 3 (commit):

- [ ] `git commit` — `scenario(multi-input): match.task selector on reply assertions`. Body: directive precedent (`outbox_payload`), reply-only mechanism.

---

## Task 5 — Integration proof

**Outcome:** A new multi-input scenario runs green end-to-end against
real infra.

### Step 1 — author `scenarios/drafts/room-create-then-join-then-send.yaml`:

- [ ] Copy the worked example from spec §2 verbatim. Adapt minor
      fields (`source:`, `tag:`, `sites.<site>.seed`) to match the
      project's actual seed grammar (Task 0 reconnaissance has these).
- [ ] Ensure assertions:
      - Reply asserts for `create`, `join`, `send` use `match.task:`
        scoping.
      - `mongo_find` on subscriptions uses content match (no `task:`).
      - `cassandra_select` on messages uses content match (no `task:`).

### Step 2 — validate offline:

- [ ] `make -C tools/integration-suite-multisite validate` — passes.

### Step 3 — run against real infra:

- [ ] `USE_INFRA=true make -C tools/integration-suite-multisite local`
      runs green.
- [ ] If any assertion times out (event-window timing on the new
      multi-fire path is the most likely culprit), DO NOT lengthen
      timeouts as a first move — investigate whether the executor's
      sequential fire is racing the poller setup. Spec §5.4: tasks
      fire one at a time; pollers were registered at Setup. The
      most likely bug is a missed substitution path or a
      poller-stamp-time misalignment, not timing.

### Step 4 (commit):

- [ ] `git commit` — `scenario(multi-input): integration proof — room-create-then-join-then-send`. Body: confirms end-to-end on USE_INFRA.

---

## Task 6 — Docs

**Outcome:** Authors can pick up the new shape from the existing docs
without re-reading the spec.

### Step 1 — `SCENARIO-REFERENCE.md`:

- [ ] Replace the `input:` section with both shapes documented as
      equivalent (the single-fire map AND the list of tasks). One
      paragraph each.
- [ ] Add one sub-section: "Reply substitution
      (`${task.reply.body_json.*}`, `${task.reply.status}`)" — point
      out it's reply-only, and the field-path lookup is dot-walk on
      decoded JSON.
- [ ] Add one sub-section: "`match.task: <id>` (reply assertions
      only)" — note the rationale (reply tagged synchronously;
      pollers disambiguate by content).

### Step 2 — `AUTHORING.md`:

- [ ] One-line note in the patterns section: **reply substitution
      chains room-lifecycle flows (create → join → send); message
      chains chain via known ids + seeded parents** (because
      `jetstream_publish` is fire-and-forget — the canonical event
      isn't a reply). Verbatim from the review's "honest scoping
      nuance."

### Step 3 (commit):

- [ ] `git commit` — `docs(multi-input): document task list shape, reply substitution, match.task selector`.

---

## Done criteria

After Task 6 is committed and pushed:

1. `git log --oneline` shows six clean Task-aligned commits on
   `claude/integration-test-automation-LXQHP`.
2. `make -C tools/integration-suite-multisite validate` is green on a
   clean clone.
3. `USE_INFRA=true make -C tools/integration-suite-multisite local`
   runs the full suite (existing 23+ scenarios + the new
   `room-create-then-join-then-send`) end-to-end green.
4. The spec's §3 grammar reference is wholly implemented; §10
   out-of-scope items are NOT in the code.
5. No `TODO(task*-…)` comments remain.

A subsequent-thread-reply scenario, a dedup scenario, and a tcount-CAS
scenario can be authored from the spec without further code changes —
that is the deep-testing unlock this plan delivers.

---

## Reviewer hot-spots (for `superpowers:requesting-code-review`)

If using a review checkpoint between phases:

- **After Task 1:** confirm dual-shape behavior is identical to
  `SeedMembership.UnmarshalYAML` and that the `Scenario.Input.X`
  shim in `runner_scenario.go` is marked TODO + temporary.
- **After Task 3:** confirm fire-anyway semantics — specifically that
  storing a rejected reply in the context and substituting from a
  missing field both produce the exact errors named in the spec.
- **After Task 4:** confirm `task:` extraction sits ahead of every
  other directive (especially `outbox_payload:`) so nesting can't
  bypass the rejection.
- **After Task 5:** confirm no Eventually timeout was extended past
  its existing default to "make the proof pass"; if it was, dig.

---

## Out-of-band (do not do in this plan)

- Author a subsequent-reply / dedup / tcount scenario. Those are
  follow-up authoring work, not implementation. They prove the
  unlock; they are not the unlock.
- Touch `chat/` source. F-009 is chat-app work, separately filed.
- Add `input_order:`, `${task.events.*}`, envelope, chaos. Each has
  its own future spec (spec §10).
