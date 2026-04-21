# History Service `getThreadMessages` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new NATS request/reply endpoint `getThreadMessages` to `history-service` that returns thread replies for a given parent message, paginated newest-first with a cursor.

**Architecture:** Thin handler in `history-service/internal/service` calls a new Cassandra repo method `GetThreadMessages` that queries `thread_messages_by_room` on the partition key `room_id` + first clustering key `thread_room_id`. Handler derives the room from the fetched parent message — it never reads `roomID` from the NATS subject. Cursor-based pagination via existing `cassrepo.QueryBuilder` (native Cassandra `PageState`).

**Tech Stack:** Go 1.25, `github.com/gocql/gocql`, `pkg/natsrouter`, `go.uber.org/mock`, `testify`, `testcontainers-go/modules/cassandra` for integration tests.

**Spec:** `docs/superpowers/specs/2026-04-21-history-get-thread-messages-design.md`

---

## File Map

| File | Change |
|------|--------|
| `pkg/subject/subject.go` | Add `MsgThreadPattern(siteID)` and `MsgThreadWildcard(siteID)` |
| `pkg/subject/subject_test.go` | Add cases for the two new builders |
| `history-service/internal/models/message.go` | Add `GetThreadMessagesRequest` + `GetThreadMessagesResponse` |
| `history-service/internal/cassrepo/repository.go` | Add `threadMessageColumns`, `threadMessageScanDest`, `GetThreadMessages` |
| `history-service/internal/cassrepo/integration_test.go` | Add seeding helper + four integration test cases |
| `history-service/internal/service/service.go` | Extend `MessageRepository` interface; register handler |
| `history-service/internal/service/messages.go` | Add `GetThreadMessages` handler |
| `history-service/internal/service/messages_test.go` | Table-driven unit tests for the handler |
| `history-service/internal/service/mocks/mock_repository.go` | Regenerated via `make generate SERVICE=history-service` |

No other services, docker init, or docs are touched.

---

## Task 1 — Subject builders in `pkg/subject`

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests for the two new builders**

Open `pkg/subject/subject_test.go`. In `TestSubjectBuilders` (the table starting around line 9), add a new case to the `tests` slice next to the other `Msg*Pattern` entries. Since the existing file doesn't yet list `MsgHistoryPattern`/`MsgNextPattern`/`MsgSurroundingPattern`/`MsgGetPattern` in that test (they are `Pattern` style, not `Wildcard`), add the thread pattern case to `TestSubjectBuilders` and the thread wildcard case to `TestWildcardPatterns`.

In `TestSubjectBuilders` add (before the closing `}` of the `tests` slice):

```go
{"MsgThreadPattern", subject.MsgThreadPattern("site-a"),
    "chat.user.{account}.request.room.{roomID}.site-a.msg.thread"},
```

In `TestWildcardPatterns` add (before the closing `}` of that slice):

```go
{"MsgThreadWild", subject.MsgThreadWildcard("site-a"),
    "chat.user.*.request.room.*.site-a.msg.thread"},
```

- [ ] **Step 2: Run tests — confirm they fail**

```bash
go test -race ./pkg/subject/...
```

Expected: FAIL with `undefined: subject.MsgThreadPattern` and `undefined: subject.MsgThreadWildcard`.

- [ ] **Step 3: Add the two builders**

Open `pkg/subject/subject.go`. Immediately after `MsgGetPattern` (line 194 area), add:

```go
func MsgThreadPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.thread", siteID)
}
```

And immediately after `MsgHistoryWildcard` (line 156 area), add:

```go
func MsgThreadWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.thread", siteID)
}
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
go test -race ./pkg/subject/...
```

Expected: PASS. All existing cases still green.

- [ ] **Step 5: Lint + commit**

```bash
make lint
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(pkg/subject): add MsgThread pattern and wildcard builders"
```

---
