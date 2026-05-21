# Branch Review: `claude/explore-room-service-7tNlq`

**Date:** 2026-05-21
**Base:** `fd7b4e2` (last commit before this branch on the upstream lineage)
**HEAD:** `8277488`
**Commits on branch:** 11 (plan docs + 9 implementation commits)
**Author:** Claude

## Scope

Adds a new per-user `mute.toggle` RPC to `room-service`, mirrors the toggle across sites via `inbox-worker`, renames `Subscription.DisableNotification` → `DisableNotifications` repo-wide, and documents the new RPC in `docs/client-api.md`.

## Services touched

| Service | Magnitude | Note |
|---|---|---|
| `room-service` | substantive | New handler + store method + `publishCore` wiring + 5 unit tests + 1 integration test |
| `inbox-worker` | medium | New dispatch case + store method + 3 unit tests |
| `room-worker` | minimal | Field-rename in one integration test only — no production code touched |

Shared `pkg/` files also changed: `pkg/model/event.go`, `pkg/model/subscription.go`, `pkg/model/model_test.go`, `pkg/subject/subject.go`, `pkg/subject/subject_test.go`.

## Findings count (deduped across all lenses)

| Severity | Count |
|---|---|
| critical | 0 |
| high | 1 |
| medium | 10 |
| low | 9 |
| nitpick | 6 |

## Top-line risk assessment

**Functionally correct and mergeable after addressing a small set of issues.** The implementation faithfully follows the `message.read` precedent (Pattern B): inline write in `room-service`, cross-site outbox publish, inbox-worker mirror. SAST is clean (gosec PASS, govulncheck PASS; semgrep unavailable in env — not a code defect). `make test` and `make lint` both green. The integration test for the new store method passes.

The notable items to address before merge:

1. **A reachable bug where `GetUserSiteID` errors silently abandon the cross-site outbox publish** (3 reviewers independently flagged this), causing permanent cross-site mute divergence on transient DB hiccups. Inconsistent with `handleMessageRead`'s precedent which propagates the error.
2. **`sanitizeError` doesn't pass through the new `"invalid mute-toggle subject: …"` error string**, so clients receive `"internal error"` instead of the message documented in this same PR's `client-api.md` update — the API contract is broken at delivery.
3. **`SubscriptionUpdateEvent.Action` field comment is now stale** — claims `"added" | "removed"` but the new code adds `"mute_toggled"` (and earlier work added `"role_updated"`). Client-facing contract drift.
4. A TOCTOU window between `GetSubscription` and `ToggleSubscriptionMute` that adds a redundant Mongo round-trip per request.
5. Missing tests for several error paths (outbox publish failure, `GetSubscription` generic error, `publishCore` failure, `GetUserSiteID` failure soft path) — coverage of `handleMuteToggle` lands at ~70–75% vs the 90%+ target for core handler logic.

None of these are blocking-critical; the branch can ship after a small fix-up commit.

---
