package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
)

func film(id, title string, year int) model.Program {
	p := prog(id, title)
	p.Year = year
	return p
}

func seed(t *testing.T) *Reader {
	t.Helper()
	dir := t.TempDir()
	a := film("M3-AAA1", "Árvíztűrő tükörfúrógép", 1995)
	a.Creators, a.Genre, a.Description = "Kiss Ödön\nNagy István", "Animáció gyerekeknek", "Egy régi mese a tükörről"
	a.PlayableKnown, a.Playable, a.IsHighlighted = true, true, true
	b := film("M3-AAA2", "Béke szigete", 1980)
	b.Creators, b.Genre, b.Description = "Nagy István", "TV-film, TV-játék", "Háborúról és békéről"
	b.PlayableKnown, b.Playable = true, false
	c := film("M3-AAA3", "(versek) Zöld", 2003)
	c.Creators, c.Genre = "Szabó Zoltán", "Dokumentum filmek"
	d := film("M3-AAA4", "Éjszakai járat", 0)
	d.Genre = "TV-film, TV-játék\nDokumentum filmek"
	run(t, dir, time.Unix(1000, 0), func(ctx context.Context, w *Writer) error {
		if _, err := w.InsertPrograms(ctx, []model.Program{a, b, c, d}); err != nil {
			return err
		}
		_, _, err := w.RecordObservations(ctx, []model.Program{a, b, c, d})
		return err
	})
	return openReader(t, dir)
}

func ids(res Result) string {
	var out []string
	for _, p := range res.Items {
		out = append(out, p.ProgramID)
	}
	return strings.Join(out, " ")
}

func q(t *testing.T, r *Reader, query Query) Result {
	t.Helper()
	if query.Limit == 0 {
		query.Limit = 50
	}
	res, err := r.SearchQuery(context.Background(), query)
	if err != nil {
		t.Fatalf("%+v: %v", query, err)
	}
	return res
}

func TestFieldFilters(t *testing.T) {
	r := seed(t)
	cases := []struct {
		name string
		q    Query
		want string
	}{
		{"creator", Query{Fields: map[string]string{"creators": "nagy istvan"}}, "M3-AAA2 M3-AAA1"},
		{"creator and genre", Query{Fields: map[string]string{"creators": "Nagy", "genre": "TV-film"}}, "M3-AAA2"},
		{"title field only", Query{Fields: map[string]string{"title": "békés"}}, ""},
		{"title field", Query{Fields: map[string]string{"title": "Béke"}}, "M3-AAA2"},
		{"term plus field", Query{Term: "mese", Fields: map[string]string{"creators": "Kiss"}}, "M3-AAA1"},
		{"term plus field, no overlap", Query{Term: "mese", Fields: map[string]string{"creators": "Szabó"}}, ""},
		{"description field", Query{Fields: map[string]string{"description": "háborúról"}}, "M3-AAA2"},
		{"empty field is ignored", Query{Fields: map[string]string{"creators": "  "}}, "M3-AAA4 M3-AAA3 M3-AAA2 M3-AAA1"},
		{"genre on a second line", Query{Fields: map[string]string{"genre": "Dokumentum"}}, "M3-AAA4 M3-AAA3"},
	}
	for _, c := range cases {
		if got := ids(q(t, r, c.q)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// a field text under 3 characters cannot be searched by the trigram index
	if _, err := r.SearchQuery(context.Background(), Query{Fields: map[string]string{"creators": "Ki"}, Limit: 10}); !errors.Is(err, ErrTermTooShort) {
		t.Errorf("short field text: %v", err)
	}
	// unknown field names are refused, never put into the query
	for _, bad := range []string{"program_id", "rowid", "programs_fts", "title : x OR creators", "*", ""} {
		if _, err := r.SearchQuery(context.Background(), Query{Fields: map[string]string{bad: "abc"}, Limit: 10}); err == nil {
			t.Errorf("field name %q accepted", bad)
		}
	}
}

func TestYearRangeFlagsAndSorting(t *testing.T) {
	r := seed(t)
	if got := ids(q(t, r, Query{YearFrom: 1990})); got != "M3-AAA3 M3-AAA1" {
		t.Errorf("year from: %q", got)
	}
	if got := ids(q(t, r, Query{YearFrom: 1980, YearTo: 1995})); got != "M3-AAA2 M3-AAA1" && got != "M3-AAA1 M3-AAA2" {
		t.Errorf("year range: %q", got)
	}
	if got := ids(q(t, r, Query{OnlyPlayable: true})); got != "M3-AAA2 M3-AAA1" && got != "M3-AAA1" {
		// only AAA1 is playable (AAA2 is observed as not playable)
		t.Errorf("playable: %q", got)
	}
	if got := ids(q(t, r, Query{OnlyPlayable: true})); got != "M3-AAA1" {
		t.Errorf("playable only: %q", got)
	}
	if got := ids(q(t, r, Query{OnlyHighlighted: true})); got != "M3-AAA1" {
		t.Errorf("highlighted: %q", got)
	}

	order := map[Sort]string{
		SortNewest:    "M3-AAA4 M3-AAA3 M3-AAA2 M3-AAA1",
		SortOldest:    "M3-AAA1 M3-AAA2 M3-AAA3 M3-AAA4",
		SortYearDesc:  "M3-AAA3 M3-AAA1 M3-AAA2 M3-AAA4",
		SortYearAsc:   "M3-AAA4 M3-AAA2 M3-AAA1 M3-AAA3",
		SortTitleAsc:  "M3-AAA1 M3-AAA2 M3-AAA4 M3-AAA3", // Árvíz, Béke, Éjszakai, (versek) Zöld: diacritics folded, "(" ignored
		SortTitleDesc: "M3-AAA3 M3-AAA4 M3-AAA2 M3-AAA1",
	}
	for s, want := range order {
		if got := ids(q(t, r, Query{Sort: s})); got != want {
			t.Errorf("sort %d: %q, want %q", s, got, want)
		}
	}
	// sorting works together with a text search, and an unknown order is refused
	if got := ids(q(t, r, Query{Fields: map[string]string{"genre": "film"}, Sort: SortTitleAsc})); got != "M3-AAA2 M3-AAA4 M3-AAA3" {
		t.Errorf("sorted search: %q", got)
	}
	if _, err := r.SearchQuery(context.Background(), Query{Sort: Sort(99), Limit: 10}); err == nil {
		t.Error("unknown sort accepted")
	}
	if s, ok := ParseSort("cim_n"); !ok || s != SortTitleAsc {
		t.Error("ParseSort")
	}
	if _, ok := ParseSort("id; DROP TABLE programs"); ok {
		t.Error("ParseSort accepted garbage")
	}
}

func TestShortSearchLooksAtTheTitleOnly(t *testing.T) {
	r := seed(t)
	// titles: "Árvíztűrő tükörfúrógép", "Béke szigete", "(versek) Zöld", "Éjszakai járat"; compared folded
	cases := map[string]string{
		"é":  "M3-AAA4 M3-AAA3 M3-AAA2 M3-AAA1", // every title has an e/é
		"ár": "M3-AAA4 M3-AAA1",                 // "jarat" and "arviz"
		"b":  "M3-AAA2",
		"zo": "M3-AAA3",
		"%":  "", // LIKE wildcards are escaped, not interpreted
		"_":  "",
		`\`:  "",
	}
	for term, want := range cases {
		if got := ids(q(t, r, Query{Term: term})); got != want {
			t.Errorf("%q: %q, want %q", term, got, want)
		}
	}
	// a word that is only in the description is NOT found by a 1-2 character search ("mese")
	if got := ids(q(t, r, Query{Term: "me"})); strings.Contains(got, "M3-AAA1") {
		t.Errorf("a short search must not look into the descriptions: %q", got)
	}
	// the old behaviour (refusing short terms) is gone: no error, and it combines with the other filters
	if _, err := r.Search(context.Background(), "a", 10, 0); err != nil {
		t.Errorf("short term: %v", err)
	}
	if got := ids(q(t, r, Query{Term: "e", Fields: map[string]string{"creators": "Nagy"}})); got != "M3-AAA2 M3-AAA1" {
		t.Errorf("short term with a field filter: %q", got)
	}
}

func TestFacetsAndHistory(t *testing.T) {
	r := seed(t)
	f, err := r.Facets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.Total != 4 || f.YearMin != 1980 || f.YearMax != 2003 {
		t.Fatalf("%+v", f)
	}
	counts := map[string]int{}
	for _, g := range f.Genres {
		counts[g.Name] = g.Count
	}
	if counts["Dokumentum filmek"] != 2 || counts["TV-film, TV-játék"] != 2 || counts["Animáció gyerekeknek"] != 1 {
		t.Fatalf("genres: %+v", f.Genres)
	}
	if f.Genres[0].Count < f.Genres[len(f.Genres)-1].Count {
		t.Fatal("genres must be sorted by frequency")
	}

	h, err := r.History(context.Background(), []string{"M3-AAA1", "M3-AAA2", "M3-NONE1", "bad id"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h["M3-AAA1"]) != 1 || !h["M3-AAA1"][0].Playable || h["M3-AAA1"][0].At != 1000 || len(h["M3-AAA2"]) != 1 || h["M3-AAA2"][0].Playable {
		t.Fatalf("%+v", h)
	}
	if len(h["M3-NONE1"]) != 0 {
		t.Fatal("unexpected history")
	}
}

func TestNewObservedFieldsAndRefresh(t *testing.T) {
	dir := t.TempDir()
	p := film("M3-OBS1", "Eredeti cím", 1990)
	p.Description, p.Creators = "eredeti leírás", "Első Szerző"
	p.IsHighlighted, p.IsLive, p.Rank, p.Collection, p.CollectionID = true, true, 7, "Gyűjtemény", "COLL1"
	p.AiredFirst, p.AiredLast = 100, 900
	run(t, dir, time.Unix(1000, 0), func(ctx context.Context, w *Writer) error {
		if _, err := w.InsertPrograms(ctx, []model.Program{p}); err != nil {
			return err
		}
		_, _, err := w.RecordObservations(ctx, []model.Program{p})
		return err
	})
	got := q(t, openReader(t, dir), Query{Term: "M3-OBS1"}).Items[0]
	if !got.IsHighlighted || !got.IsLive || got.Rank != 7 || got.Collection != "Gyűjtemény" || got.CollectionID != "COLL1" || got.AiredFirst != 100 || got.AiredLast != 900 {
		t.Fatalf("%+v", got)
	}

	// observed fields follow the upstream; the descriptive ones keep the first seen state ...
	p2 := p
	p2.IsHighlighted, p2.Rank, p2.Title, p2.Description = false, 0, "ÚJ cím", "új leírás"
	run(t, dir, time.Unix(2000, 0), func(ctx context.Context, w *Writer) error {
		_, _, err := w.RecordObservations(ctx, []model.Program{p2})
		return err
	})
	got = q(t, openReader(t, dir), Query{Term: "M3-OBS1"}).Items[0]
	if got.IsHighlighted || got.Rank != 0 || got.Title != "Eredeti cím" {
		t.Fatalf("%+v", got)
	}

	// ... until a refresh overwrites exactly the descriptive fields
	var ch RefreshChange
	run(t, dir, time.Unix(3000, 0), func(ctx context.Context, w *Writer) error {
		var err error
		ch, err = w.UpdateDescriptive(ctx, p2)
		return err
	})
	got = q(t, openReader(t, dir), Query{Term: "M3-OBS1"}).Items[0]
	if !ch.Changed || ch.Wiped {
		t.Fatalf("%+v", ch)
	}
	if got.Title != "ÚJ cím" || got.Description != "új leírás" || got.RefreshedAt != 3000 || got.AddedAt != 1000 || got.ProgramID != "M3-OBS1" {
		t.Fatalf("a refresh must update the text, stamp refreshed_at and keep added_at: %+v", got)
	}
	// the full text index and the sort key follow the update
	if res := q(t, openReader(t, dir), Query{Term: "ÚJ cím"}); res.Total != 1 {
		t.Fatalf("the full text index did not follow the refresh: %+v", res)
	}
	if res := q(t, openReader(t, dir), Query{Term: "Eredeti cím"}); res.Total != 0 {
		t.Fatal("the old title is still searchable")
	}

	// a refresh that would erase content is reported as a wipe
	wiped := p2
	wiped.Description, wiped.Creators = "", ""
	run(t, dir, time.Unix(4000, 0), func(ctx context.Context, w *Writer) error {
		var err error
		ch, err = w.UpdateDescriptive(ctx, wiped)
		return err
	})
	if !ch.Wiped {
		t.Fatalf("the loss of text was not noticed: %+v", ch)
	}
	// an unchanged refresh is not a change
	run(t, dir, time.Unix(5000, 0), func(ctx context.Context, w *Writer) error {
		var err error
		ch, err = w.UpdateDescriptive(ctx, wiped)
		return err
	})
	if ch.Changed {
		t.Fatalf("%+v", ch)
	}

	// the refresh queue: never refreshed first
	run(t, dir, time.Unix(6000, 0), func(ctx context.Context, w *Writer) error {
		if _, err := w.InsertPrograms(ctx, []model.Program{film("M3-NEWER1", "Újabb", 2000)}); err != nil {
			return err
		}
		list, err := w.StalestForRefresh(ctx, 5)
		if err != nil || len(list) != 2 || list[0] != "M3-NEWER1" {
			t.Fatalf("%v %v", list, err)
		}
		return nil
	})
}

func TestMigrationFromVersion2KeepsDataAndBackfillsTitleKeys(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, time.Unix(1000, 0), func(ctx context.Context, w *Writer) error {
		_, err := w.InsertPrograms(ctx, []model.Program{film("M3-KEY1", "Összes közül a legjobb", 1999), film("M3-KEY2", "Ábécé", 1990)})
		return err
	})
	// turn the database back into a version 2 one: no title keys, none of the new columns' data
	run(t, dir, time.Unix(1500, 0), func(ctx context.Context, w *Writer) error {
		_, err := w.db.ExecContext(ctx, "UPDATE programs SET title_key = ''")
		return err
	})
	// ... and let the migration step run again on it by hand
	run(t, dir, time.Unix(2000, 0), func(ctx context.Context, w *Writer) error {
		return backfillTitleKeys(ctx, w.db)
	})
	r := openReader(t, dir)
	if got := ids(q(t, r, Query{Sort: SortTitleAsc})); got != "M3-KEY2 M3-KEY1" {
		t.Fatalf("title order after the backfill: %q", got)
	}
}

func FuzzSearchQueryFields(f *testing.F) {
	dir := f.TempDir()
	ps := []model.Program{film("M3-FZ001", "Hello world", 2000), film("M3-FZ002", `Idézet "szó"`, 1990), film("M3-FZ003", "NEAR AND OR NOT", 0)}
	if _, err := (&Publisher{Dir: dir}).Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.InsertPrograms(ctx, ps)
		return err
	}); err != nil {
		f.Fatal(err)
	}
	r, err := OpenReader(dir + "/" + DBFile)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { r.Close() })
	for _, s := range []string{"Hello", `"`, `" OR title:* NEAR(a b)`, "*", "^x", "creators : x", "a", "é", "%", "_", "\x00", "\xff"} {
		f.Add(s, "title", s)
	}
	f.Fuzz(func(t *testing.T, term, col, val string) {
		res, err := r.SearchQuery(context.Background(), Query{Term: term, Fields: map[string]string{col: val}, Limit: 10})
		if err != nil {
			known := false
			for _, c := range FilterFields {
				known = known || c == col
			}
			if known && !errors.Is(err, ErrTermTooShort) {
				t.Fatalf("(%q, %q:%q) failed: %v", term, col, val, err)
			}
			return
		}
		if res.Total > 3 {
			t.Fatalf("matched %d of 3 rows: the text acted as an operator", res.Total)
		}
		var n int
		if err := r.db.QueryRow("SELECT count(*) FROM programs").Scan(&n); err != nil || n != 3 {
			t.Fatalf("data changed: %d %v", n, err)
		}
	})
}
