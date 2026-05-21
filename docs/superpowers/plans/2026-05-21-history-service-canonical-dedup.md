# history-service canonical edit/delete dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close two correctness holes in history-service edit/delete canonical event publishing: (a) core-NATS `Publish` without `Nats-Msg-Id` means JetStream can't dedup retries, and (b) the delete short-circuit on already-deleted messages doesn't republish, so a failed original publish is permanently lost.

**Architecture:** Switch the canonical publisher from core NATS (`nc.Publish`) to JetStream (`js.PublishMsg` with `WithMsgID`). Compute stable dedup IDs per operation — edits use `<msgID>:<editedAtMs>` (retries with the same editedAt collapse; new editedAt values produce new events that converge with Cassandra's last-write-wins), deletes use `<msgID>:deleted` (single terminal event per message). Stop swallowing publish errors in the RPC path so a failed publish surfaces as a retryable error to the client — dedup makes retries safe. Make the delete short-circuit republish so a NATS hiccup during the original delete is recovered on the next client retry.

**Tech Stack:** Go 1.25, `github.com/nats-io/nats.go/jetstream`, `github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream`, `go.uber.org/mock` (mockgen), `stretchr/testify`.

---

## File Structure

**Files to create:**
- None (the new helper `canonicalDedupID` lives alongside the existing publish code in `messages.go`).

**Files to modify:**
- `history-service/internal/service/service.go` — extend `EventPublisher` interface to accept a `msgID string` argument.
- `history-service/internal/service/messages.go` — add `canonicalDedupID` helper; rewrite `publishCanonicalBestEffort` to require a dedup ID and propagate errors; update `EditMessage` and `DeleteMessage` (including the short-circuit branch) to compute dedup IDs and surface publish errors.
- `history-service/internal/service/messages_test.go` — update two existing tests that assert the old "publish-failure-does-not-fail-RPC" and "short-circuit-does-not-publish" behaviors; add three new tests for dedup ID shape, edit publish-failure-fails-RPC, and delete short-circuit republish.
- `history-service/internal/publisher/publisher.go` — rewrite `Publish` to use JetStream + `WithMsgID`; constructor now takes `jetstream.JetStream`.
- `history-service/internal/publisher/publisher_test.go` — create new test verifying the publisher calls `js.PublishMsg` with the expected `Nats-Msg-Id`.
- `history-service/cmd/main.go` — build JetStream from the existing NATS connection and pass it to `publisher.New`.
- `history-service/internal/service/mocks/mock_repository.go` — regenerated via `make generate SERVICE=history-service`; do not hand-edit.
- `docs/client-api.md` — note that edit/delete now return an internal error on a transient publish failure (instead of returning success while silently dropping the live event).

**Decomposition rationale:** The publisher and the service handler change together (interface change), so they're sequential tasks. The two existing tests assert the old behavior and must be deleted/replaced before the implementation changes, otherwise they will fail spuriously after the implementation lands — TDD ordering matters here.

---

## Task 1: Add `canonicalDedupID` helper with table-driven tests

**Files:**
- Modify: `history-service/internal/service/messages.go` (add helper near `publishCanonicalBestEffort`, around line 442)
- Test: `history-service/internal/service/messages_test.go` (add at end of file)

- [ ] **Step 1: Write the failing test**

Append to `history-service/internal/service/messages_test.go`:

```go
func TestCanonicalDedupID(t *testing.T) {
    tests := []struct {
        name      string
        messageID string
        op        canonicalOp
        ts        int64
        want      string
    }{
        {
            name:      "edit dedup ID encodes msgID and editedAt",
            messageID: "msg-abc",
            op:        canonicalOpEdit,
            ts:        1716000000000,
            want:      "msg-abc:edit:1716000000000",
        },
        {
            name:      "delete dedup ID is stable per message (timestamp ignored)",
            messageID: "msg-abc",
            op:        canonicalOpDelete,
            ts:        1716000000000,
            want:      "msg-abc:delete",
        },
        {
            name:      "delete dedup ID is the same on every retry regardless of ts",
            messageID: "msg-abc",
            op:        canonicalOpDelete,
            ts:        9999999999999,
            want:      "msg-abc:delete",
        },
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            got := canonicalDedupID(tc.messageID, tc.op, tc.ts)
            assert.Equal(t, tc.want, got)
        })
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=history-service`

Expected: FAIL with `undefined: canonicalDedupID` / `undefined: canonicalOp` / `undefined: canonicalOpEdit` / `undefined: canonicalOpDelete`.

- [ ] **Step 3: Write minimal implementation**

In `history-service/internal/service/messages.go`, add directly above `publishCanonicalBestEffort` (around line 442):

```go
type canonicalOp string

const (
    canonicalOpEdit   canonicalOp = "edit"
    canonicalOpDelete canonicalOp = "delete"
)

// canonicalDedupID returns the JetStream Nats-Msg-Id seed for a canonical
// edit/delete event. Edits include the editedAt millis so retries that
// generate a new editedAt produce a distinct event (and Cassandra's
// last-write-wins converges); deletes ignore ts because there's only ever
// one terminal delete per message and the dedup ID must be stable across
// short-circuit retries of already-deleted messages.
func canonicalDedupID(messageID string, op canonicalOp, ts int64) string {
    if op == canonicalOpDelete {
        return fmt.Sprintf("%s:%s", messageID, op)
    }
    return fmt.Sprintf("%s:%s:%d", messageID, op, ts)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=history-service`

Expected: `TestCanonicalDedupID` passes; existing tests unchanged.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/messages_test.go
git commit -m "history-service: add canonicalDedupID helper for edit/delete events"
```

---

## Task 2: Update `EventPublisher` interface to accept msgID

**Files:**
- Modify: `history-service/internal/service/service.go:56-59`

- [ ] **Step 1: Change the interface signature**

In `history-service/internal/service/service.go`, replace lines 56-59:

```go
// EventPublisher publishes live events to a NATS subject. Implemented by a
// thin wrapper around *otelnats.Conn in main.go.
type EventPublisher interface {
    Publish(ctx context.Context, subject string, data []byte) error
}
```

with:

```go
// EventPublisher publishes canonical events to a JetStream subject. The
// msgID is set as Nats-Msg-Id so JetStream stream-level dedup absorbs
// duplicate publishes from RPC retries. Implemented by a thin wrapper
// around oteljetstream.JetStream in main.go.
type EventPublisher interface {
    Publish(ctx context.Context, subject string, data []byte, msgID string) error
}
```

- [ ] **Step 2: Verify compile breaks at every callsite**

Run: `make build SERVICE=history-service`

Expected: FAIL with errors at:
- `messages.go:452` — `s.publisher.Publish(c, subj, payload)` is missing the `msgID` argument.
- `publisher.go` — `(*Publisher).Publish` no longer satisfies `EventPublisher`.
- `mock_repository.go` — mock signature is stale (will be regenerated in Task 5).

Don't fix any callsites yet; Tasks 3 and 4 wire them.

- [ ] **Step 3: Commit (intermediate broken state is fine on a feature branch)**

```bash
git add history-service/internal/service/service.go
git commit -m "history-service: extend EventPublisher interface with msgID parameter"
```

---

## Task 3: Switch `publisher.Publisher` to JetStream

**Files:**
- Modify: `history-service/internal/publisher/publisher.go`
- Test: `history-service/internal/publisher/publisher_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `history-service/internal/publisher/publisher_test.go`:

```go
package publisher_test

import (
    "context"
    "testing"

    "github.com/nats-io/nats.go"
    "github.com/nats-io/nats.go/jetstream"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/hmchangw/chat/history-service/internal/publisher"
    "github.com/hmchangw/chat/pkg/testutil/natstest"
)

func TestPublisher_PublishSetsMsgIDOnJetStream(t *testing.T) {
    srv := natstest.StartJetStreamServer(t)
    defer srv.Shutdown()

    nc, err := nats.Connect(srv.ClientURL())
    require.NoError(t, err)
    defer nc.Drain()

    js, err := jetstream.New(nc)
    require.NoError(t, err)

    ctx := context.Background()
    _, err = js.CreateStream(ctx, jetstream.StreamConfig{
        Name:       "TEST_CANONICAL",
        Subjects:   []string{"chat.msg.canonical.test.>"},
        Duplicates: 2 * 60 * 1_000_000_000, // 2-minute dedup window
    })
    require.NoError(t, err)

    p := publisher.New(js)

    err = p.Publish(ctx, "chat.msg.canonical.test.updated", []byte(`{"hello":"world"}`), "msg-abc:edit:1716000000000")
    require.NoError(t, err)

    // Republish with the same msgID — JetStream must dedup; stream message count stays at 1.
    err = p.Publish(ctx, "chat.msg.canonical.test.updated", []byte(`{"hello":"world"}`), "msg-abc:edit:1716000000000")
    require.NoError(t, err)

    info, err := js.Stream(ctx, "TEST_CANONICAL")
    require.NoError(t, err)
    state, err := info.Info(ctx)
    require.NoError(t, err)
    assert.Equal(t, uint64(1), state.State.Msgs, "JetStream should have deduped the second publish")
}
```

Note: this assumes `pkg/testutil/natstest` provides a `StartJetStreamServer` helper. If it doesn't, defer this test to an integration test under the `//go:build integration` tag and use `testcontainers-go` with the `nats` module as in `pkg/testutil/nats` (check `grep -r "StartJetStreamServer\|JetStream.*testcontainers" pkg/testutil/` first).

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=history-service`

Expected: COMPILE FAIL — `publisher.New` still expects `*otelnats.Conn`, not `jetstream.JetStream`. Once the next step lands, the test will FAIL with the second publish being persisted (no dedup).

- [ ] **Step 3: Rewrite `publisher.go`**

Replace the entire contents of `history-service/internal/publisher/publisher.go`:

```go
// Package publisher adapts a JetStream context to the service.EventPublisher
// interface. Canonical edit/delete events are published with Nats-Msg-Id set
// so JetStream stream-level dedup absorbs duplicates from RPC retries.
package publisher

import (
    "context"
    "fmt"

    "github.com/nats-io/nats.go/jetstream"
)

// Publisher publishes byte payloads to JetStream subjects with stream-level
// dedup via Nats-Msg-Id.
type Publisher struct {
    js jetstream.JetStream
}

// New constructs a Publisher backed by the given JetStream context.
func New(js jetstream.JetStream) *Publisher {
    return &Publisher{js: js}
}

// Publish sends data to subj with msgID as the Nats-Msg-Id header. JetStream
// will dedup duplicate publishes (same msgID within the stream's Duplicates
// window) so RPC retries are safe.
func (p *Publisher) Publish(ctx context.Context, subj string, data []byte, msgID string) error {
    if msgID == "" {
        return fmt.Errorf("publish to %q: empty msgID would defeat dedup", subj)
    }
    if _, err := p.js.Publish(ctx, subj, data, jetstream.WithMsgID(msgID)); err != nil {
        return fmt.Errorf("publish to %q: %w", subj, err)
    }
    return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=history-service`

Expected: `TestPublisher_PublishSetsMsgIDOnJetStream` passes; stream message count is 1 after two publishes with the same msgID.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/publisher/publisher.go history-service/internal/publisher/publisher_test.go
git commit -m "history-service: switch canonical publisher to JetStream with WithMsgID"
```

---

## Task 4: Wire JetStream in `cmd/main.go`

**Files:**
- Modify: `history-service/cmd/main.go:61-96` (NATS connect, publisher construction)

- [ ] **Step 1: Add JetStream import and construction**

In `history-service/cmd/main.go`, update the imports block:

```go
import (
    // ... existing imports ...
    "github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
)
```

Replace lines 61-65 (NATS connect) and 96 (publisher construction). After:

```go
nc, err := natsutil.Connect(cfg.NATS.URL, cfg.NATS.CredsFile)
if err != nil {
    slog.Error("nats connect failed", "error", err)
    os.Exit(1)
}
```

insert:

```go
js, err := oteljetstream.New(nc)
if err != nil {
    slog.Error("jetstream init failed", "error", err)
    os.Exit(1)
}
```

Then change line 96 from:

```go
pub := publisher.New(nc)
```

to:

```go
pub := publisher.New(js)
```

- [ ] **Step 2: Verify the service builds**

Run: `make build SERVICE=history-service`

Expected: PASS. If the import is unused (because no JetStream usage anywhere else), the build fails with "imported and not used" — confirm the new `js` variable is referenced on the line we just changed.

- [ ] **Step 3: Commit**

```bash
git add history-service/cmd/main.go
git commit -m "history-service: wire JetStream into canonical event publisher"
```

---

## Task 5: Regenerate `EventPublisher` mock

**Files:**
- Modify (regenerated): `history-service/internal/service/mocks/mock_repository.go`

- [ ] **Step 1: Regenerate**

Run: `make generate SERVICE=history-service`

- [ ] **Step 2: Verify mock has new signature**

Run: `grep -A 2 "func.*MockEventPublisher.*Publish" history-service/internal/service/mocks/mock_repository.go`

Expected: the `Publish` method signature includes a `msgID` parameter (or the mock will fail to satisfy the interface and the existing tests will fail to compile in Task 6/7/8).

- [ ] **Step 3: Run all tests — note expected failures**

Run: `make test SERVICE=history-service`

Expected: FAIL — the existing tests
- `TestHistoryService_EditMessage_PublishesCanonicalUpdatedEvent` (line 1069)
- `TestHistoryService_EditMessage_PublishFailureDoesNotFailRPC` (line 1111)
- `TestHistoryService_DeleteMessage_AlreadyDeleted_ShortCircuits` (line 1139)
- any `TestHistoryService_DeleteMessage_*` that mocks `pub.EXPECT().Publish(...)`

will fail to compile because their `pub.EXPECT().Publish(...)` calls now have the wrong arity. **Do not fix them here** — Tasks 6, 7, 8 rewrite them with intent.

- [ ] **Step 4: Commit**

```bash
git add history-service/internal/service/mocks/mock_repository.go
git commit -m "history-service: regenerate EventPublisher mock for new signature"
```

---

## Task 6: Add `publishCanonical` alongside the existing `publishCanonicalBestEffort`

The old helper stays in place for one task so the build remains green between tasks; Task 8 removes it after both call sites have switched over.

**Files:**
- Modify: `history-service/internal/service/messages.go:442-456` (add new helper above the existing one)

- [ ] **Step 1: Add the new helper next to the existing one**

In `messages.go`, directly above the existing `publishCanonicalBestEffort` function (line 442), insert:

```go
// publishCanonical marshals and publishes a canonical MessageEvent with a
// stable dedup ID. Publish failures are propagated so the RPC reflects them —
// JetStream's Nats-Msg-Id absorbs duplicates from client retries, so a
// retried RPC produces at most one downstream event per logical operation
// (edits: per (msgID, editedAt); deletes: per msgID).
func (s *HistoryService) publishCanonical(c *natsrouter.Context, subj string, evt *model.MessageEvent, dedupID, messageID, roomID string) error {
    payload, err := json.Marshal(evt)
    if err != nil {
        return fmt.Errorf("marshal canonical event for message %s in room %s: %w", messageID, roomID, err)
    }
    if err := s.publisher.Publish(c, subj, payload, dedupID); err != nil {
        return fmt.Errorf("publish canonical event for message %s in room %s: %w", messageID, roomID, err)
    }
    return nil
}
```

Leave `publishCanonicalBestEffort` in place but update its single call to the publisher to pass an empty `msgID` so the file compiles against the new `EventPublisher` interface:

```go
if err := s.publisher.Publish(c, subj, payload, ""); err != nil {
    slog.Warn("canonical publish failed",
        "error", err, "subject", subj, "messageID", messageID, "roomID", roomID)
}
```

Note: the empty `msgID` here will *fail* (publisher.go rejects empty msgIDs per Task 3). That's intentional — the deprecated helper now degrades to a logged failure on every call, which is fine since both real call sites move off of it in Tasks 7 and 8. If you want a softer transition you can pass a degenerate ID like `"deprecated:" + messageID + ":" + roomID`, but the cleaner choice is to retire the helper.

- [ ] **Step 2: Verify the service builds**

Run: `make build SERVICE=history-service`

Expected: PASS. Both helpers compile; no call site changes yet.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/service/messages.go
git commit -m "history-service: add publishCanonical helper (paired with deprecated old one)"
```

---

## Task 7: Edit path uses dedup ID and propagates publish errors

**Files:**
- Modify: `history-service/internal/service/messages.go:298-365` (EditMessage)
- Modify: `history-service/internal/service/messages_test.go:1069-1135` (rewrite two tests)

- [ ] **Step 1: Rewrite the two existing edit tests for the new behavior**

In `messages_test.go`, replace `TestHistoryService_EditMessage_PublishesCanonicalUpdatedEvent` (lines 1069-1106) and `TestHistoryService_EditMessage_PublishFailureDoesNotFailRPC` (lines 1108-1135) with:

```go
func TestHistoryService_EditMessage_PublishesCanonicalUpdatedEventWithDedupID(t *testing.T) {
    svc, msgs, subs, pub, _ := newService(t)
    c := testContext()

    subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
    hydrated := &models.Message{
        MessageID: "msg-1",
        RoomID:    "r1",
        Sender:    models.Participant{Account: "u1", ID: "u1-id"},
        CreatedAt: time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC),
        Msg:       "original content",
    }
    msgs.EXPECT().GetMessageByID(gomock.Any(), "msg-1").Return(hydrated, nil)
    msgs.EXPECT().UpdateMessageContent(gomock.Any(), hydrated, "updated content", gomock.Any()).Return(nil)

    pub.EXPECT().
        Publish(gomock.Any(), subject.MsgCanonicalUpdated("site-test"), gomock.Any(), gomock.Any()).
        DoAndReturn(func(_ context.Context, _ string, data []byte, msgID string) error {
            var evt model.MessageEvent
            require.NoError(t, json.Unmarshal(data, &evt))
            assert.Equal(t, model.EventUpdated, evt.Event)
            assert.Equal(t, "msg-1", evt.Message.ID)
            assert.Equal(t, "updated content", evt.Message.Content)
            require.NotNil(t, evt.Message.EditedAt)
            // Dedup ID encodes msgID + editedAt millis so retries with the
            // same editedAt collapse at the JetStream layer.
            wantPrefix := "msg-1:edit:"
            assert.True(t, strings.HasPrefix(msgID, wantPrefix), "msgID %q should start with %q", msgID, wantPrefix)
            assert.Equal(t, fmt.Sprintf("%s%d", wantPrefix, evt.Message.EditedAt.UnixMilli()), msgID)
            return nil
        })

    resp, err := svc.EditMessage(c, "site-test", models.EditMessageRequest{
        MessageID: "msg-1",
        NewMsg:    "updated content",
    })
    require.NoError(t, err)
    require.NotNil(t, resp)
}

// Publish failure now surfaces to the RPC: JetStream's Nats-Msg-Id dedup makes
// the client retry safe (same editedAt → same dedup ID → JetStream collapses).
func TestHistoryService_EditMessage_PublishFailureFailsRPC(t *testing.T) {
    svc, msgs, subs, pub, _ := newService(t)
    c := testContext()

    subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
    hydrated := &models.Message{
        MessageID: "msg-1",
        RoomID:    "r1",
        Sender:    models.Participant{Account: "u1", ID: "u1-id"},
        Msg:       "original content",
    }
    msgs.EXPECT().GetMessageByID(gomock.Any(), "msg-1").Return(hydrated, nil)
    msgs.EXPECT().UpdateMessageContent(gomock.Any(), hydrated, "updated content", gomock.Any()).Return(nil)

    pub.EXPECT().
        Publish(gomock.Any(), subject.MsgCanonicalUpdated("site-test"), gomock.Any(), gomock.Any()).
        Return(errors.New("nats down"))

    resp, err := svc.EditMessage(c, "site-test", models.EditMessageRequest{
        MessageID: "msg-1",
        NewMsg:    "updated content",
    })
    require.Error(t, err, "publish failure must surface to the RPC")
    assert.Nil(t, resp)
}
```

Add the missing imports at the top of the test file if not already present (`fmt`, `strings`).

- [ ] **Step 2: Run tests — expect them to compile but fail**

Run: `make test SERVICE=history-service -run TestHistoryService_EditMessage`

Expected: FAIL at the `assert.Equal(...msgID, "msg-1:edit:...")` line and at `require.Error(...)` because EditMessage still uses the old swallow-error code path.

- [ ] **Step 3: Update `EditMessage` to use dedup ID and propagate**

In `messages.go`, replace lines 358-359 (the current call to `publishCanonicalBestEffort` and the line just above the `return` at 361):

```go
    s.publishCanonicalBestEffort(c, subject.MsgCanonicalUpdated(siteID), &canonicalEvt, req.MessageID, roomID)

    return &models.EditMessageResponse{
```

with:

```go
    dedupID := canonicalDedupID(req.MessageID, canonicalOpEdit, editedAtMs)
    if err := s.publishCanonical(c, subject.MsgCanonicalUpdated(siteID), &canonicalEvt, dedupID, req.MessageID, roomID); err != nil {
        slog.Error("edit: publish canonical", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to publish edit event")
    }

    return &models.EditMessageResponse{
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=history-service -run TestHistoryService_EditMessage`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/messages_test.go
git commit -m "history-service: edit publish uses dedup ID and surfaces errors to RPC"
```

---

## Task 8: Delete path uses dedup ID, propagates errors, AND republishes on short-circuit

This is the headline fix — the existing short-circuit test asserts the bug. Replace it.

**Files:**
- Modify: `history-service/internal/service/messages.go:373-440` (DeleteMessage)
- Modify: `history-service/internal/service/messages_test.go:1139-1162` (rewrite short-circuit test)

- [ ] **Step 1: Rewrite the short-circuit test to assert the NEW behavior**

In `messages_test.go`, replace `TestHistoryService_DeleteMessage_AlreadyDeleted_ShortCircuits` (lines 1139-1162) with:

```go
// When the message is already marked deleted (e.g. a prior delete succeeded
// at Cassandra but the canonical publish failed), the handler must republish
// with the stable delete dedup ID so JetStream either accepts it (recovering
// the lost event) or dedups it (if the original publish actually succeeded).
// The Cassandra UPDATE is skipped because the LWT already won.
func TestHistoryService_DeleteMessage_AlreadyDeleted_RepublishesWithStableDedupID(t *testing.T) {
    svc, msgs, subs, pub, _ := newService(t)
    c := testContext()

    subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)

    priorUpdatedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
    hydrated := &models.Message{
        MessageID: "m-abc",
        RoomID:    "r1",
        Sender:    models.Participant{Account: "u1", ID: "u1-id"},
        CreatedAt: priorUpdatedAt.Add(-time.Minute),
        Deleted:   true,
        UpdatedAt: &priorUpdatedAt,
    }
    msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

    // No SoftDeleteMessage call expected — the prior LWT already won.
    pub.EXPECT().
        Publish(gomock.Any(), subject.MsgCanonicalDeleted("site-test"), gomock.Any(), "m-abc:delete").
        DoAndReturn(func(_ context.Context, _ string, data []byte, _ string) error {
            var evt model.MessageEvent
            require.NoError(t, json.Unmarshal(data, &evt))
            assert.Equal(t, model.EventDeleted, evt.Event)
            assert.Equal(t, "m-abc", evt.Message.ID)
            require.NotNil(t, evt.Message.UpdatedAt)
            assert.Equal(t, priorUpdatedAt.UnixMilli(), evt.Message.UpdatedAt.UnixMilli(),
                "short-circuit republish must use the persisted updated_at, not a fresh now()")
            return nil
        })

    resp, err := svc.DeleteMessage(c, "site-test", models.DeleteMessageRequest{MessageID: "m-abc"})
    require.NoError(t, err)
    assert.Equal(t, "m-abc", resp.MessageID)
    assert.Equal(t, priorUpdatedAt.UnixMilli(), resp.DeletedAt)
}

// Publish failure now surfaces to the RPC even on the main delete path —
// stable dedup ID ("<msgID>:delete") makes the client retry safe.
func TestHistoryService_DeleteMessage_PublishFailureFailsRPC(t *testing.T) {
    svc, msgs, subs, pub, _ := newService(t)
    c := testContext()

    subs.EXPECT().GetHistorySharedSince(gomock.Any(), "u1", "r1").Return(nil, true, nil)
    hydrated := &models.Message{
        MessageID: "m-abc",
        RoomID:    "r1",
        Sender:    models.Participant{Account: "u1", ID: "u1-id"},
        CreatedAt: time.Now().UTC().Add(-time.Hour),
    }
    msgs.EXPECT().GetMessageByID(gomock.Any(), "m-abc").Return(hydrated, nil)

    appliedAt := time.Now().UTC().Truncate(time.Millisecond)
    msgs.EXPECT().
        SoftDeleteMessage(gomock.Any(), hydrated, gomock.Any()).
        Return(appliedAt, true, nil)

    pub.EXPECT().
        Publish(gomock.Any(), subject.MsgCanonicalDeleted("site-test"), gomock.Any(), "m-abc:delete").
        Return(errors.New("nats down"))

    resp, err := svc.DeleteMessage(c, "site-test", models.DeleteMessageRequest{MessageID: "m-abc"})
    require.Error(t, err, "publish failure must surface to the RPC")
    assert.Nil(t, resp)
}
```

- [ ] **Step 2: Run tests — expect them to fail**

Run: `make test SERVICE=history-service -run TestHistoryService_DeleteMessage`

Expected: FAIL — the short-circuit currently returns before publishing; the new short-circuit test will fail with `unexpected call to ... Publish`. The publish-failure test will fail with `require.Error` because the current code swallows the error.

- [ ] **Step 3: Update `DeleteMessage` short-circuit to republish**

In `messages.go`, replace lines 390-401 (the short-circuit block):

```go
    // Already-deleted short-circuit: echo the current updated_at as the DeletedAt.
    // Prevents tcount double-decrement on caller retry and avoids duplicate events.
    if msg.Deleted {
        var deletedAtMs int64
        if msg.UpdatedAt != nil {
            deletedAtMs = msg.UpdatedAt.UnixMilli()
        }
        return &models.DeleteMessageResponse{
            MessageID: req.MessageID,
            DeletedAt: deletedAtMs,
        }, nil
    }
```

with:

```go
    // Already-deleted short-circuit: skip the Cassandra LWT (a prior delete
    // won), but republish the canonical event so a previously-failed publish
    // is recovered. JetStream's Nats-Msg-Id dedup ("<msgID>:delete") absorbs
    // the case where the original publish actually succeeded.
    if msg.Deleted {
        if msg.UpdatedAt == nil {
            // Defensive: a deleted message must have updated_at set. If it
            // doesn't, surface a clear error rather than publishing a bogus
            // event with a zero timestamp.
            return nil, natsrouter.ErrInternal("deleted message has no updated_at")
        }
        canonicalEvt := model.MessageEvent{
            Event: model.EventDeleted,
            Message: model.Message{
                ID:          msg.MessageID,
                RoomID:      msg.RoomID,
                UserID:      msg.Sender.ID,
                UserAccount: msg.Sender.Account,
                CreatedAt:   msg.CreatedAt,
                UpdatedAt:   msg.UpdatedAt,
            },
            SiteID:    siteID,
            Timestamp: msg.UpdatedAt.UnixMilli(),
        }
        dedupID := canonicalDedupID(req.MessageID, canonicalOpDelete, 0)
        if err := s.publishCanonical(c, subject.MsgCanonicalDeleted(siteID), &canonicalEvt, dedupID, req.MessageID, roomID); err != nil {
            slog.Error("delete short-circuit: republish canonical", "error", err, "messageID", req.MessageID)
            return nil, natsrouter.ErrInternal("failed to republish delete event")
        }
        return &models.DeleteMessageResponse{
            MessageID: req.MessageID,
            DeletedAt: msg.UpdatedAt.UnixMilli(),
        }, nil
    }
```

- [ ] **Step 4: Update the main delete path to use dedup ID and propagate publish errors**

In `messages.go`, replace lines 433-434 (the current call to `publishCanonicalBestEffort` and the `return` directly below):

```go
    s.publishCanonicalBestEffort(c, subject.MsgCanonicalDeleted(siteID), &canonicalEvt, req.MessageID, roomID)

    return &models.DeleteMessageResponse{
```

with:

```go
    dedupID := canonicalDedupID(req.MessageID, canonicalOpDelete, 0)
    if err := s.publishCanonical(c, subject.MsgCanonicalDeleted(siteID), &canonicalEvt, dedupID, req.MessageID, roomID); err != nil {
        slog.Error("delete: publish canonical", "error", err, "messageID", req.MessageID)
        return nil, natsrouter.ErrInternal("failed to publish delete event")
    }

    return &models.DeleteMessageResponse{
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test SERVICE=history-service -run TestHistoryService_DeleteMessage`

Expected: all delete tests pass.

- [ ] **Step 6: Remove the now-unreferenced `publishCanonicalBestEffort`**

In `messages.go`, delete the entire `publishCanonicalBestEffort` function (and its comment block). Both real call sites have moved to `publishCanonical`; the old helper has no remaining callers.

- [ ] **Step 7: Run the full service test suite**

Run: `make test SERVICE=history-service`

Expected: PASS — no other tests reference `publishCanonicalBestEffort` or assert the old swallow-error behavior. If anything else fails, fix it with the same pattern (route through `publishCanonical` + propagate).

- [ ] **Step 8: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/messages_test.go
git commit -m "history-service: delete republishes on short-circuit; publish errors fail RPC"
```

---

## Task 9: Run integration tests

**Files:**
- No edits expected; verify behavior end-to-end.

- [ ] **Step 1: Run integration tests for history-service**

Run: `make test-integration SERVICE=history-service`

Expected: PASS. If the integration test in `internal/service/integration_test.go` connects to a real JetStream and asserts canonical events land, it should work unchanged (publisher.New now requires `jetstream.JetStream` — the integration test's setup will need to build it the same way `cmd/main.go` does; check `grep -n "publisher.New\|service.New" history-service/internal/service/integration_test.go` and update the test fixture if needed). If the integration test still passes `*otelnats.Conn` to `publisher.New`, replace that with `oteljetstream.New(nc)`.

- [ ] **Step 2: Run integration tests for downstream consumers**

Run: `make test-integration SERVICE=broadcast-worker && make test-integration SERVICE=search-sync-worker`

Expected: PASS. Both consume canonical `.updated`/`.deleted` events; they should behave the same regardless of whether the publisher uses core NATS or JetStream (the subject is unchanged).

- [ ] **Step 3: No commit — verification step**

---

## Task 10: Update `docs/client-api.md` for new error semantics

**Files:**
- Modify: `docs/client-api.md` — the sections for `chat.user.{account}.request.room.{roomID}.{siteID}.msg.edit` and `.msg.delete`.

- [ ] **Step 1: Locate the affected sections**

Run: `grep -n "msg\.edit\|msg\.delete" docs/client-api.md`

- [ ] **Step 2: Add a note about the new error behavior**

For both edit and delete sections, add a sentence under "Error responses" (or create the section if absent):

> When the canonical event cannot be published to JetStream (transient NATS/JetStream failure), the RPC returns `ErrInternal` with message `failed to publish edit event` (or `failed to publish delete event` / `failed to republish delete event`). The Cassandra write has already succeeded, so a client retry is safe — JetStream `Nats-Msg-Id` dedup will absorb duplicate publishes within the stream's dedup window. Edits dedup on `(messageID, editedAt millis)`; deletes dedup on `messageID` alone (a single terminal event per message).

- [ ] **Step 3: Commit**

```bash
git add docs/client-api.md
git commit -m "docs(client-api): note edit/delete RPC fails on publish error; client retry is safe"
```

---

## Task 11: Lint, SAST, and final verification

**Files:**
- No edits expected.

- [ ] **Step 1: Run lint**

Run: `make lint`

Expected: PASS. Fix any issues before continuing.

- [ ] **Step 2: Run SAST**

Run: `make sast`

Expected: PASS. The new code introduces no `InsecureSkipVerify`, no raw SQL, no obvious injection sinks.

- [ ] **Step 3: Run full test suite with race detector**

Run: `make test`

Expected: PASS across all services. The interface change is local to history-service; nothing else imports `history-service/internal/publisher` or `history-service/internal/service`.

- [ ] **Step 4: Final review-friendly commit (optional)**

If the working tree is clean, no commit needed. Otherwise:

```bash
git status
# Commit any straggling changes with a clear message before pushing.
```

- [ ] **Step 5: Push the branch**

```bash
git push -u origin claude/identify-service-bugs-5blZP
```

---

## What this plan does NOT cover

Intentionally out of scope (these belong to follow-up plans):

- **broadcast-worker's downstream behavior on duplicate `.updated`/`.deleted` events.** Already idempotent at the fan-out level (it re-publishes the same `RoomEvent` to the room subject); dedup at the source closes most of the gap.
- **search-sync-worker's reindex behavior.** Already idempotent via the painless LWW guard (`params.ts > stored`).
- **A dedup window check on the stream config.** This plan assumes the MESSAGES_CANONICAL stream has a non-zero `Duplicates` window. If it's currently zero, JetStream dedup is a no-op even with `Nats-Msg-Id` set — verify via `nats stream info MESSAGES_CANONICAL_<siteID>` before declaring the fix complete. If it's zero, a separate ops change is needed to set it (recommend 2 minutes — long enough for a reasonable client retry budget, short enough to bound JetStream's dedup memory footprint).
- **Re-architecture of "best-effort vs strict" publish semantics elsewhere.** Other services (room-worker subscription updates, broadcast-worker DM fan-out, notification-worker, etc.) still log-and-continue on publish failure. Those are documented in the audit report and warrant their own plans.
