# Room Key Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `pkg/roomkeystore` with versioned key pairs, key rotation with grace-period TTL, and version-based lookup.

**Spec:** `docs/superpowers/specs/2026-03-30-room-key-rotation-design.md`
**Base library plan:** `docs/superpowers/plans/2026-03-30-valkey-room-key-library.md` (already implemented)

**Scope:** 3 files in `pkg/roomkeystore` — no cross-service impact.

---

## Current State

`pkg/roomkeystore` provides `Set`, `Get`, and `Delete` for room encryption key pairs in Valkey. Current keys auto-expire via `KeyTTL`. There is no versioning, no rotation, and no grace period for old keys.

---

## Tasks

### Task 1: Update Config and Types

- [x] **1.1** Add `VersionedKeyPair` type to `pkg/roomkeystore/roomkeystore.go`.
- [x] **1.2** Replace `KeyTTL` with `GracePeriod` in `Config`.
- [x] **1.3** Add `ErrNoCurrentKey` sentinel error.
- [x] **1.4** Update `RoomKeyStore` interface: `Get` returns `*VersionedKeyPair`, add `GetByVersion` and `Rotate` methods. Both `Set` and `Rotate` accept a caller-provided `versionID` parameter.

### Task 2: Update hashCommander Interface

- [x] **2.1** Add `rotatePipeline` and `deletePipeline` methods to `hashCommander` interface.
- [x] **2.2** Implement in `redisAdapter` — `rotatePipeline` uses `redis.Pipeline`, `deletePipeline` uses multi-key `DEL`.
- [x] **2.3** Remove TTL from `Set` method (no longer calls `expire`).
- [x] **2.4** Update `fakeHashClient` test double with new methods (including `ErrNoCurrentKey` validation).

### Task 3: Implement New Methods

- [x] **3.1** Update `Set`: write `pub`, `priv`, `ver` fields with no TTL.
- [x] **3.2** Update `Get`: read `ver` field, return `*VersionedKeyPair`.
- [x] **3.3** Implement `GetByVersion`: check current and previous hashes, return matching key pair.
- [x] **3.4** Implement `Rotate`: `rotatePipeline` is the sole authority for detecting missing keys — no redundant pre-check.
- [x] **3.5** Update `Delete`: remove both current and previous keys via `deletePipeline`.

### Task 4: Unit Tests

- [x] **4.1** Update existing `Set`/`Get`/`Delete` tests for new signatures and behaviors.
- [x] **4.2** Add `GetByVersion` tests: match current, match previous, no match, Valkey error, corrupted base64.
- [x] **4.3** Add `Rotate` tests: happy path, no current key returns `ErrNoCurrentKey`, replaces existing previous, pipeline error.
- [x] **4.4** Add `Delete` tests: removes both keys, no-op when absent.
- [x] **4.5** Run `make test SERVICE=pkg/roomkeystore`.

### Task 5: Integration Tests

- [x] **5.1** Update `setupValkey` to use `GracePeriod` config instead of `KeyTTL`.
- [x] **5.2** Update round-trip test for `VersionedKeyPair` return type.
- [x] **5.3** Add rotate round-trip test: `Set` → `Rotate` → `Get` returns new → `GetByVersion` returns both.
- [x] **5.4** Add grace period expiry test: rotate with 1s grace, verify old key expires after sleep.
- [x] **5.5** Add rotate-with-no-current-key test: verify `errors.Is(err, ErrNoCurrentKey)`.
- [x] **5.6** Add delete-both-keys test: verify both current and previous are removed.

### Task 6: Validation

- [x] **6.1** Run `make lint` — zero warnings.
- [x] **6.2** Verify coverage ≥ 90% for business logic methods in `pkg/roomkeystore`.
- [x] **6.3** Commit and push.

---

## Spec Deviation

The spec interface shows `Set(ctx, roomID, pair)` and `Rotate(ctx, roomID, newPair)` without a `versionID` parameter, but also states "The library never generates version IDs — that responsibility belongs to the consuming service." Since both methods write a `ver` field to Valkey, a `versionID string` parameter was added to both `Set` and `Rotate` so callers can provide version IDs.

## Risk Mitigations

- **Contained scope:** Changes are isolated to `pkg/roomkeystore` — no other packages depend on it yet.
- **No redundant pre-checks:** `rotatePipeline` is the single authority for detecting missing keys, avoiding race conditions between a pre-check and the pipeline execution.
- **`fakeHashClient` faithfully simulates `redisAdapter`:** The fake returns `ErrNoCurrentKey` when no current key exists, matching real behavior.
