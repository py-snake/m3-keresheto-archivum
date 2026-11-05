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
  image_state       INTEGER NOT NULL DEFAULT 0 CHECK (image_state IN (0, 1, 2))
) STRICT;

CREATE INDEX programs_image_state_idx ON programs (image_state) WHERE image_state = 0;

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

PRAGMA user_version = 1;
