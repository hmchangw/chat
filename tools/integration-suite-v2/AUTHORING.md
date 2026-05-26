# Authoring v2 scenarios

This is the **hand-edit fallback** for v2 integration-suite scenarios.
The full workflow — outline-set discussion, five-surface validation,
draft placement — lives in the `/scenario-author` slash command at
`.claude/commands/scenario-author.md`. Use the command unless you are
making a small, targeted fix to an existing scenario.

## Six hard rules

1. **Expected behavior comes from a cited design doc — not from
   training data.** Every scenario has a `source:` line.

2. **If the design is silent, STOP.** Do not invent expectations. (No
   more blindspot register in v2.)

3. **Catalog vocabulary is closed.** Verbs, matchers, and readers
   must already exist. If they don't, STOP and surface the gap.

4. **One scenario per file.** Each scenario is independently true.
   IDs are auto-generated `it-<runID>-<scenarioID>-…` at run time.

5. **Scenarios always land in `drafts/`.** Promotion to `approved/`
   is a separate, human-reviewed PR.

6. **Transport is implicit.** The author picks a verb name; HTTP vs
   NATS follows from which verb they picked.

## Where things live

| Asset                          | Path                                                  |
|--------------------------------|-------------------------------------------------------|
| Scenario drafts                | `scenarios/drafts/<name>.yaml` (flat — no scope subfolders) |
| Approved scenarios (CI gates)  | `scenarios/approved/` (do not write directly)         |
| Verbs                          | `catalogs/verbs/*.yaml` (see comments for input shape) |
| Matchers                       | `catalogs/matchers.yaml`                              |
| Readers                        | `catalogs/readers/*.yaml` (see comments for emit shape) |
| Service ability profiles       | `catalogs/services/*.yaml` (see comments for what's "background") |
| Fixture cast                   | `catalogs/fixture-cast.yaml` (annotation of the seed) |
| Static seed data               | `seed/*.json` (authored alongside the cast catalog)   |

## Runtime globals

Scenarios may reference these variables in `input.subject`,
`input.payload`, and any string field passed through placeholder
substitution. They are resolved by the dispatcher at fire time, not
by the placeholder resolver — i.e. they have no entry in the cast.

| Global         | Resolved from                                              | Example |
|----------------|------------------------------------------------------------|---------|
| `${site}`      | `SITE_ID` env var the runner was invoked with (default `site-local`) | `chat.user.${requester.account}.request.room.${site}.create` |
| `${now}`       | Current wall-clock time in unix milliseconds (int64). For synthetic-publish scenarios that need an event timestamp inside `[T_open, T_close]` so DB readers' `createdAt >= start` filter doesn't drop the resulting Mongo write. | `payload: { timestamp: ${now} }` |
| `$auto`        | Runtime-generated `it-<runID>-<random>` token for unique IDs the scenario doesn't care about (e.g. room name) | `payload: { name: $auto }` |

Future globals (multi-site Part-2: `${primary_site}`, `${secondary_site}`) will be added here when they land. The list is closed — anything else `${…}` resolves against the scenario's `placeholders:` map.

## How Claude and you split the work

When authoring with `/scenario-author`, the conversation has a
natural division of labor — Claude knows the code, you know the
architecture.

- Claude reads the code and cites sources as `file:line` in the
  `source:` field and inline `#` comments.
- Claude asks you when it can't find a source for a field, rather
  than guessing. Architecture often lives in your head or in
  documents outside this repo.
- When you assert something contrary to what the code shows, the
  scenario follows your assertion (architecture wins) and Claude
  adds a `# arch:` comment noting the divergence — so a later
  reviewer can decide whether the code or the scenario is the bug.

For hand-editing a YAML file in this directory, the same stance
applies — the `source:` field is the architecture citation, and a
`# arch:` comment is the right vehicle for a known code/architecture
mismatch.

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
