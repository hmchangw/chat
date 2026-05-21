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

## Service: room-service

### (a) Diff correctness against existing conventions

**[medium]** `room-service/handler.go:1230` — `natsMuteToggle` logs `slog.Error("mute toggle failed", "error", err, "subject", m.Msg.Subject)` with an extra `subject` structured field that the analogous `natsMessageRead` (line 971) and `natsUpdateRole` etc. do not. Either propagate the field across all handlers or drop it here. Inconsistency only.

**[nitpick]** `room-service/handler.go:1253-1255` — `handleMuteToggle` calls `ToggleSubscriptionMute` and re-checks `ErrSubscriptionNotFound` even though the preceding `GetSubscription` guard at line 1245 should prevent that branch. Dead-code defense-in-depth that could mislead a future reader about atomicity guarantees.

### (b) Scope drift / refactor-readiness

**[low]** room-service is cohesive; `mute.toggle` is a per-subscription mutation that the service already owns (same `subscriptions` collection used by `UpdateSubscriptionRead`). No scope drift. `handler.go` is large (~1300 lines) but already was — the new handler follows the established length pattern. No split needed.

### (c) Abstraction changes

**[low]** `publishCore` closure injection is justified. room-service already had `publishToStream` for JetStream; core-NATS publish for `subscription.update` fan-out needs a separate path because the APIs differ (`js.PublishMsg` returns `(*PubAck, error)`, `nc.PublishMsg` returns only `error`). The shape mirrors `publishToStream`, it's injected in `main.go` at line 119, and existing tests that don't exercise it pass `nil`. No premature abstraction.

**[low]** `NewHandler` now has 10 positional parameters (`handler.go:44`). Borderline; a config struct would be cleaner. Pre-existing issue, not introduced by this PR but worsened by it.

### (d) Design coherence

The mute.toggle flow is a textbook match for room-service's job: validate membership → atomic Mongo write → fan out `subscription.update` core event → cross-site outbox. The plan's Pattern-B (`message.read` shape) is exactly what landed.

### (e) Project-pattern adherence

- **`pkg/subject` builders**: `subject.MuteToggleWildcard`, `subject.MuteToggle`, `subject.ParseUserRoomSubject`, `subject.SubscriptionUpdate`, `subject.Outbox` — all used correctly; no raw `fmt.Sprintf` subjects in new code. ✓
- **Outbox pattern**: `publishToStream` called with `subject.Outbox(h.siteID, userSiteID, model.OutboxSubscriptionMuteToggled)` at line 1307. ✓
- **`Timestamp int64` on new event structs**: `SubscriptionMuteToggledEvent.Timestamp` set at the publish site (`now.UnixMilli()`, line 1290). `OutboxEvent.Timestamp` set at line 1301. `SubscriptionUpdateEvent.Timestamp` set at line 1269. All correct. ✓

**[medium]** `room-service/handler.go:1280-1283` — When `GetUserSiteID` errors, the handler logs `slog.Warn` and returns `{status: "ok"}` while silently dropping the cross-site outbox publish. The peer handler `handleMessageRead` (line 1023) treats `GetUserSiteID` failure as a hard error returned to the caller. The asymmetry means a transient DB failure on `users` causes silent cross-site divergence with no client-visible signal — the user mutes on the room site but their home site keeps the old value forever. Either match `handleMessageRead`'s hard-error behaviour or document the intentional degraded-mode trade-off (and consider a retry/backfill mechanism).

### (f) Client-API doc rule

`docs/client-api.md` is updated in this branch with a full new "Toggle Mute" section (50 lines). Subject, request body, success response, error cases, triggered events, cross-site behaviour, and the `notification-worker` follow-up caveat are all documented. ✓

**[low]** The docs section lists the not-a-member error as `"only room members can list members"` (the reused `errNotRoomMember` sentinel from `helper.go:25`). The string is semantically misleading for a mute operation. Either rename the sentinel or call out the reuse in the docs note.

### Verdict

The implementation is functionally correct and pattern-compliant. The one actionable blocker is the silent skip of the cross-site outbox on `GetUserSiteID` error (`handler.go:1280-1283`), which diverges from `handleMessageRead`'s precedent and risks undetected cross-site inconsistency.

---

## Service: inbox-worker

### (a) Diff correctness against existing handler conventions

**[low]** The new `handleSubscriptionMuteToggled` (`handler.go:212-221`) is structurally identical to `handleSubscriptionRead` (`handler.go:196-206`): unmarshal inner event → call store method → wrap error with `"short description: %w"`. Dispatch case is placed immediately after `subscription_read` in the switch, mirroring how `subscription_read` was slotted between `role_updated` and `thread_subscription_upserted`. Shape correct, no issues.

### (b) Scope drift / refactor-readiness

**[medium]** `inbox-worker/main.go:39-195` — The `mongoInboxStore` struct and all its methods remain embedded in `main.go`, which now spans ~200 lines of store implementation before `main()` even starts. The plan (Task 8 step 1) explicitly noted: "Modify: `inbox-worker/store.go` (interface) and `inbox-worker/store_mongo.go` (impl) — confirm exact filenames first." The split never happened. Adding `UpdateSubscriptionMute` to `main.go:118-129` follows the existing pattern, but the growing inline store is now clearly beyond the per-service layout in CLAUDE.md (`main.go` = config + wiring + startup). Pre-existing tech debt that this PR adds to. Worth a follow-up split before the next handler grows the inline store further.

### (c) Abstraction changes

**[low]** `UpdateSubscriptionMute` earns its keep. It parallels `UpdateSubscriptionRead` one-to-one in the interface (`handler.go:33-36`) and in the Mongo impl (`main.go:120-129`). The no-op-on-missing-subscription semantic is documented in both the interface comment and the store comment.

**[nitpick]** `inbox-worker/main.go:128` — `UpdateSubscriptionMute` discards `res *UpdateResult` (assigns to `_`) and does NOT check `res.MatchedCount` before returning nil, while `UpdateSubscriptionRoles` (`main.go:60-70`) DOES return an error on `MatchedCount == 0`. The asymmetry is intentional (plan explicitly requires silent no-op on missing subscription) and is documented in both the interface and store comments — but a one-line inline comment (`// MatchedCount 0 is intentional — missing subscription is a federation-race no-op`) on the `return nil` would close that reading ambiguity for the next developer.

### (d) Design coherence

**[low]** The handler correctly fits inbox-worker's stated job: mirror a room's-home-site write onto the user's home-site subscription copy. The `OutboxEvent.Type == "subscription_mute_toggled"` dispatch case (`handler.go:66-67`) wires correctly against the constant `model.OutboxSubscriptionMuteToggled`. The silent-no-op semantic is encoded end-to-end (store Mongo impl → store interface doc → handler comment). No design issues.

### (e) Project-pattern adherence

- **Federation wiring**: dispatch string `"subscription_mute_toggled"` matches `model.OutboxSubscriptionMuteToggled` (`pkg/model/event.go:85`). `handler_test.go` uses the const directly, not a bare string. ✓
- **Mongo write**: direct driver, `bson.M` filter + `$set` update, no ORM. ✓
- **Error wrapping**: `"unmarshal subscription_mute_toggled payload: %w"` and `"update subscription mute for %q in room %q: %w"` — both follow the `"short description: %w"` convention. ✓

**[low]** The plan prescribed `gomock` + `make generate SERVICE=inbox-worker` for the new test (Task 8 steps 5/8). The landed implementation uses a hand-written `stubInboxStore` in `handler_test.go` instead. The plan was wrong about inbox-worker's testing style — the landed code correctly matches the existing convention. Worth noting for future plans against this service.

### (f) Client-API doc rule

N/A — inbox-worker has no `nc.QueueSubscribe` handlers on `chat.user.…` client-facing subjects.

### Test coverage

Three targeted tests cover the happy path (`TestHandler_SubscriptionMuteToggled`), missing-subscription no-op (`TestHandler_SubscriptionMuteToggled_MissingSubscriptionNoOp`), and malformed payload (`TestHandler_SubscriptionMuteToggled_MalformedPayload`). Missing: a store-error propagation test (see Test-automation chapter).

### Verdict

The diff is mechanically correct and safe to merge. Actionable items: a one-line clarity comment on the intentional `MatchedCount` omission (`main.go:128`), and a follow-up PR to split the inline store from `main.go` into `store.go`/`store_mongo.go` before the next handler grows it further.

---

## Service: room-worker

Scope: Test-only change — `room-worker/integration_test.go` had 8 occurrences of `DisableNotification` renamed to `DisableNotifications` (matching the field-rename in `pkg/model/subscription.go:42`). No production code in `room-worker/` was touched.

### Findings

All 8 occurrences are renamed and semantically equivalent:
- Struct-literal fixtures (lines ~1320, ~1397): the boolean values are preserved.
- `assert.True` calls and their message strings (lines ~1350, ~1352, ~1432, ~1434): same field, new name.
- Doc-comment references (lines ~1288, ~1365): updated for accuracy.

The cosmetic alignment adjustment (one space → two spaces after the colon to maintain column alignment with surrounding fields) is consistent with the rest of the struct literal.

The test continues to use `testutil.MongoDB`, `testutil.NATS`, and a `TestMain` with `testutil.RunTests(m)` — consistent with CLAUDE.md Section 4. No inline container usage was introduced. Scope discipline maintained.

### Verdict

No findings. The change is a correct, complete mechanical rename with no omissions or semantic drift — approved as-is.

---
