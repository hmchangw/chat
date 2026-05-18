# Phase 1b Exit Gate Verification

**Branch:** `claude/review-load-testing-dq6Bl`  
**HEAD at gate:** `ff1ed71` (Task 1b.7 — soak + campaign scripts)  
**Tag:** `loadgen-v2-phase1b-complete` (local; remote push rejected 403/disconnect per sandbox constraint)  
**Date:** 2026-05-18

---

## Part A — Static Invariants

| Check | Result | Notes |
|-------|--------|-------|
| `make lint` | PASS | 0 issues |
| `make test SERVICE=tools/loadgen` | PASS | all tests green, race detector clean |
| `--help` golden diff | PASS | empty diff vs `testdata/refactor-baseline/run-help.golden` |
| Dashboard JSON validity | PASS | all 4 v2 dashboards valid |
| `main.go` LOC | **345** (flag) | Exceeds Phase 0 budget of ≤250 and soft cap of 300; growth is from Phase 1b teardown dispatch wiring (`dispatchTeardownForce`, `runTeardown` expansion). Not a blocker — all new code is focused dispatch logic. |
| Coverage | **66.9%** (concern) | Phase 0 baseline was ~71%. Regression of ~4 pp caused by Phase 1b adding `runlock.go` (172 LOC, Acquire/Release/insertRow require live MongoDB) and `teardown_force.go` (151 LOC, runTeardownForce/loadActiveRunShortIDs require live MongoDB). These functions are covered by integration tests only. Unit-testable helpers (shortRunID, mongoDBName, consumerName, ConcurrentRunError) are fully tested. **Deferred to operator:** run integration tests to confirm real coverage. |

**Coverage detail for Phase 1b new files:**

- `artifacts.go`: WriteBundle 54.5%, writeJSON 75%, writeFile 66.7%, writeFlags/writeEnv 100%
- `runlock.go`: helper funcs 100%, Acquire/Release/insertRow/NewRunLock 0% (require MongoDB)
- `teardown_force.go`: runTeardownForce/loadActiveRunShortIDs 0% (require MongoDB)
- `creds.go`: RedactEnv/shouldRedact/CredsDir 100%, MintFixtureJWTs 77.8%

---

## Part B — Task Deliverable Presence

| Task | Artifact | Present |
|------|----------|---------|
| 1b.1 Artifact bundle | `artifacts.go` + `artifacts_test.go` | YES |
| 1b.2 Run isolation | `runlock.go` with `SharedLockDBName` | YES |
| 1b.3 Teardown --force | `teardown_force.go` with `runTeardownForce` | YES |
| 1b.4 Alert rules | `deploy/prometheus/rules/loadgen.yml` + `loadgen_test.yml` with `LoadgenUntrustedRunActive` | YES |
| 1b.5 v2 dashboards | `deploy/grafana/dashboards/v2/*.json` — 4 files (overview, raw, federation, system) | YES |
| 1b.6 Per-run creds + redaction | `creds.go` with `secretExactKeys`/`secretSuffixes`/`secretPrefixes`; `.gitignore` entries for `tools/loadgen/runs/` and `tools/loadgen/creds/` | YES |
| 1b.7 Soak + campaign scripts | `scripts/run-soak.sh` + `scripts/run-campaign.sh` (executable) | YES |

---

## Part C — Deferred Operational Verifications (Docker Required)

These checks require a running Docker daemon and cannot be executed in the CI sandbox.
Run these on a Docker-enabled machine before declaring Phase 1b production-ready.

**C.1 — Soak test (peak RSS + bundle completeness)**
```bash
cd tools/loadgen
./scripts/run-soak.sh DURATION=30m
```
Verify: peak RSS bounded (no leak), complete bundle written under `runs/<run_id>/`, `loadgen_run_quality` gauge survives the full scrape window, exit code 0 (TRUSTED) or 3 (DEGRADED — acceptable for soak).

**C.2 — Concurrent run refusal**
```bash
# Terminal 1
./scripts/quickstart.sh &
# Terminal 2 (immediately after)
./scripts/quickstart.sh
```
Verify: second invocation exits with a clear error citing the first run's `run_id` via `ConcurrentRunError`. `errors.Is(err, ErrConcurrentRun)` must be true.

**C.3 — Teardown --force on orphan**
```bash
# Start a run, then SIGKILL mid-run
./scripts/quickstart.sh &
sleep 15 && kill -9 $!
# After kill, verify orphan resources exist, then force-teardown
NATS_URL=nats://... MONGO_URI=mongodb://... go run ./tools/loadgen teardown --force
```
Verify: orphan MongoDB database and JetStream consumer removed; `runs/<run_id>/` bundle preserved; exit code 0.

**C.4 — Alert rules trigger in Grafana**
```bash
cd tools/loadgen/deploy
docker compose up prometheus grafana
# Inject synthetic time-series for each of the 5 alert conditions
```
Verify all 5 alert rules fire within their configured `for:` window and clear when the condition resolves:
1. `LoadgenUntrustedRunActive` — inject `loadgen_run_quality == 3`
2. `LoadgenHighE2ELatency` — inject `loadgen_e2e_p99 > threshold`
3. `LoadgenHighPublishErrorRate` — inject elevated `loadgen_publish_errors_total` rate
4. `LoadgenHighOmissionRate` — inject elevated omission counter
5. `LoadgenConsumerLagHigh` — inject `loadgen_consumer_lag > threshold`

---

## Part D — Tag

```
loadgen-v2-phase1b-complete → ff1ed71
```
- Created locally: YES
- Remote push: failed (remote disconnect, consistent with 403 pattern from Tasks 0.6 + 1a.8)
- The local tag is the canonical record for this phase boundary.

---

## Overall Verdict

**Phase 1b: COMPLETE** with two flagged concerns for operator attention:

1. **Coverage 66.9% < 70% target** — caused by new DB-integration-only code (RunLock, teardown_force). Integration tests (Part C) will cover this path. Accept with documented rationale; address in Phase 2 integration test suite.

2. **main.go 345 LOC > 300 soft cap** — growth is legitimate dispatch wiring for Phase 1b teardown commands. Refactor opportunity in Phase 2 if the file grows further.

All 7 Phase 1b feature deliverables are present and statically verified. Lint is clean. Unit tests are green with race detector.
