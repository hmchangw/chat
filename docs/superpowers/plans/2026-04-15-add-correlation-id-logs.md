# Add Correlation ID to History-Service Logs

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Include a `requestID` correlation ID in all manual `slog` calls within the history-service, enabling end-to-end request tracing.

**Architecture:** The `natsrouter.RequestID()` middleware already exists in `pkg/natsrouter/middleware.go` — it extracts `X-Request-ID` from NATS headers or generates a UUID, and stores it via `c.Set("requestID", id)`. It is tested but not yet used by any service. We will: (1) add a `RequestID()` convenience method to `natsrouter.Context` for clean access, (2) wire the middleware into the history-service router, and (3) add `"requestID", c.RequestID()` to every manual `slog` call in the handler and utility code.

**Tech Stack:** Go, `pkg/natsrouter`, `log/slog`, `testify`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/natsrouter/context.go` | Add `RequestID()` convenience method |
| Modify | `pkg/natsrouter/router_test.go` | Test the new `RequestID()` method |
| Modify | `history-service/cmd/main.go` | Wire `RequestID()` middleware |
| Modify | `history-service/internal/service/messages.go` | Add `requestID` to all `slog` calls |
| Modify | `history-service/internal/service/utils.go` | Add `requestID` to all `slog` calls |

---

### Task 1: Add `RequestID()` convenience method to `natsrouter.Context`

**Files:**
- Modify: `pkg/natsrouter/context.go` (add method after `ReplyRouteError`)
- Modify: `pkg/natsrouter/router_test.go` (add test)

- [ ] **Step 1: Write the failing test**

Add to `pkg/natsrouter/router_test.go`:

```go
func TestContext_RequestID_Set(t *testing.T) {
	c := NewContext(map[string]string{})
	c.Set("requestID", "test-req-123")
	assert.Equal(t, "test-req-123", c.RequestID())
}

func TestContext_RequestID_NotSet(t *testing.T) {
	c := NewContext(map[string]string{})
	assert.Equal(t, "", c.RequestID())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/natsrouter`

Expected: FAIL — `c.RequestID undefined (type *Context has no field or method RequestID)`

- [ ] **Step 3: Write minimal implementation**

Add to `pkg/natsrouter/context.go` after the `ReplyRouteError` method (after line 132):

```go
// RequestID returns the correlation ID stored by the RequestID middleware.
// Returns an empty string if no ID has been set.
func (c *Context) RequestID() string {
	id, ok := c.Get(requestIDKey)
	if !ok {
		return ""
	}
	s, _ := id.(string)
	return s
}
```

Note: `requestIDKey` is already defined as a constant in `middleware.go` within the same package.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/natsrouter`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/natsrouter/context.go pkg/natsrouter/router_test.go
git commit -m "feat(natsrouter): add RequestID() convenience method to Context"
```

---

### Task 2: Wire `RequestID()` middleware into history-service

**Files:**
- Modify: `history-service/cmd/main.go` (line 62, add middleware before `Recovery`)

- [ ] **Step 1: Add `RequestID()` middleware to the router**

In `history-service/cmd/main.go`, change the middleware registration (lines 62-63) from:

```go
router.Use(natsrouter.Recovery())
router.Use(natsrouter.Logging())
```

to:

```go
router.Use(natsrouter.RequestID())
router.Use(natsrouter.Recovery())
router.Use(natsrouter.Logging())
```

`RequestID()` must come first so that `Recovery()` and `Logging()` (which already call `requestAttrs()`) include the ID in their output.

- [ ] **Step 2: Verify compilation**

Run: `go build ./history-service/cmd/`

Expected: builds successfully

- [ ] **Step 3: Commit**

```bash
git add history-service/cmd/main.go
git commit -m "feat(history-service): wire RequestID middleware for correlation ID tracing"
```

---

### Task 3: Add `requestID` to all slog calls in handler and utility code

**Files:**
- Modify: `history-service/internal/service/messages.go` (lines 51, 91, 154, 161)
- Modify: `history-service/internal/service/utils.go` (lines 18, 39, 53)

- [ ] **Step 1: Update `messages.go` — `LoadHistory` slog call (line 51)**

Change:

```go
slog.Error("loading history", "error", err, "roomID", roomID)
```

to:

```go
slog.Error("loading history", "error", err, "roomID", roomID, "requestID", c.RequestID())
```

- [ ] **Step 2: Update `messages.go` — `LoadNextMessages` slog call (line 91)**

Change:

```go
slog.Error("loading next messages", "error", err, "roomID", roomID)
```

to:

```go
slog.Error("loading next messages", "error", err, "roomID", roomID, "requestID", c.RequestID())
```

- [ ] **Step 3: Update `messages.go` — `LoadSurroundingMessages` before-page slog call (line 154)**

Change:

```go
slog.Error("loading surrounding messages", "error", err, "roomID", roomID, "direction", "before")
```

to:

```go
slog.Error("loading surrounding messages", "error", err, "roomID", roomID, "direction", "before", "requestID", c.RequestID())
```

- [ ] **Step 4: Update `messages.go` — `LoadSurroundingMessages` after-page slog call (line 161)**

Change:

```go
slog.Error("loading surrounding messages", "error", err, "roomID", roomID, "direction", "after")
```

to:

```go
slog.Error("loading surrounding messages", "error", err, "roomID", roomID, "direction", "after", "requestID", c.RequestID())
```

- [ ] **Step 5: Update `utils.go` — `getAccessSince` slog call (line 18)**

Change:

```go
slog.Error("checking subscription", "error", err, "account", account, "roomID", roomID)
```

to:

```go
slog.Error("checking subscription", "error", err, "account", account, "roomID", roomID, "requestID", c.RequestID())
```

Note: `getAccessSince` receives `ctx context.Context`, not `*natsrouter.Context`. The method signature must change from `func (s *HistoryService) getAccessSince(ctx context.Context, account, roomID string)` to `func (s *HistoryService) getAccessSince(c *natsrouter.Context, account, roomID string)`. This is safe because `*natsrouter.Context` implements `context.Context` and callers already pass a `*natsrouter.Context`. Update the function signature:

```go
func (s *HistoryService) getAccessSince(c *natsrouter.Context, account, roomID string) (*time.Time, error) {
```

Remove `"context"` from the import list in `utils.go` (it will no longer be needed if no other function uses it — check `findMessage` and `parsePageRequest` first).

`findMessage` also takes `ctx context.Context`. Apply the same change:

```go
func (s *HistoryService) findMessage(c *natsrouter.Context, roomID, messageID string) (*models.Message, error) {
```

And update its slog call (line 53):

```go
slog.Error("finding message", "error", err, "messageID", messageID, "requestID", c.RequestID())
```

`parsePageRequest` does not take a context — it uses a bare `slog.Error`. This function has no access to the request context, so leave it unchanged (the pagination cursor error is input validation, not a request-scoped operation).

After these changes, `context` import in `utils.go` can be removed since `natsrouter.Context` replaces all usages.

- [ ] **Step 6: Verify compilation**

Run: `go build ./history-service/...`

Expected: builds successfully

- [ ] **Step 7: Run all history-service tests**

Run: `make test SERVICE=history-service`

Expected: all tests PASS (existing tests already pass `*natsrouter.Context` to handlers; `getAccessSince` and `findMessage` receive it unchanged since `*natsrouter.Context` satisfies `context.Context`)

- [ ] **Step 8: Run linter**

Run: `make lint`

Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/utils.go
git commit -m "feat(history-service): add requestID correlation ID to all slog error entries"
```

---

## Summary of All Changes

| File | Change |
|------|--------|
| `pkg/natsrouter/context.go` | +7 lines: `RequestID()` method |
| `pkg/natsrouter/router_test.go` | +10 lines: two tests for `RequestID()` |
| `history-service/cmd/main.go` | +1 line: `router.Use(natsrouter.RequestID())` |
| `history-service/internal/service/messages.go` | 4 slog calls gain `"requestID", c.RequestID()` |
| `history-service/internal/service/utils.go` | Signature change on `getAccessSince` and `findMessage` (`context.Context` → `*natsrouter.Context`), 2 slog calls gain `"requestID", c.RequestID()`, remove unused `context` import |
