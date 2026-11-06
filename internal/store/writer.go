package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
)

// Writer modifies a private working copy of the database. It is only handed out by Publisher.
type Writer struct {
	db  *sql.DB
	now func() time.Time
}

func newWriter(db *sql.DB, now func() time.Time) *Writer { return &Writer{db: db, now: now} }

// initSchema creates the schema in an empty database, upgrades an older one, or verifies a current one.
func initSchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		return err
	}
	if tables == 0 && version == 0 {
		if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("creating schema: %w", err)
		}
		return nil
	}
	if version < 1 || version > SchemaVersion {
		return fmt.Errorf("unsupported database schema version %d", version)
	}
	for ; version < SchemaVersion; version++ {
		if _, err := db.ExecContext(ctx, migrations[version-1]); err != nil {
			return fmt.Errorf("migrating the schema from version %d: %w", version, err)
		}
	}
	return nil
}

// InsertPrograms stores new programs; ones whose program_id exists are left untouched
// (the archive keeps the first seen state). Only that duplicate case is skipped: any other
// constraint violation is an error, never silently dropped data (the PHP INSERT IGNORE bug). It returns the number of rows inserted.
func (w *Writer) InsertPrograms(ctx context.Context, ps []model.Program) (int, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a commit

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO programs (
		program_id, title, subtitle, info, extended_info, description, short_description, company,
		year, country, creators, contributors, genre, quality, pg, ratio, duration,
		has_subtitle, is_series, series_id, episode, episodes, added_at, released_at, image_state
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,0)
		ON CONFLICT (program_id) DO NOTHING`) // only duplicates are skipped; CHECK violations are errors
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	added := w.now().Unix()
	inserted := 0
	for _, p := range ps {
		if !progid.Valid(p.ProgramID) {
			return 0, fmt.Errorf("refusing to store malformed program id %q", p.ProgramID)
		}
		res, err := stmt.ExecContext(ctx,
			p.ProgramID, p.Title, p.Subtitle, p.Info, p.ExtendedInfo, p.Description, p.ShortDescription, p.Company,
			p.Year, p.Country, p.Creators, p.Contributors, p.Genre, p.Quality, p.PG, p.Ratio, p.Duration,
			boolInt(p.HasSubtitle), boolInt(p.IsSeries), p.SeriesID, p.Episode, p.Episodes, added, p.ReleasedAt)
		if err != nil {
			return 0, fmt.Errorf("storing %s: %w", p.ProgramID, err)
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}
	return inserted, tx.Commit()
}

// RecordAvailability stores the observed availability of programs. Unlike InsertPrograms it
// UPDATES existing rows, but only the availability columns: the descriptive fields keep the
// first seen state. A change of the playable flag (or a first observation) is also appended to
// availability_log. Programs whose item carried no playable flag are skipped. It returns how
// many programs were observed and how many of them changed.
func (w *Writer) RecordAvailability(ctx context.Context, ps []model.Program) (observed, changed int, err error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a commit

	sel, err := tx.PrepareContext(ctx, "SELECT playable FROM programs WHERE program_id = ?")
	if err != nil {
		return 0, 0, err
	}
	defer sel.Close()
	updChanged, err := tx.PrepareContext(ctx, `UPDATE programs SET playable = ?, playable_checked_at = ?,
		playable_changed_at = ?, window_first = ?, window_last = ?, gone_at = 0 WHERE program_id = ?`)
	if err != nil {
		return 0, 0, err
	}
	defer updChanged.Close()
	updSame, err := tx.PrepareContext(ctx, `UPDATE programs SET playable_checked_at = ?, window_first = ?,
		window_last = ?, gone_at = 0 WHERE program_id = ?`)
	if err != nil {
		return 0, 0, err
	}
	defer updSame.Close()
	logRow, err := tx.PrepareContext(ctx, "INSERT INTO availability_log (program_id, observed_at, playable) VALUES (?, ?, ?)")
	if err != nil {
		return 0, 0, err
	}
	defer logRow.Close()

	now := w.now().Unix()
	for _, p := range ps {
		if !p.PlayableKnown || !progid.Valid(p.ProgramID) {
			continue
		}
		var prev sql.NullInt64
		if err := sel.QueryRowContext(ctx, p.ProgramID).Scan(&prev); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue // not stored (should not happen: insert comes first)
			}
			return 0, 0, err
		}
		observed++
		val := boolInt(p.Playable)
		if prev.Valid && prev.Int64 == int64(val) {
			if _, err := updSame.ExecContext(ctx, now, p.WindowFirst, p.WindowLast, p.ProgramID); err != nil {
				return 0, 0, err
			}
			continue
		}
		changed++
		if _, err := updChanged.ExecContext(ctx, val, now, now, p.WindowFirst, p.WindowLast, p.ProgramID); err != nil {
			return 0, 0, err
		}
		if _, err := logRow.ExecContext(ctx, p.ProgramID, now, val); err != nil {
			return 0, 0, err
		}
	}
	return observed, changed, tx.Commit()
}

// TouchChecked records that the given programs were checked just now, whether or not the upstream
// answered with a playable flag.
func (w *Writer) TouchChecked(ctx context.Context, ids []string) error {
	now := w.now().Unix()
	for _, id := range ids {
		if !progid.Valid(id) {
			continue
		}
		if _, err := w.db.ExecContext(ctx, "UPDATE programs SET playable_checked_at = ?, gone_at = 0 WHERE program_id = ?", now, id); err != nil {
			return err
		}
	}
	return nil
}

// MarkGone records that the upstream no longer knows a stored program. Its metadata stays.
func (w *Writer) MarkGone(ctx context.Context, id string) error {
	if !progid.Valid(id) {
		return errors.New("malformed program id")
	}
	_, err := w.db.ExecContext(ctx, "UPDATE programs SET gone_at = ? WHERE program_id = ? AND gone_at = 0", w.now().Unix(), id)
	return err
}

// StalestForRecheck returns up to limit program ids whose availability was observed longest ago
// (never observed first), skipping programs the upstream no longer knows.
func (w *Writer) StalestForRecheck(ctx context.Context, limit int) ([]string, error) {
	rows, err := w.db.QueryContext(ctx,
		"SELECT program_id FROM programs WHERE gone_at = 0 ORDER BY playable_checked_at ASC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MissingIDs returns those of the valid ids that are not stored yet, in the given order.
func (w *Writer) MissingIDs(ctx context.Context, ids []string) ([]string, error) {
	seen := make(map[string]bool, len(ids))
	var valid []string
	for _, id := range ids {
		if progid.Valid(id) && !seen[id] {
			seen[id] = true
			valid = append(valid, id)
		}
	}
	existing := make(map[string]bool)
	for start := 0; start < len(valid); start += 400 {
		chunk := valid[start:min(start+400, len(valid))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := w.db.QueryContext(ctx, "SELECT program_id FROM programs WHERE program_id IN ("+placeholders(len(chunk))+")", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			existing[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	var missing []string
	for _, id := range valid {
		if !existing[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// PendingImages returns up to limit program ids without a cover image yet, newest first.
func (w *Writer) PendingImages(ctx context.Context, limit int) ([]string, error) {
	rows, err := w.db.QueryContext(ctx, "SELECT program_id FROM programs WHERE image_state = 0 ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetImageState records the outcome of a cover image download.
func (w *Writer) SetImageState(ctx context.Context, id string, state int) error {
	if !progid.Valid(id) {
		return errors.New("malformed program id")
	}
	_, err := w.db.ExecContext(ctx, "UPDATE programs SET image_state = ? WHERE program_id = ?", state, id)
	return err
}

// Count returns the number of stored programs.
func (w *Writer) Count(ctx context.Context) (int, error) {
	var n int
	err := w.db.QueryRowContext(ctx, "SELECT count(*) FROM programs").Scan(&n)
	return n, err
}

// check verifies the structural integrity of the database and of the full text index.
func (w *Writer) check(ctx context.Context) error {
	rows, err := w.db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return err
	}
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	rows.Close()
	if len(problems) > 0 {
		return fmt.Errorf("integrity_check failed: %s", strings.Join(problems[:min(len(problems), 3)], "; "))
	}
	if _, err := w.db.ExecContext(ctx, "INSERT INTO programs_fts (programs_fts, rank) VALUES ('integrity-check', 1)"); err != nil {
		return fmt.Errorf("full text index check failed: %w", err)
	}
	return nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
