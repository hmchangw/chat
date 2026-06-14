# Integration suite multi-site — stack profiles design (P3 + P6)

> ⚠️ **Not a production tool.** Same caveats as the multi-site spec
> (`2026-06-03-integration-suite-multisite-design.md`): single-purpose
> test harness, "scenario YAML → run → report." If a feature isn't
> needed by a scenario, it doesn't get built. Failures are loud.

> 🎯 **In-place evolution.** All changes land in
> `tools/integration-suite-multisite/`. No second tree.

**Status:** design spec / not yet implemented. Captured 2026-06-14
after the tester's "still need: P3 encryption-on stack mode + P6
per-scenario service-env override" feedback. Covers both items
together because **they are the same architectural question wearing
two outfits**: how does the suite handle scenarios that need the
service stack configured differently than the default?

**Goal.** Let a scenario declare, in YAML, that it needs services
booted with a specific environment-variable set — to flip a feature
toggle (encryption on / off), lower a threshold (large-room cap,
message-bucket window), or both. The runner groups scenarios by
required env shape, boots a stack per shape, and runs the
corresponding scenarios against it.

**Why both at once.** P3 (encryption-on) is one instance of a more
general capability the chat-app already supports via env: many
services read `ENCRYPTION_ENABLED`, `LARGE_ROOM_THRESHOLD`,
`PIN_ENABLED`, `MESSAGE_BUCKET_HOURS`, etc. at boot. The right
abstraction is "alternative env profile," not "encryption mode."
Encryption then ships as one profile.

**Out of scope (explicit deferrals):**
- Per-fire env mutation (restart-mid-scenario). Profiles bind at
  stack-boot time only. A scenario that needs to flip a toggle
  mid-run is two scenarios, one per profile.
- Adding services not present in the default stack (e.g. an entirely
  separate roomcrypto microservice). Profiles parameterize the
  existing 18-service stack via env; new services are a separate
  infra ask.
- CI gating by profile (e.g. "only run encrypted profile on Tue").
  All approved-status scenarios across all profiles gate CI as today.

---

## 0. Architectural decision summary

Three shapes were considered. **Pick (B) per spec §5.2.**

| Option | What | When it'd be right |
|---|---|---|
| **(A) Single stack, conditional services** | Boot one stack; services internally branch on env at runtime. Scenarios declare env vars; runner sets them globally before boot. | Cheap. Limits us to features that toggle cleanly with no startup-time wiring (e.g. a feature flag). Encryption needs a key-store wired at startup; doesn't fit. |
| **(B) Multiple stacks, one per profile, scenarios routed** | Group scenarios by required env shape. Boot one stack per profile (parallel under USE_INFRA, sequential locally). Run that profile's scenarios against its stack. | The right answer for the chat-app: most env toggles affect boot wiring (encryption, large-room threshold caches), and we already pay a ~30s boot cost per stack — paying it once per profile is acceptable. |
| **(C) Default stack + per-scenario container restart** | Boot once. For each scenario needing a non-default env, restart the affected service(s) with new env, run, restart back. | Flexible. Catastrophically slow at 45 scenarios × ~15s/restart cycle. Rejected. |

**Decision (B)** because it preserves stack-boot determinism (each
profile boots identically every time), keeps per-scenario execution
fast (no restart cost), and matches how production actually rolls
out features (different deployments with different env). Profile
boot cost is amortized over the scenarios that need it.

---

## 1. What the tool does today

The runner boots **one** 18-service stack per invocation (under
`USE_INFRA=true`) via `internal/infra/Up`. Every service receives
the same env from `docker-local/.env` + hard-coded defaults in
`internal/infra/services.go`. Every scenario in the run uses that
stack.

Scenarios that need a different env shape (lower
`LARGE_ROOM_THRESHOLD`, encryption on, custom
`MESSAGE_BUCKET_HOURS`) have no expression — they either fudge the
state to look like the threshold tripped (`user_count: 501` to fake
a large room without 501 members) or are simply unauthorable
(encryption is off; there's no key-store; nothing to assert on).

---

## 2. The new shape — worked examples

### 2.1 Scenario declares its required profile

```yaml
scenario: encrypted-channel-delete-clears-ciphertext
source: room-service/handler.go (encrypted-channel create), …
status: draft
tag: positive

profile: encryption-on           # NEW — names a profile from catalogs/profiles/

sites:
  site-a:
    seed:
      users: { alice: { verified: true }, bob: { verified: true } }
      rooms:
        - { id: r-secret, name: SecretChannel, type: channel, encrypted: true }
      memberships:
        alice: [{ room: r-secret, roles: [owner] }]
        bob:   [{ room: r-secret, roles: [member] }]

input: [...]
expected: [...]
```

A scenario without `profile:` runs in the default profile (today's
behavior, name `default`).

### 2.2 Profile catalog entry

```yaml
# catalogs/profiles/encryption-on.yaml
profile: encryption-on
description: >
  Stack booted with end-to-end encryption services + ENCRYPTION_ENABLED=true.
  Used by scenarios that test confidentiality contracts:
  ciphertext-only storage, key rotation on member removal,
  non-member receives ciphertext blob not plaintext.
env:
  # Per-service env overrides applied at infra.Up time.
  # Keys are service names from internal/infra/services.go.
  message-gatekeeper:
    ENCRYPTION_ENABLED: "true"
    KEYSTORE_URL: "http://roomcrypto:8090"
  message-worker:
    ENCRYPTION_ENABLED: "true"
    KEYSTORE_URL: "http://roomcrypto:8090"
  room-service:
    ENCRYPTION_ENABLED: "true"
    KEYSTORE_URL: "http://roomcrypto:8090"
  room-worker:
    ENCRYPTION_ENABLED: "true"
    KEYSTORE_URL: "http://roomcrypto:8090"
  notification-worker:
    ENCRYPTION_ENABLED: "true"
extra_services:
  # Services to add to the stack ONLY when this profile is active.
  - name: roomcrypto
    image: chat/roomcrypto:dev
    port: 8090
    env:
      KEY_ROTATION_INTERVAL: "1h"
```

### 2.3 Profile catalog entry — env-only (threshold tweak)

```yaml
# catalogs/profiles/small-large-room.yaml
profile: small-large-room
description: >
  Lower LARGE_ROOM_THRESHOLD so large-room-cap scenarios can test
  the gate with 3 real members instead of inflating user_count to 501.
env:
  message-gatekeeper:
    LARGE_ROOM_THRESHOLD: "3"
  room-service:
    LARGE_ROOM_THRESHOLD: "3"
  room-worker:
    LARGE_ROOM_THRESHOLD: "3"
# No extra_services — env-only profile.
```

### 2.4 What the runner does at startup

1. Walks `scenarios/drafts/` and reads each scenario's `profile:`
   field (default if absent).
2. Groups scenarios by profile name → `{default: [scn1, scn2, ...],
   encryption-on: [scn7, scn8], small-large-room: [scn3]}`.
3. For each profile, in declaration order from `catalogs/profiles/`:
   - Boot the stack with that profile's env applied + extra_services
     added.
   - Run the profile's scenarios sequentially against it.
   - Teardown the stack.
4. Aggregate verdicts into one report.

Total run cost: `len(profiles) * boot_cost + sum(scenario_durations)`.
At ~30s/boot for 3 profiles, +90s overhead vs today's single boot.
Acceptable for the unlock.

---

## 3. Grammar reference

### 3.1 Scenario-level `profile:` field

```yaml
profile: <profile-name>     # optional; defaults to "default"
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `profile` | string | no | Names a profile from `catalogs/profiles/`. Default `"default"`. Loader rejects an unknown profile name. |

A scenario with `profile: default` is identical to no `profile:` —
both run in the default stack. Including the key explicitly is fine;
omitting it is equally fine.

### 3.2 Profile catalog file

`catalogs/profiles/<name>.yaml`. One file per profile. The file
basename (without `.yaml`) must equal the `profile:` field inside.

```yaml
profile: <profile-name>
description: <prose>
env:
  <service-name>:
    <env-var>: <value>
    …
  …
extra_services:
  - name: <service-name>
    image: <docker-image:tag>
    port: <int>
    env:
      <env-var>: <value>
      …
```

| Field | Required | Notes |
|---|---|---|
| `profile` | yes | Must match the file basename. |
| `description` | yes | Authoring discipline: every profile says why it exists. Surface in last-run.md when scenarios under this profile fail. |
| `env` | no | Map of service name → env overrides. Service names must match `internal/infra/services.go` declarations. Unknown service names rejected at load. |
| `extra_services` | no | Services to boot only under this profile. Each must declare `name`, `image`, `port`. `env` is optional. |

### 3.3 The `default` profile is implicit

There is no `catalogs/profiles/default.yaml`. The default profile is
"what the runner has always booted" — defined by
`internal/infra/services.go` + `docker-local/.env`. Scenarios with
no `profile:` field run in it.

If a scenario explicitly says `profile: default`, the runner treats
it as no profile (uses the implicit). This lets authors document
intent without forking the default into a real file.

---

## 4. Loader contract

Files: `internal/scenario/loader.go` (extension),
`internal/catalog/profiles.go` (new — profile catalog loader),
`internal/scenario/profile_validate.go` (new — referential
integrity).

### 4.1 Profile-catalog load

1. At runner startup, before scenario discovery, walk
   `catalogs/profiles/*.yaml`. Build a `ProfileCatalog` —
   `map[string]Profile`.
2. Validate each profile: name matches filename, description
   non-empty, env service names exist in
   `internal/infra/services.go`, extra_services have non-empty
   name/image/port.
3. Reject duplicate profile names with precise file:line.

### 4.2 Per-scenario validation

1. After existing `ValidateMultiInput` / `ValidateFlow`, run
   `ValidateProfile`:
   - If `Scenario.Profile == ""` or `"default"`, no-op.
   - Otherwise, require the profile to be declared in the catalog.
   Reject unknown profile name with:
   `scenario %q: profile %q not declared in catalogs/profiles/`.

### 4.3 Error messages (representative)

```
scenario %q: profile %q not declared in catalogs/profiles/ (declared: %v)
catalogs/profiles/%s: file basename %q does not match profile: field %q
catalogs/profiles/%s: env service %q does not match a service in internal/infra/services.go (known: %v)
catalogs/profiles/%s: extra_services[%d] missing required field %q
catalogs/profiles/%s: duplicate profile name %q (already declared in %s)
```

---

## 5. Runner contract

Files: `internal/runtime/runner.go` (extension),
`internal/infra/deps.go` (env override threading),
`internal/runtime/profile_router.go` (new — group scenarios by
profile).

### 5.1 Run-of-the-suite execution

`Run(cfg)` becomes a profile loop:

```
1. Load profile catalog: catalogs := LoadProfiles(cfg.CatalogsDir)
2. Discover scenarios: files := findScenarios(cfg.ScenariosDir)
3. Group by profile: groups := groupByProfile(files, catalogs)
4. For each profile in catalogs (default first, then declaration order):
     scenarios := groups[profile.Name]
     if len(scenarios) == 0: continue
     stack := infra.Up(cfg, profile)
     buildSession(stack)
     for scenario in scenarios:
       runScenario(...)
     drainSession()
     stack.TerminateAll()
5. Write report, exit.
```

Default profile is always first. Empty profile groups skip the
boot — no cost for declared-but-unused profiles.

### 5.2 Stack boot with profile

`infra.Up(cfg, profile)`:

- Merge `profile.Env` over `docker-local/.env` per-service. Profile
  env wins on conflict.
- For each `profile.ExtraServices` entry, declare a testcontainers
  ContainerRequest just like the built-in services in
  `internal/infra/services.go`.
- Wire networking the same as built-in services (shared docker
  network, per-site alias if applicable).

### 5.3 USE_INFRA=false path

Outside USE_INFRA (running against `docker-local` on host ports), the
runner can't boot per-profile stacks — there's only the one running
docker-compose. Behavior:
- `default` profile scenarios run as today.
- Any scenario whose profile is not `default` is **skipped** with a
  `scenarioVerdict{Outcome: "skipped", Reason: "profile %q requires
  USE_INFRA=true to boot its dedicated stack"}`.
- The Scope: stamp distinguishes this from PARTIAL: total cases the
  run TOUCHED stays the same, but skipped cases surface in the
  status breakdown and the Untriaged guard.

### 5.4 Per-profile reports

Single `last-run.md` covers all profiles. Per-scenario rows in the
report already carry the scenario file path; we add a `profile:`
column to the Cases table for visibility. The confusion matrix
aggregates across all profiles — a green run is "every profile's
scenarios passed."

---

## 6. Reporter contract

File: `internal/runtime/reporter.go` (extension).

- Cases table gains a `profile` column. Default profile renders as
  empty (back-compat — most rows).
- A new "## Profiles" subsection between the status breakdown and
  the Scope stamp:
  ```
  ## Profiles

  profile             count  passed  failed  boot-time
  default             38     22      16      28s
  encryption-on       5      4       1       31s
  small-large-room    2      2       0       27s
  ```
- Halted-upstream / skipped due to wrong harness (USE_INFRA=false +
  non-default profile) goes in the existing skipped count, with the
  profile-skip reason visible in failure details.

---

## 7. Migration

**No mandatory migration.** Existing scenarios have no `profile:`
field; they run in the default profile exactly as today. No catalog
files are required for the tool to function — an empty
`catalogs/profiles/` directory is a valid configuration (only the
default profile runs).

The first author who needs encryption authors the
`encryption-on` profile + scenarios; the tool starts booting two
stacks. Same for threshold profiles.

---

## 8. TDD plan & phasing

Per `CLAUDE.md`: Red → Green → Refactor → Commit per phase.

**Phase A — catalog loader + validation** (~2 days)
- RED: tests for `LoadProfiles` covering happy path, filename
  mismatch, env service unknown, duplicate profile, malformed YAML.
- GREEN: `internal/catalog/profiles.go` + `ProfileCatalog` type.

**Phase B — scenario `profile:` field + validation** (~1 day)
- RED: `Scenario.Profile` decode, `ValidateProfile` against unknown
  profile, `profile: default` no-op, empty-profile no-op.
- GREEN: extend `scenario.Scenario` + `loader.go`.

**Phase C — profile router** (~1 day)
- RED: `groupByProfile(files, catalog)` returns correct map; default
  profile first; empty groups skipped.
- GREEN: `internal/runtime/profile_router.go`.

**Phase D — infra.Up profile param** (~2-3 days)
- RED: profile env merge, extra_services declared in the test-time
  ContainerRequest list, default profile = today's services unchanged.
- GREEN: extend `internal/infra/deps.go`.

**Phase E — runner per-profile loop** (~2 days)
- RED: end-to-end test with mock infra (no real Docker): two
  profiles, each gets its scenarios, default first, skip-on-empty.
- GREEN: extend `Run()` in `runner.go`; thread profile through
  session boundaries.

**Phase F — USE_INFRA=false skip behavior** (~1 day)
- RED: non-default profile scenarios under USE_INFRA=false skip
  with the specific reason; default scenarios run as today.
- GREEN: branch in `runSweep`.

**Phase G — reporter Profiles section + profile column** (~1 day)
- RED: profile column populated; ## Profiles subsection renders;
  skipped count includes the USE_INFRA-false skips with reason.
- GREEN: extend `reporter.go`.

**Phase H — integration proof** (~1 day)
- Author the first real profile (`small-large-room` — simpler than
  encryption-on, doesn't require new services). One scenario uses
  it; USE_INFRA run shows two stacks booted, scenarios pass on the
  appropriate stack, report renders with the new section.

**Phase I — encryption-on profile + first encrypted scenario**
- Tester-side authoring. Out of tool scope; this is what unblocks
  F-015 / F-021 / F-022 encrypted-path testing.

**Phase J — docs** (~1 day)
- `SCENARIO-REFERENCE.md` gains a new "Profiles" section + the
  `profile:` field reference.
- `AUTHORING.md` gains "When to author a profile" guidance.
- `RUNBOOK.md` clarifies the per-profile boot cost.

No calendar estimate — phases are the unit of progress. Default
behavior unchanged at every phase boundary; first observable change
is when Phase H authors the small-large-room profile.

---

## 9. Risks

| Risk | Severity | Mitigation |
|---|---|---|
| **Per-profile boot cost makes full runs slow** | High | `len(profiles) * 30s` for an N-profile suite. At 3 profiles, +90s on today's ~2min run. Tunable: profile catalogs can be moved to gated test categories if it grows. |
| **Profile drift from production** — env values in profiles don't match real prod | High | Profiles are not "production parity"; they are "the env shape this test class needs." Document in profile descriptions; testers own keeping them honest. |
| **Author confusion: which profile?** | Medium | Sentinel `default` is the explicit "today's stack" reference. AUTHORING.md guidance: only add a profile when the bug class is unauthorable in default — not for stylistic preference. |
| **USE_INFRA=false skips a lot of scenarios silently** | Medium | The skip reason is visible per-scenario and counted in the status breakdown. CI flow always uses USE_INFRA=true (or pre-booted equivalents). |
| **Reporter Profiles section turns into status badges over findings** | Low | The Profiles subsection is execution-state only (boot time + pass/fail count per profile). It does not annotate findings — discipline-as-tooling lesson recorded. |
| **Extra_services balloons** — every author adds new services | Medium | Authoring rule: extra_services exist only for the encryption-on profile's roomcrypto. Other profiles should be env-only. Reviewer gate: a new extra_service requires a separate one-paragraph justification in the profile file. |

---

## 10. What this spec EXPLICITLY does NOT cover

| Out of scope | Why deferred |
|---|---|
| Per-fire env mutation mid-scenario | A scenario that needs an env change mid-run is two scenarios under two profiles. |
| Adding services beyond what extra_services covers | extra_services is the only mechanism; new infrastructural primitives (e.g., a second NATS cluster) are a separate spec. |
| Profile-level chaos overrides | Chaos engine is a separate gated effort. Profile + chaos compose orthogonally if/when chaos lands. |
| Per-scenario CI gating by profile | Approved-status is the CI gate. Profile choice is orthogonal — an approved scenario in any profile gates CI. |
| Cross-profile scenarios (one scenario runs under multiple profiles) | YAGNI for v1. If the same assertion holds under both encrypted and unencrypted, write two scenarios — small duplication is honest. |

---

## 11. Glossary

- **Profile** — a named environment shape (env vars + optional extra
  services) the runner applies when booting a stack for the
  profile's scenarios.
- **Default profile** — implicit profile matching today's behavior;
  scenarios with no `profile:` field run here.
- **Profile catalog** — `catalogs/profiles/*.yaml`; the set of
  declared profiles. Loaded at runner startup.
- **Per-profile stack** — one infra.Up invocation per profile, run
  sequentially under USE_INFRA. Default first.
- **Extra services** — services declared inside a profile, added to
  the stack only when that profile is active. Encryption-on's
  roomcrypto is the motivating use case; env-only profiles need
  none.
- **Profile skip** — under USE_INFRA=false, scenarios with a
  non-default profile skip with an explicit reason; they do not
  fail.
