# Authoring scenarios for the integration suite

This playbook applies to BOTH humans and AI agents adding scenarios.
Every rule is mandatory. Spec reference:
`docs/superpowers/specs/2026-05-12-integration-test-suite-design.md`.

## Hard rules

```
RULE 1: Expected behavior comes from the design, not from training data.
RULE 2: If the design is silent, tag the scenario @blindspot:<slug>
        and add an entry to docs/integration-suite/blindspots.md.
        Do not invent expectations.
RULE 3: Reuse step phrasing exactly. Use `make integration-suite-steps`
        to list registered steps before writing new ones.
RULE 4: Every scenario is independently true. Unique IDs per scenario,
        no shared fixtures, no execution-order dependence.
RULE 5: Every scenario starts as draft (no @status:approved tag).
        Promotion to approved is a separate, human-reviewed PR.
RULE 6: The transport (HTTP vs NATS) is chosen by which step verb you
        use, not at the framework level. If the architecture says a
        service is reached over NATS, the step uses the NATS primitive.
```

## Sources of expected behavior (priority order)

1. `docs/superpowers/specs/*.md`
2. `CLAUDE.md`
3. `<service>/README.md`
4. `pkg/<pkg>/README.md`
5. Design comments in source code

A scenario MUST cite its source as a `# Source:` comment at the top of
the `Feature` or `Scenario`. Without a citation it cannot be promoted
to `@status:approved`.

## Scope decision tree

```
Faults involved?  yes  ──┬─ multi-site? yes → regional-resilience/
                         │                no → resilience/
                         no
                         │
Multi-site coordination? yes → federation/
                         no
                         │
Multiple flows composed? yes → journey/
                         no
                         │
Crosses request → stream → worker? yes → pipeline/
                                    no → service/
```

## Phrasing rules

- Lowercase verbs after Gherkin keywords (`Given user "alice" is authenticated`).
- Quote every parameter (`"alice"`, `"general"`).
- Multi-site scenarios append `in site "<id>"`. Single-site omit it.
- Async assertions declare a budget: `Then within 5s ...`.
- No implementation details. Read as user-visible behavior.

## Step vocabulary

Discover what's already registered:

```bash
make integration-suite-steps
```

Search the output for a matching phrase BEFORE adding a new step.
Adding a near-duplicate (`is logged in` vs `is authenticated`)
fragments the vocabulary; godog treats them as different steps.

## Transport — pick by architecture, not preference

The platform provides primitives for both transports. Use the one
that matches how the target service is reached in production:

| Target service speaks | Use step verbs that wrap |
|---|---|
| HTTP (auth-service) | the Resty-based HTTP helper (Task 8) |
| NATS request/reply (everything else) | the `natsRequest` helper (Task 11b) |
| JetStream / stream observation | (Part 2) |
| Mongo / Cassandra state | (Part 2) |

Step authors do not need to think about which primitive — they pick
the right verb (`When "alice" creates channel room "general"` uses
NATS automatically). The framework's `LastResponse.Class()` dispatches
the assertion side by transport.

## Tags

- The **scope tag is implicit from the folder.** Do not duplicate it.
- `@status:approved` — human-reviewed and ratified. CI-gating.
- `@blindspot:<slug>` — undocumented behavior. Slug is kebab-case, unique.
- `@smoke` — fast, included in CI smoke runs.
- `@slow` — nightly only.
- `@multi-room` — uses multiple rooms.

## Scenario template

```gherkin
# Source: docs/superpowers/specs/2026-04-14-add-member-design.md
Feature: Room member operations
  Behavior driven by the add-member design.

  Background:
    Given user "alice" is authenticated
    And channel room "general" exists with member "alice"

  @status:approved @smoke
  Scenario: Adding a new member persists a subscription and replies success
    Given user "bob" is authenticated
    When "alice" requests to add member "bob" to room "general"
    Then within 3s "bob" is a member of room "general"
    And "alice" receives a success reply

  Scenario Outline: Adding to a missing room replies <class>
    Given user "<requester>" is authenticated
    When "<requester>" requests to add member "<target>" to room "nonexistent"
    Then the response is a <class> error

    Examples:
      | requester | target | class        |
      | alice     | bob    | HandlerError |
```

## Anti-patterns

- Inventing a near-duplicate step (`"is logged in"` vs `"is authenticated"`).
- Asserting on implementation (`handler.X was called`).
- Hard-coded IDs (`"room-1"`) instead of fixture-generated ones.
- Pre-seeded fixtures referenced across scenarios.
- Skipping `@blindspot` for "obvious" behavior the design doesn't state.
- Filling expected values from training data (model defaults, typical API conventions).
- `@smoke` on a scenario that takes more than 2 seconds.
- Omitting `within <duration>` on an async assertion.
- Adding `@status:approved` in the same PR that introduces the scenario.
  Approval is always a separate, deliberate human action.

## Author checklist (before commit)

- [ ] `# Source:` comment cites a real document and section
- [ ] Scope folder matches the decision tree
- [ ] All steps match registered phrasings (`make integration-suite-steps`)
- [ ] All fixture IDs created in `Given` steps; no hard-coded names
- [ ] Every async assertion has `within <duration>`
- [ ] No implementation details in step text
- [ ] Tags applied (no implicit-scope duplication; no premature `@status:approved`)
- [ ] If `@blindspot:<slug>`, matching entry exists in `docs/integration-suite/blindspots.md`
- [ ] Scenario runs locally and produces the expected result
- [ ] `make integration-suite-lint` passes

## AI agent prompt template

Copy this into Claude / Cursor / Copilot. Fill in `<…>` placeholders.

```
You are adding an integration test scenario to the chat backend suite.

Behavior to test: <one sentence>
Documented source: <file path + section>
Target scope: <one of: service, pipeline, journey, federation, resilience, regional-resilience>
Target service speaks: <HTTP or NATS — verify by reading <service>/routes.go and <service>/handler.go>

Before writing:
1. Run `make integration-suite-steps` and read the output.
2. Read the documented source in full. If it does not actually specify
   expected behavior for this case, STOP and follow the blindspot
   workflow (do NOT invent expectations).

Now write the scenario by:
- Selecting the right feature file under tools/integration-suite/features/<scope>/
- Reusing existing step phrasings exactly (no invented variants)
- Adding a `# Source:` comment citing the documented source
- Following all rules in tools/integration-suite/AUTHORING.md
- Tagging as draft (do NOT add @status:approved — that is a separate human PR)

Output: a unified diff containing only the .feature change (plus any
necessary new step in *_test.go if no existing step matches).
```
