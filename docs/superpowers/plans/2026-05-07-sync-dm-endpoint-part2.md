# Sync DM Endpoint — Part 2: Remove `HistoryConfig.SharedSince`

## Context

`pkg/model/member.go:19` defines `HistoryConfig` with two fields:

```go
type HistoryConfig struct {
    Mode        HistoryMode `json:"mode" bson:"mode"`
    SharedSince *int64      `json:"sharedSince,omitempty" bson:"sharedSince,omitempty"`
}
```

`SharedSince` is a caller-supplied override on `AddMembersRequest.History`. When `Mode == HistoryModeNone` and `SharedSince` is non-nil and positive, room-worker uses it as the per-subscription `HistorySharedSince` (and on the outbound `MemberAddEvent`) instead of `req.Timestamp` (the request acceptance time).

**Why we're removing it.** The synchronous DM-create endpoint introduced in Part 1 has no caller that supplies an explicit cutoff — the DM endpoint always uses request-acceptance time as the cutoff. No production code path, no test outside the dedicated SharedSince-precedence tests, and no client API consumer (per `docs/client-api.md`) sets `SharedSince`. The field is dead weight: it complicates `historySharedSincePtr`, requires its own dedicated unit test (`handler_test.go:2401`), and forces every test that constructs `HistoryConfig{Mode: HistoryModeNone}` to think about whether it cares. Removing it collapses the helper to a single branch and simplifies the request schema before downstream code stabilizes around it.

**Scope.** This plan removes ONLY the `SharedSince` field on `HistoryConfig` and the override path in `room-worker`. The per-subscription `Subscription.HistorySharedSince` field is unaffected — that's the durable per-user cutoff stored on every subscription and read by history-service to gate message access. The plan also leaves `MemberAddEvent.HistorySharedSince` (the cross-site cutoff propagated to remote sites) intact: room-worker continues to populate it from `req.Timestamp` so remote-site sub creation matches home-site sub creation.

**Verification before acting.** This plan is built from the survey in the prior message. Before writing any code, the executor MUST re-confirm by running:

```
grep -rn "HistoryConfig{" /home/user/chat --include='*.go'
grep -rn "History\.SharedSince\|history\.SharedSince" /home/user/chat --include='*.go'
grep -rn "\"sharedSince\"" /home/user/chat
```

Expected hits — all under `pkg/model/member.go`, `room-worker/handler.go`, `room-worker/handler_test.go` (the three tests at lines ~1573–1652 and ~2401), and possibly `docs/client-api.md`. Anything outside these files is a surprise — STOP and reassess before continuing. In particular, if `auth-service`, `room-service`, `inbox-worker`, or `history-service` reference `History.SharedSince` directly, the scope has changed and this plan must be revised.

**Coordination with Part 1.** Part 1 (sync DM endpoint) does not write to `History.SharedSince` and does not read it. Part 2 can be done before Part 1, after Part 1, or in parallel — the two share no files except potentially `docs/client-api.md`. Recommend executing Part 2 *after* Part 1 lands, so Part 1's diff stays focused.

---

## Tasks

### Task 1 (Red): Update existing tests to drop `SharedSince`

Touch `room-worker/handler_test.go`:

1. **Delete** `TestProcessAddMembers_HistoryNone_ExplicitSharedSince` (around lines 1573–1612 — the test that sets `SharedSince: &explicitMs`). The behavior it asserts no longer exists.

2. **Delete** `TestHandler_ProcessAddMembers_HistorySharedSinceWinsOverTimestamp` (around lines 2401–2448). Same reason.

3. **Keep but verify unchanged**:
   - `TestProcessAddMembers_HistoryNone_NoTimestamp` (~line 1615) — already uses `HistoryConfig{Mode: HistoryModeNone}` with no `SharedSince`. Fallback behavior is preserved.
   - `TestProcessAddMembers_NoHistoryConfig_LeavesNil` (~line 1655) — already uses zero-value `HistoryConfig`. Behavior preserved.

4. **Find any other** `HistoryConfig{...SharedSince...}` literals via `grep -n 'SharedSince' room-worker/handler_test.go room-worker/integration_test.go`. Remove the field. Survey at planning time found all uses are in the two tests above, but the executor must re-grep to be safe.

5. Run `make test SERVICE=room-worker`. The two deletions should already pass; the rest should still pass without modification because the `SharedSince` field hasn't been removed from `model` yet — the tests just no longer reference it.

> **Why this is the Red step despite passing.** Per CLAUDE.md §"Test-Driven Development": the Red step is "write tests that fail without the change, pass with it." Here the change is *removal*, so Red is "delete tests that assert the removed behavior, leaving only tests that constrain the kept behavior." The kept tests will still pass at this point; Task 2's removal is what they protect.

**Commit:** `room-worker: drop tests for HistoryConfig.SharedSince override`

---

### Task 2 (Green): Remove the field and the override branch

1. **`pkg/model/member.go:19-23`** — remove the `SharedSince` field, leaving:

   ```go
   type HistoryConfig struct {
       Mode HistoryMode `json:"mode" bson:"mode"`
   }
   ```

2. **`room-worker/handler.go:58-79`** — collapse `historySharedSincePtr` to:

   ```go
   // historySharedSincePtr resolves the History.SharedSince value for an
   // add-member request. Mode != HistoryModeNone → nil (history is unrestricted).
   // Otherwise returns req.Timestamp (the request acceptance time).
   func historySharedSincePtr(history model.HistoryConfig, timestamp int64, roomID string) *int64 {
       if history.Mode != model.HistoryModeNone {
           return nil
       }
       if timestamp <= 0 {
           slog.Error("restricted history with missing timestamp, emitting nil", "roomID", roomID, "mode", history.Mode)
           return nil
       }
       return &timestamp
   }
   ```

   Note: keep the `roomID` parameter and `slog.Error` log line — they're load-bearing diagnostics, not just there to support the dropped branch. Both call sites (`handler.go:616` and `handler.go:729`) pass them already; their signatures don't change.

3. **`pkg/model/model_test.go:992`** — no change needed (already uses `HistoryConfig{Mode: HistoryModeAll}` with no `SharedSince`). Confirm the round-trip test still passes.

4. Run `make generate` (no store interface changed, but cheap to confirm), then `make lint` and `make test SERVICE=room-worker`. All green.

5. Run `make test` at repo root to catch any unexpected references in other services. If it fails, STOP — the survey missed something and the plan must be revised before continuing.

**Commit:** `pkg/model,room-worker: remove HistoryConfig.SharedSince override`

---

### Task 3 (Refactor): Documentation

1. **`docs/client-api.md`** — search for `sharedSince` (case-insensitive). If the add-members request schema documents the field, remove that line. If there's prose like "callers may supply an explicit cutoff via `history.sharedSince`", remove it. If `sharedSince` is not present in the doc, no change needed; record that observation in the commit body.

2. No CLAUDE.md change — the project guide doesn't reference this field.

3. Run `make lint` once more.

**Commit:** `docs/client-api: drop history.sharedSince from add-members schema` (skip this commit entirely if step 1 found nothing).

---

## Out of scope

- `Subscription.HistorySharedSince` (per-user cutoff stored on every subscription) — kept; it's the durable cutoff history-service gates on.
- `MemberAddEvent.HistorySharedSince` (cross-site event payload) — kept; remote sites need it to set the same cutoff on their local subs.
- `history-service` `GetHistorySharedSince` repo method and its tests (`history-service/internal/mongorepo/subscription.go`, `history-service/internal/service/threads_test.go`) — kept entirely; they read `Subscription.HistorySharedSince`, not `HistoryConfig.SharedSince`.
- Any migration of existing MongoDB documents — none needed. `HistoryConfig` is embedded only in the in-flight `AddMembersRequest` NATS event, never persisted. Once room-worker drains in-flight requests during deploy, no document carries `history.sharedSince` anywhere. (If the executor finds `HistoryConfig` persisted in a Mongo document during the verification grep, STOP — the assumption is wrong.)

## Risks

- **In-flight NATS messages during deploy.** If a request was published to `MESSAGES`/`ROOMS` with a non-nil `sharedSince` field before the deploy and is consumed after, the new `room-worker` will silently drop the override (Go's `encoding/json` ignores unknown fields by default). This is acceptable: no production caller sets the field today, and even if one did, falling back to `req.Timestamp` is the documented default.
- **Test flake risk.** None — the deleted tests are independent.

## Done when

- `grep -rn "SharedSince" /home/user/chat --include='*.go' | grep -v 'HistorySharedSince'` returns zero hits (the kept `Subscription.HistorySharedSince` field continues to match — the `grep -v` filters it out).
- `make lint && make test` pass at repo root.
- `make test-integration SERVICE=room-worker` passes.
