-- Drop the legacy embedded `reactions` column from the four message tables.
-- Reactions now live in the chat.message_reactions side table (see file 14).
-- Idempotent: ALTER TABLE ... DROP IF EXISTS is a no-op on fresh keyspaces
-- and on re-runs. Cassandra 5 required (DROP IF EXISTS column added in 4.0).
ALTER TABLE chat.messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_id DROP IF EXISTS reactions;
ALTER TABLE chat.thread_messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.pinned_messages_by_room DROP IF EXISTS reactions;
