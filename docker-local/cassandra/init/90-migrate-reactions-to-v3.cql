-- One-shot migration for dev keyspaces still on the v1 reactions shape
-- (MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>). Drops the v1 column and
-- recreates it with the v3 shape (MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>).
--
-- This script is NOT globally idempotent. Safe cases:
--   • Fresh keyspace with no reactions column (no-op for DROP IF EXISTS, adds column).
--   • V1 keyspace with the old reactions column type (one-shot upgrade).
--
-- WARNING — DATA LOSS ON RE-RUN: If this script is run a second time against
-- a v3 keyspace that already contains reaction data, the DROP IF EXISTS steps
-- will tombstone all existing reaction data before the column is re-added.
-- Tombstoned data cannot be recovered without a snapshot.
--
-- SAFE RE-MIGRATION PATH (dev only): run `docker-compose down -v` to destroy
-- the volume, then bring services back up so the init scripts run against a
-- clean keyspace.
--
-- This script is for LOCAL DEVELOPMENT only. Production schema changes are
-- owned by ops/IaC and require a separate runbook — do not use this file
-- as a template for production migrations.

CREATE TYPE IF NOT EXISTS chat.reaction_key (emoji TEXT, user_account TEXT);
CREATE TYPE IF NOT EXISTS chat.reactor_info (
  user_id TEXT, eng_name TEXT, chn_name TEXT, account TEXT, reacted_at TIMESTAMP
);

ALTER TABLE chat.messages_by_room        DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_room        ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.messages_by_id          DROP IF EXISTS reactions;
ALTER TABLE chat.messages_by_id          ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.thread_messages_by_thread DROP IF EXISTS reactions;
ALTER TABLE chat.thread_messages_by_thread ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;

ALTER TABLE chat.pinned_messages_by_room DROP IF EXISTS reactions;
ALTER TABLE chat.pinned_messages_by_room ADD IF NOT EXISTS reactions MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>;
