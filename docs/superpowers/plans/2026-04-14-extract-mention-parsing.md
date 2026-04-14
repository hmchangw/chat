# Extract Mention Parsing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract `@mention` parsing from `message-worker` into a shared `pkg/mention` package. Replace `broadcast-worker`'s separate `detectMentionAll` and `extractMentionedAccounts` functions with the same shared `mention.Parse`. Both services converge on a single regex-based parser with consistent behavior.

**Architecture:** `mention.Parse(content)` returns a `ParseResult` with `Accounts []string` (lowercased, deduplicated, excluding `@all`/`@here`) and `MentionAll bool`. `message-worker` calls it in `resolveMentions` before user lookup. `broadcast-worker` calls it in `HandleMessage` to get `mentionAll` and `mentionedAccounts`.

**Tech Stack:** Go 1.25, `regexp` (stdlib), `go.uber.org/mock` + `testify`.

---

## File Map

| File | Change |
|------|--------|
| `pkg/mention/mention.go` | New — shared `Parse` function with regex and `ParseResult` type |
| `pkg/mention/mention_test.go` | New — table-driven tests covering all parsing scenarios |
| `message-worker/handler.go` | Remove `mentionRe`, `parseMentions`; update `resolveMentions` to use `mention.Parse` |
| `message-worker/handler_test.go` | Remove `TestParseMentions` |
| `broadcast-worker/handler.go` | Remove `detectMentionAll`, `extractMentionedAccounts`; use `mention.Parse` in `HandleMessage` |
| `broadcast-worker/handler_test.go` | Remove `TestDetectMentionAll`, `TestExtractMentionedAccounts` |

---

## Task 1 — Create `pkg/mention` package with TDD

**Files:**
- New: `pkg/mention/mention.go`
- New: `pkg/mention/mention_test.go`

- [ ] **Step 1: Write failing tests for `Parse`**

Create `pkg/mention/mention_test.go`:

```go
package mention

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		accounts   []string
		mentionAll bool
	}{
		{name: "no mentions", content: "hello world", accounts: nil, mentionAll: false},
		{name: "single mention", content: "hello @bob", accounts: []string{"bob"}, mentionAll: false},
		{name: "multiple mentions", content: "@alice check with @bob", accounts: []string{"alice", "bob"}, mentionAll: false},
		{name: "mention at start", content: "@alice hello", accounts: []string{"alice"}, mentionAll: false},
		{name: "email-style mention", content: "ping @user@domain.com", accounts: []string{"user@domain.com"}, mentionAll: false},
		{name: "quoted reply prefix", content: ">@alice this is quoted", accounts: []string{"alice"}, mentionAll: false},
		{name: "duplicates deduplicated", content: "@bob and @bob again", accounts: []string{"bob"}, mentionAll: false},
		{name: "dots and hyphens", content: "cc @first.last and @my-user", accounts: []string{"first.last", "my-user"}, mentionAll: false},
		{name: "empty content", content: "", accounts: nil, mentionAll: false},
		{name: "trailing period not captured", content: "hey @bob. check this", accounts: []string{"bob"}, mentionAll: false},
		{name: "@all lowercase", content: "hey @all check this", accounts: nil, mentionAll: true},
		{name: "@All uppercase", content: "attention @All everyone", accounts: nil, mentionAll: true},
		{name: "@here lowercase", content: "look @here please", accounts: nil, mentionAll: true},
		{name: "@HERE uppercase", content: "look @HERE please", accounts: nil, mentionAll: true},
		{name: "@all and individual", content: "@All and @alice", accounts: []string{"alice"}, mentionAll: true},
		{name: "case-insensitive dedup", content: "@alice @Alice", accounts: []string{"alice"}, mentionAll: false},
		{name: "mixed case lowered", content: "hey @BOB", accounts: []string{"bob"}, mentionAll: false},
		{name: "partial email@all not detected", content: "email@all.com", accounts: []string{"all.com"}, mentionAll: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.content)
			assert.Equal(t, tt.accounts, result.Accounts)
			assert.Equal(t, tt.mentionAll, result.MentionAll)
		})
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

```bash
make test SERVICE=pkg/mention
```

Expected: compilation error — `Parse` undefined.

- [ ] **Step 3: Implement `Parse` in `mention.go`**

Create `pkg/mention/mention.go`:

```go
package mention

import (
	"regexp"
	"strings"
)

// mentionRe matches @mention tokens in message content.
// A bare @ not preceded by whitespace (e.g. "hello@bob") also matches —
// this is intentional per spec. Non-existent accounts are silently skipped
// during the user lookup.
var mentionRe = regexp.MustCompile(`(^|\s|>?)@([0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*(@[0-9a-zA-Z_-]+(\.[0-9a-zA-Z_-]+)*)?)`)

// ParseResult holds parsed mention data extracted from message content.
type ParseResult struct {
	Accounts   []string // unique mentioned accounts, lowercased, excluding @all/@here
	MentionAll bool     // true if @all or @here was mentioned (case-insensitive)
}

// Parse extracts @mention tokens from content and returns the unique
// mentioned accounts along with whether @all or @here was present.
func Parse(content string) ParseResult {
	matches := mentionRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return ParseResult{}
	}

	var result ParseResult
	seen := make(map[string]struct{}, len(matches))

	for _, m := range matches {
		account := strings.ToLower(m[2])
		if account == "all" || account == "here" {
			result.MentionAll = true
			continue
		}
		if _, exists := seen[account]; !exists {
			seen[account] = struct{}{}
			result.Accounts = append(result.Accounts, account)
		}
	}

	return result
}
```

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=pkg/mention
```

Expected: all 18 `TestParse` subtests pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/mention/mention.go pkg/mention/mention_test.go
git commit -m "feat(mention): add shared pkg/mention package for @mention parsing"
```

---

## Task 2 — Update `message-worker` to use `pkg/mention`

**Files:**
- Modify: `message-worker/handler.go`
- Modify: `message-worker/handler_test.go`

- [ ] **Step 1: Remove local `mentionRe` and `parseMentions` from `handler.go`**

Remove the `regexp` import, the `mentionRe` var, and the entire `parseMentions` function. Add `"github.com/hmchangw/chat/pkg/mention"` to imports.

- [ ] **Step 2: Update `resolveMentions` to use `mention.Parse`**

Replace:
```go
func (h *Handler) resolveMentions(ctx context.Context, content string) ([]model.Participant, error) {
	parsed := parseMentions(content)
	if len(parsed) == 0 {
		return nil, nil
	}

	var mentionAll bool
	var userAccounts []string
	for _, account := range parsed {
		if account == "all" {
			mentionAll = true
		} else {
			userAccounts = append(userAccounts, account)
		}
	}
	// ...
}
```

With:
```go
func (h *Handler) resolveMentions(ctx context.Context, content string) ([]model.Participant, error) {
	parsed := mention.Parse(content)
	if len(parsed.Accounts) == 0 && !parsed.MentionAll {
		return nil, nil
	}

	var participants []model.Participant

	if len(parsed.Accounts) > 0 {
		users, err := h.userStore.FindUsersByAccounts(ctx, parsed.Accounts)
		// ...
	}

	if parsed.MentionAll {
		participants = append(participants, model.Participant{
			Account: "all",
			EngName: "all",
		})
	}
	// ...
}
```

- [ ] **Step 3: Remove `TestParseMentions` from `handler_test.go`**

Delete the entire `TestParseMentions` function — these tests now live in `pkg/mention/mention_test.go`.

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=message-worker
```

Expected: all 9 `TestHandler_ProcessMessage` subtests pass. `TestParseMentions` is no longer present.

- [ ] **Step 5: Run linter**

```bash
make lint
```

Expected: no issues (unused imports removed, formatting clean).

- [ ] **Step 6: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "refactor(message-worker): use shared pkg/mention for mention parsing"
```

---

## Task 3 — Update `broadcast-worker` to use `pkg/mention`

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Replace mention parsing in `HandleMessage`**

In `broadcast-worker/handler.go`, remove the `"strings"` import, add `"github.com/hmchangw/chat/pkg/mention"`.

Replace:
```go
mentionAll := detectMentionAll(msg.Content)
mentionedAccounts := extractMentionedAccounts(msg.Content)
```

With:
```go
parsed := mention.Parse(msg.Content)
mentionAll := parsed.MentionAll
mentionedAccounts := parsed.Accounts
```

- [ ] **Step 2: Remove `detectMentionAll` and `extractMentionedAccounts`**

Delete both functions entirely from `handler.go`.

- [ ] **Step 3: Remove unit tests for deleted functions from `handler_test.go`**

Delete `TestDetectMentionAll` and `TestExtractMentionedAccounts` — these behaviors are now covered by `TestParse` in `pkg/mention/mention_test.go`.

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=broadcast-worker
```

Expected: all existing handler tests pass:
- `TestHandler_HandleMessage_GroupRoom` (4 subtests)
- `TestHandler_HandleMessage_DMRoom` (2 subtests)
- `TestHandler_HandleMessage_Errors` (8 subtests)
- `TestHandler_HandleMessage_DMRoom_PublishError`
- `TestBuildMentionParticipants` (4 subtests)
- `TestBuildClientMessage` (2 subtests)

- [ ] **Step 5: Run linter**

```bash
make lint
```

Expected: no issues.

- [ ] **Step 6: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "refactor(broadcast-worker): use shared pkg/mention for mention parsing"
```

---

## Self-Review

### Spec coverage

| Requirement | Task |
|-------------|------|
| Shared `pkg/mention` package with regex-based parser | Task 1 |
| `Parse` returns `Accounts` + `MentionAll` in a `ParseResult` struct | Task 1 |
| Case-insensitive `@all` / `@here` detection | Task 1 (tests: `@All`, `@HERE`) |
| Lowercased, deduplicated accounts | Task 1 (tests: `case-insensitive dedup`, `mixed case lowered`) |
| `message-worker` uses shared parser in `resolveMentions` | Task 2 |
| `broadcast-worker` uses shared parser in `HandleMessage` | Task 3 |
| Local `parseMentions`, `detectMentionAll`, `extractMentionedAccounts` removed | Tasks 2, 3 |
| Tests for deleted functions moved to `pkg/mention` | Task 1 |
| All existing handler tests unchanged and passing | Tasks 2, 3 |

### Lines changed

- **Added:** `pkg/mention/mention.go` (~44 lines), `pkg/mention/mention_test.go` (~53 lines)
- **Removed:** `message-worker` local parsing (~26 lines code + ~22 lines tests), `broadcast-worker` local parsing (~30 lines code + ~44 lines tests)
- **Net:** ~97 lines added, ~122 lines removed — shared package reduces total code by ~25 lines while unifying behavior
