package store

// migrations upgrade an older database inside the private working copy of an ingest run, so the
// upgraded file is checked and published atomically like any other change. Index n upgrades from
// version n+1... i.e. migrations[0] upgrades version 1 to version 2.
var migrations = []string{
	// 1 -> 2: availability (last observed playable state) and its change history
	`ALTER TABLE programs ADD COLUMN playable INTEGER CHECK (playable IN (0, 1));
	ALTER TABLE programs ADD COLUMN playable_checked_at INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE programs ADD COLUMN playable_changed_at INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE programs ADD COLUMN window_first INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE programs ADD COLUMN window_last INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE programs ADD COLUMN gone_at INTEGER NOT NULL DEFAULT 0;
	CREATE INDEX programs_recheck_idx ON programs (gone_at, playable_checked_at);
	CREATE TABLE availability_log (
	  id          INTEGER PRIMARY KEY,
	  program_id  TEXT NOT NULL CHECK (length(program_id) BETWEEN 4 AND 30),
	  observed_at INTEGER NOT NULL,
	  playable    INTEGER NOT NULL CHECK (playable IN (0, 1))
	) STRICT;
	CREATE INDEX availability_log_program_idx ON availability_log (program_id, observed_at);
	PRAGMA user_version = 2;`,
}
