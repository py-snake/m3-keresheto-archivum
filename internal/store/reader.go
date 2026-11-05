package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
)

// Search limits.
const (
	// MinTermRunes is the shortest text search (a trigram index cannot find less).
	MinTermRunes = 3
	// MaxTermRunes caps the search term.
	MaxTermRunes = 100
	// MaxIDs caps the program ids of an identifier lookup.
	MaxIDs = 50
	// CountCap bounds the work of counting text search results.
	CountCap = 10000
)

// ErrTermTooShort is returned for a text search shorter than MinTermRunes.
var ErrTermTooShort = errors.New("store: search term too short")

// Reader is the read-only, authorizer-restricted view of a published database.
type Reader struct {
	db *sql.DB
}

// OpenReader opens the published file for the web role.
func OpenReader(path string) (*Reader, error) {
	q := url.Values{"mode": {"ro"}, "immutable": {"1"}}
	db, err := driver.Open(fileDSN(path, q), func(c *sqlite3.Conn) error {
		if err := fts5.Register(c); err != nil {
			return err
		}
		// pragmas first, authorizer last: afterwards every statement is policed
		for _, p := range []string{"PRAGMA query_only = 1", "PRAGMA trusted_schema = OFF", "PRAGMA cell_size_check = ON"} {
			if err := c.Exec(p); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
		}
		return c.SetAuthorizer(readerAuthorizer)
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	r := &Reader{db: db}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("reading schema version: %w", err)
	}
	if version != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported database schema version %d", version)
	}
	return r, nil
}

// Close releases the connections.
func (r *Reader) Close() error { return r.db.Close() }

// Ping checks that the database answers.
func (r *Reader) Ping(ctx context.Context) error {
	var n int
	return r.db.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT 1 FROM programs LIMIT 1)").Scan(&n)
}

// Result is one page of search results.
type Result struct {
	Items []model.Program
	// Total is the number of matches, capped at CountCap for text searches (see Capped).
	Total  int
	Capped bool
}

const selectColumns = `id, program_id, title, subtitle, info, extended_info, description, short_description, company,
	year, country, creators, contributors, genre, quality, pg, ratio, duration,
	has_subtitle, is_series, series_id, episode, episodes, added_at, released_at, image_state`

// NormalizeTerm trims a search term and enforces the length limit. It returns false when the
// term contains characters that never belong into a search (NUL, invalid UTF-8).
func NormalizeTerm(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return "", false
	}
	if utf8.RuneCountInString(s) > MaxTermRunes {
		s = string([]rune(s)[:MaxTermRunes])
	}
	return s, true
}

// Search returns one page of programs, newest first.
//
//   - an empty term lists everything;
//   - "M3-..." (comma separated) looks identifiers up;
//   - anything else is a text search over title, subtitle, info, extended info, description,
//     creators, contributors and genre. The user's text is never FTS syntax: it is passed as one
//     quoted phrase, every query value is a bound parameter.
func (r *Reader) Search(ctx context.Context, term string, limit, offset int) (Result, error) {
	term, ok := NormalizeTerm(term)
	if !ok {
		return Result{}, nil
	}
	if limit < 1 || limit > 100 || offset < 0 {
		return Result{}, errors.New("store: bad paging")
	}

	switch {
	case term == "":
		return r.run(ctx, "", nil, "SELECT count(*) FROM programs", nil, limit, offset)

	case progid.LooksLikeIDQuery(term) && len(progid.ParseList(term, MaxIDs)) > 0:
		ids := progid.ParseList(term, MaxIDs)
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		where := " WHERE program_id IN (" + placeholders(len(ids)) + ")"
		return r.run(ctx, where, args, "SELECT count(*) FROM programs"+where, args, limit, offset)

	default:
		if utf8.RuneCountInString(term) < MinTermRunes {
			return Result{}, ErrTermTooShort
		}
		phrase := `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
		where := " WHERE id IN (SELECT rowid FROM programs_fts WHERE programs_fts MATCH ?)"
		count := fmt.Sprintf("SELECT count(*) FROM (SELECT rowid FROM programs_fts WHERE programs_fts MATCH ? LIMIT %d)", CountCap+1)
		res, err := r.run(ctx, where, []any{phrase}, count, []any{phrase}, limit, offset)
		if err == nil && res.Total > CountCap {
			res.Total, res.Capped = CountCap, true
		}
		return res, err
	}
}

func (r *Reader) run(ctx context.Context, where string, args []any, countSQL string, countArgs []any, limit, offset int) (Result, error) {
	var res Result
	if err := r.db.QueryRowContext(ctx, countSQL, countArgs...).Scan(&res.Total); err != nil {
		return Result{}, err
	}
	if res.Total == 0 || offset >= res.Total {
		return res, nil
	}

	q := "SELECT " + selectColumns + " FROM programs" + where + " ORDER BY id DESC LIMIT ? OFFSET ?"
	rows, err := r.db.QueryContext(ctx, q, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p model.Program
		var hasSub, isSeries int
		if err := rows.Scan(&p.ID, &p.ProgramID, &p.Title, &p.Subtitle, &p.Info, &p.ExtendedInfo, &p.Description,
			&p.ShortDescription, &p.Company, &p.Year, &p.Country, &p.Creators, &p.Contributors, &p.Genre,
			&p.Quality, &p.PG, &p.Ratio, &p.Duration, &hasSub, &isSeries, &p.SeriesID, &p.Episode, &p.Episodes,
			&p.AddedAt, &p.ReleasedAt, &p.ImageState); err != nil {
			return Result{}, err
		}
		p.HasSubtitle, p.IsSeries = hasSub != 0, isSeries != 0
		res.Items = append(res.Items, p)
	}
	return res, rows.Err()
}
