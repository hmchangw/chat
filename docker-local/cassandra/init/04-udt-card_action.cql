CREATE TYPE IF NOT EXISTS __KEYSPACE__."CardAction" (
  verb          TEXT,
  text          TEXT,
  card_id       TEXT,
  display_text  TEXT,
  hide_exec_log BOOLEAN,
  card_tmid     TEXT,
  data          BLOB
);
