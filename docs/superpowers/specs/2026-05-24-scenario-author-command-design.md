# `/scenario-author` — design

**Status:** approved 2026-05-24 (revised same day — outline shape is now v2-native canonical YAML; scope folders dropped)
**Owner:** integration-suite v2
**Related:**
- `docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md` (v2 platform)
- `docs/superpowers/plans/2026-05-22-integration-suite-v2-part1.md` (v2 Part-1 plan)
- `tools/integration-suite-v2/ARCHITECTURE.md` (the four-layer model)

## Revision log

- **2026-05-24 (initial).** Original design used a flat outline shape (top-level `scope`, `verb`, `subject`, `expected`, `why`) distinct from the on-disk `scenario.Scenario` struct, plus five scope-named draft folders.
- **2026-05-24 (revision).** Two structural changes:
  1. **Outline = annotated canonical YAML.** The outline the user reviews has the exact field shape of `scenario.Scenario` (nested `input:` + ordered `sequence:` of `{service, reads}` steps). `#` comments carry rationale during review and are stripped at write time. The `source:` YAML field is the on-disk citation. Removes the v1-shaped flat outline and the translation step from outline → final YAML (and the bugs that introduced).
  2. **Scope folders dropped.** All drafts land flat under `scenarios/drafts/<name>.yaml`. Scope was a v1 concept with no CI-selection role in v2; structural meaning (single-step vs multi-step, single-site vs cross-site, mishap-active) is now read off the YAML's shape, not the directory.

  Section 4 is rewritten against these decisions. Sections 1, 2, 3, 5, 6, 9, 10 are unchanged.

## 1. Purpose

A project-local slash command that helps Claude (or a human paired with
Claude) author v2 integration-suite scenarios with the right
catalog vocabulary, the right source citation, and a structural
guarantee that every catalog reference resolves before the YAML is
written to disk.

A single invocation may produce **one or several scenarios**. A
brainstorming pass on one behavior naturally surfaces multiple
mutually exclusive cases (the happy path, the unauthorized path, an
invalid-input path, etc.), so the workflow operates on a *set* of
outlines that the user can drop / add / edit before any file is
written.

The command replaces the v1 split of `AUTHORING.md` (rulebook) +
`SCENARIO-PROMPT.md` (640-line AI brief). v2's YAML scenarios and
bounded catalogs are constrained enough that a single slash command
file can be the single source of truth.

## 2. Non-goals

- **No automatic git hooks.** Invocation is always manual. The command
  assumes intentional use.
- **No coverage register / drift detection.** v1 maintained
  `coverage.md` + `blindspots.md`; v2 does not. If the design is
  silent on a behavior, the command stops.
- **No drafting of catalog primitives.** Missing verbs / readers /
  matchers are a hard stop, surfaced to the user as separate work.
- **No promotion automation.** Scenarios always land in `drafts/`.
  Moving a scenario to `approved/` is a separate human-reviewed PR.
- **No git-ref argument** (e.g. `HEAD~3..HEAD`). Deferred as a Part-2
  advanced feature. The command always operates on the current branch
  vs `main`.

## 3. Surface

```
/scenario-author [free-form text]
```

| Invocation                        | Mode                |
|-----------------------------------|---------------------|
| `/scenario-author`                | audit mode          |
| `/scenario-author <intent prose>` | intent mode         |

Both modes converge on the same workflow once intent is established.

### 3.1 Audit mode (no arg)

The command emits a four-step product-level summary, then prompts the
user to react.

1. **Diff.** Files touched on this branch vs `main`; new NATS subjects;
   new HTTP routes; new model fields. Raw facts.
2. **Behavioral impact.** Reads the touched handlers and model changes
   to infer what the *system* now does differently — e.g. "users can
   now archive rooms", "DM creation now requires a verified email".
   This is the value-add over a plain diff: it translates code changes
   into user-facing behavior shifts.
3. **Coverage cross-reference.** Greps `scenarios/**/*.yaml` for verbs,
   subjects, and effects that touch the affected behavior; reports
   which behaviors already have scenarios and which appear bare.
4. **Insight.** A short suggestion list: "consider authoring a
   scenario for X (uncovered)". One bullet per candidate.

After the summary the command asks: *"Want to author one of these, or
something else?"* User responses:

- **Positive** (picks one of the suggestions, or names another
  uncovered behavior) → proceed straight to the workflow with that
  intent.
- **Vague or negative** → drop into brainstorming Q&A (one question
  at a time) to extract intent → then workflow.

### 3.2 Intent mode (free-form arg)

The command treats the argument as the user's intent and jumps
straight into the workflow. No audit.

### 3.3 Brainstorming escape hatch

At any outline-reaction step the user may reply `brainstorm`. The
command drops into one-question-at-a-time Q&A (verb? source? expected
reads? mishaps?). Each answer is applied to the YAML in place — the
field is updated, the rationale `#` comment is updated if it shifted —
and the full annotated YAML (or set) is re-presented. The artifact
mutates across turns; the user always sees real YAML, not an abstract
summary.

### 3.4 Authoring stance — soft direction

The conversation between Claude and the human has a natural division
of labor that the workflow should respect without enforcing:

- **Claude knows the code.** Source citations come from files Claude
  can read — `file:line` refs in `source:` and inline `#` comments.
- **The human knows the architecture.** Often architecture lives in
  the human's head or in documents outside this repo. When Claude
  can't find a source for a field, it ASKS rather than guesses
  ("I don't see X in `CreateRoomReply` — what does architecture
  specify?"). Empty-source guesses are how scenarios drift away from
  architectural truth.
- **Human assertion outranks Claude's code reading.** If the human
  says "architecture says X" and the code appears to do Y, the
  scenario follows X. Claude captures it and adds a short `# arch:`
  comment noting the divergence, so the reviewer can decide later
  whether the code or the scenario is the bug.
- **Tone is collaborative, not interrogative.** One question at a
  time during brainstorm; no rigid checklist. The five-surface check
  is the only hard gate — everything before it is conversation.

## 4. Workflow

```
1. Load catalogs
2. Build context (audit summary OR user intent)
3. Produce outline SET (1+ candidate scenarios)
4. User reacts (lgtm / drop N / add <intent> / edit N.field /
   brainstorm / cancel) — repeat until lgtm
5. Five-surface hard-stop check, per scenario
6. Write each scenario as a separate YAML file under
   scenarios/drafts/<scope>/<name>.yaml
7. Run `make -C tools/integration-suite-v2 validate` once + scenario
   loader on every written file
8. Report all file paths; remind user that promotion is a separate PR
```

A single invocation produces between 1 and N scenarios. The default
when brainstorming a behavior is to surface the obvious positive case
plus its mutually exclusive negative counterparts; the user prunes or
extends from there.

### 4.1 Outline shape

The outline presented to the user is a numbered set of candidate
scenarios. Each entry is **annotated canonical YAML** — the exact
shape of `scenario.Scenario` that will be written to disk, with `#`
comments carrying intent / source / draft-status notes. Comments are
stripped at write time; the `source:` YAML field is the on-disk
citation.

```yaml
# Scenario #1 — verified user creates a channel room (happy path)
# Source: pkg/subject/subject.go:365 (RoomCreate), pkg/model/event.go:245 (CreateRoomReply)

scenario: verified_user_creates_channel_room
source:   pkg/subject/subject.go:365 + pkg/model/event.go:245

input:
  verb:    nats_request
  subject: chat.user.${requester.account}.request.room.${site}.create
  payload:
    name: $auto                                    # runtime generates unique
  credential: ${requester.credential}
  placeholders:
    requester:
      type: user
      predicate: { verified: true }                # tag from fixture-cast.yaml

sequence:
  - service: room-service
    reads:
      - location: reply                            # synthetic, from dispatcher
        matcher:  matches_shape
        expected: { status: "accepted", roomType: "channel" }
        # status field per CreateRoomReply struct; roomType per request classification

mishaps:
  ignore: []
```

Notes on the shape:

- **Top-level fields match `scenario.Scenario` exactly**: `scenario`,
  `source`, `input`, `sequence`, `mishaps`. No `scope:` (dropped),
  no `why:` (replaced by inline `#` comments), no flat duplicates of
  nested fields.
- **`input.placeholders` is a map** keyed by name, matching the Go
  struct (not a list of `{name, predicate}`).
- **`sequence` is an ordered list of `{service, reads}` steps.**
  Single-step (sync request/reply) and multi-step (request → worker →
  persistence) are the same shape; `within:` and `optional:` on
  individual reads are available when needed, omitted by default.
- **Annotations are stripped on write.** All `#` comments are removed
  unconditionally; on-disk YAML carries only the structured fields.

The outline set is what the user reviews. Full YAML files are written
only after the user says `lgtm` and every entry in the set passes the
five-surface check independently. A failing entry blocks the entire
write — partial writes are not allowed within one invocation.

### 4.2 Outline reaction terminology

| Reply                       | Meaning                                                              |
|-----------------------------|----------------------------------------------------------------------|
| `lgtm` / `ok`               | Whole outline set is correct; proceed to five-surface check + write  |
| `drop <n>`                  | Remove candidate scenario N from the set, re-present                 |
| `add <intent>`              | Append a new candidate (a fresh outline for the given intent), re-present |
| `edit <n>.<field>`          | Adjust one field on scenario N, re-present                           |
| `edit <field>`              | Shorthand for `edit 1.<field>` when the set has exactly one entry    |
| `brainstorm`                | Drop into one-question-at-a-time Q&A across the whole set, then re-present |
| `brainstorm <n>`            | Drop into Q&A focused on scenario N                                  |
| `cancel`                    | Abort; no files written                                              |

The reactions can be issued in any order across multiple turns. The
command re-presents the full set after every operation so the user
always sees the current state before the next decision.

The word "approve" is deliberately not used — it is reserved for the
`drafts/` → `approved/` promotion (a separate human PR).

### 4.3 Five-surface hard-stop check

Before writing the YAML, the command validates *structural presence*
in the registries. Checks are adapted to the v2-native nested shape;
multi-step sequences are checked per step.

| # | Surface              | Check                                                                                 |
|---|----------------------|---------------------------------------------------------------------------------------|
| 1 | Verb                 | `input.verb` resolves to `catalogs/verbs/<name>.yaml` AND its Go executor is registered |
| 2 | Matcher              | Every `sequence[].reads[].matcher` exists in `catalogs/matchers.yaml` AND is registered |
| 3 | Reader               | Every `sequence[].reads[].location` exists in `catalogs/readers/*.yaml` AND is registered |
| 4 | Fixture cast         | For each `input.placeholders.<name>`: the predicate selects ≥1 user in `catalogs/fixture-cast.yaml` (every key in the predicate appears in that user's `tags`) AND the placeholder's `type:` matches a known cast section |
| 5 | Service ability      | **Per step:** the step's `service:` equals the `owners:` of every read location in that step's `reads[]`, AND that service's profile (`catalogs/services/<svc>.yaml`) declares the location under `on_trigger.writes:`. For the FIRST step the trigger's `pattern` must match `input.subject` (initiating trigger). Subsequent steps trigger off internal stream subjects (canonical events, etc.) and don't need pattern-match against the initial subject. **Readers with `owners: []` are dispatcher-synthetic (e.g. `reply`) and skip the owner-equality clause; the service-profile `writes:` declaration is still required.** |

The check runs **per scenario** in the outline set. The command
collects all gaps across all scenarios first, then prints them grouped
by scenario number, then exits without writing anything. The user
decides next steps (extend the failing surfaces in separate work; drop
the failing scenarios from the set; rescope).

Partial writes are not allowed: if scenario #2 fails the check,
scenario #1 is not written either. The whole set succeeds or the
whole set is rejected.

The check does **not** validate documentation quality — that is a
human review concern at promotion time.

### 4.4 File destination

Each scenario in the outline set is written to its own file under the
flat drafts directory:

```
tools/integration-suite-v2/scenarios/drafts/<name>.yaml
```

`<name>` is the outline's `scenario` value (snake_case). Names must
be unique across `scenarios/drafts/` — if a write would clobber an
existing file, the command stops and asks the user to rename.

The five scope-named subfolders that existed in the initial design
(`service/`, `journey/`, `pipeline/`, `federation/`, `resilience/`)
are removed. Scope was a v1 concept with no CI-selection role in v2;
the structural distinctions it tracked (single-step vs multi-step,
single-site vs cross-site, mishap-active) are now read off the YAML's
shape — `len(sequence)`, the spread of `service:` fields across
steps, whether `mishaps:` is empty — not the directory.

### 4.5 Post-write validation

After all files in the set are written, the command runs, in order:

1. `make -C tools/integration-suite-v2 validate` — re-validates all
   catalog references across the suite (one invocation covers every
   newly written file).
2. `scenario.LoadFile(<path>)` on each newly written file via a small
   helper inside the validator binary, to confirm parse + `source:`.

Failures here are bugs in the command (it shouldn't have written
unloadable files). The command reports the failures and leaves the
files on disk for the user to inspect.

## 5. Hard rules

These rules are reproduced verbatim inside the command file and inside
`AUTHORING.md`:

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

## 6. Companion documentation

The five-surface check is meaningful only if the catalogs are richly
documented. v2's choice: **documentation lives as YAML comments**, not
as new schema fields. The catalogs are not deploy artifacts (unlike
Kubernetes manifests, where `description:` is part of the transferred
data) — everything stays in this one repo, so inline `#` comments are
the right vehicle.

| File                                         | Comment requirement                                                            |
|----------------------------------------------|--------------------------------------------------------------------------------|
| `catalogs/fixture-cast.yaml`                 | Inline `#` per user explaining what each predicate tag means (`verified` → email-verified, JWT scope, etc.) |
| `catalogs/readers/*.yaml`                    | `#` block per reader describing the emitted event shape (fields, types)        |
| `catalogs/services/*.yaml`                   | `#` per ability pattern explaining its purpose and which effects it covers     |

Comments are concise. The criterion is: read alongside the surrounding
YAML, the meaning is clear. They are not English paragraphs, they are
short notes.

Extending the existing YAMLs with these comments is part of the
implementation work, done before the slash command is built so the
command works meaningfully on first invocation.

## 7. Files this design creates or changes

| Path                                                                                  | Action                                  |
|---------------------------------------------------------------------------------------|-----------------------------------------|
| `.claude/commands/scenario-author.md`                                                 | Rewrite Steps 3–5 against the annotated-YAML outline + flat `drafts/`; strip comments at write; describe the in-place brainstorm loop; add the §3.4 authoring-stance bullets |
| `tools/integration-suite-v2/AUTHORING.md`                                             | Update "Where things live" to flat `scenarios/drafts/<name>.yaml`; add a short "How Claude and you split the work" paragraph |
| `tools/integration-suite-v2/catalogs/fixture-cast.yaml`                               | (Already done in initial pass) `#` comments per user explaining predicate tags |
| `tools/integration-suite-v2/catalogs/readers/*.yaml`                                  | (Already done in initial pass) `#` comments describing emit shape |
| `tools/integration-suite-v2/catalogs/services/*.yaml`                                 | (Already done in initial pass) `#` comments on ability rationale |
| `tools/integration-suite-v2/scenarios/drafts/service/verified-user-creates-channel-room.yaml` | `git mv` to `tools/integration-suite-v2/scenarios/drafts/verified-user-creates-channel-room.yaml` |
| `tools/integration-suite-v2/scenarios/drafts/{service,journey,pipeline,federation,resilience}/.gitkeep` | `git rm` — five scope folders removed |

## 8. Acceptance criteria

The command ships when:

1. `/scenario-author "alice creates a channel room"` produces a draft
   YAML scenario at `scenarios/drafts/<name>.yaml` (flat) that loads
   cleanly and `make validate` passes against it. The on-disk YAML
   contains no `#` comments (they were stripped at write).
2. `/scenario-author` with no arg, on a branch with no changes, reports
   "no changes detected on this branch; provide intent" cleanly.
3. `/scenario-author` with no arg, on a branch that touches
   `room-service/handler.go`, identifies at least one behavioral
   impact and at least one coverage suggestion.
4. An outline that references an unregistered verb is hard-stopped at
   step 5 of the workflow with a one-line gap statement and no file
   written.
5. An outline whose placeholder predicate finds zero matching cast
   users is hard-stopped at step 5 with a one-line gap statement.
6. `/scenario-author "verified user creates a channel room"` followed
   by a `brainstorm` reaction produces an outline set containing at
   least the positive case and one mutually exclusive negative case
   (e.g. unverified-user-attempts-the-same-action). After `lgtm`, all
   scenarios in the set are written together; if any one fails the
   five-surface check, no files are written.
7. `drop`, `add`, and `edit N.field` reactions modify the outline set
   correctly across multiple turns; the set's current state is
   re-presented after each reaction.
8. Every Part-1 catalog YAML has `#` comments meeting the criterion in
   section 6 (read alongside surrounding YAML, meaning is clear).
9. `tools/integration-suite-v2/AUTHORING.md` exists, fits on one page,
   restates the six hard rules, and links to the slash command for the
   full workflow.

## 9. Risks and mitigations

| Risk                                                                                   | Mitigation                                                       |
|----------------------------------------------------------------------------------------|------------------------------------------------------------------|
| Audit-mode impact analysis hallucinates behavior shifts not actually in the diff       | Anchor impact bullets to specific file + line; user reviews before workflow proceeds |
| Five-surface check is too strict and blocks legitimate scenarios                       | Hard-stop messages name the exact gap so user can extend the surface in a separate PR |
| YAML comments drift from reality as catalogs evolve                                    | Reviewers verify comments during drafts → approved promotion     |
| Users invoke `/scenario-author` for a behavior the design genuinely doesn't cover, expect a scenario, get a hard stop | RULE 2 is loud; command's exit message states "design silent — extend the source first" |

## 10. Out-of-scope future work

- **Git-ref arg** (`/scenario-author HEAD~3..HEAD`) for auditing past commits.
- **Promotion helper** (`/scenario-promote <path>`) to move drafts → approved with reviewer checklist.
- **Catalog-primitive scaffolder** for the case where the hard-stop check finds a gap and the user wants Claude to draft the missing verb/reader/matcher.

These are deliberately deferred until the base command has shipped and the v2 suite has more than a handful of scenarios.
