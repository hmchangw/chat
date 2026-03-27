# Cassandra Message Data Model
Description: This schema is for message-related operation in Cassandra, include query, upsert... 
## Schema
### UDT
#### Participant
```cql
CREATE TYPE IF NOT EXISTS "Participant"(
  id UUID,
  user_name TEXT,
  eng_name TEXT,
  company_name TEXT, // need to change internal
  app_id UUID,
  app_name TEXT,
  is_bot BOOLEAN
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
  card_id UUID,
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
### Table
#### messages_by_room
```cql
CREATE TABLE IF NOT EXISTS messages_by_room(
  room_id UUID,
  created_at TIMESTAMP,
  message_id UUID,
  sender FROZEN<"Participant">,
  target_user FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  tshow BOOLEAN, // means from thread [also send to channel]
  thread_parent_created_at TIMESTAMP, // for FE to query thread parent message 
  visible_to TEXT,
  unread BOOLEAN,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  sys_msg_type TEXT,
  sys_msg_data BLOB,
  federate_from TEXT,
  edited_at TIMESTAMP,
  update_at TIMESTAMP,
  PRIMARY KEY((room_id),created_at,message_id)
)WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC);
```
#### thread_messages_by_room
```cql
CREATE TABLE IF NOT EXISTS thread_messages_by_room(
  room_id UUID,
  thread_room_id UUID,
  created_at TIMESTAMP,
  message_id UUID,
  thread_message_id UUID,
  sender FROZEN<"Participant">,
  target_user FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  visible_to TEXT,
  unread BOOLEAN,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  sys_msg_type TEXT,
  sys_msg_data BLOB,
  federate_from TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  PRIMARY KEY((room_id),thread_room_id,created_at,message_id)
)WITH CLUSTERING ORDER BY (thread_room_id DESC,created_at DESC, message_id DESC);
```
#### pinned_messages_by_room
```cql
CREATE TABLE IF NOT EXISTS pinned_messages_by_room(
  room_id UUID,
  created_at TIMESTAMP, // =pinnedAt
  message_id UUID,
  sender FROZEN<"Participant">,
  target_user FROZEN<"Participant">,
  msg TEXT,
  mentions SET<FROZEN<"Participant">>,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  card_action FROZEN<"CardAction">,
  visible_to TEXT,
  unread BOOLEAN,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  sys_msg_type TEXT,
  sys_msg_data BLOB,
  federate_from TEXT,
  edited_at TIMESTAMP,
  updated_at TIMESTAMP,
  pinned_by FROZEN<"Participant">,
  PRIMARY KEY((room_id),created_at,message_id)
)WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC);
```
