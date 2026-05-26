CREATE TYPE IF NOT EXISTS __KEYSPACE__."Participant" (
  id           TEXT,
  eng_name     TEXT,
  company_name TEXT, // need to change internal
  app_id       TEXT,
  app_name     TEXT,
  is_bot       BOOLEAN,
  account      TEXT
);
