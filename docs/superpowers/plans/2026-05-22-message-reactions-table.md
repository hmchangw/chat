# message_reactions Side Table — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the embedded `reactions MAP<...>` column on four Cassandra message tables with a dedicated side table `message_reactions((message_id), emoji)`. Reactions hydrate at read time in history-service via errgroup parallel single-partition queries.

**Architecture:** One side table per message_id (LCS compaction, tiny partitions). History-service handlers hydrate via a centralised `hydrateReactions` service helper that fans out `GetReactionsByMessageIDs` (errgroup, bounded concurrency, token-aware single-partition reads). The `Message.Reactions` field stays as the response shape but is no longer scanned from a column.

**Tech Stack:** Go 1.25, gocql, Cassandra (LCS), `golang.org/x/sync/errgroup`, mockgen, testcontainers via `pkg/testutil`.

**Reference spec:** `docs/specs/message-reactions-table.md` (single source of truth — read it first).

---

## File Map

**New files:**
- `docker-local/cassandra/init/14-table-message_reactions.cql`
- `pkg/model/cassandra/reaction.go`
- `pkg/model/cassandra/reaction_test.go`
- `history-service/internal/cassrepo/message_reactions.go`
- `history-service/internal/cassrepo/message_reactions_integration_test.go`
- `history-service/internal/cassrepo/message_reactions_bench_test.go`
- `history-service/internal/service/reactions.go` (the `hydrateReactions` helper)
- `history-service/internal/service/reactions_test.go`

**Modified files:**
- `docker-local/cassandra/init/10-table-messages_by_room.cql` (drop `reactions` column)
- `docker-local/cassandra/init/11-table-thread_messages_by_room.cql` (drop)
- `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql` (drop)
- `docker-local/cassandra/init/13-table-messages_by_id.cql` (drop)
- `pkg/model/cassandra/message.go:93` (drop `cql:"reactions"` tag, keep `json` tag)
- `pkg/model/cassandra/message_test.go:185,212` (drop reactions from row literals)
- `history-service/internal/cassrepo/messages_by_room.go:16` (drop `reactions,` from `baseColumns`)
- `history-service/internal/cassrepo/thread_messages.go:16` (drop `reactions,` from column list)
- `history-service/internal/cassrepo/integration_test.go` (drop inline `reactions MAP`, add new table)
- `history-service/internal/cassrepo/messages_by_room_integration_test.go` (surgical: drop reaction seed/assert blocks only)
- `history-service/internal/cassrepo/messages_by_id_integration_test.go:165-169` (surgical)
- `history-service/internal/cassrepo/thread_messages_integration_test.go:301-305` (surgical)
- `history-service/internal/service/service.go:18-26` (extend `MessageReader` interface)
- `history-service/internal/service/integration_test.go` (drop inline DDL, seed new table)
- `history-service/internal/service/messages.go` (wire hydrator into 4 multi-message handlers)
- `history-service/internal/service/threads.go` (wire hydrator into thread handlers)
- `history-service/internal/service/mocks/mock_repository.go` (regenerated)
- `history-service/internal/config/config.go:39-41` (add `REACTIONS_FETCH_CONCURRENCY`)
- `message-worker/integration_test.go:50,67,86` (drop inline `reactions MAP`)
- `docs/cassandra_message_model.md` (drop reactions from 4 table sections, add `message_reactions` section)
- `docs/client-api.md:949` (one-line hydration note)

---

## Task 1: Drop `reactions` column from message-table DDL and inline test schemas

**Files:**
- Modify: `docker-local/cassandra/init/10-table-messages_by_room.cql`
- Modify: `docker-local/cassandra/init/11-table-thread_messages_by_room.cql`
- Modify: `docker-local/cassandra/init/12-table-pinned_messages_by_room.cql`
- Modify: `docker-local/cassandra/init/13-table-messages_by_id.cql`
- Modify: `message-worker/integration_test.go` (3 inline `CREATE TABLE` strings)
- Modify: `history-service/internal/cassrepo/integration_test.go` (inline DDL in `setupCassandra`)
- Modify: `history-service/internal/service/integration_test.go` (if locally duplicated)

- [ ] **Step 1: Delete the `reactions MAP<...>` line from each of the four init `.cql` files**

In each file, find and delete the line:
```
reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
```
(Watch for the trailing comma — leave the surrounding columns syntactically valid.)

- [ ] **Step 2: Delete the same line from every inline `CREATE TABLE` string**

Find all six call sites:
```bash
grep -rn 'reactions\s\+MAP' message-worker/ history-service/
```
Expected hits: `message-worker/integration_test.go:50,67,86` plus history-service integration test files. Remove the `reactions` line (and its trailing comma where applicable) from each.

- [ ] **Step 3: Verify no `reactions MAP` strings remain in DDL**

Run:
```bash
grep -rn 'reactions\s\+MAP' docker-local/ message-worker/ history-service/
```
Expected: zero hits.

- [ ] **Step 4: Commit**

```bash
git add docker-local/cassandra/init/ message-worker/integration_test.go history-service/internal/cassrepo/integration_test.go history-service/internal/service/integration_test.go
git commit -m "refactor(cassandra): drop reactions column from message tables

Embedded reactions are being replaced by a dedicated message_reactions
side table; this is the schema-strip step. Production DDL and all
inline test schemas updated together so test containers match."
```

---

## Task 2: Drop `cql:"reactions"` tag from `Message.Reactions`; keep JSON shape

**Files:**
- Modify: `pkg/model/cassandra/message.go:93`
- Modify: `pkg/model/cassandra/message_test.go:185,212`

- [ ] **Step 1: Drop the `cql` tag (keep the field and the `json` tag)**

In `pkg/model/cassandra/message.go` line 93, change:
```go
Reactions             map[string][]Participant `json:"reactions,omitempty"             cql:"reactions"`
```
to:
```go
Reactions             map[string][]Participant `json:"reactions,omitempty"`
```

The field stays — it's the JSON response shape, populated at hydration time. `structScan` (`history-service/internal/cassrepo/utils.go:120`) skips fields without a `cql` tag, so dropping the tag is safe.

- [ ] **Step 2: Drop reactions from existing roundtrip test row literals**

In `pkg/model/cassandra/message_test.go` lines 185 and 212 (and any other test cases that populate the embedded `Reactions` map for round-trip assertions), remove the `Reactions: map[string][]Participant{...}` block from the test literal. **Do not delete the surrounding test cases** — only the reactions blocks.

- [ ] **Step 3: Run model tests**

```bash
make test SERVICE=pkg/model/cassandra
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/model/cassandra/message.go pkg/model/cassandra/message_test.go
git commit -m "refactor(model): drop cql tag from Message.Reactions

Field stays as the JSON response shape; reactions are now populated
by history-service hydration from the new message_reactions side
table instead of scanned as a column."
```

---

## Task 3: Drop `reactions` from cassrepo `baseColumns` + thread columns; clean up existing integration tests

**Files:**
- Modify: `history-service/internal/cassrepo/messages_by_room.go:16`
- Modify: `history-service/internal/cassrepo/thread_messages.go:16`
- Modify: `history-service/internal/cassrepo/messages_by_room_integration_test.go`
- Modify: `history-service/internal/cassrepo/messages_by_id_integration_test.go:165-169`
- Modify: `history-service/internal/cassrepo/thread_messages_integration_test.go:301-305`

- [ ] **Step 1: Drop `reactions,` from `baseColumns` in `messages_by_room.go`**

Change line 13-17 from:
```go
const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, reactions, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"
```
to:
```go
const baseColumns = "room_id, created_at, message_id, thread_room_id, sender, " +
	"msg, mentions, attachments, file, card, card_action, tshow, tcount, " +
	"thread_parent_id, thread_parent_created_at, quoted_parent_message, " +
	"visible_to, deleted, " +
	"type, sys_msg_data, site_id, edited_at, updated_at"
```

(`messages_by_id.go` reuses this constant via `messageByIDQuery`, so it picks up the change automatically.)

- [ ] **Step 2: Drop `reactions,` from `thread_messages.go` column list (line 16)**

Find the line ending `quoted_parent_message, visible_to, reactions, deleted,` and remove `reactions, ` from it.

- [ ] **Step 3: Remove `Reactions:` blocks from existing integration test seed data**

In the three integration test files listed under **Files**, find every `Reactions: map[string][]cassandra.Participant{...}` block in seed literals and the corresponding assertion blocks (e.g. `require.Equal(t, ..., got.Reactions)`) and delete those blocks **only**. Keep every other column's seed/assertion — these tests verify ~20-column row round-trips and reactions are only one of them.

- [ ] **Step 4: Run cassrepo integration tests**

```bash
make test-integration SERVICE=history-service
```
Expected: PASS. (If they fail because the schema still has `reactions` — Task 1 wasn't applied or test cache is stale; run `go clean -testcache` and retry.)

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/
git commit -m "refactor(cassrepo): drop reactions from read columns and tests

Repository no longer scans the reactions column from message tables.
Surgical removal of reactions blocks from existing integration tests
keeps the surrounding row round-trip coverage intact."
```

---

## Task 4: Create the `message_reactions` table — DDL + integration-test setup

**Files:**
- Create: `docker-local/cassandra/init/14-table-message_reactions.cql`
- Modify: `history-service/internal/cassrepo/integration_test.go` (extend `setupCassandra`)

- [ ] **Step 1: Create the production DDL file**

Write `docker-local/cassandra/init/14-table-message_reactions.cql`:
```cql
CREATE TABLE IF NOT EXISTS chat.message_reactions (
  message_id TEXT,
  emoji      TEXT,
  users      SET<FROZEN<"Participant">>,
  PRIMARY KEY ((message_id), emoji)
) WITH compaction = {'class': 'LeveledCompactionStrategy'};
```

The outer SET is unfrozen (allows `users + {?}` / `users - {?}` writes); the inner UDT is frozen because Cassandra requires UDTs inside collections to be frozen.

- [ ] **Step 2: Extend `setupCassandra` to create the table**

In `history-service/internal/cassrepo/integration_test.go`, add a `require.NoError(t, adminSession.Query(cql(...)).Exec())` block after the existing `CREATE TABLE` statements:
```go
require.NoError(t, adminSession.Query(cql(`CREATE TABLE IF NOT EXISTS %s.message_reactions (
  message_id TEXT,
  emoji      TEXT,
  users      SET<FROZEN<"Participant">>,
  PRIMARY KEY ((message_id), emoji)
) WITH compaction = {'class': 'LeveledCompactionStrategy'}`)).Exec())
```

- [ ] **Step 3: Verify schema applies**

```bash
make test-integration SERVICE=history-service
```
Expected: PASS (existing tests still green; new table created in test keyspace but not yet used).

- [ ] **Step 4: Commit**

```bash
git add docker-local/cassandra/init/14-table-message_reactions.cql history-service/internal/cassrepo/integration_test.go
git commit -m "feat(cassandra): create message_reactions side table

((message_id), emoji) partition-per-message with unfrozen outer SET
and LCS compaction. No reader/writer yet; this just lands the DDL."
```

---

## Task 5: `MessageReactionRow` model type with roundtrip test (TDD)

**Files:**
- Create: `pkg/model/cassandra/reaction.go`
- Create: `pkg/model/cassandra/reaction_test.go`

- [ ] **Step 1: Write the failing test first**

Create `pkg/model/cassandra/reaction_test.go`:
```go
package cassandra

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageReactionRow_JSONRoundTrip(t *testing.T) {
	original := MessageReactionRow{
		MessageID: "msg-123",
		Emoji:     "👍",
		Users: []Participant{
			{ID: "u1", EngName: "Alice", Account: "alice"},
			{ID: "u2", EngName: "Bob", Account: "bob"},
		},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded MessageReactionRow
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, original, decoded)
}
```

- [ ] **Step 2: Run test, confirm it fails**

```bash
make test SERVICE=pkg/model/cassandra
```
Expected: FAIL with `undefined: MessageReactionRow`.

- [ ] **Step 3: Create the type**

Create `pkg/model/cassandra/reaction.go`:
```go
package cassandra

// MessageReactionRow maps to the chat.message_reactions Cassandra table.
// No bson tag — Cassandra-only carrier (matches Participant / File / Card
// precedent in this package).
type MessageReactionRow struct {
	MessageID string        `json:"messageId" cql:"message_id"`
	Emoji     string        `json:"emoji"     cql:"emoji"`
	Users     []Participant `json:"users"     cql:"users"`
}
```

- [ ] **Step 4: Run test, confirm it passes**

```bash
make test SERVICE=pkg/model/cassandra
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/cassandra/reaction.go pkg/model/cassandra/reaction_test.go
git commit -m "feat(model): add MessageReactionRow for message_reactions table"
```

---

## Task 6: Add `REACTIONS_FETCH_CONCURRENCY` config

**Files:**
- Modify: `history-service/internal/config/config.go:39-41`

- [ ] **Step 1: Add the env-driven config field**

In `history-service/internal/config/config.go`, alongside the existing message-related fields (around lines 39-41):
```go
MessageBucketHours        int `env:"MESSAGE_BUCKET_HOURS"        envDefault:"72"`
MessageReadMaxBuckets     int `env:"MESSAGE_READ_MAX_BUCKETS"    envDefault:"122"`
MessageHistoryFloorDays   int `env:"MESSAGE_HISTORY_FLOOR_DAYS"  envDefault:"365"`
ReactionsFetchConcurrency int `env:"REACTIONS_FETCH_CONCURRENCY" envDefault:"50"`
```

(Match the existing tag-alignment style in the file.)

- [ ] **Step 2: Verify compilation**

```bash
make build SERVICE=history-service
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/config/config.go
git commit -m "feat(history-service): add REACTIONS_FETCH_CONCURRENCY config

Caps the per-call errgroup fan-out for reaction hydration.
Default 50; will be threaded through to the new cassrepo methods."
```

---

## Task 7: `GetReactionsByMessageID` — integration test, then implement (TDD)

**Files:**
- Create: `history-service/internal/cassrepo/message_reactions.go` (the repo file; this task adds the single-message variant)
- Create: `history-service/internal/cassrepo/message_reactions_integration_test.go`

- [ ] **Step 1: Extend `NewRepository` with a `reactionsConcurrency` parameter**

The existing constructor (`repository.go:20`) is `NewRepository(session *gocql.Session, bucket msgbucket.Sizer, maxBuckets int)`. Add a fourth parameter so the new fan-out method can read its cap from the struct:

```go
type Repository struct {
	session              *gocql.Session
	bucket               msgbucket.Sizer
	maxBuckets           int
	reactionsConcurrency int
}

func NewRepository(session *gocql.Session, bucket msgbucket.Sizer, maxBuckets, reactionsConcurrency int) *Repository {
	return &Repository{
		session:              session,
		bucket:               bucket,
		maxBuckets:           maxBuckets,
		reactionsConcurrency: reactionsConcurrency,
	}
}
```

Update the four existing call sites to pass the new arg:
- `history-service/cmd/main.go:93` — `cassrepo.NewRepository(cassSession, bucketSizer, cfg.MessageReadMaxBuckets, cfg.ReactionsFetchConcurrency)`
- `history-service/internal/service/integration_test.go:140,203,263` — `cassrepo.NewRepository(session, msgbucket.New(24*time.Hour), 365, 50)`

- [ ] **Step 2: Write the failing integration test**

Create `history-service/internal/cassrepo/message_reactions_integration_test.go`:
```go
//go:build integration

package cassrepo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/msgbucket"
)

func newReactionsRepo(t *testing.T) (*Repository, *gocql.Session) {
	t.Helper()
	session := setupCassandra(t)
	return NewRepository(session, msgbucket.New(24*time.Hour), 365, 50), session
}

func TestRepository_GetReactionsByMessageID_Found(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()
	const msgID = "msg-found"
	alice := cassandra.Participant{ID: "u1", EngName: "Alice", Account: "alice"}
	bob := cassandra.Participant{ID: "u2", EngName: "Bob", Account: "bob"}

	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		msgID, "👍", []cassandra.Participant{alice, bob},
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		msgID, "❤️", []cassandra.Participant{alice},
	).Exec())

	got, err := repo.GetReactionsByMessageID(ctx, msgID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []cassandra.Participant{alice, bob}, got["👍"])
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["❤️"])
}

func TestRepository_GetReactionsByMessageID_NotFound(t *testing.T) {
	repo, _ := newReactionsRepo(t)

	got, err := repo.GetReactionsByMessageID(context.Background(), "msg-does-not-exist")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got, "must return empty map, not nil")
}
```

(Import for `gocql` only needed if `newReactionsRepo` signature references it — adjust if Go complains.)

- [ ] **Step 2: Run test, confirm it fails**

```bash
make test-integration SERVICE=history-service
```
Expected: FAIL with `repo.GetReactionsByMessageID undefined`.

- [ ] **Step 3: Implement `GetReactionsByMessageID` and the `ReactionMap` alias**

Create `history-service/internal/cassrepo/message_reactions.go`:
```go
package cassrepo

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// ReactionMap is a per-message reactions view: emoji -> users who reacted.
type ReactionMap = map[string][]cassandra.Participant

// GetReactionsByMessageID returns all reactions on a single message as
// emoji -> users. Returns an empty ReactionMap (not nil) when the message
// has no reactions; never returns gocql.ErrNotFound for an empty partition.
func (r *Repository) GetReactionsByMessageID(ctx context.Context, messageID string) (ReactionMap, error) {
	iter := r.session.Query(
		`SELECT emoji, users FROM message_reactions WHERE message_id = ?`,
		messageID,
	).WithContext(ctx).Iter()

	out := make(ReactionMap)
	var emoji string
	var users []cassandra.Participant
	for iter.Scan(&emoji, &users) {
		out[emoji] = users
		users = nil // gocql reuses the slice header otherwise
	}
	if err := iter.Close(); err != nil {
		return nil, fmt.Errorf("loading reactions for message %s: %w", messageID, err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run test, confirm it passes**

```bash
make test-integration SERVICE=history-service
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/message_reactions.go history-service/internal/cassrepo/message_reactions_integration_test.go
git commit -m "feat(cassrepo): add GetReactionsByMessageID single-partition read

ReactionMap alias plus the single-message variant used by
GetMessageByID. Always returns a non-nil map; empty partition
surfaces as an empty map, not an error."
```

---

## Task 8: `GetReactionsByMessageIDs` — full edge-case integration tests, then errgroup fan-out (TDD)

**Files:**
- Modify: `history-service/internal/cassrepo/message_reactions.go` (add fan-out method)
- Modify: `history-service/internal/cassrepo/message_reactions_integration_test.go` (add cases)

- [ ] **Step 1: Write the failing tests first**

Append to `message_reactions_integration_test.go`:
```go
func TestRepository_GetReactionsByMessageIDs_Happy(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", EngName: "Alice", Account: "alice"}
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		"m1", "👍", []cassandra.Participant{alice},
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		"m2", "❤️", []cassandra.Participant{alice},
	).Exec())

	got, err := repo.GetReactionsByMessageIDs(ctx, []string{"m1", "m2", "m3"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["m1"]["👍"])
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["m2"]["❤️"])
	_, exists := got["m3"]
	assert.False(t, exists, "messages without reactions must be omitted from result map")
}

func TestRepository_GetReactionsByMessageIDs_EmptyAndNilInput(t *testing.T) {
	repo, _ := newReactionsRepo(t)
	ctx := context.Background()

	got, err := repo.GetReactionsByMessageIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)

	got, err = repo.GetReactionsByMessageIDs(ctx, []string{})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)
}

func TestRepository_GetReactionsByMessageIDs_DeduplicatesInput(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", Account: "alice"}
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		"m1", "👍", []cassandra.Participant{alice},
	).Exec())

	got, err := repo.GetReactionsByMessageIDs(ctx, []string{"m1", "m1", "m1"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["m1"]["👍"])
}

func TestRepository_GetReactionsByMessageIDs_LargeFanOut(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", Account: "alice"}
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("bulk-%03d", i)
		require.NoError(t, session.Query(
			`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
			ids[i], "👍", []cassandra.Participant{alice},
		).Exec())
	}

	got, err := repo.GetReactionsByMessageIDs(ctx, ids)
	require.NoError(t, err)
	assert.Len(t, got, 100)
}

func TestRepository_GetReactionsByMessageIDs_ContextCancellation(t *testing.T) {
	repo, _ := newReactionsRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before call

	_, err := repo.GetReactionsByMessageIDs(ctx, []string{"m1", "m2", "m3"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
```

Add the import `"fmt"` to the test file if not already present.

- [ ] **Step 2: Run tests, confirm they fail**

```bash
make test-integration SERVICE=history-service
```
Expected: FAIL with `repo.GetReactionsByMessageIDs undefined`.

- [ ] **Step 3: Implement the fan-out method**

Append to `history-service/internal/cassrepo/message_reactions.go`:
```go
import (
	// ...existing imports
	"golang.org/x/sync/errgroup"
	"sync"
)

// GetReactionsByMessageIDs fans out parallel single-partition reads via
// errgroup. Each goroutine reads one message's reactions partition directly
// from the replica that owns it (token-aware routing). Returns
// messageID -> ReactionMap. Messages with no reactions are omitted from
// the returned map (callers treat absence as "no reactions"). Empty
// messageIDs returns an empty map without contacting Cassandra. Duplicate
// IDs in input are deduplicated before fan-out. First goroutine error
// cancels siblings via the errgroup-derived context.
func (r *Repository) GetReactionsByMessageIDs(ctx context.Context, messageIDs []string) (map[string]ReactionMap, error) {
	out := make(map[string]ReactionMap)
	if len(messageIDs) == 0 {
		return out, nil
	}

	// Dedupe.
	seen := make(map[string]struct{}, len(messageIDs))
	ids := messageIDs[:0:0]
	for _, id := range messageIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, r.reactionsConcurrency)
	var mu sync.Mutex

	for _, id := range ids {
		id := id
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()

			reactions, err := r.GetReactionsByMessageID(gctx, id)
			if err != nil {
				return fmt.Errorf("loading reactions for message %s: %w", id, err)
			}
			if len(reactions) == 0 {
				return nil
			}
			mu.Lock()
			out[id] = reactions
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("loading reactions for messages: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests under `-race`, confirm they pass**

```bash
make test-integration SERVICE=history-service
```
Expected: PASS (the Makefile already passes `-race`).

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/cassrepo/message_reactions.go history-service/internal/cassrepo/message_reactions_integration_test.go
git commit -m "feat(cassrepo): add GetReactionsByMessageIDs errgroup fan-out

Bounded by REACTIONS_FETCH_CONCURRENCY (passed in at repo construction).
Empty/nil input short-circuits, duplicates are deduped, missing message
IDs are omitted from the result, ctx cancellation aborts all siblings."
```

---

## Task 9: Extend `MessageReader` interface; regenerate mocks

**Files:**
- Modify: `history-service/internal/service/service.go:18-26`
- Modify: `history-service/internal/service/mocks/mock_repository.go` (regenerated)

- [ ] **Step 1: Add the two new methods to `MessageReader`**

In `history-service/internal/service/service.go`, change lines 18-26 to:
```go
type MessageReader interface {
	GetMessagesBefore(ctx context.Context, roomID string, before time.Time, floor time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesBetweenDesc(ctx context.Context, roomID string, since, before time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesAfter(ctx context.Context, roomID string, after time.Time, ceiling time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetAllMessagesAsc(ctx context.Context, roomID string, floor, ceiling time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessageByID(ctx context.Context, messageID string) (*models.Message, error)
	GetThreadMessages(ctx context.Context, roomID, threadRoomID string, before, floor time.Time, pageReq cassrepo.PageRequest) (cassrepo.Page[models.Message], error)
	GetMessagesByIDs(ctx context.Context, messageIDs []string) ([]models.Message, error)
	GetReactionsByMessageID(ctx context.Context, messageID string) (cassrepo.ReactionMap, error)
	GetReactionsByMessageIDs(ctx context.Context, messageIDs []string) (map[string]cassrepo.ReactionMap, error)
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=history-service
```
Expected: succeeds; `internal/service/mocks/mock_repository.go` shows a diff with the two new methods stubbed.

- [ ] **Step 3: Run lint**

```bash
make lint
```
Expected: PASS (generated mocks may need import additions — fix if reported).

- [ ] **Step 4: Verify the compile-time assertion at `service.go:115`**

```bash
make build SERVICE=history-service
```
Expected: PASS. (`var _ MessageRepository = (*cassrepo.Repository)(nil)` will fail to compile if the new methods are missing — confirming both methods exist on `*cassrepo.Repository`.)

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/service.go history-service/internal/service/mocks/
git commit -m "feat(history-service): extend MessageReader with reaction fetchers

Adds GetReactionsByMessageID and GetReactionsByMessageIDs to the
interface that handlers consume. Mocks regenerated."
```

---

## Task 10: `hydrateReactions` service helper (TDD)

**Files:**
- Create: `history-service/internal/service/reactions.go`
- Create: `history-service/internal/service/reactions_test.go`

- [ ] **Step 1: Write the failing unit tests first**

Create `history-service/internal/service/reactions_test.go`:
```go
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service/mocks"
	"github.com/hmchangw/chat/pkg/model/cassandra"
)

func TestHydrateReactions_EmptyInput_SkipsCassandra(t *testing.T) {
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockMessageReader(ctrl)
	reader.EXPECT().GetReactionsByMessageIDs(gomock.Any(), gomock.Any()).Times(0)

	svc := &HistoryService{msgReader: reader}
	require.NoError(t, svc.hydrateReactions(context.Background(), nil))
	require.NoError(t, svc.hydrateReactions(context.Background(), []models.Message{}))
}

func TestHydrateReactions_PopulatesMatchingMessages(t *testing.T) {
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockMessageReader(ctrl)
	alice := cassandra.Participant{ID: "u1", Account: "alice"}

	reader.EXPECT().
		GetReactionsByMessageIDs(gomock.Any(), []string{"m1", "m2", "m3"}).
		Return(map[string]cassrepo.ReactionMap{
			"m1": {"👍": {alice}},
			// m2 has no reactions (omitted)
			"m3": {"❤️": {alice}},
		}, nil)

	svc := &HistoryService{msgReader: reader}
	msgs := []models.Message{
		{Message: cassandra.Message{ID: "m1"}},
		{Message: cassandra.Message{ID: "m2"}},
		{Message: cassandra.Message{ID: "m3"}},
	}
	require.NoError(t, svc.hydrateReactions(context.Background(), msgs))

	assert.Equal(t, map[string][]cassandra.Participant{"👍": {alice}}, msgs[0].Reactions)
	assert.Nil(t, msgs[1].Reactions, "messages with no reactions stay nil")
	assert.Equal(t, map[string][]cassandra.Participant{"❤️": {alice}}, msgs[2].Reactions)
}

func TestHydrateReactions_RepoError_Wraps(t *testing.T) {
	ctrl := gomock.NewController(t)
	reader := mocks.NewMockMessageReader(ctrl)
	sentinel := errors.New("cassandra unreachable")
	reader.EXPECT().GetReactionsByMessageIDs(gomock.Any(), gomock.Any()).Return(nil, sentinel)

	svc := &HistoryService{msgReader: reader}
	err := svc.hydrateReactions(context.Background(), []models.Message{
		{Message: cassandra.Message{ID: "m1"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}
```

> **Note on `Message` struct shape:** `internal/models/message.go` likely defines `models.Message` as a type alias or thin wrapper of `cassandra.Message`. If it's a direct alias (`type Message = cassandra.Message`), the literal `models.Message{ID: "m1"}` works directly. If it embeds the struct, use the shape shown above. Check before writing the test.

- [ ] **Step 2: Run tests, confirm they fail**

```bash
make test SERVICE=history-service
```
Expected: FAIL with `svc.hydrateReactions undefined`.

- [ ] **Step 3: Implement the helper**

Create `history-service/internal/service/reactions.go`:
```go
package service

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/history-service/internal/models"
)

// hydrateReactions fills msgs[i].Reactions from the message_reactions side
// table via a bounded errgroup fan-out. Messages with no reactions are left
// with a nil Reactions map (callers and JSON encoding must treat nil as
// "no reactions"). Returns the wrapped repo error on failure; on success
// msgs is mutated in place.
func (s *HistoryService) hydrateReactions(ctx context.Context, msgs []models.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	reactions, err := s.msgReader.GetReactionsByMessageIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("hydrating reactions: %w", err)
	}
	for i := range msgs {
		if r, ok := reactions[msgs[i].ID]; ok {
			msgs[i].Reactions = r
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests, confirm they pass**

```bash
make test SERVICE=history-service
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/reactions.go history-service/internal/service/reactions_test.go
git commit -m "feat(history-service): add hydrateReactions service helper

Centralised fan-out used by every handler that returns Messages.
Empty input short-circuits, repo errors wrap with context."
```

---

## Task 11: Wire `hydrateReactions` into multi-message handlers

**Files:**
- Modify: `history-service/internal/service/messages.go`
- Modify: `history-service/internal/service/threads.go`

The four multi-message handlers — `LoadHistory`, `LoadNextMessages`, `LoadSurroundingMessages`, `GetThreadMessages`, `GetThreadParentMessages` — call hydration after page assembly.

- [ ] **Step 1: Wire into `LoadHistory` (service.go:100 / messages.go)**

Locate the existing return path that assembles the message page (`messages.go`). Immediately before returning the response, add:
```go
if err := s.hydrateReactions(ctx, page.Items); err != nil {
    return nil, fmt.Errorf("load history: %w", err)
}
```

(Use the appropriate slice variable name — `page.Items`, `msgs`, etc. as defined in that handler.)

- [ ] **Step 2: Wire into `LoadNextMessages`, `LoadSurroundingMessages`**

Same pattern. The hydration call goes immediately before each handler's final response construction.

- [ ] **Step 3: Wire into `GetThreadMessages` and `GetThreadParentMessages` (threads.go)**

`GetThreadParentMessages` (`threads.go:124`) returns `parentMessages` — hydrate that slice. `GetThreadMessages` returns its assembled page; hydrate `page.Items` similarly.

- [ ] **Step 4: Add a handler-level test asserting hydration is called per handler**

In each affected `*_test.go`, add or update a table case that:
- mocks `GetReactionsByMessageIDs` to return a reaction for one of the seeded messages
- asserts the response's `Messages[i].Reactions` matches

Example pattern (adapt to each handler's existing test scaffold):
```go
reader.EXPECT().GetReactionsByMessageIDs(gomock.Any(), []string{"m1"}).
    Return(map[string]cassrepo.ReactionMap{"m1": {"👍": {alice}}}, nil)
// ... rest of handler test setup ...
resp, err := svc.LoadHistory(...)
require.NoError(t, err)
assert.Equal(t, map[string][]cassandra.Participant{"👍": {alice}}, resp.Messages[0].Reactions)
```

- [ ] **Step 5: Run all tests**

```bash
make test SERVICE=history-service
make test-integration SERVICE=history-service
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add history-service/internal/service/messages.go history-service/internal/service/threads.go history-service/internal/service/
git commit -m "feat(history-service): hydrate reactions in multi-message handlers

LoadHistory, LoadNextMessages, LoadSurroundingMessages,
GetThreadMessages, GetThreadParentMessages all call hydrateReactions
after page assembly. Reactions error fails the whole request
(no partial pages)."
```

---

## Task 12: Wire single-message hydration into `GetMessageByID`

**Files:**
- Modify: `history-service/internal/service/messages.go` (or wherever `GetMessageByID` lives)

- [ ] **Step 1: Add a failing handler test**

In the handler's `_test.go`, add a case:
```go
func TestHistoryService_GetMessageByID_HydratesReactions(t *testing.T) {
    // ... existing setup ...
    reader.EXPECT().GetMessageByID(gomock.Any(), "m1").Return(&models.Message{
        Message: cassandra.Message{ID: "m1", RoomID: "room-1"},
    }, nil)
    reader.EXPECT().GetReactionsByMessageID(gomock.Any(), "m1").
        Return(cassrepo.ReactionMap{"👍": {alice}}, nil)
    // ... auth-check expectations as per existing pattern ...

    resp, err := svc.GetMessageByID(...)
    require.NoError(t, err)
    assert.Equal(t, map[string][]cassandra.Participant{"👍": {alice}}, resp.Message.Reactions)
}
```

- [ ] **Step 2: Run the test, confirm it fails**

```bash
make test SERVICE=history-service
```
Expected: FAIL — handler doesn't fetch reactions yet.

- [ ] **Step 3: Wire the single-variant call into `GetMessageByID`**

In the handler, after the auth check and immediately before constructing the response:
```go
reactions, err := s.msgReader.GetReactionsByMessageID(ctx, msg.ID)
if err != nil {
    return nil, fmt.Errorf("get message by id: %w", err)
}
msg.Reactions = reactions
```

- [ ] **Step 4: Run all history-service tests**

```bash
make test SERVICE=history-service
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/service/
git commit -m "feat(history-service): hydrate reactions in GetMessageByID

Uses the single-partition variant (no fan-out). Reactions error
fails the whole request."
```

---

## Task 13: Add benchmark for the fan-out path

**Files:**
- Create: `history-service/internal/cassrepo/message_reactions_bench_test.go`

- [ ] **Step 1: Write the benchmark**

Create `history-service/internal/cassrepo/message_reactions_bench_test.go`:
```go
//go:build integration

package cassrepo

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

func BenchmarkGetReactionsByMessageIDs(b *testing.B) {
	session := setupCassandraBench(b)
	repo, _ := newReactionsRepo(t)
	_ = session // session is provided via the helper; ignore the second return if not needed
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", Account: "alice"}
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = fmt.Sprintf("bench-%03d", i)
		for j := 0; j < 5; j++ {
			emoji := fmt.Sprintf("e%d", j)
			require.NoError(b, session.Query(
				`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
				ids[i], emoji, []cassandra.Participant{alice},
			).Exec())
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.GetReactionsByMessageIDs(ctx, ids); err != nil {
			b.Fatal(err)
		}
	}
}
```

> If `setupCassandra(t *testing.T)` doesn't accept `*testing.B`, refactor it to take `testing.TB` (the common interface) so it works for both. The change is a one-line signature swap.

- [ ] **Step 2: Run the benchmark once to verify it executes**

```bash
go test -tags=integration -bench=BenchmarkGetReactionsByMessageIDs -benchtime=3x ./history-service/internal/cassrepo/
```
Expected: succeeds, prints ns/op.

- [ ] **Step 3: Commit**

```bash
git add history-service/internal/cassrepo/message_reactions_bench_test.go
git commit -m "test(cassrepo): add benchmark for GetReactionsByMessageIDs

50 messages × 5 reactions each. Baseline for future read-path
refactors."
```

---

## Task 14: Update `docs/cassandra_message_model.md`

**Files:**
- Modify: `docs/cassandra_message_model.md`

- [ ] **Step 1: Remove `reactions` rows from the four message-table sections**

Find each of these sections (search for the table names) and delete the `reactions MAP<...>` row from the column listing:
- `messages_by_room`
- `thread_messages_by_room`
- `pinned_messages_by_room`
- `messages_by_id`

- [ ] **Step 2: Add a new "Reaction table" section**

Append after the existing message-table sections:
```markdown
## Reaction table

### `message_reactions`

| Column     | Type                          | Notes                             |
|------------|-------------------------------|-----------------------------------|
| message_id | TEXT                          | Partition key. Unique site-wide.  |
| emoji      | TEXT                          | Clustering key.                   |
| users      | SET<FROZEN<"Participant">>    | Outer SET unfrozen for `+`/`-`.   |

`PRIMARY KEY ((message_id), emoji)` — partition-per-message. LCS compaction.

**Bucketing:** none. `MESSAGE_BUCKET_HOURS` does NOT apply to this table.
`created_at` is deliberately excluded from the PK — no time-range queries
on reactions.

**Read pattern:** history-service hydrates reactions at read time via
parallel single-partition queries (errgroup, token-aware routing). One
side table covers both regular messages and thread replies — message IDs
are unique site-wide.
```

- [ ] **Step 3: Update the `MESSAGE_BUCKET_HOURS` paragraph**

Find the existing paragraph about `MESSAGE_BUCKET_HOURS` and append:
> *Applies to `messages_by_room` and `thread_messages_by_room` only. NOT to `message_reactions` (which has no bucketing).*

- [ ] **Step 4: Commit**

```bash
git add docs/cassandra_message_model.md
git commit -m "docs(cassandra): document message_reactions side table

Drop the reactions column from the four message-table sections,
add a Reaction table section, clarify MESSAGE_BUCKET_HOURS scope."
```

---

## Task 15: Update `docs/client-api.md`

**Files:**
- Modify: `docs/client-api.md:949`

- [ ] **Step 1: Add the hydration note**

In `docs/client-api.md`, find the `reactions` field description near line 949:
```
| `reactions` | object | Optional. Map of `emoji → Participant[]`. |
```
Append `Populated server-side via hydration; never written by clients.` to that description (or insert as a new line directly after, matching the file's formatting).

- [ ] **Step 2: Commit**

```bash
git add docs/client-api.md
git commit -m "docs(client-api): note that reactions are server-hydrated

CLAUDE.md §5 requires client-api.md updates in PRs that touch
client-facing handlers. The wire shape is unchanged; this clarifies
that reactions on every history endpoint are populated server-side
from the new message_reactions table."
```

---

## Task 16: Final verification + drop session review reports

**Files:**
- Delete: any leftover files under `docs/reviews/`

- [ ] **Step 1: Verify all gates green**

```bash
make lint
make test
make test-integration SERVICE=history-service
make sast
```
Expected: every command exits 0.

- [ ] **Step 2: Verify coverage on new code meets the spec target**

```bash
go test -tags=integration -coverprofile=coverage.out ./history-service/internal/cassrepo/ ./history-service/internal/service/
go tool cover -func=coverage.out | grep -E 'message_reactions|reactions\.go|hydrateReactions'
```
Expected: each new file ≥80% (target ≥90% on `message_reactions.go` and `reactions.go`). If below, add tests for the missing branches before proceeding.

- [ ] **Step 3: Drop any session review reports per CLAUDE.md §5**

```bash
git rm -r docs/reviews/ 2>/dev/null || true
# Verify nothing remains
ls docs/reviews/ 2>/dev/null && echo "WARNING: docs/reviews/ still exists" || echo "ok"
```

If files were removed, commit:
```bash
git commit -m "chore: drop session review reports before PR"
```

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin claude/modest-mccarthy-KarZE
```

Open the PR with a description that references `docs/specs/message-reactions-table.md` for full context and lists the acceptance criteria from §13 of the spec as a checklist.

---

## Acceptance Criteria (from spec §13)

- [ ] All 6 history handlers (LoadHistory, LoadNextMessages, LoadSurroundingMessages, GetMessageByID, GetThreadMessages, GetThreadParentMessages) hydrate reactions.
- [ ] Integration tests cover sparse, empty, full pages, ctx-cancel, `-race` parallelism.
- [ ] ≥80% coverage on new code; ≥90% target on `cassrepo/message_reactions.go` and `service.reactions.go`.
- [ ] `make lint`, `make test`, `make test-integration SERVICE=history-service`, `make sast` all green.
- [ ] `docs/cassandra_message_model.md` and `docs/client-api.md` updated in the same PR.
- [ ] `docs/reviews/` empty before opening PR.
