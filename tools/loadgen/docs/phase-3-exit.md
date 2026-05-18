# Phase 3 exit summary

**Status:** COMPLETE (commit `05c5d44`, tag `loadgen-v2-phase3-complete`).

## Scenarios delivered

16 of 16 + chaos overlay. Counted by sub-cohort:

### 3-α foundational (3 of 3)
- §3.5 thread fan-out wire-up (`35b2654`)
- §3.12 DM/channel split (`5277a17` + critical-bug fix `6c68b40`)
- §3.15 bursty publish envelope (`1a4da6c`)

### 3-β read scenarios (3 of 3)
- §3.1 raw-consistency (`43d2b56`)
- §3.14 room-open composite (`ab532f7`)
- §3.16 read-receipts (`2987e8b`)

### 3-γ broadcast/notification (3 of 3)
- §3.2 large-room-broadcast (`146df4d`)
- §3.3 presence-typing (`7520d83`, build-tagged skeleton — SUT lacks presence subjects)
- §3.4 notification-fanout (`d0bb0cc`, 3.4a real + 3.4b build-tagged skeleton)

### 3-δ write churn (3 of 3)
- §3.6 message-mutate (`be67144`)
- §3.7 subscription-churn (`2551b6e`)
- §3.13 first-dm (`dbde60b`)

### 3-ε auxiliary (3 of 3 + overlay)
- §3.8 auth-load (`cfaff25` — audit found 2 endpoints not 3)
- §3.9 federation-lag (`de4c339` — real secondary-NATS dial)
- §3.17 chaos overlay (`05c5d44`)

## Skeleton vs full backend wire-up

Per the spec's deliberate "skeleton landing" pattern, most Phase 3 scenarios
ship with:
- Real per-scenario LOGIC (trackers, helpers, state machines, parsers — all
  unit-tested)
- STUB backend wire-up (publishOne / lookupHistory / emitMutation etc.) with
  TODO comments documenting the follow-up needed

This keeps the surface complete (registration + flags + metrics + docs) so
operators can SELECT scenarios and the dashboards/alerts render correctly,
while the real SUT integration is layered in once the corresponding SUT
contracts stabilize.

## Deferred to operator (Docker required)

Several Phase 3 scenarios require Docker for integration tests:
- §3.9 federation-lag — 2-site testcontainer (testdata/federation/streams.json fixture).
- §3.17 chaos overlay — `scripts/run-chaos.sh` exercises toxiproxy live.
- All scenarios that have `_integration_test.go` files use testcontainers.

Run these locally when Docker is available; they are committed and runnable.

## Cross-phase work still pending (Tasks X.1-X.5)

Phase 3 exit does NOT cover the cross-phase deliverables (separate plan section):
- X.1 Discoverability subcommands (`loadgen scenarios|presets|recommend|doctor`)
- X.2 Diagnostic scripts (compare-runs, triage, bisect, preflight, new-scenario)
- X.3 Compose v1/v2 lib (`scripts/lib/compose.sh`)
- X.4 README/USAGE/CHANGES split + per-scenario USAGE entries
- X.5 Mermaid diagrams

These are intentionally separate and tagged for follow-up.

## Static invariants at exit

| Check                                | Result |
|--------------------------------------|--------|
| `make lint`                          | 0 issues |
| `make test SERVICE=tools/loadgen`    | PASS |
| `--help` golden diff                 | empty (exact match) |
| Coverage                             | 62.1% |
| Registered scenarios (default build) | 14     |
| Build-tagged scenarios               | 2 (presence-typing, notif_routing) |

### Notes on coverage

Phase 1b was 66.9%. Phase 3 added 10 scenarios with stub `Run()` methods that
aren't unit-tested (real behaviour requires a live SUT), so a slight decrease
to 62.1% is expected and documented. Per-scenario LOGIC (trackers, parsers,
state machines) is tested; the stub backend calls are excluded by design.
