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

## Go expert

### Findings

**[high]** `room-service/handler.go:1245-1264` — TOCTOU window between the pre-fetch and the atomic toggle. `GetSubscription` exists to (a) gate membership and (b) supply the full `Subscription` struct for the downstream `SubscriptionUpdateEvent`. `ToggleSubscriptionMute` already returns `ErrSubscriptionNotFound` on a missing document, so the pre-fetch primarily serves the event payload. Between the two calls a concurrent writer could delete the subscription (yielding a different error class) or update another field — the constructed `updatedSub := *sub` copy then carries pre-fetch values for everything except `DisableNotifications`, which is the post-flip value. Either project the necessary fields directly in `ToggleSubscriptionMute`'s `FindOneAndUpdate` (eliminating the pre-fetch) or accept the staleness with a comment acknowledging it.

**[medium]** `pkg/model/event.go:35` — `SubscriptionUpdateEvent.Action` comment says `// "added" | "removed"`. This branch introduces `"mute_toggled"` (line 1267 in `room-service/handler.go`), and earlier work already added `"role_updated"`. Clients parsing this field rely on the comment as the contract. Update to `// "added" | "removed" | "role_updated" | "mute_toggled"`.

**[medium]** `room-service/handler.go:1242` — `fmt.Errorf("invalid mute-toggle subject: %s", subj)` is not sentinel-wrapped, so `errors.Is(err, errInvalidSubject)` cannot match. The codebase already uses sentinels elsewhere (`errInvalidRole`, `errNotRoomMember`, etc.). Introducing an `errInvalidMuteToggleSubject` sentinel would also fix the `sanitizeError` passthrough issue flagged in the Bug & Security chapter.

**[medium]** Missing test for the cross-site outbox `publishToStream` failure path. `handleMuteToggle` returns a hard error (`"publish mute-toggled outbox: %w"`) at line 1308 if the JetStream publish fails after the DB write committed. `TestHandler_MuteToggle_CrossSitePublishesOutbox` only covers the success path; the analogous `TestHandler_MessageRead_CrossSite_PublishFailureAborts` shows the pattern to follow.

**[medium]** `room-service/handler.go:1280-1283` vs `1305-1308` — Asymmetric error handling: `GetUserSiteID` failure silently returns OK, but `publishToStream` failure on the same cross-site path returns a hard error. The asymmetry is defensible (publish has JetStream retries; the users-collection read does not) but it's undocumented and surprises a reader. Add an inline comment explaining the trade-off.

**[low]** `room-service/handler.go:44` — `NewHandler` now has 10 positional parameters, including two adjacent `func(context.Context, string, []byte) error` closures (`publishToStream`, `publishCore`). They can be silently swapped at the call site with no compile error. A `HandlerConfig` struct or functional options would be more idiomatic at this arity. Not introduced by this PR but worsened by it.

**[low]** `pkg/model/event.go:225` — `SubscriptionMuteToggledEvent` carries `bson` tags it will never use (the struct is only JSON-encoded as `OutboxEvent.Payload`, never stored in Mongo). Consistent with `SubscriptionReadEvent` on line 94, so accepted as pattern-conformance noise.

**[nitpick]** `pkg/model/event.go:219` — `MuteToggleResponse.Status` is always the constant `"ok"`. Encoding a fixed string as a struct field rather than a typed const means it can only be tested by string comparison. Cosmetic.

**[nitpick]** `inbox-worker/main.go:120-129` — `UpdateSubscriptionMute` implementation lives in `main.go` rather than a `store_mongo.go`. Consistent with the pre-existing `UpdateSubscriptionRead` on the same `mongoInboxStore`, but the pattern itself deviates from the per-service layout in CLAUDE.md. Out of scope for this PR; see inbox-worker chapter for the follow-up split recommendation.

### Verdict

Logically sound and well-tested for happy and most error paths. The TOCTOU window in the two-stage subscription lookup, the stale `Action` enum comment (a client-facing contract), and the missing outbox-failure test case should all be addressed before merge.

---

## Test automation

### Mock staleness check

`make generate SERVICE=room-service` and `make generate SERVICE=inbox-worker` both produced **zero diff**. Mocks are fresh. Working tree clean after the check.

### TDD compliance for new exported functions

| New exported function | Test |
|---|---|
| `subject.MuteToggle` | `TestMuteToggle` ✓ |
| `subject.MuteToggleWildcard` | `TestMuteToggleWildcard` ✓ |
| `model.MuteToggleResponse` | `TestMuteToggleResponseJSON` ✓ |
| `model.SubscriptionMuteToggledEvent` | `TestSubscriptionMuteToggledEventJSON` ✓ |
| `model.OutboxSubscriptionMuteToggled` const | `TestOutboxSubscriptionMuteToggledConst` ✓ |
| `MongoStore.ToggleSubscriptionMute` (room-service) | `TestMongoStore_ToggleSubscriptionMute` ✓ |
| `mongoInboxStore.UpdateSubscriptionMute` (inbox-worker) | **MISSING — see [low] below** |

All other new functions are unexported. TDD compliance: PASS at the unit level, partial gap at integration level.

### Findings

**[medium]** `room-service/handler_test.go` — `handleMuteToggle`'s `GetSubscription` **generic** error arm (`case err != nil` at `handler.go:1249`, returns `"get subscription: %w"`) has no test. `TestHandler_MuteToggle_NotRoomMember` covers `ErrSubscriptionNotFound` but the generic-error path is untested. The analogous `TestHandler_MessageRead_GetRoomError` shows the expected pattern.

**[medium]** `room-service/handler_test.go` — `handleMuteToggle`'s **cross-site outbox `publishToStream` failure** is untested. The handler returns a hard error (`"publish mute-toggled outbox: %w"`) at line 1308; `TestHandler_MuteToggle_CrossSitePublishesOutbox` only covers success. See `TestHandler_MessageRead_CrossSite_PublishFailureAborts` for the pattern.

**[low]** `room-service/handler_test.go` — `GetUserSiteID` non-fatal soft path untested. When `GetUserSiteID` returns an error, the handler logs a warning and still returns `"ok"`. This unusual "success despite store error" behaviour deserves a test to pin the intent, especially since it's inconsistent with `handleMessageRead`'s hard-error treatment of the same call.

**[low]** `room-service/handler_test.go` — `publishCore` non-fatal failure untested. Line 1275-1277 swallows the error with `slog.Error` and continues. No test asserts that the handler still returns `"ok"` when `publishCore` fails.

**[low]** `inbox-worker/handler_test.go` — `UpdateSubscriptionMute` **store-error propagation** untested. `handleSubscriptionMuteToggled` (line 217) wraps store errors and returns them. The existing pattern in the same file uses small embedded-stub types (e.g. `errorDeleteStore`, `errorThreadSubStore`) to inject store errors. Missing: a `TestHandler_SubscriptionMuteToggled_StoreError` analogue.

**[low]** `inbox-worker/integration_test.go` — `mongoInboxStore.UpdateSubscriptionMute` has no integration test. All other `mongoInboxStore` methods have counterpart integration tests; this method was added to `main.go` without one. Below the 80% coverage floor required by CLAUDE.md §4 for new Mongo code paths.

**[nitpick]** `room-service/handler_test.go:3205-3360` — Five flat `TestHandler_MuteToggle_*` functions could be a single table-driven test. They share the same call site (`h.handleMuteToggle`) and differ only in setup/expectation. The file already shows the table-driven style (`TestHandler_handleMessageReadReceipt` at line 2800).

### Coverage depth

- `handleMuteToggle` (5 tests): ~70-75% branch coverage. Missing: `GetSubscription` generic error, `GetUserSiteID` error (non-fatal), `publishCore` failure, `publishToStream` failure (cross-site). Below the 90% target for core handler logic.
- `handleSubscriptionMuteToggled` (3 tests): ~75% branch coverage. Missing: store error propagation.
- `TestMongoStore_ToggleSubscriptionMute`: solid — covers false→true, persistence, true→false, not-found sentinel.

### Mock/integration discipline + race detector

- Unit tests use gomock in `room-service`, hand-written stubs in `inbox-worker` — both correct for their service's existing convention.
- No inline `testcontainers` added.
- `TestMain` already exists in both `integration_test.go` packages.
- No raw `go test` invocations added — everything runs through `make test` which already passes `-race`.

### Verdict

No `critical` or `high` findings. Mocks are fresh, TDD discipline followed at the exported-function level, all tests pass with `-race`. The two `medium` gaps (`GetSubscription` generic error + `publishToStream` cross-site failure) bring effective handler branch coverage below the 90% target — each is a small mechanical addition following patterns already in the same file.

---

## Bug & security

### SAST results

| Tool | Result |
|---|---|
| gosec | PASS — no medium+ findings introduced |
| govulncheck | PASS — no vulnerabilities |
| semgrep | NOT RUN — binary not installed in env; environment limitation, not a code defect |

### Findings

**[medium]** `room-service/handler.go:1242` + `room-service/helper.go:202` — Invalid-subject error string silently downgraded to `"internal error"` by `sanitizeError`. `handleMuteToggle` produces `fmt.Errorf("invalid mute-toggle subject: %s", subj)`. The `sanitizeError` safe-prefix list in `helper.go:202` includes `"invalid request"` and similar prefixes but NOT `"invalid mute-toggle"`. Result: NATS replies to clients carry `"internal error"` instead of the documented `"invalid mute-toggle subject: …"`. The unit tests at `handler_test.go:3340` call `handleMuteToggle` directly and never pass through `sanitizeError`, so the mismatch is not caught. `docs/client-api.md` explicitly documents `"invalid mute-toggle subject: …"` as the error clients should see — **the API contract is broken at delivery**. Fix: add `"invalid mute-toggle"` to the safe-prefix list, OR introduce an `errInvalidMuteToggleSubject` sentinel and wire it through.

**[medium]** `room-service/handler.go:1280-1283` — `GetUserSiteID` failure silently returns `"ok"` and skips the cross-site outbox publish. If `GetUserSiteID` errors on a transient or permanent DB issue, the local toggle has already committed but the federation event is dropped. The client considers the RPC successful; the user's home site never receives the mute update and permanently disagrees with the room site. Inconsistent with the analogous `handleMessageRead` (`handler.go:1010`) which propagates the `GetUserSiteID` error. (Surfaced independently by 3 reviewers — room-service generalist, Go expert, and this lens.)

**[low]** `room-service/handler.go:1253-1258` — Redundant `ErrSubscriptionNotFound` guard after the prior membership check at line 1245. `GetSubscription` already gatekeeps; the second check is unreachable in practice. Dead code that misleads readers about atomicity guarantees.

**[nitpick]** `room-service/handler.go:1230` — `slog.Error("mute toggle failed", "error", err, "subject", m.Msg.Subject)` logs the raw NATS subject on every error, including malformed-subject errors. The subject encodes `account` and `roomID` (identifiers, not secrets); not a CLAUDE.md violation but worth noting that this handler logs an extra `subject` field that peer handlers do not.

**[nitpick]** `inbox-worker/main.go:121` — `UpdateSubscriptionMute` discards `*UpdateResult` (assigns to `_`). Intentional per the design spec — missing-subscription is a silent no-op for federation races. Worth a one-line inline comment justifying the discard.

### Specific trust assertions

1. **Membership check before toggle** ✓ — `handleMuteToggle` calls `store.GetSubscription` at line 1245 before `ToggleSubscriptionMute`. A non-member cannot mutate state at the store layer.
2. **Mongo filter injection safety** ✓ — Filter `{"roomId": roomID, "u.account": account}` uses strings parsed by `subject.ParseUserRoomSubject` from the NATS subject. Subject tokens are dot-delimited and the parser rejects malformed inputs before reaching the store. No object-injection surface.
3. **Outbox payload trust** ✓ — `inbox-worker`'s `UpdateSubscriptionMute` receives `disableNotifications` from the outbox payload, which originates from `room-service`'s atomic Mongo write result (`ToggleSubscriptionMute` post-flip value) — not from client input. The trust assumption is that federated JetStream delivery from a peer site is trustworthy, consistent with the rest of `inbox-worker`.
4. **OutboxEvent assembly** ✓ — `Type`, `SiteID`, `DestSiteID`, `Payload`, `Timestamp` are all set in `handleMuteToggle` at lines 1294-1300. No fields missing.
5. **`sanitizeError` leak surface** ✓ — Internal Mongo error strings (e.g. `"db down"`) are caught by the catch-all `"internal error"` fallback at `helper.go:209`, but the same fallback ALSO catches the legitimate `"invalid mute-toggle subject"` string — see medium-1 above.

### Verdict

No security vulnerabilities, no race conditions, no injection surface. Two `medium` correctness bugs: the `sanitizeError` gap breaks the documented API contract on a single error path, and the `GetUserSiteID` silent-success creates reachable cross-site data divergence inconsistent with the rest of the codebase. Both should be fixed before merge.

---

## Performance

### Findings

**[medium]** `room-service/handler.go:1245-1253` — Redundant Mongo round-trip before atomic write. `GetSubscription` runs before `ToggleSubscriptionMute` solely to (a) check membership and (b) supply the full `Subscription` struct for the downstream `SubscriptionUpdateEvent`. Both can be addressed without the extra round-trip:
- Membership check: `ToggleSubscriptionMute` already returns `ErrSubscriptionNotFound` on filter mismatch.
- Full struct for event: `sub.User.ID` is the only field beyond `disableNotifications` actually consumed. The atomic toggle could project the needed fields directly.

This is the highest-priority performance issue since it doubles the per-request DB latency on every mute toggle.

**[medium]** `inbox-worker/main.go:121-129` + `inbox-worker/main.go` `ensureIndexes` — `UpdateSubscriptionMute` filters on `{roomId, u.account}` but `inbox-worker`'s `ensureIndexes` (around `main.go:151`) only creates the `thread_subscriptions (threadRoomId, userId)` index. It does NOT create or assert the `(roomId, u.account)` index on `subscriptions`. The index is owned by whichever service first bootstraps the collection (typically `room-service` or `room-worker`), but `inbox-worker`'s `ensureIndexes` silently relies on it. If `inbox-worker` is deployed against a fresh collection (e.g. a new site in integration tests), the update degrades to a full collection scan. Either add the (idempotent) `CreateOne` call mirroring `room-service`'s `EnsureIndexes`, or document the cross-service index dependency.

**[low]** `room-service/handler.go:1280` — `GetUserSiteID` runs strictly sequentially after the write and the `publishCore` call. It hits `users` (a different collection from `subscriptions`). It could overlap with `publishCore` (lines 1275-1278) but `publishCore` is non-fatal and core-NATS publish is very fast, so parallelisation adds complexity for ~µs gain. `users.account` index exists (`room-service/store_mongo.go:63-66`) — query is O(1). Not worth optimising.

**[low]** `room-service/handler.go:1263` — `updatedSub := *sub` copies the entire `Subscription` struct just to mutate `DisableNotifications`. The copy is then embedded by value in `SubscriptionUpdateEvent`. If `Subscription` grows in the future, this becomes a per-request allocation hotspot. Consider holding the event sub by pointer or constructing it from individual fields.

**[nitpick]** `room-service/handler.go:1307` — `publishToStream` (which calls `js.PublishMsg(ctx, ...)`) blocks waiting for JetStream PubAck with an unbounded context derived from `wrappedCtx(m)`. If the NATS server is overloaded, the handler goroutine blocks indefinitely. A `context.WithTimeout` of ~5s would bound the wait. Same pattern as the rest of the service (not introduced by this branch), but the new cross-site path makes it more reachable.

### Index audit summary

| Operation | Filter | Index | Status |
|---|---|---|---|
| `ToggleSubscriptionMute` (room-service) | `(roomId, u.account)` | unique compound at `store_mongo.go:57-61` | ✓ |
| `GetSubscription` (room-service) | `(roomId, u.account)` | same | ✓ |
| `GetUserSiteID` (room-service) | `account` | `users.account` at `store_mongo.go:63-66` | ✓ |
| `UpdateSubscriptionMute` (inbox-worker) | `(roomId, u.account)` | not declared in inbox-worker `ensureIndexes` | ✗ medium |

### Verdict

No critical issues. The new store operations hit covered indexes on the room-service side. The redundant `GetSubscription` round-trip (medium) doubles per-request DB latency on every mute toggle and could be eliminated by widening `ToggleSubscriptionMute`'s projection. The missing index assertion in `inbox-worker.ensureIndexes` is a medium latent risk.

---

## Observability

### Findings

**[medium]** `room-service/handler.go:1239` — `handleMuteToggle` is missing span attributes. The closest structural analogue `handleMessageRead` also doesn't set them (so matching that precedent is defensible), but `handleMessageReadReceipt` (line 1105) and `publishCreateRoom` (line 349) DO set `room.id` / `site.id`. Adding `span.SetAttributes(attribute.String("room.id", roomID), attribute.String("site.id", h.siteID))` would cost two lines and improve trace correlation for a per-room operation.

**[medium]** `room-service/handler.go:1282` — `slog.Warn("get user siteId failed; skipping outbox", "error", err, "account", account)`. The `Warn` level signals "degraded but OK", but `GetUserSiteID` returning an error means a real Mongo failure on `users` (not-found is `("", nil)`, not an error). The handler then silently suppresses federation. **Either**: (a) match `handleMessageRead`'s precedent and propagate the error (hard fail), OR (b) keep the soft-fail but escalate to `slog.Error` so operators know cross-site federation is silently dropping events.

**[nitpick]** `room-service/handler.go:1230` — `natsMuteToggle` logs `slog.Error` with an extra `subject` structured field. The peer `natsMessageRead` (line 971) doesn't. The new field is an improvement for debuggability — propagate to peers in a follow-up, or accept the inconsistency.

**[low]** No Prometheus metric on the new RPC path. The codebase doesn't add explicit metrics per-handler — OTel + slog is the observability mechanism. No action needed.

### Discipline checks — all PASS

1. **slog/JSON discipline**: all four new log calls use `slog` with structured key-value pairs. No `fmt.Println` / `log.Println` / text loggers. ✓
2. **No secret leakage**: tokens/passwords/full message bodies not logged. `account` is a non-secret identifier; existing handlers log it identically (e.g. `handler.go:1029`). ✓
3. **Request-ID propagation**: `natsMuteToggle` (line 1227) calls `ctx := wrappedCtx(m)` and passes that `ctx` to `handleMuteToggle`. Context flows through every store call and publish. ✓
4. **Error log levels**: `slog.Error` for the publish-failure non-fatal path is appropriate (operators need to know NATS publish is failing even though the RPC succeeds). The `slog.Warn` for the `GetUserSiteID` path is the one questionable case — see medium-2 above.
5. **inbox-worker error handling**: `handleSubscriptionMuteToggled` returns errors to the consumer loop without per-handler slog, matching `handleSubscriptionRead`. The consumer loop in `main.go:261` logs all returned errors at `slog.Error` with `request_id`. Pattern is consistent. ✓

### Verdict

PASS with two `medium` findings — both about the `GetUserSiteID` failure path (missing span attrs is a minor instrumentation gap; the Warn-vs-Error level is a meaningful operator-signal choice). Everything else — slog-only JSON, `wrappedCtx` propagation, no secret leakage, inbox-worker error pattern — is correct.

---
