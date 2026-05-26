# integration-suite v2

A scenario-driven black-box integration test platform. See
`docs/superpowers/specs/2026-05-21-integration-suite-v2-design.md`
for the design.

This is v2. v1 lives at `tools/integration-suite/` and is not affected
by anything here.

## Quick start (after `make deps-up && make up` from repo root):

```bash
make -C tools/integration-suite-v2 local        # run scenarios
make -C tools/integration-suite-v2 validate     # catalog validator only
```

## Layout

- `catalogs/` — YAML data: verbs, readers, services, fixtures, matchers.
- `scenarios/` — scenario YAML files (drafts + approved).
- `internal/` — Go runtime (verbs, readers, matchers, scenario loader, runtime).
- `cmd/runner` — main test runner binary.
- `cmd/validator` — catalog validator CLI.
- Reports are written to `<repo>/docs/integration-suite-v2/last-run.md`.
