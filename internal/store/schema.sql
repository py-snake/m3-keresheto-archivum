CREATE TABLE programs (
  id                INTEGER PRIMARY KEY,
  program_id        TEXT NOT NULL UNIQUE CHECK (length(program_id) BETWEEN 4 AND 30),
  title             TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 255),
  subtitle          TEXT NOT NULL DEFAULT '' CHECK (length(subtitle) <= 255),
  info              TEXT NOT NULL DEFAULT '' CHECK (length(info) <= 20000),
  extended_info     TEXT NOT NULL DEFAULT '' CHECK (length(extended_info) <= 20000),
  description       TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 20000),
  short_description TEXT NOT NULL DEFAULT '' CHECK (length(short_description) <= 20000),
  company           TEXT NOT NULL DEFAULT '' CHECK (length(company) <= 255),
  year              INTEGER NOT NULL DEFAULT 0 CHECK (year BETWEEN 0 AND 2100),
  country           TEXT NOT NULL DEFAULT '' CHECK (length(country) <= 3),
  creators          TEXT NOT NULL DEFAULT '' CHECK (length(creators) <= 20000),
  contributors      TEXT NOT NULL DEFAULT '' CHECK (length(contributors) <= 20000),
  genre             TEXT NOT NULL DEFAULT '' CHECK (length(genre) <= 20000),
  quality           TEXT NOT NULL DEFAULT '' CHECK (length(quality) <= 15),
  pg                TEXT NOT NULL DEFAULT '' CHECK (length(pg) <= 15),
  ratio             TEXT NOT NULL DEFAULT '' CHECK (length(ratio) <= 15),
  duration          TEXT NOT NULL DEFAULT '' CHECK (length(duration) <= 15),
  has_subtitle      INTEGER NOT NULL DEFAULT 0 CHECK (has_subtitle IN (0, 1)),
  is_series         INTEGER NOT NULL DEFAULT 0 CHECK (is_series IN (0, 1)),
  series_id         TEXT NOT NULL DEFAULT '' CHECK (length(series_id) <= 255),
  episode           INTEGER NOT NULL DEFAULT 0 CHECK (episode >= 0),
  episodes          INTEGER NOT NULL DEFAULT 0 CHECK (episodes >= 0),
  added_at          INTEGER NOT NULL,
  released_at       INTEGER NOT NULL DEFAULT 0 CHECK (released_at >= 0),
  image_state       INTEGER NOT NULL DEFAULT 0 CHECK (image_state IN (0, 1, 2)),
  -- Availability is the LAST OBSERVED state, unlike the descriptive columns above (first seen).
  -- NULL = never observed. playable_checked_at says how fresh the value is.
  playable            INTEGER CHECK (playable IN (0, 1)),
  playable_checked_at INTEGER NOT NULL DEFAULT 0,
  playable_changed_at INTEGER NOT NULL DEFAULT 0,
  window_first        INTEGER NOT NULL DEFAULT 0,
  window_last         INTEGER NOT NULL DEFAULT 0,
  gone_at             INTEGER NOT NULL DEFAULT 0,
  -- comparison key of the title (folded, no leading punctuation): sorting and the 1-2 character search
  title_key           TEXT NOT NULL DEFAULT '',
  -- set by `ingest refresh` (0 = the descriptive fields still hold the first seen state)
  refreshed_at        INTEGER NOT NULL DEFAULT 0,
  -- more upstream fields, LAST OBSERVED like the availability. The upstream does not use collection,
  -- collection_id, rank and is_catchup at the moment (always empty): they are stored for when it does.
  is_highlighted      INTEGER NOT NULL DEFAULT 0 CHECK (is_highlighted IN (0, 1)),
  is_live             INTEGER NOT NULL DEFAULT 0 CHECK (is_live IN (0, 1)),
  is_catchup          INTEGER NOT NULL DEFAULT 0 CHECK (is_catchup IN (0, 1)),
  rank                INTEGER NOT NULL DEFAULT 0 CHECK (rank >= 0),
  collection          TEXT NOT NULL DEFAULT '' CHECK (length(collection) <= 2000),
  collection_id       TEXT NOT NULL DEFAULT '' CHECK (length(collection_id) <= 255),
  aired_first         INTEGER NOT NULL DEFAULT 0,  -- earliest / latest broadcast time (unix, UTC)
  aired_last          INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX programs_image_state_idx ON programs (image_state) WHERE image_state = 0;
CREATE INDEX programs_recheck_idx ON programs (gone_at, playable_checked_at);
CREATE INDEX programs_refresh_idx ON programs (gone_at, refreshed_at);
CREATE INDEX programs_year_idx ON programs (year, id);
CREATE INDEX programs_title_key_idx ON programs (title_key, id);
CREATE INDEX programs_released_idx ON programs (released_at, id);

-- Append-only history: one row per CHANGE of the playable flag.
CREATE TABLE availability_log (
  id          INTEGER PRIMARY KEY,
  program_id  TEXT NOT NULL CHECK (length(program_id) BETWEEN 4 AND 30),
  observed_at INTEGER NOT NULL,
  playable    INTEGER NOT NULL CHECK (playable IN (0, 1))
) STRICT;
CREATE INDEX availability_log_program_idx ON availability_log (program_id, observed_at);

-- Full text search: trigram = case insensitive substring search, diacritics folded.
-- Queries shorter than 3 characters find nothing (the application refuses them).
CREATE VIRTUAL TABLE programs_fts USING fts5(
  title, subtitle, info, extended_info, description, creators, contributors, genre,
  content = 'programs', content_rowid = 'id',
  tokenize = 'trigram case_sensitive 0 remove_diacritics 1'
);

CREATE TRIGGER programs_ai AFTER INSERT ON programs BEGIN
  INSERT INTO programs_fts (rowid, title, subtitle, info, extended_info, description, creators, contributors, genre)
  VALUES (new.id, new.title, new.subtitle, new.info, new.extended_info, new.description, new.creators, new.contributors, new.genre);
END;

CREATE TRIGGER programs_ad AFTER DELETE ON programs BEGIN
  INSERT INTO programs_fts (programs_fts, rowid, title, subtitle, info, extended_info, description, creators, contributors, genre)
  VALUES ('delete', old.id, old.title, old.subtitle, old.info, old.extended_info, old.description, old.creators, old.contributors, old.genre);
END;

CREATE TRIGGER programs_au AFTER UPDATE OF title, subtitle, info, extended_info, description, creators, contributors, genre ON programs BEGIN
  INSERT INTO programs_fts (programs_fts, rowid, title, subtitle, info, extended_info, description, creators, contributors, genre)
  VALUES ('delete', old.id, old.title, old.subtitle, old.info, old.extended_info, old.description, old.creators, old.contributors, old.genre);
  INSERT INTO programs_fts (rowid, title, subtitle, info, extended_info, description, creators, contributors, genre)
  VALUES (new.id, new.title, new.subtitle, new.info, new.extended_info, new.description, new.creators, new.contributors, new.genre);
END;

PRAGMA user_version = 3;
