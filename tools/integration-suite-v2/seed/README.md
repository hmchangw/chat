# Seed data for integration-suite-v2

The JSON files in this directory are the canonical description of the
test world every scenario starts from. They are loaded into Mongo via
`seed.LoadAll(ctx, db)` at the start of every scenario test —
drop-and-insert, so each scenario gets a byte-identical baseline.

## Files

| File | Maps to | Mongo collection |
|---|---|---|
| `users.json` | `[]pkg/model.User` | `users` |
| `rooms.json` | `[]pkg/model.Room` | `rooms` |
| `subscriptions.json` | `[]pkg/model.Subscription` | `subscriptions` |

## Catalog annotation

`tools/integration-suite-v2/catalogs/fixture-cast.yaml` annotates these
documents with predicate tags so scenarios can pick fixtures by role
(`{ verified: true }`, `{ owner_of: r-engineering }`, etc.). The
catalog and the JSON MUST stay in sync — edit both in the same PR and
mention both edits in the PR description. There is no code-level
validator for drift in Part-1 (see §3.0 of the corrections spec).

## Naming convention

- User `_id` = `u-<account>` (e.g. `u-alice`)
- Room `_id` = `r-<slug>` (e.g. `r-engineering`)
- Subscription `_id` = `sub-<account>-<room_id>` (e.g. `sub-alice-r-engineering`)

Deterministic so two runs produce byte-identical world state.

## Current role matrix

| Account | Role facts |
|---|---|
| alice | `owner` of `r-engineering`; `member` of `r-design` |
| bob | `member` of `r-engineering` (not an owner) |
| carol | `owner` of `r-design` |
| dave | in no rooms |

Sufficient for single-actor scenarios plus the start of role-based
predicates (demote, kick, "only owner can X").

## Growing the seed

When scenarios need a new role distribution, extend the JSON AND the
cast YAML together. The 50-entity ceiling is where the static-JSON
approach starts to creak (see §9.1 of the corrections spec for the
future cast-driven-seeder maturity path).
