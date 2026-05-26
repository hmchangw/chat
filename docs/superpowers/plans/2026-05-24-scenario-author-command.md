# `/scenario-author` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a project-local `/scenario-author` slash command that helps Claude author v2 integration-suite scenarios via an outline-set workflow with a five-surface hard-stop check, plus annotate the v2 catalogs with the comment-based documentation the command relies on.

**Architecture:** No Go code is added. The command is a markdown file Claude reads at runtime (`.claude/commands/scenario-author.md`). Companion documentation lives as inline `#` comments inside the existing v2 catalog YAMLs. A 1-page `AUTHORING.md` provides the hand-edit fallback.

**Tech Stack:** Markdown (slash command + AUTHORING.md), YAML comments (catalog annotations), `make -C tools/integration-suite-v2 validate` as the structural gate.

**Spec:** `docs/superpowers/specs/2026-05-24-scenario-author-command-design.md`.

---

## Task 1: Scope folder scaffolding

The v2 scenario tree today has only `scenarios/drafts/service/`. The spec lists five scopes; add the four missing ones with `.gitkeep` so the slash command can target any of them.

**Files:**
- Create: `tools/integration-suite-v2/scenarios/drafts/journey/.gitkeep`
- Create: `tools/integration-suite-v2/scenarios/drafts/pipeline/.gitkeep`
- Create: `tools/integration-suite-v2/scenarios/drafts/federation/.gitkeep`
- Create: `tools/integration-suite-v2/scenarios/drafts/resilience/.gitkeep`

- [ ] **Step 1: Create the four folders with .gitkeep**

```bash
mkdir -p tools/integration-suite-v2/scenarios/drafts/journey \
         tools/integration-suite-v2/scenarios/drafts/pipeline \
         tools/integration-suite-v2/scenarios/drafts/federation \
         tools/integration-suite-v2/scenarios/drafts/resilience
touch    tools/integration-suite-v2/scenarios/drafts/journey/.gitkeep \
         tools/integration-suite-v2/scenarios/drafts/pipeline/.gitkeep \
         tools/integration-suite-v2/scenarios/drafts/federation/.gitkeep \
         tools/integration-suite-v2/scenarios/drafts/resilience/.gitkeep
```

- [ ] **Step 2: Verify**

Run: `ls tools/integration-suite-v2/scenarios/drafts/`
Expected: `federation  journey  pipeline  resilience  service`

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-v2/scenarios/drafts/{journey,pipeline,federation,resilience}/.gitkeep
git commit -m "feat(v2): scaffold drafts/{journey,pipeline,federation,resilience} scope folders"
```

---

## Task 2: Annotate `fixture-cast.yaml` with predicate-meaning comments

Today the cast lists three users with `tags: [verified, has_user_record]` but never explains what those tags MEAN. The slash command's placeholder-resolution step needs the author to understand each predicate's identity implications. Per spec §6, documentation lives as inline `#` comments — not new YAML fields.

**Files:**
- Modify: `tools/integration-suite-v2/catalogs/fixture-cast.yaml`

- [ ] **Step 1: Rewrite the file with comments documenting each tag and user**

```yaml
# Fixture cast — the seeded user identities a scenario can claim via placeholders.
#
# Predicate tags (used by scenarios in `placeholders[].predicate`):
#   verified         — user has completed email/account verification flow;
#                      JWT was minted by auth-service against a successful
#                      nkey + /auth handshake. All current users carry this.
#   has_user_record  — the auth flow created a `users` row in Mongo, so
#                      services that look up the actor (room-service,
#                      message-gatekeeper) will find them. Implied by
#                      `verified` today but kept explicit so future
#                      "verified-but-no-user-record" cases stay expressible.
#
# Credential strategies:
#   generate-and-auth — at seed time the seeder generates a fresh nkey
#                       keypair and POSTs the public key to auth-service
#                       /auth, recording the returned JWT + seed for the
#                       user. Dev mode only; production seeding is out of
#                       scope for the suite.

users:
  - id: alice
    tags: [verified, has_user_record]
    credentials:
      strategy: generate-and-auth
      account: alice
  - id: bob
    tags: [verified, has_user_record]
    credentials:
      strategy: generate-and-auth
      account: bob
  - id: carol
    tags: [verified, has_user_record]
    credentials:
      strategy: generate-and-auth
      account: carol

# Reserved for future placeholder types (room, message, subscription).
# Scenarios that need a pre-existing room will set type: room with a
# predicate against this list once room placeholders land in Part-2.
rooms: []
messages: []
subscriptions: []
```

- [ ] **Step 2: Verify the loader still accepts the file**

Run: `make -C tools/integration-suite-v2 validate`
Expected: exit 0, no errors. Comments are no-ops to the YAML parser.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-v2/catalogs/fixture-cast.yaml
git commit -m "docs(v2): document fixture-cast predicate tags and credential strategy"
```

---

## Task 3: Annotate reader catalogs with emit-shape comments

The slash command picks matchers against expected reads. Without a documented event shape per reader, the AI picks blindly. Annotate the three Part-1 readers with `#` comments describing what fields each emits and why an author would target this location.

**Files:**
- Modify: `tools/integration-suite-v2/catalogs/readers/reply.yaml`
- Modify: `tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml`
- Modify: `tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml`

- [ ] **Step 1: Rewrite `reply.yaml` with annotations**

```yaml
# Reader: reply
#
# The synthetic location for the immediate response of a request/reply verb.
# The dispatcher captures the reply (or transport-error class) and injects
# a single event into the observer stream — there is no service that
# "owns" this location, hence `owners: []`.
#
# Emit shape (one event per scenario, injected by the dispatcher):
#   payload      — raw reply bytes; usually JSON of the responding service's
#                  reply struct (e.g. model.Room for room.create).
#   traceparent  — the W3C header the dispatcher set before firing the verb,
#                  re-attached so classification can confirm in-cascade.
#   timestamp    — dispatcher-recorded receive time (monotonic-anchored).
#   transport_error — populated instead of `payload` on no-responders /
#                     timeout / connection-closed; matchers can target this
#                     for negative-path scenarios.
#
# Choose this location when the scenario asserts on what the responding
# service returned synchronously. Pair with `matches_shape` for typed
# bodies, `contains` for substring/error-text checks.

location: reply
owners:    []
timestamp_source: dispatcher-recorded receive time
shape:    bytes
executor: NATSReplyReader
```

- [ ] **Step 2: Rewrite `mongo.rooms.yaml` with annotations**

```yaml
# Reader: mongo.rooms
#
# Tails the `rooms` collection in the operational Mongo. Emits one event
# per insert (full document) starting from the observer start time —
# pre-existing rooms seeded by the fixture cast are NOT replayed.
#
# Emit shape (one event per inserted document):
#   document    — the full model.Room (id, name, createdBy, members,
#                 createdAt, type, …). Field names are camelCase from
#                 the model package's bson tags.
#   timestamp   — the document's createdAt field (NOT the Mongo write
#                 timestamp), so out-of-order writes still classify
#                 against scenario T_open by domain time.
#
# Choose this location when the scenario asserts on persistence side
# effects. Pair with `count_eq` for "exactly N new rooms" or
# `matches_shape` for "a room with these specific fields landed".
#
# Owners: room-service is the only writer in Part-1. If a future service
# (e.g. dm-bootstrapper) starts writing rooms, add it here so the
# classifier accepts its writes as in-cascade.

location: mongo.rooms
owners:    [room-service]
timestamp_source: createdAt
shape:    model.Room
executor: MongoRoomsReader
```

- [ ] **Step 3: Rewrite `logs.room-service.yaml` with annotations**

```yaml
# Reader: logs.room-service
#
# Streams `docker logs -f` for the room-service container, parses each
# line as slog JSON, and emits an event per log entry. Useful for
# asserting on observable behavior the service intentionally logs
# (e.g. "room created", request-ID propagation) without coupling to
# implementation internals.
#
# Emit shape (one event per JSON-parseable log line):
#   fields      — map[string]any of the parsed slog entry (time, level,
#                 msg, plus structured key/value pairs the service set).
#   timestamp   — the log entry's "time" field (RFC3339); the reader
#                 falls back to wall-clock receive time only if "time"
#                 is missing, and tags such events so the classifier
#                 can be strict if desired.
#   raw_line    — the original line, for debugging.
#
# Choose this location ONLY for behavior the service contractually
# logs (per CLAUDE.md §3 request logging & tracing). Do NOT use it to
# assert on internal state — Mongo/Cassandra/JetStream readers exist
# for that. Pair with `contains` for message text, `matches_shape` for
# typed field assertions.

location: logs.room-service
owners:    [room-service]
timestamp_source: log line's "time" field (slog JSON)
shape:    structured log entry (map[string]any)
executor: ContainerLogsReader[room-service]
```

- [ ] **Step 4: Verify the loader still accepts all three files**

Run: `make -C tools/integration-suite-v2 validate`
Expected: exit 0, no errors.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-v2/catalogs/readers/reply.yaml \
        tools/integration-suite-v2/catalogs/readers/mongo.rooms.yaml \
        tools/integration-suite-v2/catalogs/readers/logs.room-service.yaml
git commit -m "docs(v2): document reader emit shapes and selection criteria"
```

---

## Task 4: Annotate service ability profile with pattern rationale

The classifier uses each service's ability profile to decide whether an out-of-trace event is "background" or "anomaly". The slash command also uses it for hard-stop check #5: every expected effect a scenario asserts must be listed in the target service's profile. Annotate today's only profile so authors understand what each trigger declares.

**Files:**
- Modify: `tools/integration-suite-v2/catalogs/services/room-service.yaml`

- [ ] **Step 1: Rewrite with annotations**

```yaml
# Service ability profile: room-service
#
# Declares every effect this service is allowed to produce. The runtime
# uses this profile two ways:
#   1. Out-of-trace events whose owner is room-service and whose
#      location/pattern matches an entry below are classified as
#      "background" and ignored.
#   2. The /scenario-author hard-stop check #5 verifies that every
#      expected read in a scenario targeting room-service maps to an
#      `on_trigger.writes` entry below — preventing the runtime from
#      flagging a legitimate effect as anomaly.
#
# When adding a trigger:
#   - `source` is the transport carrying the inbound request
#     (nats | http | jetstream-pull | jetstream-push).
#   - `pattern` is a wildcard subject (NATS) or URL pattern (HTTP).
#   - `on_trigger.writes` lists locations the service touches as a
#     direct consequence of that trigger. Reads-only effects (e.g.
#     mongo lookups) are not listed — only writes/outputs the runtime
#     can observe.

service: room-service
triggers:
  - source: nats
    pattern: "chat.user.*.request.room.*.create"
    # Channel-room creation handler. Writes the reply, persists the
    # new room, logs the structured "room created" line.
    on_trigger:
      - writes: reply
      - writes: mongo.rooms
      - writes: logs.room-service
```

- [ ] **Step 2: Verify**

Run: `make -C tools/integration-suite-v2 validate`
Expected: exit 0, no errors.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-v2/catalogs/services/room-service.yaml
git commit -m "docs(v2): document room-service ability profile triggers and writes"
```

---

## Task 5: Write `.claude/commands/scenario-author.md` (the slash command)

This is the core deliverable. The file is the AI's instructions for the entire workflow defined in the spec: surface, audit mode, outline-set workflow, five-surface check, write phase, validation. It must reproduce the six hard rules verbatim and must be self-contained — no implicit reliance on other docs at runtime beyond ARCHITECTURE.md and the catalogs themselves.

**Files:**
- Create: `.claude/commands/scenario-author.md`

- [ ] **Step 1: Write the file**

```markdown
---
description: Author v2 integration-suite scenarios via outline-set workflow with five-surface hard-stop check
argument-hint: [free-form intent text]
---

You are authoring scenarios for the v2 integration-suite at
`tools/integration-suite-v2/`. Read this entire file before taking
any action. The companion architecture doc is
`tools/integration-suite-v2/ARCHITECTURE.md` — open it now if you
have not internalised the four-layer model.

## Hard rules (verbatim, enforced)

```
RULE 1: Expected behavior comes from a cited design doc — not from
        training data. Every scenario you write has a `source:` line
        pointing to an actual file path + section/line.

RULE 2: If the design is silent on a behavior, STOP. Do not invent
        expectations. (v2 has no blindspot register; silence kills
        the scenario.)

RULE 3: Catalog vocabulary is closed. Verbs, matchers, and readers
        must already exist in catalogs/. If they don't, STOP and
        surface the gap to the user.

RULE 4: One scenario per file. Each scenario is independently true.
        IDs are auto-generated `it-<runID>-<scenarioID>-…` at run
        time — you never write IDs by hand.

RULE 5: Scenarios always land in `drafts/`. Promotion to `approved/`
        is a separate, human-reviewed PR. Never write to `approved/`.

RULE 6: Transport is implicit. Pick a verb name; HTTP vs NATS
        follows from which verb you picked.
```

## Step 0 — Load the catalog universe

Before doing anything else, read these files and form an in-memory
summary of the closed vocabulary you can use:

- `tools/integration-suite-v2/catalogs/verbs/*.yaml` — every verb name + input shape
- `tools/integration-suite-v2/catalogs/matchers.yaml` — every matcher name + description
- `tools/integration-suite-v2/catalogs/readers/*.yaml` — every reader location + emit shape (read the comments — they tell you which matcher fits which field)
- `tools/integration-suite-v2/catalogs/services/*.yaml` — every service's ability profile (triggers + on_trigger.writes)
- `tools/integration-suite-v2/catalogs/fixture-cast.yaml` — every cast user + the meaning of each predicate tag (read the comments at the top)

Quote nothing from training data — every fact about the vocabulary
must come from a file you just read.

## Step 1 — Pick a mode based on $ARGUMENTS

- If `$ARGUMENTS` is empty: **audit mode**. Go to Step 2A.
- If `$ARGUMENTS` is non-empty free-form text: **intent mode**. The
  argument is the user's intent. Skip to Step 3.

## Step 2A — Audit mode (4-step summary)

Output the following four sections, in order, in a single message to
the user. Be terse: this is a briefing, not an essay.

1. **Diff.** Run `git diff main...HEAD --name-only` and `git diff main...HEAD --stat`. List touched files. For each touched handler file, grep for new subject strings (`chat.user.*.request.*`), new HTTP routes (`r.GET`, `r.POST`, …), and new NATS subscriptions. State the raw facts: "room-service/handler.go gained a handler for `chat.user.*.request.room.*.archive`".

2. **Behavioral impact.** Read each touched handler with intent. Translate code changes into user-facing behavior shifts: "users can now archive channel rooms they own" / "DM creation now rejects unverified senders". Anchor each bullet to a specific `file:line`. Do NOT speculate beyond what the code shows.

3. **Coverage cross-reference.** Grep `tools/integration-suite-v2/scenarios/**/*.yaml` for verbs/subjects that touch each behavior bullet from §2. State: "behavior X has 0 scenarios" or "behavior X is covered by scenarios/drafts/service/foo.yaml". Report gaps.

4. **Insight.** A short bulleted list — one suggested scenario per uncovered behavior. Each suggestion is one line: a candidate `scenario:` snake_case name plus the gist.

After the four sections, ask:
> "Want to author one of these, or something else? Reply with a number/name to pick, or describe a different scenario in your own words. Reply `cancel` to exit."

If the user picks something: that's the intent. Go to Step 3.
If the user is vague or asks something off-list: drop into brainstorming Q&A (one question at a time — verb? source? expected effects? mishaps?) until intent is clear, then go to Step 3.

## Step 3 — Produce the outline SET

A single invocation produces between 1 and N scenarios. When
brainstorming any non-trivial behavior, the natural outline set is:

- The happy path (the documented positive case).
- One or more mutually exclusive negative cases that the design also
  specifies (unauthorized actor, malformed input, missing prerequisite).
- Edge cases the design explicitly calls out (boundary values, idempotency).

Do NOT invent negatives the design doesn't mention. RULE 1 still
applies — every scenario in the set must have its own cited source.

Present the set as numbered candidates, each in this exact shape:

```yaml
# Scenario #1
scenario:     <snake_case_name>
source:       <path/to/design.md#section or :line>
scope:        service | journey | pipeline | federation | resilience
verb:         <name from catalogs/verbs/>
subject:      <full subject string with ${placeholder} substitutions, if NATS>
placeholders:
  - name: requester
    type: user
    predicate: { verified: true }
expected:
  - location: <reader name>
    matcher:  <matcher name>
    expected: <value or shape>
    why:      <one-line justification>
mishaps:
  ignore: []
```

After the set, ask:
> "React with: `lgtm` / `drop N` / `add <intent>` / `edit N.<field>` / `brainstorm [N]` / `cancel`."

Re-present the full current state of the set after EVERY operation
the user takes. The user can issue any number of operations across
any number of turns before `lgtm`.

### 3.1 Brainstorming sub-loop

If the user replies `brainstorm` or `brainstorm N`, drop into
one-question-at-a-time Q&A. Each question targets ONE field
(verb? source? matcher choice? placeholder predicate?). Wait for the
answer. Then ask the next question. When you believe the field set
is stable, re-present the outline (or the set, if `brainstorm`
without N).

## Step 4 — Five-surface hard-stop check (run per scenario, fail collectively)

When the user says `lgtm`, run these five checks for EVERY scenario
in the set. Collect all failures, then either proceed or stop —
partial writes are forbidden.

| # | Surface           | Check |
|---|-------------------|-------|
| 1 | Verb              | Outline's `verb` matches a file in `catalogs/verbs/` AND the `executor:` named in that file exists in `tools/integration-suite-v2/internal/verbs/registry.go` |
| 2 | Matcher           | Every `matcher:` in `expected[]` is listed in `catalogs/matchers.yaml` AND registered in `internal/matchers/registry.go` |
| 3 | Reader            | Every `location:` in `expected[]` matches a file in `catalogs/readers/*.yaml` AND that file's `executor:` is registered in `internal/readers/registry.go` |
| 4 | Fixture cast      | Every placeholder's `predicate` selects ≥1 user in `catalogs/fixture-cast.yaml` (every key in the predicate object appears in that user's `tags`) |
| 5 | Service ability   | For each `expected` entry, the OWNER of that reader location (per `catalogs/readers/<loc>.yaml`'s `owners:` list) must declare the location in its `catalogs/services/<owner>.yaml` `on_trigger.writes:` under a trigger whose `pattern` matches the scenario's `subject` |

If any check fails, output a per-scenario gap report:

```
Scenario #2 (alice_archives_unowned_room) — 2 gaps:
  [4] placeholder `requester` predicate {verified:true, admin:true} matches 0 cast users (no user has `admin` tag)
  [5] expected write `mongo.rooms` is not declared in room-service ability profile for pattern `chat.user.*.request.room.*.archive`
```

After listing all gaps across all scenarios, stop. Tell the user:
> "Hard stop. Resolve the gaps above (extend the surface, or drop the affected scenarios with `drop N`), then re-issue `lgtm`."

Do NOT write any file when a check fails.

## Step 5 — Write the YAML files

If all checks pass, for each scenario in the set:

1. Pick the destination: `tools/integration-suite-v2/scenarios/drafts/<scope>/<scenario>.yaml`. If a file with that path already exists, STOP and ask the user to rename.
2. Render the scenario into the suite's canonical v2 YAML shape (see `tools/integration-suite-v2/scenarios/drafts/service/verified-user-creates-channel-room.yaml` for the exact field names and order). The outline-level `verb`, `subject`, `placeholders`, `payload` go under `input:`; `expected` goes under `sequence:` with the right `service:` derived from the reader's owner; `mishaps` stays at top level.
3. Write the file via the Write tool.

When all files are written, proceed to Step 6.

## Step 6 — Validate

Run, in order:
1. `make -C tools/integration-suite-v2 validate` — must exit 0.
2. For each newly written file, ensure it parses by running the validator's scenario sub-check (the validator already loads every scenario in `scenarios/drafts/` and reports per-file errors).

If validation fails, surface the failure to the user and DO NOT delete the file — leave it for inspection. This is a bug in your YAML rendering; fix it and re-validate.

## Step 7 — Final report

Once validation passes, output:

```
Wrote N scenario(s) to:
  - tools/integration-suite-v2/scenarios/drafts/service/foo.yaml
  - tools/integration-suite-v2/scenarios/drafts/service/foo_unauthorized.yaml

These are drafts. Promotion to scenarios/approved/ is a separate
human-reviewed PR — do not move them yourself.

Run them with:
  make -C tools/integration-suite-v2 local
```

Stop. Do not commit unless the user explicitly asks.

## Brainstorming style reference

When in the brainstorming sub-loop, prefer multiple-choice questions
when the answer space is bounded:
- "Which verb fits? (a) nats_request — sync, (b) <other registered verbs>"
- "Which placeholder predicate? (a) verified:true, (b) verified:false, (c) admin:true (would fail check #4 today)"

Use open-ended questions only when the answer is genuinely free-form
(e.g. naming the scenario, or finding the right source citation).
```

- [ ] **Step 2: Verify the file is readable and the description renders in help**

Run: `ls -la .claude/commands/scenario-author.md && head -5 .claude/commands/scenario-author.md`
Expected: file exists, frontmatter has `description:` and `argument-hint:`.

- [ ] **Step 3: Spot-check against acceptance criteria from spec §8**

Read the file you just wrote. Confirm by inspection:
- AC1 path mentioned (Step 5 writes to drafts/service/)
- AC2 no-changes case handled (Step 2A.4 produces empty insight → user picks "something else")
- AC3 audit mode covers behavioral impact (Step 2A.2)
- AC4 unregistered verb hard-stop (Step 4 check #1)
- AC5 zero-cast-match hard-stop (Step 4 check #4)
- AC6 outline set with positive + negative + lgtm flow (Step 3)
- AC7 drop/add/edit operations (Step 3 reaction list)

- [ ] **Step 4: Commit**

```bash
git add .claude/commands/scenario-author.md
git commit -m "feat: /scenario-author slash command for v2 scenario authoring"
```

---

## Task 6: Write `tools/integration-suite-v2/AUTHORING.md` (hand-edit fallback)

A one-page reference for humans who edit scenarios directly without invoking the slash command. Restates the six hard rules and points to the slash command for the full workflow.

**Files:**
- Create: `tools/integration-suite-v2/AUTHORING.md`

- [ ] **Step 1: Write the file**

```markdown
# Authoring v2 scenarios

This is the **hand-edit fallback** for v2 integration-suite scenarios.
The full workflow — outline-set discussion, five-surface validation,
draft placement — lives in the `/scenario-author` slash command at
`.claude/commands/scenario-author.md`. Use the command unless you are
making a small, targeted fix to an existing scenario.

## Six hard rules

1. **Expected behavior comes from a cited design doc — not from
   training data.** Every scenario has a `source:` line pointing to
   an actual file path + section/line.

2. **If the design is silent, STOP.** Do not invent expectations. v2
   has no blindspot register; silence kills the scenario.

3. **Catalog vocabulary is closed.** Verbs, matchers, and readers
   must already exist under `catalogs/`. If you need one that
   doesn't exist, extending the catalog is separate work — not part
   of scenario authoring.

4. **One scenario per file.** Each scenario is independently true.
   IDs are auto-generated `it-<runID>-<scenarioID>-…` at run time.
   Never write IDs by hand.

5. **Scenarios always land in `drafts/`.** Promotion to `approved/`
   is a separate, human-reviewed PR.

6. **Transport is implicit.** The verb you pick determines HTTP vs
   NATS. You never declare transport at the scenario level.

## Where things live

| Asset                          | Path                                                  |
|--------------------------------|-------------------------------------------------------|
| Scenario drafts                | `scenarios/drafts/<scope>/<name>.yaml`                |
| Approved scenarios (CI gates)  | `scenarios/approved/` (do not write directly)         |
| Verbs                          | `catalogs/verbs/*.yaml` (see comments for input shape) |
| Matchers                       | `catalogs/matchers.yaml`                              |
| Readers                        | `catalogs/readers/*.yaml` (see comments for emit shape) |
| Service ability profiles       | `catalogs/services/*.yaml` (see comments for what's "background") |
| Fixture cast                   | `catalogs/fixture-cast.yaml` (see comments for predicate meanings) |

## Validating a hand-edit

```bash
make -C tools/integration-suite-v2 validate
```

This runs the catalog validator and the scenario loader against
every YAML in the tree. It does NOT run scenarios — for that, see
`make -C tools/integration-suite-v2 local`.

## When to escalate to `/scenario-author`

- You are adding a NEW scenario from scratch.
- You are unsure which verb/matcher/reader to pick.
- The behavior you want to test isn't obviously documented in a
  design doc — the command will walk you through finding (or not
  finding) a source citation.
- You want to author multiple related scenarios (positive + negative
  pair) in one pass.

For trivial fixes (typo in a matcher's expected value, swapping a
placeholder predicate from `verified` to a hypothetical
`has_admin_role`, renaming the scenario) hand-editing is fine.
Run `make validate` afterwards.

## Architecture

`tools/integration-suite-v2/ARCHITECTURE.md` explains the four-layer
model (scenarios → catalogs → runtime → chaos) and the event-driven
classification pipeline. Read it once before authoring your first
scenario.
```

- [ ] **Step 2: Verify**

Run: `wc -l tools/integration-suite-v2/AUTHORING.md`
Expected: ~70 lines (one-page reference).

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-v2/AUTHORING.md
git commit -m "docs(v2): AUTHORING.md hand-edit fallback for v2 scenarios"
```

---

## Task 7: Integration verification

Final pass: confirm the toolchain is still green and the docs cross-link cleanly.

**Files:** (none modified)

- [ ] **Step 1: Run the validator**

Run: `make -C tools/integration-suite-v2 validate`
Expected: exit 0, no errors.

- [ ] **Step 2: Run the v2 Go tests**

Run: `go test ./tools/integration-suite-v2/...`
Expected: all packages PASS. Comments are no-ops; nothing should have changed.

- [ ] **Step 3: Confirm cross-links resolve**

Read the slash command at `.claude/commands/scenario-author.md`. For every file path it references, confirm the file exists:
- `tools/integration-suite-v2/ARCHITECTURE.md`
- `tools/integration-suite-v2/catalogs/verbs/*.yaml`
- `tools/integration-suite-v2/catalogs/matchers.yaml`
- `tools/integration-suite-v2/catalogs/readers/*.yaml`
- `tools/integration-suite-v2/catalogs/services/*.yaml`
- `tools/integration-suite-v2/catalogs/fixture-cast.yaml`
- `tools/integration-suite-v2/scenarios/drafts/service/verified-user-creates-channel-room.yaml`
- `tools/integration-suite-v2/internal/verbs/registry.go`
- `tools/integration-suite-v2/internal/matchers/registry.go`
- `tools/integration-suite-v2/internal/readers/registry.go`

Run: `ls <each-path>` for any path you're unsure about. Fix the slash command if a path is wrong.

- [ ] **Step 4: Confirm AUTHORING.md ↔ slash command consistency**

Read both files side by side. The six hard rules must be byte-identical between the two. If they drift, fix AUTHORING.md to match the slash command (the command is the source of truth).

- [ ] **Step 5: Push**

```bash
git push -u origin claude/integration-test-automation-LXQHP
```

Retry up to 4 times with exponential backoff (2s, 4s, 8s, 16s) on network errors.

---

## Self-review (run after writing the plan, before handing it off)

- **Spec coverage:**
  - §1 Purpose / §2 Non-goals → captured in Task 5 (frontmatter + Step 0).
  - §3 Surface (audit / intent / brainstorm) → Task 5 Steps 1, 2A, 3.1.
  - §4 Workflow → Task 5 Steps 0–7.
  - §4.3 Five-surface check → Task 5 Step 4 (verbatim table).
  - §5 Hard rules → Task 5 hard-rules block + Task 6 §"Six hard rules" (identical).
  - §6 Companion documentation → Tasks 2, 3, 4 (one task per catalog family).
  - §7 Files this design creates → Tasks 1–6, one task per file family.
  - §8 Acceptance criteria → Task 5 Step 3 spot-check, Task 7 verification.
  - §9 Risks → embedded in Tasks 5 Step 4 (hard-stop verbosity) and Task 7 Step 4 (rule drift).
  - §10 Out-of-scope → not implemented, by design.
- **Placeholder scan:** no TBD/TODO/"add appropriate" patterns; every step has the actual content.
- **Type consistency:** the catalog field names used in Tasks 2–4 match the existing YAMLs read at the top of this session. The slash command's check #5 uses the same `on_trigger.writes` shape that Task 4 documents. The frontmatter `description` + `argument-hint` keys match the pattern from `.claude/commands/branch_review.md`.

Plan is internally consistent and spec-covering.
