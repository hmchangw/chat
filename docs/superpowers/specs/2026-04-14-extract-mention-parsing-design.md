# Extract Mention Parsing & Resolution into Shared `pkg/mention` Package

**Date:** 2026-04-14
**Status:** Approved

## Summary

Extract `@mention` parsing **and** user resolution from `message-worker` and `broadcast-worker` into a shared `pkg/mention` package. The package provides two levels of API:

1. **`mention.Parse(content)`** — regex-based parsing, returns accounts + mentionAll flag (stdlib-only, no DB)
2. **`mention.Resolve(ctx, content, lookupFn)`** — parse + DB lookup + Participant building in a single call

Both services use `mention.Resolve` as their primary entry point. This eliminates `message-worker/resolveMentions`, `broadcast-worker/buildMentionParticipants`, and the duplicated parse-lookup-build pipeline.

## Motivation

- `message-worker/resolveMentions` and `broadcast-worker/buildMentionParticipants` implement the same pipeline: parse content → look up users by account → build `[]model.Participant` → handle `@all`
- Both services had separate parsing functions with divergent behavior (regex vs whitespace splitting) — now unified via `mention.Parse`
- The resolve/build step is still duplicated: `message-worker` has `resolveMentions` (a handler method), `broadcast-worker` has `buildMentionParticipants` (a standalone function that takes a pre-built userMap)
- The remaining differences between the two are not hard requirements:
  - **UserID in Participant:** broadcast-worker omitted it, but `Participant.UserID` has `json:"userId,omitempty"` — including it is additive and harmless to clients
  - **Fallback names for unresolved accounts:** broadcast-worker fell back to raw account names; skipping unresolved is cleaner (showing `@nonexistent` as a mention with display name `nonexistent` is misleading)
  - **Error handling:** each caller already handles errors differently, and `Resolve` returns partial results on error to support both strategies
- Future services consuming MESSAGES_CANONICAL (e.g. notification-worker, search-sync-worker) will need the same pipeline — a shared `Resolve` prevents further duplication

## Design

### 1. `pkg/mention` Package

**File:** `pkg/mention/mention.go` (existing — add `Resolve`)

```go
// LookupFunc abstracts user-by-account lookup so Resolve is testable without a real DB.
type LookupFunc func(ctx context.Context, accounts []string) ([]model.User, error)

// ResolveResult holds mention resolution output.
type ResolveResult struct {
    Participants []model.Participant // enriched mentioned users + @all entry if present
    MentionAll   bool                // true if @all or @here was mentioned
    Accounts     []string            // raw parsed accounts (for caller use outside resolution)
}

// Resolve parses @mentions from content, looks up users via lookupFn, and builds
// Participants. On lookup error, returns partial result (MentionAll and Accounts
// populated, Participants empty) along with the error — callers decide whether to
// fail or fall back.
func Resolve(ctx context.Context, content string, lookupFn LookupFunc) (*ResolveResult, error)
```

Key behaviors:
- Calls `Parse(content)` internally — no double-parsing needed
- Looks up only non-`@all`/`@here` accounts via `lookupFn`
- Builds `Participant` with `UserID`, `Account`, `ChineseName`, `EngName` from matched `model.User`
- Appends `{Account: "all", EngName: "all"}` when `MentionAll` is true
- Accounts not found by `lookupFn` are silently skipped (not included in `Participants`)
- On error, `result.MentionAll` and `result.Accounts` are always populated — `Participants` may be empty

`Parse` remains available for callers that only need parsing without DB lookup.

### 2. `message-worker` Changes

**File:** `message-worker/handler.go`

- Remove `resolveMentions` method entirely
- `processMessage` calls `mention.Resolve` directly:

```go
func (h *Handler) processMessage(ctx context.Context, data []byte) error {
    // ... unmarshal ...
    resolved, err := mention.Resolve(ctx, evt.Message.Content, h.userStore.FindUsersByAccounts)
    if err != nil {
        return fmt.Errorf("resolve mentions: %w", err)
    }
    evt.Message.Mentions = resolved.Participants
    // ... sender lookup, save ...
}
```

**File:** `message-worker/handler_test.go`

- Existing `TestHandler_ProcessMessage` cases unchanged (same mock expectations, same assertions)

### 3. `broadcast-worker` Changes

**File:** `broadcast-worker/handler.go`

- Remove `buildMentionParticipants` function
- Replace the manual parse + lookup + build pipeline with `mention.Resolve`:

```go
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
    // ... unmarshal, get room ...
    resolved, err := mention.Resolve(ctx, msg.Content, h.userStore.FindUsersByAccounts)
    if err != nil {
        slog.Warn("mention resolve failed", "error", err)
    }

    // Side effects use parsed data from ResolveResult
    if err := h.store.UpdateRoomOnNewMessage(ctx, room.ID, msg.ID, msg.CreatedAt, resolved.MentionAll); err != nil { ... }
    if len(resolved.Accounts) > 0 {
        if err := h.store.SetSubscriptionMentions(ctx, room.ID, resolved.Accounts); err != nil { ... }
    }

    // Sender lookup (separate call — previously batched with mentions)
    senderMap := make(map[string]model.User)
    senderUsers, err := h.userStore.FindUsersByAccounts(ctx, []string{msg.UserAccount})
    if err != nil {
        slog.Warn("sender lookup failed, falling back to account", "error", err)
    } else {
        for _, u := range senderUsers { senderMap[u.Account] = u }
    }

    clientMsg := buildClientMessage(&msg, senderMap)

    // Use resolved.Participants directly for mentions in events
    switch room.Type {
    case model.RoomTypeGroup:
        return h.publishGroupEvent(ctx, room, clientMsg, resolved.MentionAll, resolved.Participants)
    case model.RoomTypeDM:
        return h.publishDMEvents(ctx, room, clientMsg, resolved.Accounts)
    }
}
```

**File:** `broadcast-worker/handler_test.go`

- User lookup mocks split: one for mention accounts (inside `Resolve`), one for sender
- Remove `assert.Empty(t, m.UserID)` — UserID is now populated for resolved mentions
- Remove `TestBuildMentionParticipants` — function deleted
- For unresolved mentions: they are now silently skipped instead of showing fallback names

### 4. Behavioral Changes in `broadcast-worker`

| Aspect | Before | After | Rationale |
|--------|--------|-------|-----------|
| UserID in mention Participants | Empty | Populated from User | `omitempty` makes it additive; client can use it for profile links |
| Unresolved mentions | Fallback to account name | Skipped | Showing `@nonexistent` with display name `nonexistent` is misleading |
| DB calls | 1 (batched sender+mentions) | 2 (mentions in Resolve + sender) | Trivial overhead for a simple `$in` query; cleaner separation of concerns |
| Mention resolve error | N/A (warn on combined lookup) | Warn + use empty Participants | Consistent with existing error tolerance |

### 5. Unchanged

- `broadcast-worker/buildClientMessage` — still builds sender Participant from userMap
- `broadcast-worker/publishDMEvents` — still uses `resolved.Accounts` for per-user mention detection
- Store interfaces — unchanged
- All other services — no changes

## Approach Rationale

- **`Resolve` in shared package:** The parse → lookup → build pipeline is the same in both services. Extracting it eliminates the handler method (`resolveMentions`) and standalone function (`buildMentionParticipants`) that duplicated this logic.
- **`LookupFunc` parameter:** Avoids coupling `pkg/mention` to a specific store interface. Both services pass `h.userStore.FindUsersByAccounts` as a method value. Tests inject a simple function.
- **Partial result on error:** `ResolveResult` always returns `MentionAll` and `Accounts` even when lookup fails. This lets `message-worker` fail fast while `broadcast-worker` warns and continues.
- **Accept extra DB call in broadcast-worker:** Previously broadcast-worker batched sender + mention lookups into one `FindUsersByAccounts` call. With `Resolve`, mentions are looked up inside `Resolve` and the sender is a separate call. This is one extra MongoDB `$in` query — trivial for a single account. The simplification in handler code outweighs the minor overhead.
- **Drop fallback names:** The previous `buildMentionParticipants` showed unresolved accounts with `ChineseName: account, EngName: account`. This was a display hack — if a user doesn't exist, including them as a mention with their raw account as display name is misleading. Skipping them (as `message-worker` already does) is cleaner.
- **Include UserID:** `Participant.UserID` uses `json:"userId,omitempty"`. Including it is a strictly additive change — existing clients that don't use it are unaffected, while new clients can leverage it for profile links or mention highlighting.
