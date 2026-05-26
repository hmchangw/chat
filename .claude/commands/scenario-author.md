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
        training data. Every scenario has a `source:` line.

RULE 2: If the design is silent, STOP. Do not invent expectations.
        (No more blindspot register in v2.)

RULE 3: Catalog vocabulary is closed. Verbs, matchers, and readers
        must already exist. If they don't, STOP and surface the gap.

RULE 4: One scenario per file. Each scenario is independently true.
        IDs are auto-generated `it-<runID>-<scenarioID>-…` at run time.

RULE 5: Scenarios always land in `drafts/`. Promotion to `approved/`
        is a separate, human-reviewed PR.

RULE 6: Transport is implicit. The author picks a verb name; HTTP vs
        NATS follows from which verb they picked.
```

## Authoring stance — how you and the human split the work

The conversation has a natural division of labor. Respect it without
enforcing it.

- **You know the code.** Source citations come from files you can
  read — `file:line` refs in `source:` and inline `#` comments.
- **The human knows the architecture.** Architecture often lives in
  the human's head or in documents outside this repo. When you can't
  find a source for a field, **ASK** rather than guess ("I don't see
  `roomType` enforced in `CreateRoomReply` — what does architecture
  specify for channel vs DM?"). Empty-source guesses are how
  scenarios drift away from architectural truth.
- **Human assertion outranks your code reading.** If the human says
  "architecture says X" and the code appears to do Y, the scenario
  follows X. Capture it and add a short `# arch:` comment noting the
  divergence so the reviewer can later decide whether the code or the
  scenario is the bug.
- **Tone is collaborative, not interrogative.** One question at a
  time during brainstorm; no rigid checklist. The five-surface check
  (Step 4) is the only hard gate — everything before it is
  conversation.

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

First run `git diff main...HEAD --name-only`. If the output is empty
(no changes on this branch), short-circuit: tell the user

> "No changes detected on this branch. Provide intent directly — e.g. `/scenario-author <your intent>` — or `cancel`."

and stop. Do not produce the four-section summary on an empty diff.

Otherwise, output the following four sections, in order, in a single
message to the user. Be terse: this is a briefing, not an essay.

1. **Diff.** Reuse the `git diff main...HEAD --name-only` output and run `git diff main...HEAD --stat`. List touched files. For each touched handler file, grep for new subject strings (`chat.user.*.request.*`), new HTTP routes (`r.GET`, `r.POST`, …), and new NATS subscriptions. State the raw facts: "room-service/handler.go gained a handler for `chat.user.*.request.room.*.archive`".

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

### 3.1 Outline shape — annotated canonical YAML

The outline you present is the **exact YAML shape of `scenario.Scenario`**
that will be written to disk, with `#` comments carrying intent and
source rationale. Comments are stripped at write time (Step 5);
the on-disk `source:` field is the citation. There is no flat outline
form that gets translated into the final shape — what the user
reviews IS the file.

Present each candidate in this exact shape:

```yaml
# Scenario #1 — <one-line gist of the case>
# Source: <file:line> (<what it shows>), <file:line> (<what it shows>)

scenario: <snake_case_name>
source:   <file:line or doc#section — short, machine-readable>

input:
  verb:    <name from catalogs/verbs/>
  subject: <full subject string with ${placeholder} substitutions, if NATS>
  payload:
    <key>: <value or $auto>                        # short # note when non-obvious
  credential: ${<placeholder>.credential}          # omit if verb doesn't need one
  placeholders:
    <name>:
      type: user
      predicate: { <tag>: <value> }                # tag from fixture-cast.yaml

sequence:
  - service: <service name>
    reads:
      - location: <reader name>
        matcher:  <matcher name>
        expected: <value or shape>
        # one-line rationale: which field of the emit shape this matches and why
  # additional steps for multi-step pipelines (each is {service, reads})

mishaps:
  ignore: []
```

Notes:

- **Top-level fields are exactly**: `scenario`, `source`, `input`,
  `sequence`, `mishaps`. No `scope:`, no `why:`, no flat duplicates.
- **`input.placeholders` is a map** (key = placeholder name), not a
  list of `{name, predicate}` objects.
- **`sequence` is an ordered list** of `{service, reads}` steps.
  Single-step scenarios have one entry; multi-step pipelines list
  each step that reads something observable. `within:` and `optional:`
  on individual reads are available when needed, omit them by default.
- **Annotations express intent, not output.** Use `#` comments to:
  cite source per top-of-file, justify a matcher choice, flag a draft
  note like `# arch: human says X, code looks like Y`, or hint
  `# negative: verified flag false`. They will be removed at write.

After the set, ask:
> "React with: `lgtm` (or `ok`) / `drop N` / `add <intent>` / `edit N.<field>` / `edit <field>` (when set has 1 entry) / `brainstorm [N]` / `cancel`."

Re-present the full current state of the set after EVERY operation
the user takes. The user can issue any number of operations across
any number of turns before `lgtm`.

### 3.2 Brainstorming sub-loop

If the user replies `brainstorm` or `brainstorm N`, drop into
one-question-at-a-time Q&A. Each question targets ONE field
(verb? source? matcher choice? placeholder predicate?). Wait for the
answer. **Apply the answer to the YAML in place** — edit the field,
update or add the rationale `#` comment if needed — then re-present
the full annotated YAML (or set, if `brainstorm` without N). Repeat
until the user is satisfied; then re-prompt with the Step 3 reaction
options.

The artifact is canonical YAML throughout; the user always sees real
YAML, not an abstract summary that gets translated later.

If a question has a bounded answer space, ask it as multiple choice
(see the bottom of this file for style). Otherwise ask open-ended.

## Step 4 — Five-surface hard-stop check (run per scenario, fail collectively)

When the user says `lgtm`, run these five checks for EVERY scenario
in the set. Checks read fields off the v2-native nested shape; the
service-ability check runs **per step** of `sequence[]`. Collect all
failures, then either proceed or stop — partial writes are forbidden.

| # | Surface           | Check |
|---|-------------------|-------|
| 1 | Verb              | `input.verb` matches a file in `catalogs/verbs/` AND the `executor:` named in that file exists in `tools/integration-suite-v2/internal/verbs/registry.go` |
| 2 | Matcher           | Every `sequence[].reads[].matcher` is listed in `catalogs/matchers.yaml` AND registered in `internal/matchers/registry.go` |
| 3 | Reader            | Every `sequence[].reads[].location` matches a file in `catalogs/readers/*.yaml` AND that file's `executor:` is registered in `internal/readers/registry.go` |
| 4 | Fixture cast      | For each `input.placeholders.<name>`: the `predicate` selects ≥1 user in `catalogs/fixture-cast.yaml` (every key in the predicate object appears in that user's `tags`) AND the placeholder's `type:` matches a known cast section (today: `user`) |
| 5 | Service ability   | For each step in `sequence[]`: the step's `service:` must equal the `owners:` of every read location in that step's `reads[]` (per `catalogs/readers/<loc>.yaml`'s `owners:` list), AND that service's profile (`catalogs/services/<svc>.yaml`) must declare every read location under `on_trigger.writes:`. For the FIRST step the trigger's `pattern` must also match `input.subject` (initiating trigger). Subsequent steps trigger off internal stream subjects (canonical events, etc.) and don't require pattern-match against `input.subject`. **Readers with `owners: []` are dispatcher-synthetic (e.g. `reply`) and skip the owner-equality clause; the service-profile `writes:` declaration is still required.** |

If any check fails, output a per-scenario gap report:

```
Scenario #2 (alice_archives_unowned_room) — 2 gaps:
  [4] placeholder `requester` predicate {verified:true, admin:true} matches 0 cast users (no user has `admin` tag)
  [5] step #1 service `room-service`: write `mongo.rooms` is not declared under any trigger whose pattern matches `chat.user.*.request.room.*.archive`
```

After listing all gaps across all scenarios, stop. Tell the user:
> "Hard stop. Resolve the gaps above (extend the surface, or drop the affected scenarios with `drop N`), then re-issue `lgtm`."

Do NOT write any file when a check fails.

## Step 5 — Write the YAML files

If all checks pass, for each scenario in the set:

1. Pick the destination: `tools/integration-suite-v2/scenarios/drafts/<scenario>.yaml` (flat — no scope subfolder). If a file with that path already exists, STOP and ask the user to rename.
2. **Render with comments stripped.** Take the annotated YAML from the
   outline and remove every `#` comment line and trailing `#` comment.
   Keep the structured fields exactly as they appeared in the outline
   — the outline IS the canonical shape; no field-name remapping is
   needed. Reference: `tools/integration-suite-v2/scenarios/drafts/verified-user-creates-channel-room.yaml`.
3. Write the file via the Write tool.

When all files are written, proceed to Step 6.

## Step 6 — Validate

Run, in order:
1. `make -C tools/integration-suite-v2 validate` — must exit 0.
2. `make validate` already walks `scenarios/drafts/` and runs `scenario.LoadFile` on each YAML; if any new file fails to parse, the error surfaces here.

If validation fails, surface the failure to the user and DO NOT delete the file — leave it for inspection. This is a bug in your YAML rendering; fix it and re-validate.

## Step 7 — Final report

Once validation passes, output:

```
Wrote N scenario(s) to:
  - tools/integration-suite-v2/scenarios/drafts/foo.yaml
  - tools/integration-suite-v2/scenarios/drafts/foo_unauthorized.yaml

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
