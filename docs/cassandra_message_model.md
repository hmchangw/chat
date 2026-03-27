# Cassandra Message Data Model
Description: This schema is for message related operation in Cassandra, include query, upsert... 
## Schema
### UDT
#### Participant
```cql
CREATE TYPE IF NOT EXISTS "Participant"(
  id UUID,
  userName TEXT,
  engName TEXT,
  appId UUID,
  appName TEXT,
  isBot BOOLEAN
);
```
#### Card
```cql
CREATE TYPE IF NOT EXISTS "Card"(
  template TEXT,
  data BLOB,
);
```
#### CardAction
```cql
CREATE TYPE IF NOT EXISTS "CardAction"(
  verb TEXT,
  text TEXT,
  cardId UUID,
  displayText TEXT,
  hideExecLog BOOLEAN,
  cardTmid TEXT,
  data BLOB
);
```
#### File
```cql
CREATE TYPE IF NOT EXISTS "File"(
  id TEXT,
  name TEXT,
  type TEXT,
);
```
### Table
#### messages_by_room
```cql
CREATE TABLE IF NOT EXISTS messages_by_room(
  roomId UUID,
  createAt TIMESTAMP,
  messageId UUID,
  sender FROZEN<"Participant">,
  targetUser FROZEN<"Participant">,
  msg TEXT,
  mentions SET<"Participants">,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  cardAction FROZEN<"CardAction">,
  visibleTo TEXT,
  unread BOOLEAN,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  sysMsgType TEXT,
  sysMsgData BLOB,
  federateFrom TEXT,
  editedAt TIMESTAMP,
  updateAt TIMESTAMP,
  PRIMARY KEY((roomId),createAt,messageId)
)WITH CLUSTRING ORDER BY (createAt DESC, messageId DESC);
```
#### thread_messages_by_room
```cql
CREATE TABLE IF NOT EXISTS thread_messages_by_room(
  roomId UUID,
  threadRoomId UUID,
  createAt TIMESTAMP,
  messageId UUID,
  threadMessageId UUID,
  tshow BOOLEAN,
  sender FROZEN<"Participant">,
  targetUser FROZEN<"Participant">,
  msg TEXT,
  mentions SET<"Participants">,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  cardAction FROZEN<"CardAction">,
  visibleTo TEXT,
  unread BOOLEAN,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  sysMsgType TEXT,
  sysMsgData BLOB,
  federateFrom TEXT,
  editedAt TIMESTAMP,
  updateAt TIMESTAMP,
  PRIMARY KEY((roomId),threadRoomId,createAt,messageId)
)WITH CLUSTRING ORDER BY (threadRoomId DESC,createAt DESC, messageId DESC);
```
#### pinned_messages_by_room
```cql
CREATE TABLE IF NOT EXISTS pinned_messages_by_room(
  roomId UUID,
  createAt TIMESTAMP, // =pinnedAt
  messageId UUID,
  sender FROZEN<"Participant">,
  targetUser FROZEN<"Participant">,
  msg TEXT,
  mentions SET<"Participants">,
  attachments LIST<BLOB>,
  file FROZEN<"File">,
  card FROZEN<"Card">,
  cardAction FROZEN<"CardAction">,
  visibleTo TEXT,
  unread BOOLEAN,
  reactions MAP<TEXT,FROZEN<SET<FROZEN<"Participant">>>>,
  deleted BOOLEAN,
  sysMsgType TEXT,
  sysMsgData BLOB,
  federateFrom TEXT,
  editedAt TIMESTAMP,
  updateAt TIMESTAMP,
  pinnedBy FROZEN<"Participant">,
  PRIMARY KEY((roomId),createAt,messageId)
)WITH CLUSTRING ORDER BY (createAt DESC, messageId DESC);
```
