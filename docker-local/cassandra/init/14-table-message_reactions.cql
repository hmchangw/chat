CREATE TABLE IF NOT EXISTS chat.message_reactions (
  message_id TEXT,
  emoji      TEXT,
  users      SET<FROZEN<"Participant">>,
  PRIMARY KEY ((message_id), emoji)
) WITH compaction = {'class': 'LeveledCompactionStrategy'};
