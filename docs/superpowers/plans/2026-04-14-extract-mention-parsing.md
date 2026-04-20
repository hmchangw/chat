# Extract Mention Parsing & Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract `@mention` parsing and user resolution from `message-worker` and `broadcast-worker` into a shared `pkg/mention` package. The package provides `Parse` (regex-only) and `Resolve` (parse + DB lookup + Participant building). Both services converge on `mention.Resolve`, eliminating `message-worker/resolveMentions` and `broadcast-worker/buildMentionParticipants`.

**Architecture:** `mention.Parse(content)` returns `ParseResult{Accounts, MentionAll}`. `mention.Resolve(ctx, content, lookupFn)` calls `Parse` internally, looks up users via the injected `LookupFunc`, builds `[]model.Participant` (with UserID, @all entry), and returns `ResolveResult{Participants, MentionAll, Accounts}`. On error, partial results (MentionAll + Accounts) are always returned — callers decide whether to fail or fall back.

**Tech Stack:** Go 1.25, `regexp` (stdlib), `go.uber.org/mock` + `testify`.

---

## File Map

| File | Change |
|------|--------|
| `pkg/mention/mention.go` | Add `Resolve`, `LookupFunc`, `ResolveResult` (existing `Parse` and `ParseResult` unchanged) |
| `pkg/mention/mention_test.go` | Add `TestResolve` table-driven tests |
| `message-worker/handler.go` | Remove `resolveMentions`; call `mention.Resolve` directly in `processMessage` |
| `message-worker/handler_test.go` | Unchanged — same mock expectations and assertions |
| `broadcast-worker/handler.go` | Remove `buildMentionParticipants`; use `mention.Resolve` + separate sender lookup |
| `broadcast-worker/handler_test.go` | Split user lookup mocks; remove `TestBuildMentionParticipants`; update mention assertions (UserID now populated, unresolved accounts skipped) |

---

## Completed Tasks

### Task 1 — Create `pkg/mention` package with `Parse` ✅

Shared `Parse` function with regex and `ParseResult` type. 18 test cases.

### Task 2 — Update `message-worker` to use `mention.Parse` ✅

Removed local `mentionRe`, `parseMentions`. `resolveMentions` now calls `mention.Parse`.

### Task 3 — Update `broadcast-worker` to use `mention.Parse` ✅

Removed `detectMentionAll`, `extractMentionedAccounts`. `HandleMessage` uses `mention.Parse`.

---

## Task 4 — Add `mention.Resolve` and update `message-worker`

**Files:**
- Modify: `pkg/mention/mention.go`
- Modify: `pkg/mention/mention_test.go`
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Write failing tests for `Resolve`**

Add `TestResolve` to `pkg/mention/mention_test.go`:

```go
func TestResolve(t *testing.T) {
	bobUser := model.User{ID: "u-bob", Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃"}
	aliceUser := model.User{ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"}

	tests := []struct {
		name           string
		content        string
		lookupUsers    []model.User
		lookupErr      error
		wantAccounts   []string
		wantMentionAll bool
		wantParts      []model.Participant
		wantErr        bool
	}{
		{
			name:    "no mentions",
			content: "hello world",
		},
		{
			name:         "single mention resolved",
			content:      "hey @bob",
			lookupUsers:  []model.User{bobUser},
			wantAccounts: []string{"bob"},
			wantParts: []model.Participant{
				{UserID: "u-bob", Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃"},
			},
		},
		{
			name:         "multiple mentions resolved",
			content:      "@alice and @bob",
			lookupUsers:  []model.User{aliceUser, bobUser},
			wantAccounts: []string{"alice", "bob"},
			wantParts: []model.Participant{
				{UserID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"},
				{UserID: "u-bob", Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃"},
			},
		},
		{
			name:           "@all only — no lookup",
			content:        "hello @all",
			wantMentionAll: true,
			wantParts: []model.Participant{
				{Account: "all", EngName: "all"},
			},
		},
		{
			name:           "@all and individual",
			content:        "@all and @bob",
			lookupUsers:    []model.User{bobUser},
			wantAccounts:   []string{"bob"},
			wantMentionAll: true,
			wantParts: []model.Participant{
				{UserID: "u-bob", Account: "bob", EngName: "Bob Chen", ChineseName: "鮑勃"},
				{Account: "all", EngName: "all"},
			},
		},
		{
			name:         "unresolved account skipped",
			content:      "@alice and @unknown",
			lookupUsers:  []model.User{aliceUser},
			wantAccounts: []string{"alice", "unknown"},
			wantParts: []model.Participant{
				{UserID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"},
			},
		},
		{
			name:         "lookup error — partial result",
			content:      "hey @bob",
			lookupErr:    errors.New("db error"),
			wantAccounts: []string{"bob"},
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookupFn := func(_ context.Context, accounts []string) ([]model.User, error) {
				if tt.lookupErr != nil {
					return nil, tt.lookupErr
				}
				return tt.lookupUsers, nil
			}
			result, err := Resolve(context.Background(), tt.content, lookupFn)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			assert.Equal(t, tt.wantAccounts, result.Accounts)
			assert.Equal(t, tt.wantMentionAll, result.MentionAll)
			assert.Equal(t, tt.wantParts, result.Participants)
		})
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

```bash
make test SERVICE=pkg/mention
```

Expected: compilation error — `Resolve` undefined.

- [ ] **Step 3: Implement `Resolve` in `mention.go`**

Add to `pkg/mention/mention.go`:

```go
import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/pkg/model"
)

// LookupFunc abstracts user-by-account lookup so Resolve is testable without a real DB.
type LookupFunc func(ctx context.Context, accounts []string) ([]model.User, error)

// ResolveResult holds mention resolution output.
type ResolveResult struct {
	Participants []model.Participant
	MentionAll   bool
	Accounts     []string
}

// Resolve parses @mentions from content, looks up users via lookupFn,
// and builds Participants. On lookup error, returns partial result
// (MentionAll and Accounts populated, Participants empty) with the error.
func Resolve(ctx context.Context, content string, lookupFn LookupFunc) (*ResolveResult, error) {
	parsed := Parse(content)
	result := &ResolveResult{
		MentionAll: parsed.MentionAll,
		Accounts:   parsed.Accounts,
	}
	if len(parsed.Accounts) == 0 && !parsed.MentionAll {
		return result, nil
	}

	if len(parsed.Accounts) > 0 {
		users, err := lookupFn(ctx, parsed.Accounts)
		if err != nil {
			return result, fmt.Errorf("find mentioned users: %w", err)
		}
		for _, u := range users {
			result.Participants = append(result.Participants, model.Participant{
				UserID:      u.ID,
				Account:     u.Account,
				ChineseName: u.ChineseName,
				EngName:     u.EngName,
			})
		}
	}

	if parsed.MentionAll {
		result.Participants = append(result.Participants, model.Participant{
			Account: "all",
			EngName: "all",
		})
	}

	return result, nil
}
```

- [ ] **Step 4: Run `pkg/mention` tests**

```bash
make test SERVICE=pkg/mention
```

Expected: all `TestParse` and `TestResolve` subtests pass.

- [ ] **Step 5: Remove `resolveMentions` from `message-worker/handler.go`**

Replace the `resolveMentions` method and its call in `processMessage` with a direct `mention.Resolve` call:

```go
func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	resolved, err := mention.Resolve(ctx, evt.Message.Content, h.userStore.FindUsersByAccounts)
	if err != nil {
		return fmt.Errorf("resolve mentions: %w", err)
	}
	evt.Message.Mentions = resolved.Participants

	// ... sender lookup, save (unchanged) ...
}
```

- [ ] **Step 6: Run message-worker tests**

```bash
make test SERVICE=message-worker
```

Expected: all 9 `TestHandler_ProcessMessage` subtests pass unchanged.

- [ ] **Step 7: Run linter**

```bash
make lint
```

- [ ] **Step 8: Commit**

```bash
git add pkg/mention/mention.go pkg/mention/mention_test.go message-worker/handler.go
git commit -m "feat(mention): add Resolve function; remove message-worker resolveMentions"
```

---

## Task 5 — Update `broadcast-worker` to use `mention.Resolve`

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Replace mention pipeline in `HandleMessage`**

Replace the manual parse + combined lookup + buildMentionParticipants flow with `mention.Resolve` + separate sender lookup:

```go
func (h *Handler) HandleMessage(ctx context.Context, data []byte) error {
	// ... unmarshal, get room ...

	resolved, err := mention.Resolve(ctx, msg.Content, h.userStore.FindUsersByAccounts)
	if err != nil {
		slog.Warn("mention resolve failed", "error", err)
	}

	if err := h.store.UpdateRoomOnNewMessage(ctx, room.ID, msg.ID, msg.CreatedAt, resolved.MentionAll); err != nil {
		return fmt.Errorf("update room on new message: %w", err)
	}

	if len(resolved.Accounts) > 0 {
		if err := h.store.SetSubscriptionMentions(ctx, room.ID, resolved.Accounts); err != nil {
			return fmt.Errorf("set subscription mentions: %w", err)
		}
	}

	// Sender lookup (separate from mention lookup)
	senderMap := make(map[string]model.User)
	senderUsers, err := h.userStore.FindUsersByAccounts(ctx, []string{msg.UserAccount})
	if err != nil {
		slog.Warn("sender lookup failed, falling back to account", "error", err)
	} else {
		for _, u := range senderUsers {
			senderMap[u.Account] = u
		}
	}

	clientMsg := buildClientMessage(&msg, senderMap)

	switch room.Type {
	case model.RoomTypeGroup:
		return h.publishGroupEvent(ctx, room, clientMsg, resolved.MentionAll, resolved.Participants)
	case model.RoomTypeDM:
		return h.publishDMEvents(ctx, room, clientMsg, resolved.Accounts)
	default:
		slog.Warn("unknown room type, skipping fan-out", "type", room.Type, "roomID", room.ID)
		return nil
	}
}
```

- [ ] **Step 2: Remove `buildMentionParticipants`**

Delete the function from `handler.go`.

- [ ] **Step 3: Update `handler_test.go`**

Key changes:
- Split `expectUserLookup` calls: one for mention accounts (called inside `Resolve`), one for sender
- Remove `assert.Empty(t, m.UserID)` assertion — UserID now populated for resolved mentions
- Remove `TestBuildMentionParticipants` — function deleted
- Update tests for unresolved mentions: they are now skipped instead of showing fallback names

Example for "individual mentions" test case — before:
```go
expectUserLookup(us, []string{"sender", "alice", "bob"}, append([]model.User{senderUser}, testUsers...))
```

After:
```go
// Mention lookup (inside Resolve)
us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice", "bob"}).Return(testUsers, nil)
// Sender lookup
us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"sender"}).Return([]model.User{senderUser}, nil)
```

Mention participant assertions — before:
```go
assert.Empty(t, m.UserID, "mention participants should not have userID")
```

After:
```go
assert.NotEmpty(t, m.UserID, "mention participants should have userID")
```

- [ ] **Step 4: Run broadcast-worker tests**

```bash
make test SERVICE=broadcast-worker
```

Expected: all handler tests pass with updated assertions.

- [ ] **Step 5: Run linter**

```bash
make lint
```

- [ ] **Step 6: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "refactor(broadcast-worker): use mention.Resolve, remove buildMentionParticipants"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|-------------|------|
| Shared `pkg/mention` package with regex-based parser | Task 1 ✅ |
| `Parse` returns `Accounts` + `MentionAll` in a `ParseResult` struct | Task 1 ✅ |
| `Resolve` encapsulates parse + lookup + Participant building | Task 4 |
| `LookupFunc` parameter for testability | Task 4 |
| Partial result on lookup error | Task 4 (test: `lookup error — partial result`) |
| `message-worker` uses `mention.Resolve`, `resolveMentions` removed | Task 4 |
| `broadcast-worker` uses `mention.Resolve`, `buildMentionParticipants` removed | Task 5 |
| UserID now populated in broadcast-worker mention Participants | Task 5 |
| Unresolved accounts skipped (not fallback names) | Task 4 (test: `unresolved account skipped`) |
| Sender lookup separated from mention lookup in broadcast-worker | Task 5 |

### Lines changed (estimated, Tasks 4-5)

- **Added:** `mention.Resolve` + `ResolveResult` + `LookupFunc` (~40 lines), `TestResolve` (~80 lines)
- **Removed:** `message-worker/resolveMentions` (~35 lines), `broadcast-worker/buildMentionParticipants` (~17 lines), combined lookup wiring (~10 lines), `TestBuildMentionParticipants` (~30 lines)
- **Net:** ~120 lines added, ~92 lines removed — adds `Resolve` tests while removing duplicated handler-level logic
