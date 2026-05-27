# Cassandra Message Data Model
Description: This schema is for message-related operation in Cassandra, include query, upsert... 
## Schema
### UDT
#### Participant
```cql
CREATE TYPE IF NOT EXISTS "Participant"(
  id TEXT,
  eng_name TEXT,
  company_name TEXT, // need to change internal
  app_id TEXT,
  app_name TEXT,
  is_bot BOOLEAN,
  account TEXT
);
```
#### Card
```cql
CREATE TYPE IF NOT EXISTS "Card"(
  template TEXT,
  data BLOB
);
```
#### CardAction
```cql
CREATE TYPE IF NOT EXISTS "CardAction"(
  verb TEXT,
  text TEXT,
  card_id TEXT,
  display_text TEXT,
  hide_exec_log BOOLEAN,
  card_tmid TEXT,
  data BLOB
);
```
#### File
```cql
CREATE TYPE IF NOT EXISTS "File"(
  id TEXT,
  name TEXT,
  type TEXT
);
```
#### QuotedParentMessage
```cql
CREATE TYPE IF NOT EXISTS "QuotedParentMessage"(
  message_id TEXT,
  room_id TEXT,
  sender FROZEN<"Participant">,
  created_at TIMESTAMP,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  message_link TEXT,
  thread_parent_id TEXT,          // set by message-worker when quoted message is a TShow reply
  thread_parent_created_at TIMESTAMP  // actual CreatedAt of the thread parent; used by history-service
                                      // to enforce access-window checks without a Cassandra round-trip
);
```
#### reaction_key
```cql
CREATE TYPE IF NOT EXISTS chat.reaction_key (
  emoji        TEXT,
  user_account TEXT
);
```

| Field        | Type | Notes                                                    |
|--------------|------|----------------------------------------------------------|
| `emoji`      | TEXT | NFC-normalised emoji string.                             |
| `user_account` | TEXT | Account identifier of the reacting user. Load-bearing identity — see account immutability caveat below. |

#### reactor_info
```cql
CREATE TYPE IF NOT EXISTS chat.reactor_info (
  user_id     TEXT,
  eng_name    TEXT,
  chn_name    TEXT,
  account     TEXT,
  reacted_at  TIMESTAMP
);
```

| Field        | Type      | Notes                                                                       |
|--------------|-----------|-----------------------------------------------------------------------------|
| `user_id`    | TEXT      | Internal user ID.                                                           |
| `eng_name`   | TEXT      | Display name (English). May be empty.                                       |
| `chn_name`   | TEXT      | Display name (Chinese). May be empty.                                       |
| `account`    | TEXT      | Duplicates `reaction_key.user_account` for read-side ergonomics.            |
| `reacted_at` | TIMESTAMP | Wall-clock time when the reaction was added (UTC).                          |

**Frozen UDT extensibility caveat.** Both UDTs are `FROZEN`. Adding a field to `reaction_key` later requires rewriting every map key on every row across the four message tables — effectively a full table backfill. Adding a field to `reactor_info` is easier (values can be lazily rewritten on next toggle) but still requires a migration. Do not add or reorder fields on either UDT post-launch without a migration plan.

**Account immutability.** `reaction_key.user_account` is the load-bearing identity. If accounts were ever renamed, a stale key would become orphaned (un-react would silently fail to find the cell). Accounts are immutable in this codebase (`pkg/userstore`). If that ever changes, the key must switch to `user_id`.

**Mirror-write source-of-truth.** A single reaction toggle writes to `messages_by_id` first, then mirrors to `messages_by_room`, `thread_messages_by_room` (when applicable), and `pinned_messages_by_room` (when applicable). The writes are not atomic across tables — `messages_by_id` is authoritative. Readers must not diff reactions across mirrors.

### Table

### Partition Bucketing

`messages_by_room` and `thread_messages_by_room` use a composite partition key
`(room_id, bucket)`. `bucket` is the start-of-window in unix milliseconds derived
deterministically from `created_at` via `pkg/msgbucket.Sizer`. The window size
is configured per service via `MESSAGE_BUCKET_HOURS` (default 24); all services
that read or write these tables MUST be configured with the same window.

#### messages_by_room
```cql
CREATE TABLE IF NOT EXISTS messages_by_room(
  room_id TEXT,
  bucket BIGINT,
  created_at TIMESTAMP,
  message_id TEXT,
  thread_room_id TEXT,
  sender FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  tshow BOOLEAN, // means from thread [also send to channel]
  tcount INT, // message reply thread count
  thread_parent_id TEXT,
  thread_parent_created_at TIMESTAMP, // for FE to query thread parent message when also sent to channel (tshow=true)
  quoted_parent_message FROZEN<"QuotedParentMessage">,
  visible_to TEXT,
  reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  PRIMARY KEY((room_id, bucket),created_at,message_id)
)WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)
  AND compaction = {'class': 'TimeWindowCompactionStrategy', 'compaction_window_unit': 'DAYS', 'compaction_window_size': 1};
```
#### thread_messages_by_room
```cql
CREATE TABLE IF NOT EXISTS thread_messages_by_room(
  room_id TEXT,
  bucket BIGINT,
  thread_room_id TEXT,
  created_at TIMESTAMP,
  message_id TEXT,
  thread_parent_id TEXT,
  sender FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  quoted_parent_message FROZEN<"QuotedParentMessage">,
  visible_to TEXT,
  reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  PRIMARY KEY((room_id, bucket),thread_room_id,created_at,message_id)
)WITH CLUSTERING ORDER BY (thread_room_id DESC,created_at DESC, message_id DESC)
  AND compaction = {'class': 'TimeWindowCompactionStrategy', 'compaction_window_unit': 'DAYS', 'compaction_window_size': 1};
```
#### pinned_messages_by_room
```cql
CREATE TABLE IF NOT EXISTS pinned_messages_by_room(
  room_id TEXT,
  created_at TIMESTAMP, // =pinnedAt
  message_id TEXT,
  sender FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  quoted_parent_message FROZEN<"QuotedParentMessage">,
  visible_to TEXT,
  reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  pinned_by FROZEN<"Participant">,
  PRIMARY KEY((room_id),created_at,message_id)
)WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)
  AND compaction = {'class': 'LeveledCompactionStrategy'};
```
#### messages_by_id
```cql
CREATE TABLE IF NOT EXISTS messages_by_id(
  message_id TEXT,
  room_id TEXT,
  thread_room_id TEXT,
  sender FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  tshow BOOLEAN,
  tcount INT, // message reply thread count
  thread_parent_id TEXT,
  thread_parent_created_at TIMESTAMP,
  quoted_parent_message FROZEN<"QuotedParentMessage">,
  visible_to TEXT,
  reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  created_at TIMESTAMP,
  updated_at TIMESTAMP,
  pinned_at TIMESTAMP,
  pinned_by FROZEN<"Participant">,
  PRIMARY KEY(message_id,created_at)
)WITH CLUSTERING ORDER BY (created_at DESC)
  AND compaction = {'class': 'LeveledCompactionStrategy'};
```

### Compaction strategy

`messages_by_room` and `thread_messages_by_room` use **TWCS** (TimeWindowCompactionStrategy, 1-day windows). These tables see append-only write patterns where rows cluster by time; TWCS keeps same-window SSTables together and expires them cleanly, avoiding read amplification on recent buckets.

`messages_by_id` and `pinned_messages_by_room` use **LCS** (LeveledCompactionStrategy). Both are point-lookup tables with random-access patterns across the full key space; LCS bounds read amplification by keeping each level to a fixed set of non-overlapping SSTables.
