-- One-shot migration for dev keyspaces still on the v1 reactions shape
-- (MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>). Drops the v1 column and
-- recreates it with the v3 shape (MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>).
--
-- IDEMPOTENT for: fresh keyspaces (no-op), v1 keyspaces (one-shot upgrade),
-- and re-runs on v3 keyspaces that have NO reaction data yet.
--
-- WARNING: re-running this on a v3 keyspace that already contains reaction
-- data will tombstone those reactions (DROP COLUMN runs before ADD COLUMN).
-- In local dev this is acceptable (ephemeral containers, throwaway data).
-- DO NOT cargo-cult this script into a production rollback — production
-- migration needs its own runbook.

CREATE TYPE IF NOT EXISTS chat.reaction_key (emoji TEXT, user_account TEXT);
CREATE TYPE IF NOT EXISTS chat.reactor_info (
  user_id TEXT, eng_name TEXT, chn_name TEXT, account TEXT, reacted_at TIMESTAMP
);

ALTER TABLE chat.messages_by_room        DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_room        ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.messages_by_id          DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_id          ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.thread_messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.thread_messages_by_room ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.pinned_messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.pinned_messages_by_room ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
