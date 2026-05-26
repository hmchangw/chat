# Placeholder value plumbing (v2 substitution generalization)

**Status:** draft 2026-05-24
**Owner:** integration-suite v2
**Related:**
- `2026-05-21-integration-suite-v2-design.md` (v2 platform — placeholder concept)
- `2026-05-24-integration-suite-v2-part1-corrections-design.md` (Part-1 corrections; this spec enables RW-1's identity-level follow-on assertions)
- `tools/integration-suite-v2/ARCHITECTURE.md`

## 1. Context

Scenarios pick fixtures by predicate
(`requester: { type: user, predicate: { verified: true } }`).
The resolver picks alice; the dispatcher substitutes
`${requester.account}` → `alice` into `input.subject` and
`input.payload`. The classifier then compares observed events
against `expected:` — but `expected:` only sees literal scalars.
It cannot reference the fixture the resolver picked for THIS run.

Consequence: every assertion is shape-only. A read can say "a
channel-typed room landed in Mongo" but not "the room created by
the user we picked, with the name we generated." Identity-level
assertions require the same `${X.…}` substitution to work in
`expected:` blocks too, against the same picked values.

The current substituter
(`tools/integration-suite-v2/internal/runtime/dispatcher.go:67-95`)
hardcodes two paths: `${site}` and `${<name>.account}`. Generalising
to any field of any namespace, in any value position, is the unlock.

This spec captures the design and implementation plan.

## 2. Non-goals

- **No predicate-language extension.** Predicates stay tag-based and
  resolver-driven. This spec is about what HAPPENS AFTER a
  placeholder resolves, not about how it gets picked.
- **No new placeholder types.** Today only `type: user` is
  implemented (`type: room`, `type: message` are reserved by v2
  design). When they land, they reuse this machinery.
- **No partial substitution.** A `${X.path}` that fails to resolve
  is a HARD ERROR — never a passthrough that silently sends the
  literal token to the matcher.

## 3. Design

### 3.1 Substitution context

```go
type Context struct {
    Site         string
    Placeholders map[string]map[string]any  // name → fixture-as-map
    Input        InputSnapshot              // populated after fire
}

type InputSnapshot struct {
    Subject   string
    Payload   map[string]any
    RequestID string
}
```

Built incrementally:
1. After the resolver picks fixtures → `Placeholders` populated.
2. Before fire → `Site` from runner config.
3. After fire → `Input` populated from the substituted subject +
   payload + dispatcher-generated request ID.

### 3.2 Namespaces (closed set)

| First segment | Walks |
|---|---|
| `${site}` | `ctx.Site` (no further path) |
| `${<placeholder name>.<path>}` | `ctx.Placeholders[name]` — see §3.3 |
| `${input.subject}` | `ctx.Input.Subject` |
| `${input.payload.<key>}` | `ctx.Input.Payload[<key>]` — nested supported |
| `${input.requestId}` | `ctx.Input.RequestID` (the UUIDv7 the dispatcher set on the outbound X-Request-ID header) |

Anything else → hard error at substitution time.

### 3.3 Placeholder field source

For `type: user` placeholders the resolver JOINS by
`cast.id == seed.account` (already 1:1 by convention; the seed JSON
under `tools/integration-suite-v2/seed/users.json` defines the
authoritative production-shape user record). The seed user's JSON
shape becomes the substitution map exposed to scenarios:

| Token | Resolves to |
|---|---|
| `${requester.id}` | seed user `_id` (e.g. `u-alice`) |
| `${requester.account}` | seed user `account` (e.g. `alice`) |
| `${requester.engName}` | seed user `engName` (e.g. `Alice`) |
| `${requester.chineseName}` | seed user `chineseName` |
| `${requester.siteId}` | seed user `siteId` |
| `${requester.<unknown>}` | hard error |

Cast YAML's `id: alice` is the pick-key. Cast's `tags:` drive
predicate selection at pick time but are NOT exposed via
substitution — that would conflict with seed `_id` (two `.id` fields
ambiguous). **Seed wins for everything.**

### 3.4 Walker semantics

```go
func Substitute(value any, ctx Context) (any, error)
```

Walk recursively:
- `string` → tokenise for `${…}` patterns and substitute. If the
  whole string IS exactly one `${…}` token, return the resolved
  value with its native type (so `${input.payload.userCount}`
  returns `int(2)`, not `"2"`). If mixed with literal text, return
  the substituted string.
- `map[string]any` → recurse on each value, return new map.
- `[]any` → recurse on each element, return new slice.
- `int / bool / float / nil / other scalar` → return as-is.
- Unresolvable token → return error naming the token, the namespace
  consulted, and the available paths under that namespace (helpful
  for authoring).

Type fidelity matters because matchers like `matches_shape` already
compare JSON-unmarshalled numbers as `float64`; preserving the
native type at the substitution boundary keeps the matcher's
existing normalisation rules valid.

### 3.5 Where substitution runs

Two call sites:

1. **Dispatcher** (`tools/integration-suite-v2/internal/runtime/dispatcher.go:30-58`):
   substitute `input.subject` and `input.payload` once before fire.
   Today this is `substitutePlaceholders` (lines 67-73) + a scalar-
   only path in `buildPayload` (lines 75-96) — both replaced by
   calls to the new `Substitute`.

2. **Classifier** (the matcher-invocation site, currently in
   `tools/integration-suite-v2/internal/runtime/verdict.go` —
   `Classify`): substitute each `read.Expected` against the full
   context (including `ctx.Input`) before passing it to the matcher.
   This is the new behavior.

## 4. Files

| Path | Action |
|---|---|
| `tools/integration-suite-v2/internal/runtime/substitute.go` | New — `Context`, `InputSnapshot`, `Substitute` |
| `tools/integration-suite-v2/internal/runtime/substitute_test.go` | New — covers every namespace, type fidelity, error modes, nested expected blocks |
| `tools/integration-suite-v2/internal/fixtures/seeder.go` | Extend `CastUser` with a `UserDoc *model.User` field; resolver loads the seed JSON, joins by `cast.id == seed.account`, attaches it |
| `tools/integration-suite-v2/internal/runtime/dispatcher.go` | Replace `substitutePlaceholders` + `substituteScalarPlaceholders` + `buildPayload` substitution with `Substitute`; capture `ctx.Input` after fire |
| `tools/integration-suite-v2/internal/runtime/verdict.go` | Substitute `read.Expected` before passing to matcher |
| `tools/integration-suite-v2/internal/runtime/runner.go` | Plumb the `Context` from dispatcher to classifier |

## 5. Acceptance criteria

1. **Substitute unit tests** cover every namespace + nested maps +
   slices + type fidelity (int stays int) + hard error on unknown
   path.
2. **Back-compat:** the existing room-service scenario at
   `tools/integration-suite-v2/scenarios/drafts/verified-user-creates-channel-room.yaml`
   passes unchanged.
3. **New capability demonstrated:** a test scenario can assert
   `createdBy: ${requester.id}` and `name: ${input.payload.name}`
   against a `mongo.rooms` read and the substitution resolves
   correctly at classify time. (End-to-end PASS requires RW-1 from
   the corrections spec to land first — out of scope here; unit test
   for substitution is the bar.)
4. **Error messages** name both the offending token and the
   namespace consulted (e.g. `unknown path "requester.foo": user
   placeholders expose [id account engName chineseName siteId
   sectId sectName employeeId]`).

## 6. Implementation order

1. Spec (this file).
2. `substitute.go` + tests (TDD: tests first, then impl).
3. Resolver enrichment in `internal/fixtures/seeder.go` so
   `CastUser` carries the joined seed user.
4. Dispatcher: replace inline substitution with `Substitute` calls;
   populate `ctx.Input` after fire.
5. Classifier: substitute `read.Expected` before matcher call.
6. (Optional, separate PR) update the sample scenario to demonstrate
   identity-level assertions once RW-1 also lands.

## 7. Risks

| Risk | Mitigation |
|---|---|
| Resolver enrichment requires seed JSON loaded before placeholders resolve | Already true today — `seed.LoadAll` runs before scenarios in `internal/runtime/runner.go` per corrections-spec §3.0 P0-A |
| Type-fidelity edge cases (numbers, bools, slices in `expected`) | Walker semantics in §3.4 are explicit; unit tests cover them per AC #1 |
| Existing scenario breaks on the substitution change | Only one scenario today; AC #2 is the regression gate |
| `${input.requestId}` is generated by the verb, not the dispatcher; capturing it requires the dispatcher to read `Outcome.RequestID` (already on the struct post P0-1) | `ctx.Input.RequestID = out.RequestID` after fire; one assignment |

## 8. Out of scope

- New placeholder types (`room`, `message`) — when they land, they
  attach their own field source to the same machinery.
- Cross-placeholder references in PREDICATES
  (`predicate: { owner_of: $target_room.id }`) — Part-2 resolver
  work, separate from substitution.
- Templating beyond `${...}` (no `{{ }}`, no expressions, no
  conditional logic) — by design.
