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
#### EncMeta
```cql
CREATE TYPE IF NOT EXISTS "EncMeta"(
  nonce BLOB  // 12 bytes, AES-256-GCM nonce for enc_payload
);
```
Per-row metadata for at-rest encryption. The KEK version that wrapped the
room's DEK is intentionally **not** stored here — it lives on the
`room_data_keys` MongoDB document and is authoritative there. See
`docs/superpowers/specs/2026-05-05-message-at-rest-encryption-design.md`.
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
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  enc_payload BLOB,                 // bundled JSON ciphertext of user-authored content; non-null for rows
                                    //   written after the at-rest encryption rollout
  enc_meta FROZEN<"EncMeta">,       // 12-byte AES-GCM nonce; null for legacy plaintext rows
  PRIMARY KEY((room_id, bucket),created_at,message_id)
)WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC);
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
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  enc_payload BLOB,                 // bundled JSON ciphertext of user-authored content; non-null for rows
                                    //   written after the at-rest encryption rollout
  enc_meta FROZEN<"EncMeta">,       // 12-byte AES-GCM nonce; null for legacy plaintext rows
  PRIMARY KEY((room_id, bucket),thread_room_id,created_at,message_id)
)WITH CLUSTERING ORDER BY (thread_room_id DESC,created_at DESC, message_id DESC);
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
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  pinned_by FROZEN<"Participant">,
  enc_payload BLOB,                 // bundled JSON ciphertext of user-authored content; non-null for rows
                                    //   written after the at-rest encryption rollout
  enc_meta FROZEN<"EncMeta">,       // 12-byte AES-GCM nonce; null for legacy plaintext rows
  PRIMARY KEY((room_id),created_at,message_id)
)WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC);
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
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  type TEXT,
  sys_msg_data BLOB,
  site_id TEXT,
  edited_at TIMESTAMP,
  created_at TIMESTAMP,
  updated_at TIMESTAMP,
  pinned_at TIMESTAMP,
  pinned_by FROZEN<"Participant">,
  enc_payload BLOB,                 // bundled JSON ciphertext of user-authored content; non-null for rows
                                    //   written after the at-rest encryption rollout
  enc_meta FROZEN<"EncMeta">,       // 12-byte AES-GCM nonce; null for legacy plaintext rows
  PRIMARY KEY(message_id,created_at)
)WITH CLUSTERING ORDER BY (created_at DESC);
```

## Encryption (at rest)

Rows written after the at-rest encryption rollout encrypt user-authored
content into a single `enc_payload` blob and leave the legacy plaintext
columns (`msg`, `attachments`, `card`, `card_action`, `sys_msg_data`, and
the body fields of `quoted_parent_message`) null. Rows written before the
rollout retain their plaintext columns and have `enc_payload IS NULL`. The
read path branches on `enc_payload IS NOT NULL`. See the design spec for
details: `docs/superpowers/specs/2026-05-05-message-at-rest-encryption-design.md`.
