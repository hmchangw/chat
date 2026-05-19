# loadgen

Capacity-baseline load generator for the chat platform.

## Quick start

    ./tools/loadgen/scripts/up.sh
    ./tools/loadgen/scripts/quickstart.sh
    ./tools/loadgen/scripts/down.sh

## Discoverability

    loadgen scenarios     # list all registered scenarios
    loadgen presets       # list all built-in presets
    loadgen recommend --target-rps=N --duration=5m
    loadgen doctor        # check host readiness

## Where to find more

| For X                                    | See Y                                    |
|------------------------------------------|------------------------------------------|
| Recipes & worked examples                | [USAGE.md → Recipes](USAGE.md#recipes)   |
| Concepts (HDR, omission, verdicts)       | [USAGE.md → Concepts](USAGE.md#concepts) |
| Per-scenario reference                   | [USAGE.md → Scenarios](USAGE.md#scenarios) |
| Tuning knobs (every flag)                | [USAGE.md → Tuning](USAGE.md#tuning-knobs) |
| Migrating from v1                        | [USAGE.md → Migrating](USAGE.md#migrating-from-v1) |
| Changelog                                | [CHANGES.md](CHANGES.md)                 |
| UNTRUSTED run triage                     | [docs/runbooks/loadgen-untrusted.md](docs/runbooks/loadgen-untrusted.md) |
| Per-scenario specifics                   | [docs/scenarios/](docs/scenarios/)       |
| Design rationale (the v2 spec)           | [../../docs/superpowers/specs/2026-05-12-loadgen-v2-design.md](../../docs/superpowers/specs/2026-05-12-loadgen-v2-design.md) |

## Non-goals

- Not a CI regression gate; invoked manually.
- Not a cross-machine numerical benchmark; compare within one machine across changes.
- Not a production replay tool.
