# Source data shape (legacy RocketChat)

## 1. Source document (`rocketchat_message`)

Example document (values rotated/sanitized):

```js
{
    _id: 'aB3kD9fH2jL5mN7pQ',
    t: 'au',
    u: {
        _id: 'xY8wV2tR6sU4qP0nM',
        username: 'p_demoadmin'
    },
    ts: ISODate('2024-01-15T09:00:00.000Z'),
    msg: 'p_newmember',
    _updatedAt: ISODate('2024-01-15T09:05:00.000Z'),
    unread: true,
    rid: 'rM4nB7vC2xZ9kJ5hG',
    groupable: false,
    federation: {
        origin: 'site-a.example.internal'
    }
}
```

### `t` — message type

| `t` | Meaning |
|---|---|
| `null` (absent) | Normal message |
| `room_changed_avatar` | Avatar changed |
| `room_changed_description` | Description changed |
| `room_changed_name` | Name changed |
| `room_changed_privacy` | Privacy changed |
| `user_added` | User added |
| `user_removed` | User removed |
| `user_left` | User left |
| `msg_encrypted` | Encrypted (requires decryption) |
| `subscription_role` | Role changed |

### Key fields

| Field | Notes |
|---|---|
| `_id` | PK; used for deduplication (always present) |
| `msg` | Meaningful when `t` is null; localized system text when `t` is set |
| `ts` | Insert timestamp — ordering, immutable |
| `_updatedAt` | Modification timestamp — edit detection |
| `federation.origin` | Remote server origin (if federated) |
| `unread` | Legacy flag — do not rely on (use receipts / group mentions) |

## 2. Change event structure

### 2A. Common fields

| Field | Type | Notes |
|---|---|---|
| `_id` | ObjectId | Event id (not the doc id). Used for `resumeAfter`. Not the msg id. |
| `operationType` | str | `insert` \| `update` \| `delete` \| `replace` \| `drop` \| `rename` \| `dropDatabase` \| `invalidate` |
| `clusterTime` | Timestamp | Monotonic; different values = distinct ops |
| `ns.db` / `ns.coll` | str | DB / collection names |
| `wallTime` | Date | Applied wall-clock time |
| `documentKey` | `{_id:<val>}` | Affected doc's `_id` (RC msg id) |
| `txnNumber` (Long) / `txnId` (UUID) | — | Present when inside a transaction |
| `userName` | str | Authenticated user that executed the op |

### 2B. Insert event
- **Insert-only:** `fullDocument` — the complete, newly-inserted doc.
- **Never:** `updateDescription`, `previousDocument`, `fullDocumentBeforeChange`.

### 2C. Update event
- `updateDescription` — **always**. The diff: `updatedFields` (set/modified), `removedFields` (deleted paths), `truncatedArrays` (`$pop`/`$slice`).
- `fullDocument` — only with `updateLookup`/`whenAvailable`. The post-update doc (re-read; may reflect concurrent writes).
- `previousDocument` — only with `showExpandedEvents:true` (5.0+). The pre-update doc.

### 2D. Delete event
- `fullDocument` — **never** (absent; cannot re-read).
- `previousDocument` — only with `showExpandedEvents:true` (5.0+). The pre-deletion doc — **critical for deleted content**. Without it, only `documentKey._id` is available; no way to reconstruct `msg`.

### 2E. Replace event
- `updateDescription` — absent (no field-level diff).
- `fullDocument` — the new version (with the `fullDocument` option).
- `previousDocument` — the old version (with `showExpandedEvents:true`).
