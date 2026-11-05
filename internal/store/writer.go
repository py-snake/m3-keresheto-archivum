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

// initSchema creates the schema in an empty database, or verifies an existing one.
func initSchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		return err
	}
	switch {
	case tables == 0 && version == 0:
		if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
			return fmt.Errorf("creating schema: %w", err)
		}
		return nil
	case version == SchemaVersion:
		return nil
	}
	return fmt.Errorf("unsupported database schema version %d", version)
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
