package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
	"github.com/py-snake/m3-keresheto-archivum/internal/textkey"
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

	// the database file is immutable, so facets are computed once per Reader
	facetsOnce sync.Once
	facets     Facets
	facetsErr  error
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
	has_subtitle, is_series, series_id, episode, episodes, added_at, released_at, image_state,
	playable, playable_checked_at, playable_changed_at, window_first, window_last, gone_at,
	is_highlighted, is_live, is_catchup, rank, collection, collection_id, aired_first, aired_last, refreshed_at`

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

// ProgramByID returns the stored program with exactly this id; found is false when there is none.
func (r *Reader) ProgramByID(ctx context.Context, id string) (p model.Program, found bool, err error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+selectColumns+" FROM programs WHERE program_id = ? LIMIT 1", id)
	if err != nil {
		return model.Program{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return model.Program{}, false, rows.Err()
	}
	p, err = scanProgram(rows)
	return p, err == nil, err
}

// SitemapEntry is what a sitemap needs of a program: its id and when its page last changed (Unix
// seconds, 0 = unknown).
type SitemapEntry struct {
	ID       string
	Modified int64
}

// SitemapEntries returns up to limit programs that the upstream still knows, in id order. The
// modification time is the latest of the times the program was added, refreshed or changed its
// availability, which are the things its page shows.
func (r *Reader) SitemapEntries(ctx context.Context, limit int) ([]SitemapEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT program_id, max(added_at, refreshed_at, playable_changed_at) FROM programs WHERE gone_at = 0 ORDER BY id LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SitemapEntry
	for rows.Next() {
		var e SitemapEntry
		if err := rows.Scan(&e.ID, &e.Modified); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Walk calls fn for every program in id order, streaming from the database. It is meant for the
// export; an error from fn stops the walk.
func (r *Reader) Walk(ctx context.Context, fn func(model.Program) error) error {
	rows, err := r.db.QueryContext(ctx, "SELECT "+selectColumns+" FROM programs ORDER BY id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanProgram(rows)
		if err != nil {
			return err
		}
		if err := fn(p); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Sort is a result order. The SQL of every order is fixed here: a request only ever picks one of them.
type Sort int

// The available orders.
const (
	SortNewest       Sort = iota // most recently added first (default)
	SortOldest                   // first added first
	SortYearDesc                 // production year, newest first
	SortYearAsc                  // production year, oldest first
	SortTitleAsc                 // title A-Z (diacritics folded)
	SortTitleDesc                // title Z-A
	SortReleasedDesc             // earliest known availability date, latest first
)

var sortSQL = map[Sort]string{
	SortNewest:       "id DESC",
	SortOldest:       "id ASC",
	SortYearDesc:     "year DESC, id DESC",
	SortYearAsc:      "year ASC, id ASC",
	SortTitleAsc:     "title_key ASC, id ASC",
	SortTitleDesc:    "title_key DESC, id DESC",
	SortReleasedDesc: "released_at DESC, id DESC",
}

// sortSlugs maps the names used in URLs (the web UI uses Hungarian ones, the API English ones).
var sortSlugs = map[string]Sort{
	"uj": SortNewest, "regi": SortOldest, "ev_cs": SortYearDesc, "ev_n": SortYearAsc,
	"cim_n": SortTitleAsc, "cim_cs": SortTitleDesc, "megjelenes": SortReleasedDesc,
	"newest": SortNewest, "oldest": SortOldest, "year_desc": SortYearDesc, "year_asc": SortYearAsc,
	"title_asc": SortTitleAsc, "title_desc": SortTitleDesc, "released_desc": SortReleasedDesc,
}

// ParseSort resolves a URL value; "" is the default order. ok is false for unknown values.
func ParseSort(v string) (s Sort, ok bool) {
	if v == "" {
		return SortNewest, true
	}
	s, ok = sortSlugs[v]
	return s, ok
}

// FilterFields are the full text columns a search can be restricted to. Only these names are ever
// put into the full text query; the user supplies the text, never the structure.
var FilterFields = []string{"title", "subtitle", "description", "creators", "contributors", "genre", "info"}

// Query describes a search.
type Query struct {
	// Term is the user's search text (see SearchQuery for how it is interpreted).
	Term string
	// Fields restricts the search to single columns (keys must be in FilterFields), e.g. {"creators": "Kiss"}.
	Fields map[string]string
	// YearFrom / YearTo bound the production year (0 = open).
	YearFrom, YearTo int
	// OnlyPlayable keeps only programs whose last observed state is playable; OnlyHighlighted only the highlighted ones.
	OnlyPlayable, OnlyHighlighted bool
	Sort                          Sort
	Limit, Offset                 int
}

// Search returns one page of programs, newest first. See SearchQuery.
func (r *Reader) Search(ctx context.Context, term string, limit, offset int) (Result, error) {
	return r.SearchQuery(ctx, Query{Term: term, Limit: limit, Offset: offset})
}

// SearchQuery returns one page of programs.
//
//   - an empty term lists everything;
//   - "M3-..." (comma separated) looks identifiers up;
//   - a term of 1-2 characters searches the title only (a trigram index cannot do less);
//   - anything longer is a text search over title, subtitle, info, extended info, description,
//     creators, contributors and genre.
//
// The user's text is never SQL or full text syntax: it is passed as one quoted phrase, column
// restrictions use names from a fixed list, every value is a bound parameter.
func (r *Reader) SearchQuery(ctx context.Context, q Query) (Result, error) {
	term, ok := NormalizeTerm(q.Term)
	if !ok {
		return Result{}, nil
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 {
		return Result{}, errors.New("store: bad paging")
	}
	order, ok := sortSQL[q.Sort]
	if !ok {
		return Result{}, errors.New("store: unknown sort order")
	}

	var conds []string
	var args []any
	var fts []string

	switch {
	case term == "":
	case progid.LooksLikeIDQuery(term) && len(progid.ParseList(term, MaxIDs)) > 0:
		ids := progid.ParseList(term, MaxIDs)
		conds = append(conds, "program_id IN ("+placeholders(len(ids))+")")
		for _, id := range ids {
			args = append(args, id)
		}
	case utf8.RuneCountInString(term) < MinTermRunes:
		key := textkey.Fold(term)
		if key == "" {
			return Result{}, nil
		}
		conds = append(conds, `title_key LIKE ? ESCAPE '\'`)
		args = append(args, "%"+textkey.EscapeLike(key)+"%")
	default:
		fts = append(fts, quotePhrase(term))
	}

	for _, col := range FilterFields {
		v, ok := q.Fields[col]
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		v, ok = NormalizeTerm(v)
		if !ok {
			return Result{}, nil
		}
		if utf8.RuneCountInString(v) < MinTermRunes {
			return Result{}, ErrTermTooShort
		}
		fts = append(fts, col+" : "+quotePhrase(v))
	}
	for col := range q.Fields {
		known := false
		for _, f := range FilterFields {
			known = known || f == col
		}
		if !known {
			return Result{}, errors.New("store: unknown filter field")
		}
	}

	if len(fts) > 0 {
		conds = append(conds, "id IN (SELECT rowid FROM programs_fts WHERE programs_fts MATCH ?)")
		args = append(args, strings.Join(fts, " AND "))
	}
	if q.YearFrom > 0 {
		conds = append(conds, "year >= ?")
		args = append(args, q.YearFrom)
	}
	if q.YearTo > 0 {
		conds = append(conds, "year <= ?")
		args = append(args, q.YearTo)
	}
	if q.OnlyPlayable {
		conds = append(conds, "playable = 1")
	}
	if q.OnlyHighlighted {
		conds = append(conds, "is_highlighted = 1")
	}

	where, count := "", "SELECT count(*) FROM programs"
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
		count = fmt.Sprintf("SELECT count(*) FROM (SELECT id FROM programs%s LIMIT %d)", where, CountCap+1)
	}
	res, err := r.run(ctx, where, args, count, args, order, q.Limit, q.Offset)
	if err == nil && res.Total > CountCap {
		res.Total, res.Capped = CountCap, true
	}
	return res, err
}

// quotePhrase turns text into one FTS5 phrase: the text can never become query syntax.
func quotePhrase(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Change is one entry of the availability history.
type Change struct {
	At       int64 // unix seconds
	Playable bool
}

// History returns the availability changes of the given programs, oldest first.
func (r *Reader) History(ctx context.Context, ids []string) (map[string][]Change, error) {
	out := make(map[string][]Change)
	var valid []any
	for _, id := range ids {
		if progid.Valid(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 || len(valid) > 100 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, "SELECT program_id, observed_at, playable FROM availability_log WHERE program_id IN ("+
		placeholders(len(valid))+") ORDER BY program_id, observed_at, id", valid...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var c Change
		var v int
		if err := rows.Scan(&id, &c.At, &v); err != nil {
			return nil, err
		}
		c.Playable = v == 1
		out[id] = append(out[id], c)
	}
	return out, rows.Err()
}

// GenreCount is a genre text and the number of programs that carry it.
type GenreCount struct {
	Name  string
	Count int
}

// Facets summarize the database for the search form.
type Facets struct {
	Total   int
	Genres  []GenreCount // most frequent first
	YearMin int          // plausible production years only (1850-2100)
	YearMax int
}

// Facets returns the genre list and the year range. The database file never changes while it is
// open, so the result is computed once.
func (r *Reader) Facets(ctx context.Context) (Facets, error) {
	r.facetsOnce.Do(func() { r.facets, r.facetsErr = r.computeFacets(ctx) })
	return r.facets, r.facetsErr
}

func (r *Reader) computeFacets(ctx context.Context) (Facets, error) {
	var f Facets
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM programs").Scan(&f.Total); err != nil {
		return f, err
	}
	var lo, hi sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT min(year), max(year) FROM programs WHERE year BETWEEN 1850 AND 2100").Scan(&lo, &hi); err != nil {
		return f, err
	}
	f.YearMin, f.YearMax = int(lo.Int64), int(hi.Int64)

	rows, err := r.db.QueryContext(ctx, "SELECT genre FROM programs WHERE genre <> ''")
	if err != nil {
		return f, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return f, err
		}
		seen := map[string]bool{}
		for _, line := range strings.Split(g, "\n") {
			if line = strings.TrimSpace(line); line != "" && !seen[line] {
				seen[line] = true
				counts[line]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return f, err
	}
	for name, n := range counts {
		f.Genres = append(f.Genres, GenreCount{Name: name, Count: n})
	}
	sort.Slice(f.Genres, func(i, j int) bool {
		if f.Genres[i].Count != f.Genres[j].Count {
			return f.Genres[i].Count > f.Genres[j].Count
		}
		return f.Genres[i].Name < f.Genres[j].Name
	})
	if len(f.Genres) > 200 {
		f.Genres = f.Genres[:200]
	}
	return f, nil
}

func (r *Reader) run(ctx context.Context, where string, args []any, countSQL string, countArgs []any, order string, limit, offset int) (Result, error) {
	var res Result
	if err := r.db.QueryRowContext(ctx, countSQL, countArgs...).Scan(&res.Total); err != nil {
		return Result{}, err
	}
	if res.Total == 0 || offset >= res.Total {
		return res, nil
	}

	q := "SELECT " + selectColumns + " FROM programs" + where + " ORDER BY " + order + " LIMIT ? OFFSET ?"
	rows, err := r.db.QueryContext(ctx, q, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanProgram(rows)
		if err != nil {
			return Result{}, err
		}
		res.Items = append(res.Items, p)
	}
	return res, rows.Err()
}

// scanProgram reads one row of selectColumns.
func scanProgram(rows *sql.Rows) (model.Program, error) {
	var p model.Program
	var hasSub, isSeries int
	var playable sql.NullInt64
	var highlighted, live, catchup int
	if err := rows.Scan(&p.ID, &p.ProgramID, &p.Title, &p.Subtitle, &p.Info, &p.ExtendedInfo, &p.Description,
		&p.ShortDescription, &p.Company, &p.Year, &p.Country, &p.Creators, &p.Contributors, &p.Genre,
		&p.Quality, &p.PG, &p.Ratio, &p.Duration, &hasSub, &isSeries, &p.SeriesID, &p.Episode, &p.Episodes,
		&p.AddedAt, &p.ReleasedAt, &p.ImageState,
		&playable, &p.PlayableCheckedAt, &p.PlayableChangedAt, &p.WindowFirst, &p.WindowLast, &p.GoneAt,
		&highlighted, &live, &catchup, &p.Rank, &p.Collection, &p.CollectionID, &p.AiredFirst, &p.AiredLast, &p.RefreshedAt); err != nil {
		return model.Program{}, err
	}
	p.IsHighlighted, p.IsLive, p.IsCatchup = highlighted != 0, live != 0, catchup != 0
	p.HasSubtitle, p.IsSeries = hasSub != 0, isSeries != 0
	p.PlayableKnown, p.Playable = playable.Valid, playable.Valid && playable.Int64 == 1
	return p, nil
}
