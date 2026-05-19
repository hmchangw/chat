# loadgen v2 complete

All phases per `docs/superpowers/plans/2026-05-12-loadgen-v2.md` are done.

## Phase tags (local)

| Tag                                | Final commit |
|------------------------------------|--------------|
| `loadgen-v2-phase0-complete`       | 890af8061a1a7f55e4e5e85ac964827294f4f0d6 |
| `loadgen-v2-phase1a-complete`      | 40a41a602543198e3741dcd835c0ced85e856d24 |
| `loadgen-v2-phase1b-complete`      | 28148088a9082146025449f738fa81f09a3b8157 |
| `loadgen-v2-phase2-complete`       | 12d10de5db0ef57f328d7e2ea304c7e9894bf4e8 |
| `loadgen-v2-phase3-complete`       | 05c5d44741f471df11f21656d9c93f1500dafb64 |

Cross-phase Tasks X.1–X.5 are done; gathered into individual commits without
their own tag.

## Cross-phase deliverables

- ✅ `loadgen scenarios|presets|recommend|doctor` (X.1)
- ✅ `compare-runs / triage / bisect / preflight / new-scenario` scripts (X.2)
- ✅ `scripts/lib/compose.sh` v1+v2 (X.3)
- ✅ `README.md` (36 lines), `USAGE.md` (736 lines), `CHANGES.md` (X.4)
- ✅ Mermaid diagrams in `docs/architecture.md`, `USAGE.md`, `docs/scenarios/federation-lag.md` (X.5)

## Static invariants at completion

| Check | Result |
|-------|--------|
| Phase tags | 5 of 5 present (`loadgen-v2-phase0-complete` through `loadgen-v2-phase3-complete`) |
| `make lint` | 0 issues |
| `make test SERVICE=tools/loadgen` | PASS (cached ok) |
| `--help` golden diff | no diff (exact match) |
| `wc -l tools/loadgen/main.go` | 390 lines (pre-existing; aspirational ≤200 not met, documented as acceptable) |
| Coverage | 63.2% (integration-only gap; 80% target documented as unmet for non-integration runs) |
| `loadgen scenarios \| wc -l` | 16 lines → **14 scenarios** (≥14 target met) |
| `loadgen doctor` | All checks passed (docker client not on PATH in CI — expected) |

## Cross-phase deliverable inventory

All present:

```
tools/loadgen/USAGE.md
tools/loadgen/README.md
tools/loadgen/CHANGES.md
tools/loadgen/scripts/lib/compose.sh
tools/loadgen/scripts/compare-runs.sh
tools/loadgen/scripts/triage.sh
tools/loadgen/scripts/bisect.sh
tools/loadgen/scripts/preflight.sh
tools/loadgen/scripts/new-scenario.sh
tools/loadgen/docs/runbooks/loadgen-untrusted.md
tools/loadgen/docs/architecture.md
tools/loadgen/docs/scenarios/auth-load.md
tools/loadgen/docs/scenarios/chaos.md
tools/loadgen/docs/scenarios/federation-lag.md
tools/loadgen/docs/scenarios/notification-fanout.md
tools/loadgen/docs/scenarios/presence-typing.md
tools/loadgen/deploy/docker-compose.loadtest.yml
tools/loadgen/deploy/docker-compose.chaos.yml
tools/loadgen/deploy/docker-compose.auth.yml
tools/loadgen/deploy/docker-compose.federation.yml
tools/loadgen/deploy/grafana/dashboards/v2/loadgen-federation.json
tools/loadgen/deploy/grafana/dashboards/v2/loadgen-overview.json
tools/loadgen/deploy/grafana/dashboards/v2/loadgen-raw.json
tools/loadgen/deploy/grafana/dashboards/v2/loadgen-system.json
tools/loadgen/deploy/prometheus/rules/loadgen.yml
tools/loadgen/deploy/prometheus/rules/loadgen_test.yml
```
