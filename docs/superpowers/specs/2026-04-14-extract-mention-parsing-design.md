# Extract Mention Parsing into Shared `pkg/mention` Package

**Date:** 2026-04-14
**Status:** Approved

## Summary

Extract the `@mention` parsing logic from `message-worker` into a shared `pkg/mention` package and replace the broadcast-worker's separate mention parsing functions (`detectMentionAll`, `extractMentionedAccounts`) with the same shared parser. Both services will use `mention.Parse(content)` as their single entry point for mention extraction.

## Motivation

- `message-worker` and `broadcast-worker` each implement their own mention parsing with different approaches:
  - `message-worker` uses a regex (`mentionRe`) that handles email-style mentions (`@user@domain.com`), dots, hyphens, and quoted reply prefixes (`>@user`)
  - `broadcast-worker` uses `strings.Fields` whitespace splitting, which misses mentions not preceded by whitespace and cannot handle email-style accounts
- The divergence means mentions are parsed inconsistently across the system — e.g. `@first.last` is recognized by `message-worker` but not by `broadcast-worker`
- Both services duplicate the logic for detecting `@all` / `@here` as special tokens
- Future services consuming MESSAGES_CANONICAL will also need mention parsing — a shared package prevents further duplication

## Design

### 1. New `pkg/mention` Package

**File:** `pkg/mention/mention.go`

```go
package mention

var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*(@[0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*)?)`)

type ParseResult struct {
    Accounts   []string // unique mentioned accounts, lowercased, excluding @all/@here
    MentionAll bool     // true if @all or @here was mentioned (case-insensitive)
}

func Parse(content string) ParseResult
```

Key behaviors:
- Uses the existing regex from `message-worker` (more robust than whitespace splitting)
- Lowercases all account names for consistent dedup (matches `broadcast-worker`'s existing behavior)
- Detects `@all` and `@here` case-insensitively as special tokens (`MentionAll: true`)
- Deduplicates accounts after lowercasing
- Returns `Accounts: nil` (not empty slice) when no individual mentions exist

### 2. `message-worker` Changes

**File:** `message-worker/handler.go`

- Remove local `mentionRe` regex and `parseMentions` function
- `resolveMentions` calls `mention.Parse(content)` instead of `parseMentions`
- Uses `parsed.Accounts` for user lookup and `parsed.MentionAll` for the special `{Account:"all"}` participant

```go
func (h *Handler) resolveMentions(ctx context.Context, content string) ([]model.Participant, error) {
    parsed := mention.Parse(content)
    if len(parsed.Accounts) == 0 && !parsed.MentionAll {
        return nil, nil
    }
    // ... user lookup with parsed.Accounts, append @all participant if parsed.MentionAll
}
```

**File:** `message-worker/handler_test.go`

- Remove `TestParseMentions` (moved to `pkg/mention/mention_test.go`)
- All existing `TestHandler_ProcessMessage` cases remain unchanged

### 3. `broadcast-worker` Changes

**File:** `broadcast-worker/handler.go`

- Remove `detectMentionAll` and `extractMentionedAccounts` functions
- `HandleMessage` calls `mention.Parse(msg.Content)` and uses `parsed.MentionAll` and `parsed.Accounts` directly

```go
parsed := mention.Parse(msg.Content)
mentionAll := parsed.MentionAll
mentionedAccounts := parsed.Accounts
```

The rest of the handler logic (`UpdateRoomOnNewMessage`, `SetSubscriptionMentions`, user lookup, event publishing) remains unchanged — only the source of `mentionAll` and `mentionedAccounts` changes.

**File:** `broadcast-worker/handler_test.go`

- Remove `TestDetectMentionAll` and `TestExtractMentionedAccounts` (covered by `pkg/mention` tests)
- All existing handler-level tests (`TestHandler_HandleMessage_GroupRoom`, `TestHandler_HandleMessage_DMRoom`, etc.) remain unchanged

### 4. Behavioral Differences Resolved

| Behavior | Before (`broadcast-worker`) | Before (`message-worker`) | After (shared) |
|----------|---------------------------|--------------------------|----------------|
| `@first.last` | Not recognized (whitespace split) | Recognized (regex) | Recognized |
| `@user@domain.com` | Not recognized | Recognized | Recognized |
| `>@user` quoted reply | Not recognized | Recognized | Recognized |
| `@All` / `@HERE` | Case-insensitive detection | Case-sensitive (`"all"` only) | Case-insensitive |
| `@here` | Treated as mention-all | Not handled (passed to DB lookup) | Treated as mention-all |
| Account case | Lowercased | Preserved | Lowercased |
| Dedup | Case-insensitive | Case-sensitive | Case-insensitive |

### 5. Unchanged

- `message-worker/resolveMentions` — remains a handler method (needs `userStore` for DB lookup)
- `broadcast-worker/buildMentionParticipants`, `buildClientMessage` — unchanged
- Store interfaces — unchanged
- All other services — no changes

## Approach Rationale

- **Shared package (not inline helper):** Follows the `pkg/userstore` and `pkg/subject` precedent. Multiple services need this, and the regex + special-token handling is non-trivial enough to warrant a single source of truth.
- **`Parse` returns a struct:** Cleaner than returning `([]string, bool)` — named fields make call sites self-documenting and allow future extension (e.g. adding `MentionHere bool` separately).
- **Lowercasing in `Parse`:** Accounts are case-insensitive identifiers. Lowercasing at parse time ensures consistent dedup and matches how `broadcast-worker` already behaves. The `message-worker`'s MongoDB `$in` lookup works with lowercase since accounts are stored lowercase.
- **`@here` handled as `MentionAll`:** The `broadcast-worker` already treats `@here` the same as `@all`. Unifying this in the shared parser means `message-worker` also gains `@here` support.
