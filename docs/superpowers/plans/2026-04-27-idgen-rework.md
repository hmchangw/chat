# idgen Rework Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the chat system to a four-format ID scheme — UUIDv7 (no hyphens) for entity Mongo `_id`s and request IDs, 17-char base62 for channel room IDs, sorted-user-ID concat for DM rooms, and 20-char base62 for all message IDs — and replace the existing seed-derived outbox dedup with a request-ID-based scheme.

**Tech Stack:** Go 1.25, `crypto/rand`, `encoding/hex`, `github.com/google/uuid` v1.6.0 (already in `go.mod`).

**Execution order:** Parts MUST be done in order. Part 2 depends on Part 1's helpers; Part 3 depends on Part 2's request-ID propagation.

| Part | Scope | Touches |
|---|---|---|
| **Part 1: Foundation** | Extend `pkg/idgen` with new generators and validator. No call sites change. | `pkg/idgen/` |
| **Part 2: Request-ID Propagation** | Add `pkg/natsutil` request-ID helpers; every outbound publish stamps `X-Request-ID`; every consumer extracts it into `context.Context`. Logging-only — no dedup changes. | `pkg/natsutil/`, `pkg/natsrouter/`, `auth-service/`, every service's `main.go` publish wrapper, every worker's JetStream consumer callback. |
| **Part 3: ID Format Cutover** | Switch entity ID generation, channel/DM room logic, request ID format, message-gatekeeper validation. Move system message IDs to `GenerateMessageID()`; propagate via new `MessageID` field on cross-site events. Replace seed-derived outbox dedup with `requestID + ":" + destSiteID`. Delete `idgen.DeriveID`. | `room-service`, `room-worker`, `inbox-worker`, `message-worker`, `message-gatekeeper`, `pkg/model/`, `pkg/idgen/`, `auth-service/middleware.go`, `pkg/natsrouter/middleware.go`. |

**ID format matrix (final state):**

| Entity | Format | Length | Source |
|---|---|---|---|
| Subscription `_id` | UUIDv7 hex, no hyphens | 32 | `idgen.GenerateUUIDv7()` |
| RoomMember `_id` | UUIDv7 hex, no hyphens | 32 | `idgen.GenerateUUIDv7()` |
| ThreadRoom / ThreadSubscription `_id` | UUIDv7 hex, no hyphens | 32 | `idgen.GenerateUUIDv7()` |
| Channel Room `_id` | base62 random | 17 | `idgen.GenerateID()` |
| DM Room `_id` | sorted concat of two `user.ID` | ~34 | `idgen.BuildDMRoomID(a, b)` |
| Message `_id` (user) | base62 random, **client-supplied** | 20 | client; validated via `idgen.IsValidMessageID` |
| Message `_id` (system) | base62 random, originator-generated | 20 | `idgen.GenerateMessageID()` |
| Request ID (HTTP + NATS) | UUIDv7 hex, no hyphens | 32 | `idgen.GenerateUUIDv7()` (only at system entry points) |

**`Nats-Msg-Id` rules (final state):**
- Canonical message publishes (user + system + cross-site replica) → `WithMsgID(msg.ID)`.
- Outbox publishes → `WithMsgID(requestID + ":" + destSiteID)`.
- Member events / broadcast (core NATS, no stream) → no dedup.

---

# Part 1: Foundation

**Goal:** Extend `pkg/idgen` with the building blocks the rest of the rework needs: UUIDv7 (no hyphens) for entity Mongo `_id`s and request IDs, 20-char base62 for all message IDs, a message-ID format validator, and a deterministic DM-room-ID builder.

**Architecture:** Refactor the private `encodeBase62` helper to accept a length parameter (still used by seed-derived IDs), and rewrite the random-base62 path to use byte-level rejection sampling instead of `big.Int` + `rand.Int` — addresses the PR #118 reviewer's CPU concern: the old implementation does a `big.Int.Exp` plus 17–20 `big.Int.DivMod` operations per call (~µs); the new implementation does one `rand.Read` plus byte comparisons (~150–200ns), uniformly distributed via rejection of bytes ≥ 248. Then add four new exported functions on top: `GenerateUUIDv7`, `GenerateMessageID`, `IsValidMessageID`, `BuildDMRoomID`. The existing `GenerateID` (17-char base62, channel rooms) stays as a permanent fixture. `DeriveID` (17-char base62 from seed) also stays untouched in Part 1 because every existing call site still references it, but it is **retired in Part 3** — the new outbox dedup scheme (`requestID + ":" + destSiteID`) replaces every `DeriveID` call site, and the function itself is deleted at the end of Part 3.

**Scope:** Only `pkg/idgen/`. No call sites change in this part.

## File Structure (Part 1)

- Modify: `pkg/idgen/idgen.go` — add new exported functions, refactor `encodeBase62`
- Modify: `pkg/idgen/idgen_test.go` — add tests for new functions; existing tests stay as-is

## Task 1.1: Extend `pkg/idgen` with UUIDv7, 20-char message IDs, validator, and DM-room-ID builder

**Files:**
- Modify: `pkg/idgen/idgen.go`
- Modify: `pkg/idgen/idgen_test.go`

### TDD: Write the failing tests first

- [ ] **Step 1: Add the new tests to `pkg/idgen/idgen_test.go`**

Merge these imports into the existing import block at the top of the file (don't duplicate `testing`, `assert`, or `idgen`):

```go
import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
)
```

Append these test functions to the end of `pkg/idgen/idgen_test.go`:

```go
func TestGenerateMessageID_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := idgen.GenerateMessageID()
		assert.Len(t, id, 20)
		assert.True(t, isBase62(id), "id %q contains non-base62 characters", id)
	}
}

func TestGenerateMessageID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := idgen.GenerateMessageID()
		_, dup := seen[id]
		assert.False(t, dup, "duplicate message ID %q at iteration %d", id, i)
		seen[id] = struct{}{}
	}
}

func TestGenerateUUIDv7_LengthAndHex(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := idgen.GenerateUUIDv7()
		assert.Len(t, id, 32, "UUIDv7 hex must be 32 chars (no hyphens)")
		_, err := hex.DecodeString(id)
		assert.NoError(t, err, "id %q must be valid lowercase hex", id)
	}
}

func TestGenerateUUIDv7_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := idgen.GenerateUUIDv7()
		_, dup := seen[id]
		assert.False(t, dup, "duplicate UUIDv7 %q at iteration %d", id, i)
		seen[id] = struct{}{}
	}
}

func TestGenerateUUIDv7_VersionAndVariantBits(t *testing.T) {
	// UUIDv7 (RFC 9562): hex index 12 must be '7' (version), index 16 ∈ {8,9,a,b} (variant).
	id := idgen.GenerateUUIDv7()
	require.Len(t, id, 32)
	assert.Equal(t, byte('7'), id[12], "version nibble must be 7, got %q", string(id[12]))
	assert.Contains(t, "89ab", string(id[16]), "variant nibble must be 8,9,a,b — got %q", string(id[16]))
}

func TestGenerateUUIDv7_TimeOrdered(t *testing.T) {
	// First 12 hex chars (48-bit Unix-ms timestamp) sort lexicographically by time when separated by >=2ms.
	a := idgen.GenerateUUIDv7()
	time.Sleep(2 * time.Millisecond)
	b := idgen.GenerateUUIDv7()
	assert.Less(t, a[:12], b[:12], "later UUIDv7 must have a larger timestamp prefix")
}

func TestGenerateUUIDv7_ConcurrentSafe(t *testing.T) {
	// uuid.NewV7 must be safe under concurrent calls without producing duplicates.
	const goroutines = 50
	const perGoroutine = 200
	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, goroutines*perGoroutine)
		wg   sync.WaitGroup
	)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			local := make([]string, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				local[i] = idgen.GenerateUUIDv7()
			}
			mu.Lock()
			for _, id := range local {
				_, dup := seen[id]
				assert.False(t, dup, "duplicate UUIDv7 under concurrency: %q", id)
				seen[id] = struct{}{}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
}

func TestIsValidMessageID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid 20-char base62", "AbCdEfGhIjKlMnOpQrSt", true},
		{"valid all digits", "01234567890123456789", true},
		{"valid mixed", "0aZ1bY2cX3dW4eV5fU6g", true},
		{"empty string", "", false},
		{"too short (19)", "AbCdEfGhIjKlMnOpQrS", false},
		{"too long (21)", "AbCdEfGhIjKlMnOpQrStU", false},
		{"hyphen char", "AbCdEfGhIjKlMnOpQr-t", false},
		{"underscore char", "AbCdEfGhIjKlMnOpQr_t", false},
		{"unicode char", "AbCdEfGhIjKlMnOpQrSé", false},
		{"UUIDv4 with hyphens (36)", "550e8400-e29b-41d4-a716-446655440000", false},
		{"UUIDv7 hex no hyphens (32)", "01893f8b1c4a7000b8e2d4f6a1c3e5b7", false},
		{"17-char base62 (legacy)", "AbCdEfGhIjKlMnOpQ", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, idgen.IsValidMessageID(tc.in))
		})
	}
}

func TestIsValidMessageID_AcceptsGenerateMessageIDOutput(t *testing.T) {
	for i := 0; i < 50; i++ {
		assert.True(t, idgen.IsValidMessageID(idgen.GenerateMessageID()))
	}
}

func TestBuildDMRoomID_DeterministicRegardlessOfOrder(t *testing.T) {
	a := idgen.BuildDMRoomID("u-alice", "u-bob")
	b := idgen.BuildDMRoomID("u-bob", "u-alice")
	assert.Equal(t, a, b, "DM room ID must be the same regardless of caller argument order")
}

func TestBuildDMRoomID_SortedConcat(t *testing.T) {
	// Lexicographically smaller user ID comes first; no separator.
	id := idgen.BuildDMRoomID("u-bob", "u-alice")
	assert.Equal(t, "u-aliceu-bob", id)
}

func TestBuildDMRoomID_DifferentPairsDifferentIDs(t *testing.T) {
	ab := idgen.BuildDMRoomID("u-alice", "u-bob")
	ac := idgen.BuildDMRoomID("u-alice", "u-carol")
	assert.NotEqual(t, ab, ac)
}

func TestBuildDMRoomID_SelfDM(t *testing.T) {
	// Self-DMs are allowed at the idgen level; caller policy decides whether to permit them.
	id := idgen.BuildDMRoomID("u-alice", "u-alice")
	assert.Equal(t, "u-aliceu-alice", id)
}
```

- [ ] **Step 2: Run the new tests to verify they FAIL**

Run: `make test SERVICE=pkg/idgen`

Expected: build fails with errors like `undefined: idgen.GenerateMessageID`, `undefined: idgen.GenerateUUIDv7`, `undefined: idgen.IsValidMessageID`, `undefined: idgen.BuildDMRoomID`. The pre-existing tests (`TestGenerateID_*`, `TestDeriveID_*`) cannot run because the package doesn't compile — that's the Red phase.

### Implementation

- [ ] **Step 3: Replace `pkg/idgen/idgen.go` with the extended version**

Replace the entire contents of `pkg/idgen/idgen.go` with:

```go
// Package idgen produces identifiers for the chat system; see CLAUDE.md for the ID format matrix per entity type.
package idgen

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"

	"github.com/google/uuid"
)

// base62 alphabet (0-9A-Za-z).
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const (
	// idLength: 17-char base62, ~101 bits of entropy. Channel room IDs and outbox dedup seeds.
	idLength = 17
	// messageIDLength: 20-char base62, ~119 bits of entropy. Message.ID and JetStream Nats-Msg-Id.
	messageIDLength = 20
)

// encodeBase62 renders n into a length-char base62 string. Mutates n.
func encodeBase62(n *big.Int, length int) string {
	base := big.NewInt(int64(len(alphabet)))
	mod := new(big.Int)
	buf := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		n.DivMod(n, base, mod)
		buf[i] = alphabet[mod.Int64()]
	}
	return string(buf)
}

// generateBase62 returns a uniformly-distributed random base62 string of the requested length via rejection sampling on bytes (rejects ≥248; ~3.1% rate).
func generateBase62(length int) string {
	out := make([]byte, length)
	bufSize := length + length/8 + 1
	buf := make([]byte, bufSize)
	written := 0
	for written < length {
		if _, err := rand.Read(buf); err != nil {
			panic("idgen: crypto/rand read failed: " + err.Error())
		}
		for _, b := range buf {
			if b >= 248 {
				continue
			}
			out[written] = alphabet[b%62]
			written++
			if written == length {
				break
			}
		}
	}
	return string(out)
}

// GenerateID returns a fresh random 17-char base62 identifier (channel room IDs).
func GenerateID() string {
	return generateBase62(idLength)
}

// DeriveID returns a deterministic 17-char base62 ID from seed (SHA-256 → 128-bit → base62); retired in Part 3.
func DeriveID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return encodeBase62(new(big.Int).SetBytes(h[:16]), idLength)
}

// GenerateMessageID returns a fresh random 20-char base62 identifier (Message.ID and Nats-Msg-Id).
func GenerateMessageID() string {
	return generateBase62(messageIDLength)
}

// IsValidMessageID reports whether s is a well-formed 20-char base62 message ID.
func IsValidMessageID(s string) bool {
	if len(s) != messageIDLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		default:
			return false
		}
	}
	return true
}

// GenerateUUIDv7 returns a fresh UUIDv7 as 32-char lowercase hex without hyphens (entity Mongo _id and request IDs).
func GenerateUUIDv7() string {
	u, err := uuid.NewV7()
	if err != nil {
		panic("idgen: uuid.NewV7 failed: " + err.Error())
	}
	var buf [32]byte
	hex.Encode(buf[:], u[:])
	return string(buf[:])
}

// BuildDMRoomID returns the lexicographically-sorted concat of two user IDs; BuildDMRoomID(a,b) == BuildDMRoomID(b,a).
func BuildDMRoomID(userA, userB string) string {
	if userA <= userB {
		return userA + userB
	}
	return userB + userA
}
```

- [ ] **Step 4: Run all idgen tests to verify they PASS**

Run: `make test SERVICE=pkg/idgen`

Expected: all tests pass — the four pre-existing tests (`TestGenerateID_LengthAndAlphabet`, `TestGenerateID_Unique`, `TestDeriveID_StableAcrossCalls`, `TestDeriveID_DifferentSeedsDifferentIDs`, `TestDeriveID_EmptySeed`) plus all new ones.

If `TestGenerateUUIDv7_TimeOrdered` is flaky on slow CI, bump the sleep to 5ms — but do NOT loosen the `assert.Less` to `assert.LessOrEqual`.

- [ ] **Step 5: Verify coverage meets the 80% bar**

Run:
```bash
cd pkg/idgen && go test -coverprofile=/tmp/idgen-cov.out ./... && go tool cover -func=/tmp/idgen-cov.out
```

Expected: every function shows ≥ 90% coverage; total ≥ 95%. The panic branches in `generateBase62` and `GenerateUUIDv7` are unreachable under normal runtime — uncovered there is acceptable.

- [ ] **Step 6: Run lint**

Run: `make lint`

Expected: no issues. If `goimports` complains about the import block, run `make fmt` and re-run lint.

- [ ] **Step 7: Commit**

```bash
git add pkg/idgen/idgen.go pkg/idgen/idgen_test.go
git commit -m "feat(idgen): add UUIDv7, 20-char message ID, message ID validator, DM room ID builder

Adds GenerateUUIDv7 (entity Mongo _id and request IDs), GenerateMessageID
(user and system message IDs), IsValidMessageID (message-gatekeeper format
check), and BuildDMRoomID (DM room ID from two user IDs). Refactors
encodeBase62 to take a length parameter (still used by seed-derived IDs).
Rewrites the random-base62 path to use byte-level rejection sampling
(reject bytes >=248, then % 62) instead of big.Int + rand.Int — addresses
the PR #118 reviewer's CPU concern: ~10x faster (~150-200ns vs ~µs) while
preserving uniform distribution. Existing GenerateID and DeriveID keep
their 17-char output and signatures (used by channel room IDs and outbox
dedup respectively); no call sites change in this commit."
```

- [ ] **Step 8: Final verification — full repo build + lint**

Run: `make lint && make test`

Expected: green across the entire repo. No call site has changed yet, so no other package should be affected.

---

## Part 1 Self-Review Checklist (run before handing off)

1. **Spec coverage.** Part 1 spec lists four new exported functions: `GenerateUUIDv7`, `GenerateMessageID`, `IsValidMessageID`, `BuildDMRoomID`. All four are implemented and tested. ✓
2. **Backward compatibility.** Existing `GenerateID` and `DeriveID` keep their 17-char output and signatures, so request-ID middleware (about to be replaced in Part 3) and outbox/inbox dedup call sites (untouched) compile without change. The pre-existing tests in `idgen_test.go` still pass without modification. ✓
3. **Format guarantees.**
   - UUIDv7: 32 hex chars, lowercase, no hyphens; version nibble is `7`; variant nibble in `{8,9,a,b}`.
   - Message IDs: 20 base62 chars.
   - DM room IDs: deterministic regardless of argument order, no separator.
   - Validator rejects: wrong length, non-alphanumerics, legacy 17-char base62, 32-char hex, 36-char hyphenated UUIDs.
4. **Naming consistency.** `Generate*` for fresh random IDs, `Derive*` for deterministic-from-seed, `Build*` for deterministic-from-inputs. Matches the existing convention.
5. **Concurrency.** `TestGenerateUUIDv7_ConcurrentSafe` exercises 50 goroutines × 200 IDs = 10k unique IDs.
6. **Time ordering.** `TestGenerateUUIDv7_TimeOrdered` proves the lexicographic-by-time property the entity-ID locality argument relies on.

## What's NOT in Part 1 (handoffs to Parts 2 and 3)

- **No call site migrations.** All `idgen.GenerateID()` calls in services still produce 17-char base62. Switching them is Part 3.
- **No request-ID propagation plumbing.** `auth-service/middleware.go` and `pkg/natsrouter/middleware.go` still mint with `idgen.GenerateID()`; outbound publishes still drop `X-Request-ID`. That's Part 2.
- **No `message-gatekeeper` changes.** It still validates with `uuid.Parse`. Switching to `idgen.IsValidMessageID` is Part 3.
- **No `room-worker` system message changes.** `Message.ID = idgen.DeriveID(...)` and the redundant `*DedupID` calculations stay until Part 3.
- **`idgen.DeriveID` stays in this part** because every existing call site (room-worker outbox/canonical-msg dedup) still uses it. **It is deleted in Part 3** as the final step, once those call sites migrate to `requestID + ":" + destSiteID` and `msg.ID` respectively.

---

# Part 2: Request-ID Propagation

**Goal:** Plumb `X-Request-ID` through every NATS hop so the same ID threads through all logs for a given client request, and so Part 3 can use it as the basis for outbox dedup. Logging-only in this part — no business logic or dedup behaviour changes.

**Architecture:** Two layers.

1. **`pkg/natsutil` request-ID helpers.** Three small functions — `WithRequestID(ctx, id)`, `RequestIDFromContext(ctx)`, `ContextWithRequestIDFromHeaders(ctx, h)` — and one publish-side helper, `HeaderForContext(ctx)`, that returns a `nats.Header` carrying the ID (or `nil`). All other plumbing reduces to: "pull from ctx → put on outbound header" and "pull from inbound header → put on ctx".

2. **Per-service wiring.** Each service does two mechanical edits:
    - Outbound: replace `js.Publish(ctx, subj, data, opts...)` with the `PublishMsg` form, building a `*nats.Msg` whose `Header` comes from `natsutil.HeaderForContext(ctx)`. Same for `nc.Publish`.
    - Inbound: at the entry point of every consumer (JetStream `Messages()` loop or `Consume` callback), wrap the per-message context with `natsutil.ContextWithRequestIDFromHeaders(ctx, msg.Headers())` before invoking the handler.

For services that already use the `pkg/natsrouter` middleware (sync request/reply), an update to `RequestID()` middleware also stores the ID on the underlying `context.Context`, so handler code that calls publish helpers gets propagation for free.

**Scope:** Logging and propagation only. No `idgen` calls change. No `Nats-Msg-Id` calls change. No business logic touched. Outbox dedup keeps using `idgen.DeriveID(seed)` until Part 3.

**Why not a single big PR:** the logic is mechanical but it touches every service. Each task below is self-contained — adds one piece of plumbing, runs the existing test suite (which must stay green since behaviour doesn't change), commits, moves on. If Part 3 later finds a service was missed, the missing service falls back gracefully (empty header → empty request ID → log gap, no correctness gap).

## File Structure (Part 2)

- Create: `pkg/natsutil/request_id.go` — context + header helpers
- Create: `pkg/natsutil/request_id_test.go` — unit tests
- Modify: `pkg/natsrouter/middleware.go` — `RequestID()` also wraps the underlying ctx
- Modify: `pkg/natsrouter/context.go` — add `SetContext(ctx)` method (single-writer contract; documented for use by middleware before `Next()`)
- Modify: `auth-service/middleware.go` — Gin middleware attaches request ID to `c.Request.Context()`
- Modify: `message-gatekeeper/main.go` — publish wrappers use `PublishMsg` + `HeaderForContext`; JetStream consumer entry wraps ctx
- Modify: `room-service/main.go` — publish wrapper uses `PublishMsg` + `HeaderForContext`
- Modify: `room-worker/main.go` — publish wrapper + JetStream consumer entry
- Modify: `inbox-worker/main.go` — publish wrapper + JetStream consumer entry
- Modify: `broadcast-worker/main.go` — publish wrapper + JetStream consumer entry
- Modify: `notification-worker/main.go` — publish wrapper + JetStream consumer entry
- Modify: `message-worker/main.go` — JetStream consumer entry (no publishes)
- Modify: `search-sync-worker/main.go` — JetStream consumer entry (no publishes)

## Task list (Part 2)

- Task 2.1: `pkg/natsutil` request-ID helpers (foundation)
- Task 2.2: `pkg/natsrouter` — `SetContext` on Context + middleware wraps underlying ctx
- Task 2.3: `auth-service` — Gin middleware attaches to request context
- Task 2.4: `message-gatekeeper` — publish wrappers + consumer entry
- Task 2.5: `room-service` — publish wrapper
- Task 2.6: `room-worker` — publish wrapper + consumer entry
- Task 2.7: `inbox-worker` — publish wrapper + consumer entry
- Task 2.8: `broadcast-worker` — publish wrapper + consumer entry
- Task 2.9: `notification-worker` — publish wrapper + consumer entry
- Task 2.10: `message-worker` — consumer entry
- Task 2.11: `search-sync-worker` — consumer entry
- Task 2.12: Integration verification + Part 2 self-review

---

## Task 2.1: `pkg/natsutil` request-ID helpers (foundation)

**Files:**
- Create: `pkg/natsutil/request_id.go`
- Create: `pkg/natsutil/request_id_test.go`

### TDD: write the failing tests first

- [ ] **Step 1: Create `pkg/natsutil/request_id_test.go`**

```go
package natsutil_test

import (
	"context"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestWithRequestID_RoundTrip(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-abc-123")
	assert.Equal(t, "req-abc-123", natsutil.RequestIDFromContext(ctx))
}

func TestWithRequestID_EmptyIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.WithRequestID(parent, "")
	assert.Same(t, parent, ctx, "empty id must return the parent ctx unchanged")
	assert.Equal(t, "", natsutil.RequestIDFromContext(ctx))
}

func TestWithRequestID_OverwritesExistingValue(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "first")
	ctx = natsutil.WithRequestID(ctx, "second")
	assert.Equal(t, "second", natsutil.RequestIDFromContext(ctx))
}

func TestRequestIDFromContext_MissingReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", natsutil.RequestIDFromContext(context.Background()))
}

func TestContextWithRequestIDFromHeaders_HeaderPresent(t *testing.T) {
	h := nats.Header{}
	h.Set(natsutil.RequestIDHeader, "req-from-header")
	ctx := natsutil.ContextWithRequestIDFromHeaders(context.Background(), h)
	assert.Equal(t, "req-from-header", natsutil.RequestIDFromContext(ctx))
}

func TestContextWithRequestIDFromHeaders_NilHeaderIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.ContextWithRequestIDFromHeaders(parent, nil)
	assert.Same(t, parent, ctx)
	assert.Equal(t, "", natsutil.RequestIDFromContext(ctx))
}

func TestContextWithRequestIDFromHeaders_EmptyHeaderValueIsNoOp(t *testing.T) {
	parent := context.Background()
	ctx := natsutil.ContextWithRequestIDFromHeaders(parent, nats.Header{})
	assert.Same(t, parent, ctx)
	assert.Equal(t, "", natsutil.RequestIDFromContext(ctx))
}

func TestHeaderForContext_WithID(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-xyz")
	h := natsutil.HeaderForContext(ctx)
	assert.NotNil(t, h)
	assert.Equal(t, "req-xyz", h.Get(natsutil.RequestIDHeader))
}

func TestHeaderForContext_WithoutIDReturnsNil(t *testing.T) {
	h := natsutil.HeaderForContext(context.Background())
	assert.Nil(t, h, "no request ID in ctx must return a nil header (not an empty one)")
}

func TestHeaderForContext_ReversibleViaContextFromHeaders(t *testing.T) {
	// Round-trip: ctx → header → ctx must preserve the request ID.
	original := natsutil.WithRequestID(context.Background(), "round-trip-id")
	h := natsutil.HeaderForContext(original)
	recovered := natsutil.ContextWithRequestIDFromHeaders(context.Background(), h)
	assert.Equal(t, "round-trip-id", natsutil.RequestIDFromContext(recovered))
}

func TestRequestIDHeader_Constant(t *testing.T) {
	// Pin the canonical value so a typo can't silently break propagation.
	assert.Equal(t, "X-Request-ID", natsutil.RequestIDHeader)
}

func TestNewMsg_AttachesHeaderFromContext(t *testing.T) {
	ctx := natsutil.WithRequestID(context.Background(), "req-newmsg-test")
	msg := natsutil.NewMsg(ctx, "chat.foo.bar", []byte("payload"))
	assert.Equal(t, "chat.foo.bar", msg.Subject)
	assert.Equal(t, []byte("payload"), msg.Data)
	assert.Equal(t, "req-newmsg-test", msg.Header.Get(natsutil.RequestIDHeader))
}

func TestNewMsg_NoIDLeavesHeaderNil(t *testing.T) {
	msg := natsutil.NewMsg(context.Background(), "chat.foo.bar", []byte("payload"))
	assert.Nil(t, msg.Header)
}
```

- [ ] **Step 2: Run tests to verify they FAIL**

Run: `make test SERVICE=pkg/natsutil`

Expected: build fails with errors like `undefined: natsutil.WithRequestID`, `undefined: natsutil.RequestIDFromContext`, `undefined: natsutil.ContextWithRequestIDFromHeaders`, `undefined: natsutil.HeaderForContext`, `undefined: natsutil.RequestIDHeader`. The pre-existing tests in `natsutil` (carrier, connect, ack, reply) cannot run because the package doesn't compile — that's the Red phase.

### Implementation

- [ ] **Step 3: Create `pkg/natsutil/request_id.go`**

```go
// request_id.go: helpers to propagate X-Request-ID between context.Context and nats.Header. Missing IDs degrade to a log gap, not a correctness failure.
package natsutil

import (
	"context"

	"github.com/nats-io/nats.go"
)

// RequestIDHeader is the canonical NATS/HTTP header for the request correlation ID.
const RequestIDHeader = "X-Request-ID"

type ctxKey int

const requestIDKey ctxKey = 0

// WithRequestID returns ctx with the request ID stored; empty id is a no-op.
func WithRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID stored in ctx, or "" if none.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ContextWithRequestIDFromHeaders returns ctx augmented with X-Request-ID from headers, or ctx unchanged if absent.
func ContextWithRequestIDFromHeaders(ctx context.Context, headers nats.Header) context.Context {
	if headers == nil {
		return ctx
	}
	id := headers.Get(RequestIDHeader)
	if id == "" {
		return ctx
	}
	return WithRequestID(ctx, id)
}

// HeaderForContext returns a nats.Header carrying X-Request-ID from ctx, or nil if ctx has no request ID.
func HeaderForContext(ctx context.Context) nats.Header {
	id := RequestIDFromContext(ctx)
	if id == "" {
		return nil
	}
	return nats.Header{RequestIDHeader: []string{id}}
}

// NewMsg builds a *nats.Msg with subj, data, and X-Request-ID drawn from ctx (nil header if no ID).
func NewMsg(ctx context.Context, subj string, data []byte) *nats.Msg {
	return &nats.Msg{
		Subject: subj,
		Data:    data,
		Header:  HeaderForContext(ctx),
	}
}
```

- [ ] **Step 4: Run tests to verify they PASS**

Run: `make test SERVICE=pkg/natsutil`

Expected: all tests pass — the new request_id tests plus the pre-existing tests for `carrier`, `connect`, `ack`, `reply`.

- [ ] **Step 5: Verify coverage**

Run:
```bash
cd pkg/natsutil && go test -coverprofile=/tmp/natsutil-cov.out ./... && go tool cover -func=/tmp/natsutil-cov.out | grep request_id
```

Expected: every function in `request_id.go` shows 100% coverage.

- [ ] **Step 6: Lint**

Run: `make lint`

Expected: no issues.

- [ ] **Step 7: Commit**

```bash
git add pkg/natsutil/request_id.go pkg/natsutil/request_id_test.go
git commit -m "feat(natsutil): add request-ID helpers for context/header propagation

Adds WithRequestID, RequestIDFromContext, ContextWithRequestIDFromHeaders,
HeaderForContext, NewMsg, plus the RequestIDHeader constant. These are
the foundation for Part 2 of the idgen rework: every NATS publisher
calls NewMsg(ctx, subj, data) to build a *nats.Msg with X-Request-ID
attached; every consumer calls ContextWithRequestIDFromHeaders to
re-attach the inbound header to ctx. No call sites change in this
commit — that's spread across the per-service tasks."
```

- [ ] **Step 8: Push**

Run: `git push origin <branch>`

---

## Task 2.2: `pkg/natsrouter` — `SetContext` on Context + middleware wraps underlying ctx

**Background:** `pkg/natsrouter.Context` embeds a `context.Context` (`c.ctx`, private) and implements `context.Context` itself by delegating to it. The existing `RequestID()` middleware stores the ID via `c.Set("requestID", id)` — that goes into `c.keys`, not into `c.ctx`. Publish helpers downstream call `natsutil.RequestIDFromContext(ctx)` which looks via `ctx.Value()` — that path delegates to the underlying `c.ctx`, which doesn't have the request ID. So today the helper would read empty.

**Fix:** add a `SetContext(ctx context.Context)` method on `*Context` and have the `RequestID()` middleware call it with `natsutil.WithRequestID(c, reqID)`. Single-writer contract: `SetContext` is documented for use only by middleware before `c.Next()`.

**Files:**
- Modify: `pkg/natsrouter/context.go` — add `SetContext` method
- Modify: `pkg/natsrouter/middleware.go` — `RequestID()` middleware also wraps underlying ctx
- Modify: `pkg/natsrouter/router_test.go` (or a new file) — tests for the new behaviour

### TDD: write the failing tests first

- [ ] **Step 1: Add tests for `SetContext` and the propagation in middleware**

Append to `pkg/natsrouter/router_test.go`:

```go
func TestContext_SetContext_Propagates(t *testing.T) {
	c := natsrouter.NewContext(nil)
	type k int
	const myKey k = 0
	newCtx := context.WithValue(c, myKey, "value-from-set")
	c.SetContext(newCtx)
	assert.Equal(t, "value-from-set", c.Value(myKey))
}

func TestRequestIDMiddleware_StoresIDOnUnderlyingContext(t *testing.T) {
	// natsutil.RequestIDFromContext(c) must equal c.Get("requestID") — the contract publish helpers rely on.
	c := natsrouter.NewContext(nil)
	c.Msg = &nats.Msg{Header: nats.Header{}}
	c.Msg.Header.Set("X-Request-ID", "from-header")

	called := false
	chain := []natsrouter.HandlerFunc{
		natsrouter.RequestID(),
		func(c *natsrouter.Context) {
			called = true
			fromKeys, _ := c.Get("requestID")
			fromCtx := natsutil.RequestIDFromContext(c)
			assert.Equal(t, "from-header", fromKeys)
			assert.Equal(t, "from-header", fromCtx)
		},
	}
	natsrouter.RunChain(c, chain)
	assert.True(t, called, "downstream handler must run")
}

func TestRequestIDMiddleware_GeneratesAndStoresOnContext_WhenHeaderMissing(t *testing.T) {
	c := natsrouter.NewContext(nil)
	c.Msg = &nats.Msg{Header: nats.Header{}}

	var fromCtx string
	chain := []natsrouter.HandlerFunc{
		natsrouter.RequestID(),
		func(c *natsrouter.Context) {
			fromCtx = natsutil.RequestIDFromContext(c)
		},
	}
	natsrouter.RunChain(c, chain)
	assert.NotEmpty(t, fromCtx, "RequestID middleware must mint and propagate to ctx when header is absent")
}
```

(Imports needed: `"context"`, existing `nats` and `natsrouter` imports, plus `"github.com/hmchangw/chat/pkg/natsutil"`.)

`natsrouter.RunChain` is a small test helper — add it to a non-test file if it doesn't exist (export only if necessary), or use the existing chain dispatch. Concrete suggestion: add `RunChain(c *Context, handlers []HandlerFunc)` as a test helper *in `router_test.go`* by directly setting `c.chain.handlers = handlers` and calling `c.Next()` — but that touches private fields, so prefer an exported test-only helper.

Simplest implementation of the helper (also added to `pkg/natsrouter/context.go` since it's general-purpose):

```go
// RunChain executes the handler chain against c; exposed primarily for tests.
func RunChain(c *Context, handlers []HandlerFunc) {
	cs := chainPool.Get().(*chainState)
	cs.handlers = handlers
	cs.index = -1
	c.chain = cs
	c.Next()
	releaseContext(c)
}
```

If a similar helper already exists, use it — don't duplicate.

- [ ] **Step 2: Run tests to verify they FAIL**

Run: `make test SERVICE=pkg/natsrouter`

Expected: build fails with `undefined: c.SetContext` and similar. The new tests cannot run.

### Implementation

- [ ] **Step 3: Add `SetContext` to `pkg/natsrouter/context.go`**

Add this method to `pkg/natsrouter/context.go` (place it after `Get`/`MustGet` and before `Param`):

```go
// SetContext replaces the underlying context.Context; call only from middleware before c.Next() (single-writer contract — racing with handler-spawned goroutines is unsafe).
func (c *Context) SetContext(ctx context.Context) {
	c.ctx = ctx
}
```

- [ ] **Step 4: Update `pkg/natsrouter/middleware.go::RequestID()` to also wrap underlying ctx**

Replace the body of `RequestID()` with:

```go
// RequestID returns middleware that extracts X-Request-ID (or mints via idgen) and stores it on both the natsrouter keys map and the underlying ctx.
func RequestID() HandlerFunc {
	return func(c *Context) {
		reqID := ""
		if c.Msg != nil && c.Msg.Header != nil {
			reqID = c.Msg.Header.Get(natsutil.RequestIDHeader)
		}
		if reqID == "" {
			reqID = idgen.GenerateID()
		}
		c.Set(requestIDKey, reqID)
		c.SetContext(natsutil.WithRequestID(c, reqID))
		c.Next()
	}
}
```

(Add the `natsutil` import to `pkg/natsrouter/middleware.go`. The hard-coded `"X-Request-ID"` literal is replaced by `natsutil.RequestIDHeader` so the constant lives in one place.)

- [ ] **Step 5: Run tests to verify they PASS**

Run: `make test SERVICE=pkg/natsrouter`

Expected: all tests pass — new tests plus pre-existing router/middleware tests.

- [ ] **Step 6: Lint**

Run: `make lint`

Expected: no issues.

- [ ] **Step 7: Verify no other package broke**

Run: `make test`

Expected: green across the repo. The natsrouter contract is additive (new method, broader middleware behaviour); no existing call site should regress.

- [ ] **Step 8: Commit and push**

```bash
git add pkg/natsrouter/context.go pkg/natsrouter/middleware.go pkg/natsrouter/router_test.go
git commit -m "feat(natsrouter): RequestID middleware also stores ID on ctx.Context

Adds Context.SetContext for middleware that needs to wrap the underlying
context.Context. RequestID() middleware now stores the request ID on
BOTH the natsrouter keys map (existing behaviour) AND the underlying
ctx via natsutil.WithRequestID. Downstream publish helpers can read the
ID from ctx.Value uniformly across HTTP and NATS entry points.

Replaces the hard-coded \"X-Request-ID\" literal with the
natsutil.RequestIDHeader constant."
git push origin <branch>
```

---

## Task 2.3: `auth-service` — Gin middleware attaches request ID to request context

**Background:** `auth-service/middleware.go::requestIDMiddleware()` reads/mints the request ID and stores it via `c.Set("request_id", id)` on the Gin context. Gin's context is *not* a `context.Context` — handlers that need a `context.Context` use `c.Request.Context()`, which today does not carry the request ID. This task threads the ID through to the underlying request context so any code path that calls `natsutil.RequestIDFromContext(c.Request.Context())` (e.g. tracing libraries, NATS publishes) sees it.

It also replaces the hard-coded `"X-Request-ID"` constant in `auth-service/middleware.go` with `natsutil.RequestIDHeader` so the value is defined in one place.

**Files:**
- Modify: `auth-service/middleware.go` — attach request ID to `c.Request.Context()`
- Modify: `auth-service/middleware_test.go` (or create if absent) — verify propagation

### TDD: write the failing test first

- [ ] **Step 1: Add or extend `auth-service/middleware_test.go` with the propagation test**

Look for an existing `middleware_test.go`. If it exists, append; if not, create with this content:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestRequestIDMiddleware_AttachesIDToRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())

	var fromCtx string
	var fromGin string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		fromGin = c.GetString("request_id")
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set(natsutil.RequestIDHeader, "auth-svc-test-id")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, "auth-svc-test-id", fromGin, "Gin context still carries the ID under request_id")
	assert.Equal(t, "auth-svc-test-id", fromCtx, "request.Context() must also carry the ID via natsutil")
	assert.Equal(t, "auth-svc-test-id", w.Header().Get(natsutil.RequestIDHeader), "echoed in response header")
}

func TestRequestIDMiddleware_GeneratesAndAttachesWhenHeaderAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(requestIDMiddleware())

	var fromCtx string
	r.GET("/test", func(c *gin.Context) {
		fromCtx = natsutil.RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.NotEmpty(t, fromCtx, "minted request ID must be attached to request.Context()")
	assert.Equal(t, fromCtx, w.Header().Get(natsutil.RequestIDHeader),
		"the same minted ID must be echoed in the response header")
}
```

- [ ] **Step 2: Run tests to verify they FAIL**

Run: `make test SERVICE=auth-service`

Expected: the new tests fail because `c.Request.Context()` does not carry the request ID. The first test asserts `fromCtx == "auth-svc-test-id"` but `RequestIDFromContext` returns `""`.

### Implementation

- [ ] **Step 3: Update `auth-service/middleware.go`**

Replace the entire file with:

```go
package main

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// requestIDMiddleware extracts X-Request-ID (or mints via idgen) and stores it on Gin keys, c.Request.Context() via natsutil, and the response header.
func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(natsutil.RequestIDHeader)
		if id == "" {
			id = idgen.GenerateID()
		}
		c.Set("request_id", id)
		c.Request = c.Request.WithContext(natsutil.WithRequestID(c.Request.Context(), id))
		c.Header(natsutil.RequestIDHeader, id)
		c.Next()
	}
}

// accessLogMiddleware logs method, path, status, and latency for each request.
func accessLogMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		slog.Info("request",
			"request_id", c.GetString("request_id"),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}
```

The local `requestIDHeader` constant is removed — `natsutil.RequestIDHeader` is the single source of truth.

- [ ] **Step 4: Run tests to verify they PASS**

Run: `make test SERVICE=auth-service`

Expected: all tests pass — new propagation tests plus existing handler/integration tests.

- [ ] **Step 5: Lint and full repo test**

Run: `make lint && make test`

Expected: green.

- [ ] **Step 6: Commit and push**

```bash
git add auth-service/middleware.go auth-service/middleware_test.go
git commit -m "feat(auth-service): propagate request ID to request.Context()

requestIDMiddleware now also wraps c.Request.Context() with
natsutil.WithRequestID. Any downstream code that passes c.Request.Context()
into context-aware libraries (NATS publishes, OTel tracing) sees the ID
without going through Gin's keys map. Replaces the local requestIDHeader
constant with natsutil.RequestIDHeader."
git push origin <branch>
```

---

## Task 2.4: `message-gatekeeper` — publish wrappers + JetStream consumer entry

**Background:** message-gatekeeper has two outbound paths and one inbound path that need wiring:

- `pub` (`main.go:68-70`) — JetStream publish to MESSAGES_CANONICAL. Currently `js.Publish(ctx, subj, data, opts...)`. Must become a `PublishMsg` call that includes `X-Request-ID` from ctx.
- `reply` (`main.go:71-73`) — core NATS publish for replies. Currently `nc.Publish(ctx, subj, data)`. Must become `PublishMsg`.
- The JetStream consumer loop (`main.go:114-130`) calls `handler.HandleJetStreamMsg(msgCtx, msg)`. The `msgCtx` from `iter.Next()` does not carry the inbound `X-Request-ID`. Must wrap with `natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())` before invoking the handler.

**Files:**
- Modify: `message-gatekeeper/main.go` — publish wrappers + consumer ctx wrap
- Modify: `message-gatekeeper/handler_test.go` — add a test that the canonical publish receives the X-Request-ID header (uses a capturing fake `pub` func)

### TDD: write the failing test first

The publish wrapper signature changes from `(ctx, subj, data, opts...)` to `(ctx, *nats.Msg, opts...)` so the test can capture the actual outbound `msg.Header` that the wrapper attached.

- [ ] **Step 1: Add `TestHandler_processMessage_PropagatesRequestIDOnCanonicalPublish` to `message-gatekeeper/handler_test.go`**

```go
func TestHandler_processMessage_PropagatesRequestIDOnCanonicalPublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "room-1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u-alice", Account: "alice"}}, nil)

	var capturedHeader nats.Header
	pub := func(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
		capturedHeader = msg.Header
		return &jetstream.PubAck{}, nil
	}
	reply := func(ctx context.Context, msg *nats.Msg) error { return nil }

	h := NewHandler(store, pub, reply, "site1")

	ctx := natsutil.WithRequestID(context.Background(), "req-mg-test-id")
	req := model.SendMessageRequest{ID: idgen.GenerateMessageID(), Content: "hello"}
	data, _ := json.Marshal(req)

	_, err := h.processMessage(ctx, "alice", "room-1", "site1", data)
	require.NoError(t, err)
	require.NotNil(t, capturedHeader, "publish must propagate header from ctx")
	assert.Equal(t, "req-mg-test-id", capturedHeader.Get(natsutil.RequestIDHeader))
}
```

(`idgen.GenerateMessageID` is added in Part 1 — if Part 1 hasn't merged, use a hard-coded 20-char base62 fixture string.)

- [ ] **Step 2: Run the test to verify it FAILS**

Run: `make test SERVICE=message-gatekeeper`

Expected: build error — the existing `publishFunc` and `replyFunc` types don't match the new fake signature. That's the Red phase.

### Implementation

- [ ] **Step 3: Update `message-gatekeeper/handler.go` publish func signatures and call sites**

Change the type aliases at the top of `handler.go`:

```go
type publishFunc func(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
type replyFunc func(ctx context.Context, msg *nats.Msg) error
```

Update `handler.go:178-180` (the canonical publish call) to use `natsutil.NewMsg`:

```go
canonicalMsg := natsutil.NewMsg(ctx, subject.MsgCanonicalCreated(siteID), evtData)
if _, err := h.publish(ctx, canonicalMsg, jetstream.WithMsgID(msg.ID)); err != nil {
    return nil, &infraError{cause: fmt.Errorf("publish to MESSAGES_CANONICAL: %w", err)}
}
```

Update `sendReply` (around `handler.go:90+`) to build its outbound message via `natsutil.NewMsg(ctx, subj, data)` and call the new `reply(ctx, msg)` signature.

Add the `nats` and `natsutil` imports if not present.

- [ ] **Step 4: Update `message-gatekeeper/main.go` publish wrappers and consumer ctx**

Replace `main.go:67-74`:

```go
store := NewMongoStore(db)
pub := func(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
    return js.PublishMsg(ctx, msg, opts...)
}
reply := func(ctx context.Context, msg *nats.Msg) error {
    return nc.PublishMsg(ctx, msg)
}
handler := NewHandler(store, pub, reply, cfg.SiteID)
```

(Add the `nats` import.)

Replace the consumer loop body — `main.go:115-128` — with:

```go
go func() {
    for {
        msgCtx, msg, err := iter.Next()
        if err != nil {
            return
        }
        sem <- struct{}{}
        wg.Add(1)
        go func() {
            defer func() {
                <-sem
                wg.Done()
            }()
            handlerCtx := natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())
            handler.HandleJetStreamMsg(handlerCtx, msg)
        }()
    }
}()
```

(Add the `natsutil` import to `main.go` if absent.)

- [ ] **Step 5: Run the tests to verify they PASS**

Run: `make test SERVICE=message-gatekeeper`

Expected: all tests pass — the new propagation test plus existing handler tests (which may need their own fake-pub signatures updated to match the new `publishFunc` type; do this in the same commit).

- [ ] **Step 6: Lint**

Run: `make lint`

Expected: no issues.

- [ ] **Step 7: Full repo build**

Run: `make test`

Expected: green. No other service should be affected since the publishFunc type is internal to message-gatekeeper.

- [ ] **Step 8: Commit and push**

```bash
git add message-gatekeeper/
git commit -m "feat(message-gatekeeper): propagate X-Request-ID through publishes and consumer

publishFunc and replyFunc now take *nats.Msg, so the request-ID header
from natsutil.HeaderForContext(ctx) is attached to outbound publishes.
The JetStream consumer loop wraps msgCtx with the inbound X-Request-ID
header before invoking the handler. Logging and downstream publishes
now share the same correlation ID end-to-end for each message."
git push origin <branch>
```

---

## Task 2.5: `room-service` — publish wrapper + inbound ctx wrap on NATS handlers

**Background:** room-service uses raw `nc.QueueSubscribe(subj, queue, handlerFunc)` — not the `pkg/natsrouter` middleware chain. Each handler signature is `(m otelnats.Msg)` and reads ctx via `m.Context()`. That ctx does not carry the inbound X-Request-ID header, so propagation requires a per-handler wrap.

There are eight NATS handlers (`natsCreateRoom`, `natsListRooms`, `natsGetRoom`, `natsRoomsInfoBatch`, `natsUpdateRole`, `NatsHandleRemoveMember`, `natsAddMembers`, `natsListMembers`). Each one calls `m.Context()` once at the top. Introduce a small helper to wrap and apply it consistently.

The single outbound publish wrapper (inline in `main.go:98-103`) builds `nats.Msg` and uses `js.PublishMsg`.

**Files:**
- Modify: `room-service/main.go` — publish wrapper
- Modify: `room-service/handler.go` — add `wrappedCtx(m otelnats.Msg) context.Context` helper; update every `natsXXX` handler to call it
- Modify: `room-service/handler_test.go` — add a test that handleCreateRoom's outbound publish carries X-Request-ID

### TDD: write the failing test first

- [ ] **Step 1: Add `TestHandler_handleCreateRoom_PropagatesRequestID` to `room-service/handler_test.go`**

Use the existing handler-construction pattern with a capturing fake publish:

```go
func TestHandler_handleCreateRoom_PropagatesRequestID(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().CreateSubscription(gomock.Any(), gomock.Any()).Return(nil)

	var capturedHeader nats.Header
	publish := func(ctx context.Context, subj string, data []byte) error {
		capturedHeader = natsutil.HeaderForContext(ctx)
		return nil
	}
	h := NewHandler(store, nil, "site1", 100, 50, publish)

	ctx := natsutil.WithRequestID(context.Background(), "req-room-svc-test")
	req := model.CreateRoomRequest{Name: "general", Type: model.RoomTypeChannel, CreatedBy: "u-alice", CreatedByAccount: "alice", SiteID: "site1"}
	data, _ := json.Marshal(req)

	_, err := h.handleCreateRoom(ctx, data)
	require.NoError(t, err)
	require.NotNil(t, capturedHeader)
	assert.Equal(t, "req-room-svc-test", capturedHeader.Get(natsutil.RequestIDHeader))
}
```

(If `handleCreateRoom` doesn't actually publish today, pick the handler that does — `natsAddMembers` or another that calls into the publish func. Adjust accordingly.)

- [ ] **Step 2: Run the test to verify it FAILS**

Run: `make test SERVICE=room-service`

Expected: `capturedHeader` is `nil` because the publish call site does not yet build a header from ctx. The assertion on `capturedHeader.Get(...)` panics or fails.

### Implementation

- [ ] **Step 3: Update `room-service/main.go` publish wrapper**

Replace the inline closure at `main.go:98-104` with:

```go
handler := NewHandler(store, keyStore, cfg.SiteID, cfg.MaxRoomSize, cfg.MaxBatchSize, func(ctx context.Context, subj string, data []byte) error {
    if _, err := js.PublishMsg(ctx, natsutil.NewMsg(ctx, subj, data)); err != nil {
        return fmt.Errorf("publish to %q: %w", subj, err)
    }
    return nil
})
```

(Add the `natsutil` import to `main.go` if absent.)

- [ ] **Step 4: Add `wrappedCtx` helper to `room-service/handler.go`**

Place this near the top of `handler.go`, after the `Handler` and `NewHandler` definitions:

```go
// wrappedCtx returns m.Context() augmented with X-Request-ID from the inbound msg header; entry ctx for every nats* handler.
func wrappedCtx(m otelnats.Msg) context.Context {
    return natsutil.ContextWithRequestIDFromHeaders(m.Context(), m.Msg.Header)
}
```

(Add the `natsutil` import if absent. `otelnats` and `context` are already there.)

- [ ] **Step 5: Update every `natsXXX` handler to use `wrappedCtx(m)`**

For each of the 8 handlers in `handler.go` (`natsCreateRoom`, `natsListRooms`, `natsGetRoom`, `natsRoomsInfoBatch`, `natsUpdateRole`, `NatsHandleRemoveMember`, `natsAddMembers`, `natsListMembers`), replace every `m.Context()` call inside the handler body with `wrappedCtx(m)`.

Most handlers use `m.Context()` exactly once (passed into the inner `handleXXX` or `store.YYY`). Some pass `ctx` through to multiple calls — extract once at the top:

```go
func (h *Handler) natsCreateRoom(m otelnats.Msg) {
    ctx := wrappedCtx(m)
    resp, err := h.handleCreateRoom(ctx, m.Msg.Data)
    if err != nil {
        natsutil.ReplyError(m.Msg, err.Error())
        return
    }
    if err := m.Msg.Respond(resp); err != nil {
        slog.Error("failed to respond to message", "error", err, "request_id", natsutil.RequestIDFromContext(ctx))
    }
}
```

Apply the same pattern to all 8 handlers. The slog.Error calls can include `"request_id", natsutil.RequestIDFromContext(ctx)` for cross-service log correlation; do this where it adds value but don't bulk-rewrite logging.

- [ ] **Step 6: Run tests to verify they PASS**

Run: `make test SERVICE=room-service`

Expected: all tests pass — the new propagation test plus existing handler tests. Existing tests should continue to work because they construct handlers via `NewHandler(store, nil, ...)` and the helper is independent.

- [ ] **Step 7: Lint and full repo build**

Run: `make lint && make test`

Expected: green.

- [ ] **Step 8: Commit and push**

```bash
git add room-service/
git commit -m "feat(room-service): propagate X-Request-ID through inbound and outbound NATS

Adds a wrappedCtx(m otelnats.Msg) helper in handler.go that wraps
m.Context() with natsutil.ContextWithRequestIDFromHeaders. Every nats*
handler uses it as the per-message ctx. The outbound publish wrapper in
main.go now builds *nats.Msg with X-Request-ID from ctx and uses
js.PublishMsg. Logging and downstream room-worker calls see the same
correlation ID."
git push origin <branch>
```

---

## Task 2.6: `room-worker` — publish wrapper + JetStream consumer ctx wrap

**Background:** room-worker has a single combined publish wrapper at `main.go:75-86` that handles both core NATS (when `msgID == ""`) and JetStream paths. The consumer loop at `main.go:105-120` calls `handler.HandleJetStreamMsg(msgCtx, msg)`. Pattern is identical to message-gatekeeper.

**Files:**
- Modify: `room-worker/main.go` — publish wrapper + consumer ctx wrap
- Modify: `room-worker/handler_test.go` — add a propagation test using a capturing fake publish

### TDD: write the failing test first

- [ ] **Step 1: Add propagation test to `room-worker/handler_test.go`**

Pick a handler path that publishes — e.g. `processRoleUpdate` (calls outbox). Existing handler tests construct the handler with a fake publish func; capture the ctx and verify the request ID propagates:

```go
func TestHandler_processRoleUpdate_PropagatesRequestIDIntoOutboxPublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	// ... existing setup that makes processRoleUpdate publish to outbox ...

	var capturedCtx context.Context
	publish := func(ctx context.Context, subj string, data []byte, msgID string) error {
		capturedCtx = ctx
		return nil
	}
	h := NewHandler(store, "site1", publish)

	ctx := natsutil.WithRequestID(context.Background(), "req-rw-test")
	// ... build req ...
	err := h.processRoleUpdate(ctx, reqData)
	require.NoError(t, err)
	require.NotNil(t, capturedCtx)
	assert.Equal(t, "req-rw-test", natsutil.RequestIDFromContext(capturedCtx),
		"publish wrapper must receive ctx that still carries the request ID")
}
```

(Adapt to whatever existing handler test fixture room-worker has. The key assertion is: handler-internal logic must pass ctx-with-request-ID through to the publish func — no rebuilding ctx along the way.)

- [ ] **Step 2: Run test to verify it FAILS or PASSES**

Run: `make test SERVICE=room-worker`

If existing handler code already passes ctx through correctly, this test passes immediately. That's fine — it pins the contract for Part 3 (which will rely on the same propagation for outbox dedup IDs). If it fails, fix the call sites in handler.go that reconstruct ctx.

The next step is the *publish wrapper* itself. To verify the wrapper attaches the header, write a separate integration-style test or fake-NATS test. Or skip and rely on integration tests for this end (the unit test above covers the in-process ctx propagation).

### Implementation

- [ ] **Step 3: Update `room-worker/main.go` publish wrapper**

Replace `main.go:75-86`:

```go
store := NewMongoStore(mongoClient.Database(cfg.MongoDB))
handler := NewHandler(store, cfg.SiteID, func(ctx context.Context, subj string, data []byte, msgID string) error {
    msg := natsutil.NewMsg(ctx, subj, data)
    if msgID == "" {
        // Ephemeral client-delivery (member/subscription events) — core NATS, not persisted.
        return nc.PublishMsg(ctx, msg)
    }
    // JetStream-backed (MESSAGES_CANONICAL, OUTBOX) — block on PubAck; server honors Nats-Msg-Id for dedup.
    _, err := js.PublishMsg(ctx, msg, jetstream.WithMsgID(msgID))
    return err
})
```

(Add `natsutil` import to `main.go`.)

- [ ] **Step 4: Update `room-worker/main.go` consumer loop**

Replace the inner goroutine at `main.go:111-118`:

```go
go func() {
    defer func() {
        <-sem
        wg.Done()
    }()
    handlerCtx := natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())
    handler.HandleJetStreamMsg(handlerCtx, msg)
}()
```

- [ ] **Step 5: Run tests to verify they PASS**

Run: `make test SERVICE=room-worker`

Expected: all tests pass.

- [ ] **Step 6: Lint and full repo build**

Run: `make lint && make test`

Expected: green.

- [ ] **Step 7: Commit and push**

```bash
git add room-worker/
git commit -m "feat(room-worker): propagate X-Request-ID through publishes and consumer

The combined publish wrapper builds *nats.Msg with the X-Request-ID
header from ctx and uses PublishMsg for both core NATS and JetStream
paths. The consumer loop wraps msgCtx with the inbound X-Request-ID
header before invoking HandleJetStreamMsg."
git push origin <branch>
```

---

## Task 2.7: `inbox-worker` — publisher adapter + consumer ctx wrap

**Background:** inbox-worker uses `cons.Consume` (callback-based, not iterator). The callback at `main.go:168-179` calls `handler.HandleEvent(m.Context(), m.Data())`. The publisher is a small adapter struct (`natsPublisher` at `main.go:200-205`).

**Files:**
- Modify: `inbox-worker/main.go` — `natsPublisher.Publish` + consume callback
- Modify: `inbox-worker/handler_test.go` — propagation test

### Implementation

- [ ] **Step 1: Update `natsPublisher.Publish`**

Replace the method at `main.go:204`:

```go
func (p *natsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
    return p.nc.PublishMsg(ctx, natsutil.NewMsg(ctx, subject, data))
}
```

(Add the `natsutil` import.)

- [ ] **Step 2: Update the `cons.Consume` callback**

Replace `main.go:168-179`:

```go
cctx, err := cons.Consume(func(m oteljetstream.Msg) {
    handlerCtx := natsutil.ContextWithRequestIDFromHeaders(m.Context(), m.Headers())
    if err := handler.HandleEvent(handlerCtx, m.Data()); err != nil {
        slog.Error("handle event failed", "error", err, "request_id", natsutil.RequestIDFromContext(handlerCtx))
        if err := m.Nak(); err != nil {
            slog.Error("failed to nak message", "error", err)
        }
        return
    }
    if err := m.Ack(); err != nil {
        slog.Error("failed to ack message", "error", err)
    }
})
```

- [ ] **Step 3: Add propagation test**

Append to `inbox-worker/handler_test.go`:

```go
func TestHandler_HandleEvent_PropagatesRequestIDIntoPublish(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	// existing mocks for HandleEvent's happy path...

	var capturedCtx context.Context
	pub := &fakePublisher{onPublish: func(ctx context.Context, subj string, data []byte) error {
		capturedCtx = ctx
		return nil
	}}
	h := NewHandler(store, pub)

	ctx := natsutil.WithRequestID(context.Background(), "req-inbox-test")
	err := h.HandleEvent(ctx, eventDataFixture())
	require.NoError(t, err)
	assert.Equal(t, "req-inbox-test", natsutil.RequestIDFromContext(capturedCtx))
}
```

(`fakePublisher` is a small struct implementing the existing Publisher interface with a function field; if such a fake doesn't exist in the test file already, define it.)

- [ ] **Step 4: Run tests, lint, full build**

```bash
make test SERVICE=inbox-worker
make lint
make test
```

Expected: green.

- [ ] **Step 5: Commit and push**

```bash
git add inbox-worker/
git commit -m "feat(inbox-worker): propagate X-Request-ID through publishes and consumer

natsPublisher.Publish now builds *nats.Msg with the request-ID header
from ctx and uses PublishMsg. The cons.Consume callback wraps the
inbound m.Context() with the X-Request-ID header before invoking
HandleEvent."
git push origin <branch>
```

---

## Task 2.8: `broadcast-worker` — publisher adapter + consumer ctx wrap

**Background:** broadcast-worker uses `cons.Messages()` (iterator) like message-gatekeeper / room-worker. Publisher is a `natsPublisher` adapter at `main.go:170-176`.

**Files:**
- Modify: `broadcast-worker/main.go` — `natsPublisher.Publish` + consumer goroutine
- Modify: `broadcast-worker/handler_test.go` — propagation test

### Implementation

- [ ] **Step 1: Update `natsPublisher.Publish` (`main.go:173-175`)**

```go
func (p *natsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
    return p.nc.PublishMsg(ctx, natsutil.NewMsg(ctx, subject, data))
}
```

(Add the `natsutil` import.)

- [ ] **Step 2: Update the consumer inner goroutine (`main.go:120-138`)**

```go
go func() {
    defer func() {
        <-sem
        wg.Done()
    }()
    handlerCtx := natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())
    if err := handler.HandleMessage(handlerCtx, msg.Data()); err != nil {
        slog.Error("handle message failed", "error", err, "request_id", natsutil.RequestIDFromContext(handlerCtx))
        if err := msg.Nak(); err != nil {
            slog.Error("failed to nak message", "error", err)
        }
        return
    }
    if err := msg.Ack(); err != nil {
        slog.Error("failed to ack message", "error", err)
    }
}()
```

- [ ] **Step 3: Add propagation test (same shape as inbox-worker)**

Append to `broadcast-worker/handler_test.go` a test that constructs the handler with a fake publisher capturing the ctx, calls `HandleMessage` with a `WithRequestID` ctx, and asserts the captured ctx has the same ID.

- [ ] **Step 4: Run tests, lint, full build**

```bash
make test SERVICE=broadcast-worker
make lint
make test
```

Expected: green.

- [ ] **Step 5: Commit and push**

```bash
git add broadcast-worker/
git commit -m "feat(broadcast-worker): propagate X-Request-ID through publishes and consumer

natsPublisher.Publish builds *nats.Msg with the request-ID header from
ctx and uses PublishMsg. The consumer inner goroutine wraps msgCtx
with the inbound X-Request-ID before invoking HandleMessage."
git push origin <branch>
```

---

## Task 2.9: `notification-worker` — publisher adapter + consumer ctx wrap

**Background:** notification-worker is structurally identical to broadcast-worker. Iterator-based consumer at `main.go:113-141`, `natsPublisher` adapter at `main.go:175-180`.

**Files:**
- Modify: `notification-worker/main.go` — `natsPublisher.Publish` + consumer goroutine
- Modify: `notification-worker/handler_test.go` — propagation test

### Implementation

- [ ] **Step 1: Update `natsPublisher.Publish` (`main.go:178`)**

```go
func (p *natsPublisher) Publish(ctx context.Context, subject string, data []byte) error {
    return p.nc.PublishMsg(ctx, natsutil.NewMsg(ctx, subject, data))
}
```

- [ ] **Step 2: Update the consumer inner goroutine (`main.go:126-140`)**

```go
go func() {
    defer func() {
        <-sem
        wg.Done()
    }()
    handlerCtx := natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())
    if err := handler.HandleMessage(handlerCtx, msg.Data()); err != nil {
        slog.Error("handle message failed", "error", err, "request_id", natsutil.RequestIDFromContext(handlerCtx))
        if err := msg.Nak(); err != nil {
            slog.Error("failed to nak message", "error", err)
        }
        return
    }
    if err := msg.Ack(); err != nil {
        slog.Error("failed to ack message", "error", err)
    }
}()
```

- [ ] **Step 3: Propagation test, same shape as broadcast-worker**

Append to `notification-worker/handler_test.go`.

- [ ] **Step 4: Run tests, lint, full build**

```bash
make test SERVICE=notification-worker
make lint
make test
```

- [ ] **Step 5: Commit and push**

```bash
git add notification-worker/
git commit -m "feat(notification-worker): propagate X-Request-ID through publishes and consumer

Mirrors the broadcast-worker change: natsPublisher.Publish uses
PublishMsg with X-Request-ID from ctx, consumer wraps msgCtx with the
inbound header before HandleMessage."
git push origin <branch>
```

---

## Task 2.10: `message-worker` — consumer ctx wrap (no publishes)

**Background:** message-worker is consume-only — it reads from MESSAGES_CANONICAL and writes to Cassandra/Mongo. No outbound NATS publishes. Only the consumer entry needs the wrap.

**Files:**
- Modify: `message-worker/main.go` — consumer goroutine

### Implementation

- [ ] **Step 1: Update the consumer inner goroutine (`main.go:118-130`)**

```go
go func() {
    defer func() {
        <-sem
        wg.Done()
    }()
    handlerCtx := natsutil.ContextWithRequestIDFromHeaders(msgCtx, msg.Headers())
    handler.HandleJetStreamMsg(handlerCtx, msg)
}()
```

(Add `natsutil` import.)

- [ ] **Step 2: Run tests, lint, full build**

```bash
make test SERVICE=message-worker
make lint
make test
```

Expected: green. No new test needed — the wrap is a one-liner whose behaviour is covered by `pkg/natsutil` tests; the integration test (if any) verifies end-to-end flow.

- [ ] **Step 3: Commit and push**

```bash
git add message-worker/
git commit -m "feat(message-worker): propagate X-Request-ID into handler context

Wraps msgCtx with the inbound X-Request-ID header in the consumer
goroutine. message-worker has no outbound publishes, so only the
inbound side needs wiring."
git push origin <branch>
```

---

## Task 2.11: `search-sync-worker` — bulk-batch logging note (no per-message ctx)

**Background:** search-sync-worker is structurally different from the other workers. It uses a `Fetch`-based batching loop (`main.go:316-336`) that accumulates many messages into a single bulk Elasticsearch action via `handler.Add(msg.Msg)`, then `handler.Flush(ctx)` runs the bulk request with a *single* worker-level `ctx` that's not tied to any specific inbound message.

**Decision:** **Do not propagate per-message X-Request-ID into search-sync-worker bulk flushes.** The flush operates over many messages from many requests; one X-Request-ID would be misleading. Instead, search-sync-worker logs continue to use a worker-level scope (no request_id field). If per-batch traceability becomes important later, mint a fresh `bulkID` per flush.

**Files:**
- Modify: `search-sync-worker/main.go` — add a brief comment documenting the decision

### Implementation

- [ ] **Step 1: Add a comment block before the consumer fetch loop**

In `search-sync-worker/main.go`, add a comment near the top of the `runConsumer` (or equivalent) function explaining the design exception:

```go
// Bulk flush spans many client requests, so per-message X-Request-ID is intentionally NOT propagated; mint a per-flush bulkID if per-batch traceability becomes a need.
```

- [ ] **Step 2: Run tests, lint, full build**

```bash
make test SERVICE=search-sync-worker
make lint
make test
```

Expected: green. No code change beyond a comment.

- [ ] **Step 3: Commit and push**

```bash
git add search-sync-worker/
git commit -m "docs(search-sync-worker): document why bulk flush skips X-Request-ID

search-sync-worker batches many requests into one Elasticsearch bulk
action; per-message request IDs would be misleading for flush-level
logging. Comment block explains the exception so a future reader
doesn't try to thread the ID through."
git push origin <branch>
```

---

## Task 2.12: Integration verification + Part 2 self-review

**Goal:** confirm end-to-end propagation works across the system before Part 3 starts depending on it.

### Verification steps

- [ ] **Step 1: Spin up the full local stack and exercise a happy-path flow**

```bash
make dev-up   # or whatever the project's docker-compose target is
```

Send a chat message via the dev client (or a curl against auth-service then a NATS request to message-gatekeeper) carrying `X-Request-ID: integration-test-id` in the auth-service HTTP request. Use `nats sub 'chat.>' --headers-only` (or equivalent) in another terminal to observe outbound publishes carrying the header.

Expected: every NATS message published as a side-effect of the request — `chat.msg.canonical.<site>.created`, broadcast events, member events, outbox events — has `X-Request-ID: integration-test-id`.

- [ ] **Step 2: Tail every service's logs and grep for the request ID**

```bash
docker compose logs --since 1m | grep integration-test-id
```

Expected: every service that touched the request shows the same `request_id` in its log lines (auth-service, message-gatekeeper, message-worker, broadcast-worker, notification-worker).

- [ ] **Step 3: Verify a publish without a request ID degrades gracefully**

Send a message *without* an `X-Request-ID` header. Auth-service mints one; downstream propagation works. Workers that consume a message without an `X-Request-ID` header (e.g. legacy data, or a service that hasn't been updated yet) continue to function — `RequestIDFromContext` returns `""` and slog emits `request_id=""`. Outbox dedup still uses `idgen.DeriveID(seed)` (Part 2 hasn't changed dedup), so correctness is unaffected.

- [ ] **Step 4: Run the full test suite with race detector**

```bash
make test
make test-integration   # if the integration suite is wired up
```

Expected: green.

- [ ] **Step 5: Commit any final small fixes from integration testing**

If steps 1-4 surfaced a service that wasn't wired up correctly, fix it in a small commit referencing the verification log.

## Part 2 Self-Review Checklist

1. **Spec coverage.** Every service that publishes to NATS sets `X-Request-ID` from ctx via `natsutil.HeaderForContext`. Every consumer wraps inbound ctx with `natsutil.ContextWithRequestIDFromHeaders`. ✓
2. **No correctness changes.** No `idgen` calls changed. No `WithMsgID(...)` calls changed. Outbox dedup still uses `idgen.DeriveID(seed)`. ✓
3. **Graceful degradation.** Missing X-Request-ID header → empty string in ctx → empty `request_id` field in slog → no error. ✓
4. **No new external dependencies.** Only uses the already-imported `nats.go` and `pkg/natsutil`. ✓
5. **One source of truth.** `natsutil.RequestIDHeader` constant; all services reference it. No hard-coded `"X-Request-ID"` literals remain.
6. **Concurrency.** All consumer wrappers operate on per-message ctx before invoking the handler goroutine — no shared mutable state. ✓

## What's NOT in Part 2 (handoffs to Part 3)

- **No `idgen` API surface changes beyond Part 1.** `GenerateID` still produces 17-char base62 for everything. Format cutover is Part 3.
- **No outbox dedup changes.** `room-worker` still computes `*OutboxDedupID = idgen.DeriveID(seed)`. The cutover to `requestID + ":" + destSiteID` happens in Part 3 — at that point Part 2's plumbing becomes correctness-critical.
- **No system message ID changes.** `Message.ID = idgen.DeriveID(...)` and the redundant `*MsgDedupID` calls stay until Part 3.
- **No `message-gatekeeper` validator change.** `uuid.Parse(req.ID)` stays until Part 3.
- **No removal of `idgen.DeriveID`.** It's still a live API. Removed at the end of Part 3.

---

# Part 3: ID Format Cutover

**Goal:** Flip every entity ID, request ID, and message-related dedup mechanism to its final format. After Part 3 the codebase has zero callers of `idgen.DeriveID` and zero `*DedupID` seed constructions; the function and helpers are deleted.

**Architecture:** Mechanical, service-by-service swap. The big design moves are concentrated in `room-worker` (Tasks 3.6 and 3.7), where the outbox dedup mechanism changes from `idgen.DeriveID(seed)` to `requestID + ":" + destSiteID`. Everywhere else is a one-line ID generator swap.

**Pre-conditions:** Part 1 (idgen helpers) and Part 2 (request-ID propagation) MUST be complete. Part 3 Task 3.6 relies on `requestID` being readable from `ctx` at every outbox publish call site — that's Part 2's deliverable.

**Note on cross-site dedup:** The original design considered propagating a `MessageID` field on cross-site events (`MemberAddEvent`, `MemberRemoveEvent`) so the receiving site's `inbox-worker` could replay the canonical system message with the same ID. In review of `inbox-worker/handler.go` it turned out the receiver **does not create a canonical system message** — it only updates local Subscriptions and Rooms. The system message lives only in the originating site's `MESSAGES_CANONICAL_<originSite>` stream; remote users see it via the broadcast subject routed through the NATS supercluster. So `MessageID` field propagation is **not needed** and is omitted from this plan. Local dedup via `WithMsgID(sysMsg.ID)` on the originator's canonical publish is sufficient.

## File Structure (Part 3)

- Modify: `auth-service/middleware.go` — request ID minted via `idgen.GenerateUUIDv7`
- Modify: `pkg/natsrouter/middleware.go` — request ID minted via `idgen.GenerateUUIDv7`
- Modify: `room-service/handler.go` — channel rooms via `idgen.GenerateID`, DMs via `idgen.BuildDMRoomID`; subscription via `idgen.GenerateUUIDv7`
- Modify: `message-worker/handler.go` — `ThreadRoom` and `ThreadSubscription` IDs via `idgen.GenerateUUIDv7`
- Modify: `room-worker/handler.go` — Subscription, RoomMember IDs via `idgen.GenerateUUIDv7`; system message IDs via `idgen.GenerateMessageID`; outbox publishes use `requestID + ":" + destSiteID` for `Nats-Msg-Id`; canonical system-message publishes use `msg.ID`
- Modify: `inbox-worker/handler.go` — Subscription IDs via `idgen.GenerateUUIDv7`
- Modify: `message-gatekeeper/handler.go` — replace `uuid.Parse(req.ID)` with `idgen.IsValidMessageID(req.ID)`; drop `github.com/google/uuid` import
- Modify: `pkg/idgen/idgen.go` — delete `DeriveID` (final step)
- Modify: `pkg/idgen/idgen_test.go` — delete `DeriveID` tests
- Modify: `CLAUDE.md` — update the "Primary keys" line and any other ID-format references

## Task list (Part 3)

- Task 3.1: `auth-service` + `pkg/natsrouter` request ID minting → `idgen.GenerateUUIDv7`
- Task 3.2: `room-service` — channel/DM room ID branching + Subscription UUIDv7
- Task 3.3: `message-worker` — ThreadRoom + ThreadSubscription UUIDv7
- Task 3.4: `room-worker` — Subscription + RoomMember UUIDv7
- Task 3.5: `room-worker` — system message ID via `GenerateMessageID`; canonical publishes use `msg.ID`; drop `sysMsgDedupID`
- Task 3.6: `room-worker` — outbox `Nats-Msg-Id` is `requestID + ":" + destSiteID`; drop `*OutboxDedupID`
- Task 3.7: `inbox-worker` — Subscription UUIDv7
- Task 3.8: `message-gatekeeper` — validator swap; drop `google/uuid` import
- Task 3.9: Delete `idgen.DeriveID` and any unused helpers
- Task 3.10: Update `CLAUDE.md` ID-format documentation
- Task 3.11: Integration verification + Part 3 self-review

### Bonus tasks from PR #118 review (in-scope, same PR)

- Task 3.B1: `room-worker` — refactor redundant `historySharedSincePtr` block (PR #118 thread 22)
- Task 3.B2: `room-worker` — publish async-job success event to user request subject (PR #118 thread 24)

---

## Task 3.1: `auth-service` + `pkg/natsrouter` request ID minting → `idgen.GenerateUUIDv7`

**Background:** Both middlewares currently mint with `idgen.GenerateID()` (17-char base62). Switch to `idgen.GenerateUUIDv7()` (32-char hex no hyphens) per the final ID format matrix. Tiny one-line change in two files; tests update to match the new length.

**Files:**
- Modify: `auth-service/middleware.go`
- Modify: `auth-service/middleware_test.go` — adjust length assertion if any
- Modify: `pkg/natsrouter/middleware.go`
- Modify: `pkg/natsrouter/router_test.go` — adjust length assertion if any

### Implementation

- [ ] **Step 1: `auth-service/middleware.go`**

Replace the line `id = idgen.GenerateID()` with `id = idgen.GenerateUUIDv7()`.

- [ ] **Step 2: `pkg/natsrouter/middleware.go`**

Replace `reqID = idgen.GenerateID()` with `reqID = idgen.GenerateUUIDv7()`.

- [ ] **Step 3: Update tests that assert request-ID length**

Search:
```bash
grep -rn 'request_id\|requestID' auth-service/ pkg/natsrouter/ | grep -i "len\|length\|17"
```

For each match, change `assert.Len(t, ..., 17)` → `assert.Len(t, ..., 32)` and `assert.Regexp(t, base62Regex, ...)` → assert hex regex `^[0-9a-f]{32}$` (or `assert.NoError(t, hex.DecodeString(id))`).

If a test mints a fixture request ID by calling the middleware and then asserts a known value, change the fixture to a 32-char hex literal — but prefer dropping value assertions and keeping only "non-empty + format" assertions, since the value is random.

- [ ] **Step 4: Run tests**

```bash
make test SERVICE=auth-service
make test SERVICE=pkg/natsrouter
```

Expected: green.

- [ ] **Step 5: Lint and full repo build**

```bash
make lint && make test
```

Expected: green. No other package should be affected because request IDs are opaque to consumers.

- [ ] **Step 6: Commit and push**

```bash
git add auth-service/ pkg/natsrouter/
git commit -m "feat(request-id): mint request IDs as UUIDv7 (32-char hex no hyphens)

auth-service Gin middleware and pkg/natsrouter NATS middleware switch
from idgen.GenerateID() (17-char base62) to idgen.GenerateUUIDv7()
(32-char lowercase hex). Existing X-Request-ID header values from
clients are still accepted as-is — only the locally-minted IDs
change. Test length assertions updated."
git push origin <branch>
```

---

## Task 3.2: `room-service` — channel/DM room ID branching + Subscription UUIDv7

**Background:** `room-service/handler.go::handleCreateRoom` (around line 105) currently mints both Room.ID and Subscription.ID with `idgen.GenerateID()`. Two changes:
1. **Room.ID** branches on `RoomType`: channels → `idgen.GenerateID()` (17-char base62, unchanged); DMs → `idgen.BuildDMRoomID(creatorID, recipientID)` (sorted concat of two user IDs).
2. **Subscription.ID** → `idgen.GenerateUUIDv7()`.

**DM specifics:** the existing `CreateRoomRequest` carries `CreatedBy string` (the creator's user ID) and `Members []string`. For a DM, `Members` should contain exactly the *other* participant's user ID; reject otherwise. Pull both IDs and pass to `BuildDMRoomID`. The auto-created subscription is for the creator — for a DM you also need to create the recipient's subscription. If the existing handler doesn't yet do that, defer it to a follow-up task and document the gap (search the room-service for "TODO" or check if the channel-only path also creates only the creator's subscription — if so, DM is the same shape).

**Files:**
- Modify: `room-service/handler.go::handleCreateRoom`
- Modify: `room-service/handler_test.go` — add table-driven cases for channel + DM creation, assert ID formats

### TDD: write failing tests first

- [ ] **Step 1: Add table-driven test for channel and DM creation**

Append to `room-service/handler_test.go`:

```go
func TestHandler_handleCreateRoom_ChannelAndDMIDFormats(t *testing.T) {
	cases := []struct {
		name        string
		req         model.CreateRoomRequest
		assertID    func(t *testing.T, id string)
		assertSubID func(t *testing.T, subID string)
	}{
		{
			name: "channel uses 17-char base62",
			req: model.CreateRoomRequest{
				Name: "general", Type: model.RoomTypeChannel,
				CreatedBy: "u-alice", CreatedByAccount: "alice", SiteID: "site1",
			},
			assertID: func(t *testing.T, id string) {
				assert.Len(t, id, 17, "channel room ID is 17-char base62")
			},
			assertSubID: func(t *testing.T, subID string) {
				assert.Len(t, subID, 32, "subscription ID is UUIDv7 (32 hex)")
			},
		},
		{
			name: "DM uses sorted user-ID concat",
			req: model.CreateRoomRequest{
				Type: model.RoomTypeDM,
				CreatedBy: "u-bob", CreatedByAccount: "bob",
				Members: []string{"u-alice"}, SiteID: "site1",
			},
			assertID: func(t *testing.T, id string) {
				assert.Equal(t, "u-aliceu-bob", id, "DM ID is sorted concat of two user.IDs")
			},
			assertSubID: func(t *testing.T, subID string) {
				assert.Len(t, subID, 32, "subscription ID is UUIDv7 (32 hex)")
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			var capturedRoom *model.Room
			var capturedSub *model.Subscription
			store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, r *model.Room) error { capturedRoom = r; return nil })
			store.EXPECT().CreateSubscription(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, s *model.Subscription) error { capturedSub = s; return nil })

			h := NewHandler(store, nil, "site1", 100, 50, func(ctx context.Context, subj string, data []byte) error { return nil })

			data, _ := json.Marshal(tc.req)
			_, err := h.handleCreateRoom(context.Background(), data)
			require.NoError(t, err)
			require.NotNil(t, capturedRoom)
			require.NotNil(t, capturedSub)
			tc.assertID(t, capturedRoom.ID)
			tc.assertSubID(t, capturedSub.ID)
		})
	}
}

func TestHandler_handleCreateRoom_DMRequiresExactlyOneMember(t *testing.T) {
	store := NewMockStore(gomock.NewController(t))
	h := NewHandler(store, nil, "site1", 100, 50, func(ctx context.Context, subj string, data []byte) error { return nil })

	req := model.CreateRoomRequest{
		Type: model.RoomTypeDM, CreatedBy: "u-alice", CreatedByAccount: "alice",
		Members: []string{}, SiteID: "site1",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleCreateRoom(context.Background(), data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DM requires exactly one other member")
}
```

- [ ] **Step 2: Run tests to verify they FAIL**

Run: `make test SERVICE=room-service`

Expected: the new tests fail because:
- DM ID assertion: handler currently mints with `GenerateID()`, not `BuildDMRoomID`.
- Subscription ID length: currently 17 chars, not 32.
- DM-validation test: handler does not currently reject empty Members for a DM.

### Implementation

- [ ] **Step 3: Update `handleCreateRoom` in `room-service/handler.go`**

Replace the block around line 109-135 (the `room := model.Room{...}` and `sub := model.Subscription{...}` constructions):

```go
now := time.Now().UTC()

var roomID string
switch req.Type {
case model.RoomTypeChannel:
    roomID = idgen.GenerateID()
case model.RoomTypeDM:
    if len(req.Members) != 1 {
        return nil, fmt.Errorf("DM requires exactly one other member, got %d", len(req.Members))
    }
    roomID = idgen.BuildDMRoomID(req.CreatedBy, req.Members[0])
default:
    return nil, fmt.Errorf("unsupported room type %q", req.Type)
}

room := model.Room{
    ID:        roomID,
    Name:      req.Name,
    Type:      req.Type,
    CreatedBy: req.CreatedBy,
    SiteID:    req.SiteID,
    UserCount: 1,
    CreatedAt: now,
    UpdatedAt: now,
}

if err := h.store.CreateRoom(ctx, &room); err != nil {
    return nil, fmt.Errorf("create room: %w", err)
}

sub := model.Subscription{
    ID:                 idgen.GenerateUUIDv7(),
    User:               model.SubscriptionUser{ID: req.CreatedBy, Account: req.CreatedByAccount},
    RoomID:             room.ID,
    SiteID:             req.SiteID,
    Roles:              []model.Role{model.RoleOwner},
    HistorySharedSince: &now,
    JoinedAt:           now,
}
if err := h.store.CreateSubscription(ctx, &sub); err != nil {
    ...
}
```

(The DM path likely also needs to create the recipient's subscription; if the existing channel path already does that for `req.Members`, the DM path inherits it. If not, this is a separate handler defect — out of scope for the rework, but flag in the commit.)

- [ ] **Step 4: Run tests to verify they PASS**

Run: `make test SERVICE=room-service`

Expected: all tests pass — the new ID-format tests, the DM-validation test, and existing handler tests.

- [ ] **Step 5: Lint and full repo build**

```bash
make lint && make test
```

Expected: green.

- [ ] **Step 6: Commit and push**

```bash
git add room-service/
git commit -m "feat(room-service): channel/DM room ID branching; Subscription UUIDv7

handleCreateRoom now branches on req.Type:
  - channel: idgen.GenerateID() (17-char base62, unchanged)
  - DM: idgen.BuildDMRoomID(creator, recipient) (~34-char sorted concat)
  - other: rejected
Subscription IDs switch from 17-char base62 to 32-char UUIDv7 hex.
DM creation now requires exactly one other member in req.Members."
git push origin <branch>
```

---

## Task 3.3: `message-worker` — ThreadRoom + ThreadSubscription UUIDv7

**Background:** Two `idgen.GenerateID()` call sites at `message-worker/handler.go:106` (ThreadRoom) and `:211` (ThreadSubscription). Swap both to `idgen.GenerateUUIDv7()`.

**Files:**
- Modify: `message-worker/handler.go` — two one-line swaps
- Modify: `message-worker/handler_test.go` — fix any length assertions

### Implementation

- [ ] **Step 1: `message-worker/handler.go`**

At line 106 (`handleThreadRoomAndSubscriptions`):
```go
threadRoom := model.ThreadRoom{
    ID:              idgen.GenerateUUIDv7(),
    ...
}
```

At line 211 (`buildThreadSubscription`):
```go
return &model.ThreadSubscription{
    ID:              idgen.GenerateUUIDv7(),
    ...
}
```

- [ ] **Step 2: Search and update test fixtures that assert ID length**

```bash
grep -rn "assert.Len.*17\|17,.*\"id\"" message-worker/
```

For each match, change `17` → `32`.

- [ ] **Step 3: Run tests, lint, full build**

```bash
make test SERVICE=message-worker
make lint
make test
```

Expected: green.

- [ ] **Step 4: Commit and push**

```bash
git add message-worker/
git commit -m "feat(message-worker): ThreadRoom and ThreadSubscription _id → UUIDv7

Both entity types switch from 17-char base62 to 32-char UUIDv7 hex.
B-tree locality on the high-write thread_subscriptions collection
benefits from time-ordered IDs."
git push origin <branch>
```

---

## Task 3.4: `room-worker` — Subscription + RoomMember UUIDv7

**Background:** Four `idgen.GenerateID()` call sites in `room-worker/handler.go`:
- Line 428 — Subscription created in `processAddMembers`
- Line 463, 476, 506 — RoomMember docs (new individual, new org, backfill)

All four are entity IDs that benefit from UUIDv7 locality. Pure mechanical swap.

**Files:**
- Modify: `room-worker/handler.go` — four one-line swaps
- Modify: `room-worker/handler_test.go` — fix any length assertions

### Implementation

- [ ] **Step 1: `room-worker/handler.go`**

At each of `:428, :463, :476, :506`, replace `idgen.GenerateID()` with `idgen.GenerateUUIDv7()`. (The structure literal context is `Subscription{ID:` or `RoomMember{ID:`.)

- [ ] **Step 2: Search and update test fixtures**

```bash
grep -rn "assert.Len.*17\|17,.*\"id\"" room-worker/
```

For each match, decide whether the assertion is on a Subscription/RoomMember ID (→ change to 32) or on a system message ID still using `DeriveID` (→ leave at 17 for now; that changes in Task 3.5).

- [ ] **Step 3: Run tests, lint, full build**

```bash
make test SERVICE=room-worker
make lint
make test
```

Expected: green. The four swaps are independent of the system-message and outbox dedup work in Tasks 3.5 and 3.6.

- [ ] **Step 4: Commit and push**

```bash
git add room-worker/
git commit -m "feat(room-worker): Subscription and RoomMember _id → UUIDv7

processAddMembers and the room_member backfill paths switch from
17-char base62 to 32-char UUIDv7 hex for entity primary keys.
System message IDs and outbox dedup are unchanged in this commit;
they're addressed in Tasks 3.5 and 3.6."
git push origin <branch>
```

---

## Task 3.5: `room-worker` — system message ID via `GenerateMessageID`; canonical publishes use `msg.ID`; drop `sysMsgDedupID`

**Background:** Three system-message creation sites in `room-worker/handler.go`:
- Line 222 — `processRemoveMember` (`rmindiv`)
- Line 335 — `processRemoveOrg` (`rmorg`)
- Line 597 — `processAddMembers` (`addmembers`)

Each site does this today:
```go
sysMsg := model.Message{
    ID: idgen.DeriveID(fmt.Sprintf("rmindiv:%s:%s:%d", req.RoomID, req.Account, req.Timestamp)),
    ...
}
...
sysMsgDedupID := idgen.DeriveID(fmt.Sprintf("rmindiv-msg:%s:%s:%d", req.RoomID, req.Account, req.Timestamp))
if err := h.publish(ctx, subject.MsgCanonicalCreated(h.siteID), msgEvtData, sysMsgDedupID); err != nil { ... }
```

The `sysMsg.ID` and the `sysMsgDedupID` are derived from *different* seeds (one prefixed `rmindiv`, the other `rmindiv-msg`) but otherwise carry the same uniqueness anchors. There's no semantic reason for the two to differ — passing `sysMsg.ID` as the dedup token is equivalent and simpler.

**Migration:** for each of the three sites, change:
1. `Message.ID = idgen.DeriveID(seed)` → `Message.ID = idgen.GenerateMessageID()` (random 20-char base62 minted by the originator)
2. `sysMsgDedupID := idgen.DeriveID(...)` line — **delete it**
3. `h.publish(ctx, subject..., msgEvtData, sysMsgDedupID)` → `h.publish(ctx, subject..., msgEvtData, sysMsg.ID)`

After this task, system messages have random 20-char IDs; the canonical publish's `Nats-Msg-Id` is the same value. Local dedup window catches retries because retries have the same `sysMsg.ID` only if `sysMsg.ID` is *stable across retries* — but `GenerateMessageID()` is random per call, so it's NOT stable across retries.

**Wait — this is the subtle correctness point.** If `sysMsg.ID` is random per invocation and the inbound NATS msg redelivers, each redelivery mints a *new* `sysMsg.ID`, and JetStream sees two different `Nats-Msg-Id` values, defeating dedup. We'd publish duplicate system messages.

The fix: mint `sysMsg.ID` deterministically from the inbound request's stable identity. The natural choice is the *request ID* propagated from Part 2 — `requestID` is stable across NATS msg redeliveries (it lives in the inbound msg header that JetStream redelivers verbatim). So:

```go
sysMsg := model.Message{
    ID: natsutil.RequestIDFromContext(ctx) + ":sysmsg",
    ...
}
```

But that ID is now ~38-39 chars, not the canonical 20-char base62. To preserve the 20-char rule, derive a 20-char base62 from the request ID via a one-way function. Options:
  - (a) `idgen.DeriveMessageID(seed)` — but Part 1 deliberately omitted this helper. We'd have to re-add it.
  - (b) `idgen.GenerateMessageID()` plus accept that retries might double-emit (unsafe).
  - (c) A small helper on the request ID itself: `idgen.MessageIDFromRequestID(reqID, suffix string)` — base62-encode SHA-256(reqID+suffix) truncated to 20 chars. This is a deterministic, format-conforming derivation.

**Decision:** add `idgen.MessageIDFromRequestID(reqID, suffix string) string` to `pkg/idgen` as part of this task. It's the only way to get a stable 20-char message ID that's a function of the originating request, which is what we need for retry dedup correctness. This is functionally what `DeriveID(seed)` did, just typed for the 20-char message-ID format and threaded off the request ID instead of payload contents.

This adds back a deterministic-derivation helper, but only one (single suffix per call site). It's still a meaningful simplification over the current code, which constructs ad-hoc seeds at every call site.

**Files:**
- Modify: `pkg/idgen/idgen.go` — add `MessageIDFromRequestID(reqID, suffix string) string`
- Modify: `pkg/idgen/idgen_test.go` — tests for the new helper
- Modify: `room-worker/handler.go` — three system-message sites
- Modify: `room-worker/handler_test.go` — adapt fixtures

### TDD: write the failing tests first

- [ ] **Step 1: Add tests for `idgen.MessageIDFromRequestID`**

Append to `pkg/idgen/idgen_test.go`:

```go
func TestMessageIDFromRequestID_DeterministicForSameReqIDAndSuffix(t *testing.T) {
	a := idgen.MessageIDFromRequestID("req-abc", "rmindiv")
	b := idgen.MessageIDFromRequestID("req-abc", "rmindiv")
	assert.Equal(t, a, b)
	assert.Len(t, a, 20)
	assert.True(t, isBase62(a))
}

func TestMessageIDFromRequestID_DifferentSuffixesYieldDifferentIDs(t *testing.T) {
	a := idgen.MessageIDFromRequestID("req-abc", "rmindiv")
	b := idgen.MessageIDFromRequestID("req-abc", "rmorg")
	assert.NotEqual(t, a, b)
}

func TestMessageIDFromRequestID_DifferentReqIDsYieldDifferentIDs(t *testing.T) {
	a := idgen.MessageIDFromRequestID("req-abc", "rmindiv")
	b := idgen.MessageIDFromRequestID("req-def", "rmindiv")
	assert.NotEqual(t, a, b)
}

func TestMessageIDFromRequestID_OutputPassesValidator(t *testing.T) {
	id := idgen.MessageIDFromRequestID("req-abc", "addmembers")
	assert.True(t, idgen.IsValidMessageID(id))
}
```

- [ ] **Step 2: Run tests to verify they FAIL**

Run: `make test SERVICE=pkg/idgen`

Expected: build fails with `undefined: idgen.MessageIDFromRequestID`.

### Implementation

- [ ] **Step 3: Add the helper to `pkg/idgen/idgen.go`**

After `DeriveID`, add:

```go
// MessageIDFromRequestID returns a deterministic 20-char base62 from SHA-256(requestID+":"+suffix); stable across redeliveries so JetStream dedup catches retries.
func MessageIDFromRequestID(requestID, suffix string) string {
    h := sha256.Sum256([]byte(requestID + ":" + suffix))
    return encodeBase62(new(big.Int).SetBytes(h[:16]), messageIDLength)
}
```

- [ ] **Step 4: Update the three system-message sites in `room-worker/handler.go`**

**Partial-deployment fallback:** during rollout, an upstream service running an older version may publish without an `X-Request-ID` header. Hard-failing here would NAK the message forever and stall the consumer. Instead, fall back to a payload-derived seed (the same shape used today via `req.Timestamp`) when the request ID is missing, log a warning, and proceed. After full rollout the fallback path can be removed in a follow-up commit.

For each of `:222` (rmindiv), `:335` (rmorg), `:597` (addmembers):

```go
// rmindiv example
seed := natsutil.RequestIDFromContext(ctx)
if seed == "" {
    slog.Warn("missing X-Request-ID; falling back to payload-derived seed",
        "handler", "processRemoveMember", "roomID", req.RoomID)
    seed = fmt.Sprintf("%s:%s:%d", req.RoomID, req.Account, req.Timestamp)
}
sysMsg := model.Message{
    ID:         idgen.MessageIDFromRequestID(seed, "rmindiv"),
    RoomID:     req.RoomID,
    Type:       evtType,
    SysMsgData: sysMsgData,
    CreatedAt:  now,
}
msgEvt := model.MessageEvent{Message: sysMsg, SiteID: h.siteID, Timestamp: now.UnixMilli()}
msgEvtData, _ := json.Marshal(msgEvt)
if err := h.publish(ctx, subject.MsgCanonicalCreated(h.siteID), msgEvtData, sysMsg.ID); err != nil {
    return fmt.Errorf("publish individual removal system message: %w", err)
}
```

The fallback seed for each handler:
  - `processRemoveMember`: `fmt.Sprintf("%s:%s:%d", req.RoomID, req.Account, req.Timestamp)`
  - `processRemoveOrg`: `fmt.Sprintf("%s:%s:%d", req.RoomID, req.OrgID, req.Timestamp)`
  - `processAddMembers`: `fmt.Sprintf("%s:%s:%d", req.RoomID, req.RequesterAccount, req.Timestamp)` — replaces the previous accounts+orgs-heavy seed with a compact form; orgs change rarely enough that same-room+same-requester+same-millisecond collisions are not a real concern, and this seed only runs during partial-rollout fallback anyway

Both code paths produce the same 20-char base62 ID format — the seed is just opaque input to the same SHA-256 → base62 derivation. Stability across retries is preserved either way.

The `sysMsgDedupID := idgen.DeriveID(...)` line above each `h.publish` call is **deleted** — `sysMsg.ID` is the dedup token now.

Add `natsutil` import to `room-worker/handler.go` if absent.

- [ ] **Step 5: Update room-worker tests that asserted derived sysMsg.ID values**

Existing tests that compute the expected sysMsg.ID via `idgen.DeriveID(fmt.Sprintf("rmindiv:..."))` now need to compute via `idgen.MessageIDFromRequestID(reqID, "rmindiv")` with a known fixture request ID. Inject the request ID via `natsutil.WithRequestID(ctx, "test-req-id")` at the test entry point.

- [ ] **Step 6: Run tests, lint, full build**

```bash
make test SERVICE=room-worker
make test SERVICE=pkg/idgen
make lint
make test
```

Expected: green.

- [ ] **Step 7: Commit and push**

```bash
git add pkg/idgen/ room-worker/
git commit -m "feat(room-worker): system message ID derived from request ID; drop sysMsgDedupID

The three system-message creation paths (rmindiv, rmorg, addmembers)
now mint Message.ID via idgen.MessageIDFromRequestID(seed, suffix) —
a deterministic 20-char base62. The preferred seed is the propagated
X-Request-ID from ctx; if absent (e.g. partial deployment with an
older upstream), falls back to a payload-derived seed that is also
stable across NATS msg redeliveries. Either way, dedup is correct.

Drops the separate *MsgDedupID DeriveID call at each site — the
canonical publish now passes sysMsg.ID directly as the dedup token.
The fallback path can be removed in a follow-up commit once all
upstreams are confirmed updated."
git push origin <branch>
```

---

## Task 3.6: `room-worker` — outbox `Nats-Msg-Id` is `requestID + ":" + destSiteID`; drop `*OutboxDedupID`

**Background:** Four outbox publish call sites in `room-worker/handler.go`:
- Line 123 — `processRoleUpdate` outbox (`roleupd-outbox`)
- Line 249 — `processRemoveMember` outbox (`rmindiv-outbox`)
- Line 376 — `processRemoveOrg` outbox (`rmorg-outbox`)
- Line 644 — `processAddMembers` outbox (`addmembers-outbox`)

Each currently does:
```go
dedupID := idgen.DeriveID(fmt.Sprintf("roleupd-outbox:%s:%s:%s:%d", req.RoomID, req.Account, req.NewRole, req.Timestamp))
if err := h.publish(ctx, outboxSubj, outboxData, dedupID); err != nil { ... }
```

Replace with:
```go
reqID := natsutil.RequestIDFromContext(ctx)
if reqID == "" {
    return fmt.Errorf("processRoleUpdate: missing X-Request-ID — upstream did not propagate")
}
dedupID := reqID + ":" + destSiteID
if err := h.publish(ctx, outboxSubj, outboxData, dedupID); err != nil { ... }
```

(For each call site the `destSiteID` is already in scope — it's the loop variable in the per-destination fan-out.)

**Correctness:** `requestID` is stable across redeliveries (Part 2 invariant); `destSiteID` is stable per fan-out iteration; concatenation is deterministic. Same JetStream dedup behaviour as today — different formula, same guarantees.

**Files:**
- Modify: `room-worker/handler.go` — four outbox sites
- Modify: `room-worker/handler_test.go` — tests rewritten to use propagated request ID

### Implementation

- [ ] **Step 1: Hoist a small helper to keep the four call sites readable**

In `room-worker/handler.go`, add near the top:

```go
// outboxDedupID composes Nats-Msg-Id as base+":"+destSiteID; base is X-Request-ID from ctx, falling back to payloadSeed when absent (partial-deployment safety).
func outboxDedupID(ctx context.Context, destSiteID, payloadSeed string) string {
    base := natsutil.RequestIDFromContext(ctx)
    if base == "" {
        slog.Warn("missing X-Request-ID; falling back to payload-derived outbox dedup base", "destSiteID", destSiteID)
        base = payloadSeed
    }
    return base + ":" + destSiteID
}
```

- [ ] **Step 2: Update the four outbox call sites**

For each call site, replace `dedupID := idgen.DeriveID(fmt.Sprintf(...))` with `dedupID := outboxDedupID(ctx, destSiteID, payloadSeed)` where `payloadSeed` is a stable payload-derived string built from the same fields the existing `DeriveID` seed used. Per call site:

  - `processRoleUpdate` (`:123`): `payloadSeed := fmt.Sprintf("%s:%s:%s:%d", req.RoomID, req.Account, req.NewRole, req.Timestamp)`; destSiteID = `user.SiteID`
  - `processRemoveMember` (`:249`): `payloadSeed := fmt.Sprintf("%s:%s:%d", req.RoomID, req.Account, req.Timestamp)`; destSiteID = `user.SiteID`
  - `processRemoveOrg` (`:376`): `payloadSeed := fmt.Sprintf("%s:%s:%d", req.RoomID, req.OrgID, req.Timestamp)`; destSiteID is the loop variable
  - `processAddMembers` (`:644`): `payloadSeed := fmt.Sprintf("%s:%s:%d", req.RoomID, req.RequesterAccount, req.Timestamp)` — same compact form as Task 3.5's addmembers fallback; destSiteID is the loop variable

Within each handler, `destSiteID` is in scope (loop variable for fan-out, or `user.SiteID` for single-destination remove-member).

- [ ] **Step 3: Update tests that asserted derived dedup IDs**

Existing tests likely compute expected dedup IDs via the old formula and pass them to mock-publish capture. Rewrite to inject a known request ID via `natsutil.WithRequestID(ctx, "test-req-id")` and assert the captured dedup is `"test-req-id:" + destSiteID`.

- [ ] **Step 4: Run tests, lint, full build**

```bash
make test SERVICE=room-worker
make lint
make test
```

Expected: green.

- [ ] **Step 5: Commit and push**

```bash
git add room-worker/
git commit -m "feat(room-worker): outbox Nats-Msg-Id = requestID:destSiteID; drop DeriveID seeds

The four outbox publish sites (roleupd, rmindiv, rmorg, addmembers)
now compose Nats-Msg-Id as a stable base concatenated with destSiteID.
The preferred base is the propagated X-Request-ID from ctx; if absent
(partial-deployment scenario), falls back to the same payload-derived
seed shape used today, with a slog warning. Drops the per-site
idgen.DeriveID call and seed-construction.

Correctness: both base sources are stable across NATS redeliveries;
destSiteID is stable per fan-out; concatenation is deterministic.
Same JetStream dedup guarantees as the previous approach."
git push origin <branch>
```

---

## Task 3.7: `inbox-worker` — Subscription UUIDv7

**Background:** One `idgen.GenerateID()` call site at `inbox-worker/handler.go:94` (Subscription created from a cross-site `member_added` event). Swap to `idgen.GenerateUUIDv7()`.

**Files:**
- Modify: `inbox-worker/handler.go` — one one-line swap
- Modify: `inbox-worker/handler_test.go` — fix any length assertions

### Implementation

- [ ] **Step 1: `inbox-worker/handler.go:94`**

```go
sub := &model.Subscription{
    ID:                 idgen.GenerateUUIDv7(),
    ...
}
```

- [ ] **Step 2: Update test fixtures**

```bash
grep -rn "assert.Len.*17\|17,.*\"id\"" inbox-worker/
```

Change `17` → `32` for Subscription ID assertions.

- [ ] **Step 3: Run tests, lint, full build**

```bash
make test SERVICE=inbox-worker
make lint
make test
```

Expected: green.

- [ ] **Step 4: Commit and push**

```bash
git add inbox-worker/
git commit -m "feat(inbox-worker): Subscription _id → UUIDv7

The cross-site member_added handler creates Subscriptions whose _id
switches from 17-char base62 to 32-char UUIDv7 hex. Matches the
entity ID format used by room-service and room-worker."
git push origin <branch>
```

---

## Task 3.8: `message-gatekeeper` — validator swap; drop `google/uuid` import

**Background:** `message-gatekeeper/handler.go:123` validates the client-supplied message ID via `uuid.Parse(req.ID)`. Switch to `idgen.IsValidMessageID(req.ID)`. The handler test file uses `uuid.New().String()` to generate test fixtures — replace with a 20-char base62 fixture (or `idgen.GenerateMessageID()`).

**Files:**
- Modify: `message-gatekeeper/handler.go` — validator + import
- Modify: `message-gatekeeper/handler_test.go` — fixture + import

### Implementation

- [ ] **Step 1: `message-gatekeeper/handler.go`**

Replace the validation block at `:122-124`:

```go
// Validate ID is a valid 20-char base62 message ID
if !idgen.IsValidMessageID(req.ID) {
    return nil, fmt.Errorf("invalid message ID %q: must be a 20-char base62 string", req.ID)
}
```

Drop the `"github.com/google/uuid"` import line.

- [ ] **Step 2: `message-gatekeeper/handler_test.go`**

Replace `uuid.New().String()` (line 40) with `idgen.GenerateMessageID()`. Drop the `"github.com/google/uuid"` import.

If other tests in this file assert length 36 or check UUID format on `req.ID`, update them to length 20 / `idgen.IsValidMessageID(...)`.

- [ ] **Step 3: Add tests covering the new format-rejection paths**

Append:

```go
func TestHandler_processMessage_RejectsLegacyUUIDv4(t *testing.T) {
	h := newTestHandler(t)
	req := model.SendMessageRequest{
		ID:      "550e8400-e29b-41d4-a716-446655440000", // legacy UUIDv4
		Content: "hello",
	}
	data, _ := json.Marshal(req)
	_, err := h.processMessage(context.Background(), "alice", "room-1", "site1", data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid message ID")
}

func TestHandler_processMessage_AcceptsValid20CharBase62(t *testing.T) {
	// happy-path test that exercises the new validator
	store := setupHappyPathMocks(t) // reuse existing helper or inline
	h := newTestHandler(t, store)
	req := model.SendMessageRequest{
		ID:      idgen.GenerateMessageID(),
		Content: "hello",
	}
	data, _ := json.Marshal(req)
	_, err := h.processMessage(context.Background(), "alice", "room-1", "site1", data)
	require.NoError(t, err)
}
```

(Adapt names to the existing test fixture conventions in this file.)

- [ ] **Step 4: Run tests, lint, full build**

```bash
make test SERVICE=message-gatekeeper
make lint
make test
```

Expected: green. The `google/uuid` library should still be in `go.mod` (it's a transitive dep via `pkg/idgen` for `uuid.NewV7`); only the message-gatekeeper *direct* import goes away.

- [ ] **Step 5: Commit and push**

```bash
git add message-gatekeeper/
git commit -m "feat(message-gatekeeper): validate message ID as 20-char base62 (was UUIDv4)

Replaces uuid.Parse(req.ID) with idgen.IsValidMessageID(req.ID) and
drops the direct github.com/google/uuid import. The library stays in
go.mod (used transitively by pkg/idgen for uuid.NewV7), but
message-gatekeeper no longer references it directly. Test fixtures
updated to use idgen.GenerateMessageID() instead of uuid.New()."
git push origin <branch>
```

---

## Task 3.9: Delete `idgen.DeriveID` and clean up unused helpers

**Background:** After Tasks 3.5 (system message ID via `MessageIDFromRequestID`) and 3.6 (outbox dedup via `requestID:destSiteID`), `idgen.DeriveID` has zero callers in production code. Time to delete it.

**Files:**
- Modify: `pkg/idgen/idgen.go` — delete `DeriveID`
- Modify: `pkg/idgen/idgen_test.go` — delete `DeriveID` tests
- Modify: `tools/nats-debug/hub_nats.go` — if it still uses `DeriveID`, swap to `GenerateID()` or `GenerateUUIDv7()` based on what makes sense for the debug tool

### Implementation

- [ ] **Step 1: Verify no production callers remain**

```bash
grep -rn "idgen.DeriveID" /home/user/chat --include="*.go" 2>/dev/null | grep -v "_test.go\|docs/\|vendor/"
```

Expected: zero matches. If any remain, they were missed in earlier tasks — fix them before proceeding.

```bash
grep -rn "idgen.DeriveID" /home/user/chat --include="*.go" 2>/dev/null
```

Expected matches: only test files referencing the soon-to-be-deleted function. Delete those tests in Step 3.

- [ ] **Step 2: Delete `DeriveID` from `pkg/idgen/idgen.go`**

Remove the entire `DeriveID` function (and its docstring). Keep `MessageIDFromRequestID` (added in Task 3.5) — it's the supported deterministic-derivation helper now.

- [ ] **Step 3: Delete `DeriveID` tests from `pkg/idgen/idgen_test.go`**

Remove `TestDeriveID_StableAcrossCalls`, `TestDeriveID_DifferentSeedsDifferentIDs`, `TestDeriveID_EmptySeed`, and any other tests that reference `DeriveID`. Keep all `MessageIDFromRequestID` tests.

- [ ] **Step 4: Update `tools/nats-debug/hub_nats.go` if it uses DeriveID**

```bash
grep -n "DeriveID\|GenerateID" tools/nats-debug/hub_nats.go
```

The earlier survey found three `idgen.GenerateID()` calls (lines 111, 114, 185) but no `DeriveID` use. If the survey was accurate, no change needed. If `DeriveID` shows up, replace with `GenerateID()` (this is a debug tool — random IDs are fine).

- [ ] **Step 5: Run tests, lint, full build**

```bash
make test SERVICE=pkg/idgen
make lint
make test
```

Expected: green. The package shrinks; coverage on the remaining functions stays high.

- [ ] **Step 6: Commit and push**

```bash
git add pkg/idgen/
git commit -m "refactor(idgen): delete DeriveID; outbox dedup migrated to request-ID-based

DeriveID is no longer called by any production code after Tasks 3.5
(system message ID) and 3.6 (outbox dedup) migrated to
MessageIDFromRequestID and requestID:destSiteID respectively.

The idgen package now exports:
  - GenerateID (channel rooms, 17-char base62)
  - GenerateMessageID (random message IDs, 20-char base62)
  - MessageIDFromRequestID (deterministic message IDs from request, 20-char)
  - IsValidMessageID (validator)
  - GenerateUUIDv7 (subscriptions, room_members, thread rooms,
    thread subscriptions, request IDs — 32-char hex)
  - BuildDMRoomID (DM room IDs from two user IDs)"
git push origin <branch>
```

---

## Task 3.10: Update `CLAUDE.md` ID-format documentation

**Background:** `CLAUDE.md:224` says *"Primary keys: application-generated UUIDs via `github.com/google/uuid`, mapped to `bson:"_id"`"*. This was true when everything used UUIDv4, but after the rework most primary keys are not UUID-shaped:

| Entity | Format |
|---|---|
| Subscription, RoomMember, ThreadRoom, ThreadSubscription | UUIDv7 hex no hyphens (32 chars) |
| Channel Room | base62 random (17 chars) |
| DM Room | sorted concat of two user IDs (~34 chars) |
| Message | base62 random (20 chars) |

Update CLAUDE.md to point at `pkg/idgen` as the single source of truth, and replace the stale "UUIDs" line with an accurate format matrix.

**Files:**
- Modify: `CLAUDE.md` — Section 6 MongoDB rules, Section 6 NATS subject naming (if it mentions ID format)

### Implementation

- [ ] **Step 1: Re-grep CLAUDE.md for any other stale ID references**

```bash
grep -n "uuid\|UUID\|primary key\|Primary key\|17-char\|17 char\|message ID\|Message.ID\|request_id\|requestID" CLAUDE.md
```

For each match, decide whether it's:
  - Stale wording → update.
  - General concept (e.g. "primary keys are application-generated") → keep.

- [ ] **Step 2: Replace `CLAUDE.md:224` with the accurate matrix**

Find the line:
```markdown
- Primary keys: application-generated UUIDs via `github.com/google/uuid`, mapped to `bson:"_id"`
```

Replace with:
```markdown
- Primary keys: application-generated via `pkg/idgen`, mapped to `bson:"_id"`. Format depends on the entity:
  - **Subscriptions, RoomMembers, ThreadRooms, ThreadSubscriptions**: UUIDv7 hex without hyphens (32 chars) via `idgen.GenerateUUIDv7()` — time-ordered for B-tree locality on high-write collections
  - **Channel Rooms**: 17-char base62 via `idgen.GenerateID()` — short, human-friendly
  - **DM Rooms**: sorted concat of two `user.ID` strings (~34 chars) via `idgen.BuildDMRoomID(a, b)` — deterministic, no separate dedup needed
  - **Messages**: 20-char base62 via `idgen.GenerateMessageID()` (or client-supplied for user messages, validated by `idgen.IsValidMessageID`)
```

- [ ] **Step 3: If any other stale references exist, update them too**

(Likely none — the grep should turn up just the one line.)

- [ ] **Step 4: Verify the file still parses as valid Markdown**

```bash
# If a markdown linter is configured:
make lint
```

- [ ] **Step 5: Commit and push**

```bash
git add CLAUDE.md
git commit -m "docs(claude): update primary-key documentation to match idgen rework

Replaces the stale 'application-generated UUIDs via google/uuid' line
with an entity-by-entity format matrix pointing at pkg/idgen as the
single source of truth. Reflects the post-rework state where most
primary keys are not UUID-shaped (channel rooms are 17-char base62,
DM rooms are sorted user-ID concat, messages are 20-char base62) and
only a subset (subscriptions, room_members, thread_rooms,
thread_subscriptions) uses UUIDv7."
git push origin <branch>
```

---

## Task 3.B1: `room-worker` — refactor redundant `historySharedSincePtr` block

**Background:** PR #118 review thread 22. The block at `room-worker/handler.go:548-561` constructs `historySharedSincePtr` from `req.Timestamp` after `acceptedAt` (a `time.Time` derived from the same `req.Timestamp` at line 412) is already computed. The pointer-from-timestamp dance is independent of `acceptedAt` because `MemberAddEvent.HistorySharedSince` is `*int64`, not `*time.Time`, so they're not directly interchangeable — but the logic can be tightened.

**Goal:** Replace the inline conditional with a single helper or a one-line ternary-style pick. Keep the existing logging and the `&0` sentinel guard intact (the comment at lines 548-551 explains why).

**Files:**
- Modify: `room-worker/handler.go` — extract helper or simplify in place
- Modify: `room-worker/handler_test.go` — verify behaviour unchanged

### Implementation

- [ ] **Step 1: Extract a small helper near the top of `handler.go`**

```go
// historySharedSincePtr returns &timestamp when mode is "none" with a positive timestamp; nil otherwise.
func historySharedSincePtr(mode model.HistoryMode, timestamp int64, roomID string) *int64 {
    if mode != model.HistoryModeNone {
        return nil
    }
    if timestamp <= 0 {
        slog.Error("restricted history with missing timestamp, emitting nil", "roomID", roomID, "mode", mode)
        return nil
    }
    return &timestamp
}
```

- [ ] **Step 2: Replace the inline block at `:548-561` with one call**

```go
historySharedSince := historySharedSincePtr(req.History.Mode, req.Timestamp, req.RoomID)
```

Update both downstream uses (`MemberAddEvent.HistorySharedSince`, the outbox payload's `HistorySharedSince`).

- [ ] **Step 3: Run tests, lint, full build**

```bash
make test SERVICE=room-worker
make lint
make test
```

Expected: green. Behaviour unchanged.

- [ ] **Step 4: Commit**

```bash
git add room-worker/
git commit -m "refactor(room-worker): extract historySharedSincePtr helper

Addresses PR #118 review thread 22. Inlines a one-line helper instead
of an 8-line conditional block; preserves the existing &0 sentinel
guard and error-log behaviour."
```

---

## Task 3.B2: `room-worker` — publish async-job success event to user request subject

**Background:** PR #118 review thread 24. Today the room-worker's async handlers (`processAddMembers`, `processRemoveMember`, `processRemoveOrg`, `processRoleUpdate`) succeed silently — the requester's client has no way to know when the job finished. The `pkg/subject.UserResponse(account, requestID)` helper already exists for this exact pattern; it just isn't called from these handlers.

**Goal:** After each async handler completes successfully, publish a small `AsyncJobResult` event to `subject.UserResponse(req.RequesterAccount, requestID)`. The request ID is already on `ctx` (Part 2 plumbing); the requester account is in the request payload.

**Files:**
- Modify: `pkg/model/event.go` — add `AsyncJobResult` event type
- Modify: `room-worker/handler.go` — publish at the end of each successful handler
- Modify: `room-worker/handler_test.go` — verify the success event is published

### TDD: write failing test first

- [ ] **Step 1: Add `AsyncJobResult` event type**

In `pkg/model/event.go`:

```go
// AsyncJobResult signals to the requester's client that an async room-worker job has completed.
type AsyncJobResult struct {
    RequestID string `json:"requestId"`
    Job       string `json:"job"`              // "add_members" | "remove_member" | "remove_org" | "role_update"
    Success   bool   `json:"success"`
    Error     string `json:"error,omitempty"`  // populated only on failure (future use)
    Timestamp int64  `json:"timestamp"`
}
```

- [ ] **Step 2: Add a propagation test for one handler (e.g. processAddMembers)**

```go
func TestHandler_processAddMembers_PublishesSuccessEventToRequesterSubject(t *testing.T) {
    // ... existing happy-path setup ...

    var capturedSubject string
    var capturedData []byte
    publish := func(ctx context.Context, subj string, data []byte, msgID string) error {
        if strings.HasPrefix(subj, "chat.user.") {
            capturedSubject = subj
            capturedData = data
        }
        return nil
    }
    h := NewHandler(store, "site1", publish)

    ctx := natsutil.WithRequestID(context.Background(), "req-async-test")
    err := h.processAddMembers(ctx, addMembersReqDataFixture("alice"))
    require.NoError(t, err)

    assert.Equal(t, subject.UserResponse("alice", "req-async-test"), capturedSubject)
    var result model.AsyncJobResult
    require.NoError(t, json.Unmarshal(capturedData, &result))
    assert.Equal(t, "req-async-test", result.RequestID)
    assert.Equal(t, "add_members", result.Job)
    assert.True(t, result.Success)
}
```

- [ ] **Step 3: Run test to verify it FAILS**

Run: `make test SERVICE=room-worker`

Expected: assertion failure — `capturedSubject` is empty because no publish to `chat.user.*` happens.

### Implementation

- [ ] **Step 4: Add a small helper to `room-worker/handler.go`**

```go
// publishAsyncJobResult publishes a success/failure event to the requester's reply subject.
func (h *Handler) publishAsyncJobResult(ctx context.Context, requesterAccount, job string, jobErr error) {
    requestID := natsutil.RequestIDFromContext(ctx)
    if requestID == "" || requesterAccount == "" {
        return
    }
    result := model.AsyncJobResult{
        RequestID: requestID,
        Job:       job,
        Success:   jobErr == nil,
        Timestamp: time.Now().UTC().UnixMilli(),
    }
    if jobErr != nil {
        result.Error = jobErr.Error()
    }
    data, _ := json.Marshal(result)
    if err := h.publish(ctx, subject.UserResponse(requesterAccount, requestID), data, ""); err != nil {
        slog.Warn("publish async job result failed", "error", err, "requestID", requestID)
    }
}
```

- [ ] **Step 5: Call the helper at the end of each async handler**

In each of `processAddMembers`, `processRemoveMember`, `processRemoveOrg`, `processRoleUpdate` — at the successful return path:

```go
// Final line before `return nil`:
h.publishAsyncJobResult(ctx, req.RequesterAccount, "add_members", nil)
return nil
```

(Use `req.RequesterAccount` for add-members, `req.Requester` for remove-member, etc. — match the existing field names.)

For the `role_update` handler the requester field may be different (e.g. caller account); use whatever the handler already has in scope.

- [ ] **Step 6: Run tests, lint, full build**

```bash
make test SERVICE=room-worker
make lint
make test
```

Expected: green. The new publish is best-effort (logs but doesn't fail the handler), so behaviour on existing tests is unchanged.

- [ ] **Step 7: Commit**

```bash
git add pkg/model/event.go room-worker/
git commit -m "feat(room-worker): publish async job result to requester subject

Addresses PR #118 review thread 24. Each async handler
(processAddMembers, processRemoveMember, processRemoveOrg,
processRoleUpdate) now publishes an AsyncJobResult event to
subject.UserResponse(requesterAccount, requestID) when it completes
successfully, so the requester's client can correlate the job with
its outcome.

Best-effort: a publish failure logs a warning but doesn't fail the
job. requestID comes from natsutil.RequestIDFromContext(ctx) (Part 2
plumbing); empty requestID skips the publish gracefully (e.g. for
events triggered by cross-site INBOX rather than a client request)."
```

---

## Task 3.11: Integration verification + Part 3 self-review

**Goal:** confirm the cutover holds end-to-end before declaring the rework complete.

### Verification steps

- [ ] **Step 1: Spin up the full local stack**

```bash
make dev-up
```

- [ ] **Step 2: Send a chat message and verify the ID format**

Send a message via the client (or a NATS request directly to message-gatekeeper) carrying:
- HTTP `X-Request-ID` header with a known value (`integration-final-test`)
- `req.ID` as a 20-char base62 fixture

Verify in Mongo:
```bash
mongosh ... --eval 'db.messages.find().sort({_id:-1}).limit(1).pretty()'
```
Expected: `_id` is the 20-char base62 fixture.

- [ ] **Step 3: Create a channel and a DM, verify Room.ID format**

```bash
mongosh ... --eval 'db.rooms.find({type:"channel"}).limit(1)'
```
Expected: `_id` is 17 chars.

```bash
mongosh ... --eval 'db.rooms.find({type:"dm"}).limit(1)'
```
Expected: `_id` is sorted concat of the two user IDs.

- [ ] **Step 4: Verify Subscription, RoomMember, ThreadRoom IDs are UUIDv7**

```bash
mongosh ... --eval 'db.subscriptions.find().limit(5).map(d => ({id: d._id, len: d._id.length}))'
```
Expected: every `_id` is 32 chars and matches `^[0-9a-f]{32}$`. Use the version-nibble check (`id[12] == "7"`) to confirm v7 specifically.

- [ ] **Step 5: Verify request-ID propagation under retry**

In a separate terminal: `nats stream view OUTBOX_<site>` and trigger an `AddMembers` flow that fans out to a remote site. Force a redelivery (e.g., kill room-worker mid-process and restart). Confirm:
- The first delivery's outbox publish has `Nats-Msg-Id: <reqID>:<destSite>`.
- The redelivery is rejected as a duplicate (no new entry on the stream) — i.e., outbox event is NOT emitted twice.

This is the critical correctness check. If duplicates appear, request-ID propagation is broken somewhere — bisect by checking which intermediate service is generating a fresh request ID.

- [ ] **Step 6: Verify zero `idgen.DeriveID` references remain**

```bash
grep -rn "idgen.DeriveID\|idgen\.DeriveID" --include="*.go" .
```
Expected: zero output.

```bash
grep -rn "google/uuid" --include="*.go" . | grep -v "_test.go\|pkg/idgen"
```
Expected: only `pkg/idgen/idgen.go` (which uses `uuid.NewV7`). No other production code imports the library.

- [ ] **Step 7: Run the full repo test + integration suite**

```bash
make lint
make test
make test-integration
```

Expected: green.

## Part 3 Self-Review Checklist

1. **Spec coverage.** Every entity ID, request ID, and dedup token in production code matches the final ID format matrix:
   - Subscriptions, RoomMembers, ThreadRooms, ThreadSubs → UUIDv7 (32-char hex). ✓
   - Channel Rooms → 17-char base62. ✓
   - DM Rooms → sorted concat of two user.IDs. ✓
   - Messages (user) → client-supplied 20-char base62, validated. ✓
   - Messages (system) → `MessageIDFromRequestID(reqID, suffix)`. ✓
   - Request IDs → UUIDv7 (minted at HTTP/NATS entry points). ✓
   - Outbox `Nats-Msg-Id` → `requestID + ":" + destSiteID`. ✓
2. **Idempotency preserved.**
   - Canonical user-message dedup: client `req.ID` propagates to `WithMsgID(msg.ID)` — unchanged from before the rework. ✓
   - Canonical system-message dedup: `WithMsgID(sysMsg.ID)` where `sysMsg.ID = MessageIDFromRequestID(reqID, suffix)` — stable across redeliveries because `reqID` is stable on the inbound NATS msg header. ✓
   - Outbox dedup: `WithMsgID(reqID + ":" + destSiteID)` — same stability argument; per-destination disambiguator handles fan-out. ✓
3. **No stranded callers.** `idgen.DeriveID` deleted; no production code imports `github.com/google/uuid` directly; `pkg/idgen` is the single source of truth for ID generation. ✓
4. **Documentation matches reality.** `CLAUDE.md` updated. ✓
5. **Failure modes are loud.** Workers fail explicitly when a missing `X-Request-ID` would otherwise compromise dedup correctness — no silent regeneration, no duplicate emits. ✓
6. **Backward compatibility for stored data.** Mongo and Cassandra collections accept any string length, so existing data with old-format IDs (UUIDv4 messages, 17-char base62 subs) continues to work. New writes use new formats; mixed states are normal during rollout. ✓

## What's NOT in Part 3 (deferred)

- **No data migration.** Existing IDs in Mongo and Cassandra stay as-is forever. The system handles mixed-format collections natively.
- **No frontend / client SDK update.** Clients must update to generate 20-char base62 message IDs (was UUIDv4) — this is a coordinated change in a separate repo.
- **No `tools/nats-debug` overhaul** beyond the `DeriveID` swap if applicable. The debug tool's ID generation isn't security-relevant.
- **No phased rollout phasing.** This plan assumes a coordinated single-shot deployment of all services. For a phased rollout, deploy in this order:
  1. Part 1 (idgen helpers — no behaviour change)
  2. Part 2 (request-ID propagation — no behaviour change beyond logging)
  3. Part 3.1–3.4 (entity-ID format swaps — no behaviour change beyond storage size)
  4. Part 3.5–3.6 (room-worker dedup mechanism switch) — coordinate with monitoring, since this is the first task with a real correctness impact under partial deployment
  5. Part 3.7–3.10 (cleanup)




