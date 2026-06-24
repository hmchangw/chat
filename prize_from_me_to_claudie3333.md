# org-sync-worker — Integration Specification

**Date:** 2026-06-23
**Status:** Final — implement from this document only

---
## 1. Stream & Subject

**Stream:** `HR_{siteID}` (e.g. `HR_00302000`)

**Subject:** `chat.hr.{siteID}.org.membership.changed`

**Consumer filter:** `chat.hr.{siteID}.>` (wildcard catches the subject above)

Use the site-local helper functions from this monorepo to build subject strings and consumer filters. Do not construct subjects with raw string formatting.

---

## 2. Event Model

### OrgMembershipChange

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | `"member_added"`, `"member_removed"`, or `"org_disbanded"` |
| `orgId` | `string` | Organization identifier |
| `account` | `string` | User account identifier |
| `timestamp` | `int64` | Unix milliseconds |

### OrgMembershipBatch

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | `int64` | Batch generation time, Unix milliseconds |
| `changes` | `OrgMembershipChange[]` | List of membership deltas |

These types are defined in the root `pkg/model` package. Import them directly — do not redefine.

---

## 3. Wire Format

Each NATS message carries:

**Header:**
- `Nats-Encoding: zstd` — always present

**Payload:**
- A JSON array of `OrgMembershipBatch` objects, zstd-compressed
- After decompressing, parse the JSON as `[]OrgMembershipBatch`
**Example (decompressed):**
```json
[
  {
    "timestamp": 1750000000000,
    "changes": [
      { "type": "member_added", "orgId": "0763", "account": "alice", "timestamp": 1750000000000 },
      { "type": "member_removed", "orgId": "0763", "account": "bob", "timestamp": 1750000000000 },
      { "type": "org_disbanded", "orgId": "0799", "timestamp": 1750000000000 }
    ]
  }
]
```

**Batch sizing:**
- 100 changes per batch (configurable by the producer)
- 64KB max compressed size per message (configurable by the producer)
- A single sync run may produce multiple messages if changes exceed these limits

---

## 4. Change Types & Semantics

### `member_added`

User is joining an organization. Apply to all local rooms that have org as a member.

### `member_removed`

User is leaving an organization. If the user is not explicitly added to the room (via non-org path), remove them.

### `org_disbanded`

Organization no longer exists in the source system. **The `account` field is empty** for this event type — do not use it.

When an org is disbanded, the producer emits:
- One `org_disbanded` event (for the org itself)
- One `member_removed` event for **each** former member

These are separate events in the same batch. Handle both independently.

---

## 5. Idempotency & Ordering


**No ordering guarantee within or across batches.** Changes are emitted in map iteration order, which is non-deterministic. Your worker must process each change independently and idempotently.

**Duplicates are expected.** The producer may emit the same events across consecutive sync runs if it detects no state change. Your worker should deduplicate using the key: `(type, orgId, account)`.

**First-run behavior.** On initial deployment, the producer emits all current org members as `member_added` events. Expect a large burst. No `member_removed` or `org_disbanded` events appear on first run.

**Retries & redelivery.** If the producer encounters a transient failure, the same events may be delivered again. Your worker must be fully idempotent — re-applying an already-processed change must be a no-op.

---

## 6. Implementation Notes

### Consumer configuration

- Durable consumer, explicit ACK policy
- Sequential processing (not parallel worker pool) — preserves per-batch ordering for correctness
- Retry on transient failures with backoff

### MongoDB writes

- Use existing collections (`rooms`, `subscriptions`, `room_members`) — do NOT create new schema or indexes
- Recompute room member counts after mutations using a recompute-and-`$set` pattern (not `$inc`) for redelivery idempotency
- Bot/pseudo accounts are identified with the canonical helper from `pkg/model` and should be excluded from member count calculations
### Error handling

- Per-change failures should be logged and skipped — continue processing remaining changes in the batch
- Batch-level failures (unparseable payload, decompression error) should cause the message to be rejected (not retried)
- MongoDB write failures are transient — Nak with delay for retry
### Model package location

Import `OrgMembershipChange` and `OrgMembershipBatch` from the shared `pkg/model` package in this repository. Do not create duplicate type definitions.

---

## Summary Checklist

- [ ] Consumer subscribes to `chat.hr.{siteID}.>` filter
- [ ] Decompresses zstd payload (check `Nats-Encoding` header)
- [ ] Parses JSON as `[]OrgMembershipBatch`
- [ ] Handles all three change types independently
- [ ] Handles `org_disbanded` with empty `account` field
- [ ] Handles `member_removed` cascade that accompanies `org_disbanded`
- [ ] Deduplicates using `(type, orgId, account)` key
- [ ] Processes changes idempotently regardless of order
- [ ] Uses recompute-and-`$set` for member count updates
- [ ] Does not create new schema or indexes