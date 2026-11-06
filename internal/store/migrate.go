package store

import (
	"context"
	"database/sql"

	"github.com/py-snake/m3-keresheto-archivum/internal/textkey"
)

// migrations upgrade an older database inside the private working copy of an ingest run, so the
// upgraded file is checked and published atomically like any other change.
// migrations[0] upgrades version 1 to 2, migrations[1] version 2 to 3, and so on.
var migrations = []func(ctx context.Context, db *sql.DB) error{
	// 1 -> 2: availability (last observed playable state) and its change history
	func(ctx context.Context, db *sql.DB) error {
		return execAll(ctx, db, `ALTER TABLE programs ADD COLUMN playable INTEGER CHECK (playable IN (0, 1));
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
	PRAGMA user_version = 2;`)
	},

	// 2 -> 3: title keys (sorting, short search), more observed upstream fields, refresh bookkeeping, indexes
	func(ctx context.Context, db *sql.DB) error {
		if err := execAll(ctx, db, `ALTER TABLE programs ADD COLUMN title_key TEXT NOT NULL DEFAULT '';
	ALTER TABLE programs ADD COLUMN refreshed_at INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE programs ADD COLUMN is_highlighted INTEGER NOT NULL DEFAULT 0 CHECK (is_highlighted IN (0, 1));
	ALTER TABLE programs ADD COLUMN is_live INTEGER NOT NULL DEFAULT 0 CHECK (is_live IN (0, 1));
	ALTER TABLE programs ADD COLUMN is_catchup INTEGER NOT NULL DEFAULT 0 CHECK (is_catchup IN (0, 1));
	ALTER TABLE programs ADD COLUMN rank INTEGER NOT NULL DEFAULT 0 CHECK (rank >= 0);
	ALTER TABLE programs ADD COLUMN collection TEXT NOT NULL DEFAULT '' CHECK (length(collection) <= 2000);
	ALTER TABLE programs ADD COLUMN collection_id TEXT NOT NULL DEFAULT '' CHECK (length(collection_id) <= 255);
	ALTER TABLE programs ADD COLUMN aired_first INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE programs ADD COLUMN aired_last INTEGER NOT NULL DEFAULT 0;`); err != nil {
			return err
		}
		if err := backfillTitleKeys(ctx, db); err != nil {
			return err
		}
		return execAll(ctx, db, `CREATE INDEX programs_refresh_idx ON programs (gone_at, refreshed_at);
	CREATE INDEX programs_year_idx ON programs (year, id);
	CREATE INDEX programs_title_key_idx ON programs (title_key, id);
	CREATE INDEX programs_released_idx ON programs (released_at, id);
	PRAGMA user_version = 3;`)
	},
}

func execAll(ctx context.Context, db *sql.DB, script string) error {
	_, err := db.ExecContext(ctx, script)
	return err
}

// backfillTitleKeys computes title_key for the rows that exist before the column did.
func backfillTitleKeys(ctx context.Context, db *sql.DB) error {
	type row struct {
		id    int64
		title string
	}
	rows, err := db.QueryContext(ctx, "SELECT id, title FROM programs")
	if err != nil {
		return err
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a commit
	stmt, err := tx.PrepareContext(ctx, "UPDATE programs SET title_key = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range all {
		if _, err := stmt.ExecContext(ctx, textkey.SortKey(r.title), r.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
