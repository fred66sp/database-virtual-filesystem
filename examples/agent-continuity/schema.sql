-- Agent Continuity Memory — esquema de la base del proyecto.
-- Fuente canónica: docs/DESIGN.md §4.8 (DDL completo). No se re-diseña aquí:
-- cualquier cambio de tabla, columna, restricción o índice empieza en el DESIGN.
--
-- Se aplica una sola vez con SQLite, fuera de db-ai (db-ai no hace DDL).
-- init.sh lo ejecuta en una base nueva; las migraciones futuras son aditivas y
-- revisadas por el operador (NFR-ACM-07).

PRAGMA foreign_keys = ON;

CREATE TABLE meta (
  id          TEXT NOT NULL PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE CHECK (length(trim(name)) > 0),
  value       TEXT NOT NULL CHECK (length(trim(value)) > 0),
  updated_at  TEXT NOT NULL
);

CREATE TABLE sessions (
  id          TEXT NOT NULL PRIMARY KEY,
  agent_id    TEXT NOT NULL,
  source      TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  goal        TEXT NOT NULL CHECK (length(trim(goal)) > 0),
  status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed','abandoned')),
  started_at  TEXT NOT NULL,
  ended_at    TEXT,
  summary     TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE INDEX ix_sessions_agent_status ON sessions (agent_id, status, started_at);
CREATE INDEX ix_sessions_status_started ON sessions (status, started_at);

CREATE TABLE observations (
  id              TEXT NOT NULL PRIMARY KEY,
  session_id      TEXT NOT NULL REFERENCES sessions(id),
  agent_id        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  kind            TEXT NOT NULL CHECK (kind IN ('discovery','decision','pattern','bugfix','architecture','learning')),
  title           TEXT NOT NULL CHECK (length(trim(title)) > 0),
  body            TEXT NOT NULL CHECK (length(trim(body)) > 0),
  rationale       TEXT,
  alternatives    TEXT,
  consequences    TEXT,
  topic_key       TEXT,
  confidence      TEXT NOT NULL CHECK (confidence IN ('confirmed','inference','hypothesis','proposal','unknown')),
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','obsolete','needs_review')),
  review_after    TEXT,
  superseded_by   TEXT REFERENCES observations(id),
  duplicate_count INTEGER NOT NULL DEFAULT 1 CHECK (duplicate_count >= 1),
  last_seen_at    TEXT NOT NULL,
  content_hash    TEXT NOT NULL CHECK (content_hash LIKE 'sha256:%'),
  origin_ref      TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  CHECK (kind <> 'decision' OR (topic_key IS NOT NULL AND rationale IS NOT NULL AND length(trim(rationale)) > 0)),
  CHECK (status <> 'superseded' OR superseded_by IS NOT NULL),
  CHECK (status <> 'active' OR superseded_by IS NULL),
  UNIQUE (content_hash)
);

CREATE INDEX ix_observations_session ON observations (session_id, created_at);
CREATE INDEX ix_observations_kind_status ON observations (kind, status, created_at);
CREATE INDEX ix_observations_topic ON observations (topic_key, status);

CREATE TABLE checkpoints (
  id              TEXT NOT NULL PRIMARY KEY,
  session_id      TEXT NOT NULL REFERENCES sessions(id),
  agent_id        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  goal            TEXT NOT NULL CHECK (length(trim(goal)) > 0),
  state           TEXT NOT NULL CHECK (length(trim(state)) > 0),
  next_steps      TEXT NOT NULL CHECK (length(trim(next_steps)) > 0),
  files           TEXT,
  open_questions  TEXT,
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','obsolete','needs_review')),
  superseded_by   TEXT REFERENCES checkpoints(id),
  content_hash    TEXT NOT NULL CHECK (content_hash LIKE 'sha256:%'),
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  CHECK (status <> 'superseded' OR superseded_by IS NOT NULL),
  CHECK (status <> 'active' OR superseded_by IS NULL),
  UNIQUE (content_hash)
);

CREATE INDEX ix_checkpoints_session ON checkpoints (session_id, created_at);
CREATE INDEX ix_checkpoints_status ON checkpoints (status, created_at);

CREATE TABLE preferences (
  id              TEXT NOT NULL PRIMARY KEY,
  session_id      TEXT REFERENCES sessions(id),
  agent_id        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  pref_key        TEXT NOT NULL CHECK (length(trim(pref_key)) > 0),
  value           TEXT NOT NULL CHECK (length(trim(value)) > 0),
  rationale       TEXT,
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','obsolete','needs_review')),
  review_after    TEXT,
  superseded_by   TEXT REFERENCES preferences(id),
  content_hash    TEXT NOT NULL CHECK (content_hash LIKE 'sha256:%'),
  origin_ref      TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  CHECK (status <> 'superseded' OR superseded_by IS NOT NULL),
  CHECK (status <> 'active' OR superseded_by IS NULL),
  UNIQUE (content_hash)
);

CREATE INDEX ix_preferences_key ON preferences (pref_key, status);
CREATE INDEX ix_preferences_status ON preferences (status, created_at);
