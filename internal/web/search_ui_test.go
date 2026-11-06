package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/images"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
	webfs "github.com/py-snake/m3-keresheto-archivum/web"
)

func catalog() []model.Program {
	mk := func(id, title string, year int, creators, genre, desc string) model.Program {
		p := prog(id, title)
		p.Year, p.Creators, p.Genre, p.Description = year, creators, genre, desc
		return p
	}
	a := mk("M3-C0001", "Árvíztűrő tükörfúrógép", 1995, "Kiss Ödön\nNagy István", "Animáció gyerekeknek", "Egy régi mese")
	a.IsHighlighted = true
	a.PlayableKnown, a.Playable = true, true
	b := mk("M3-C0002", "Béke szigete", 1980, "Nagy István", "TV-film, TV-játék", "Háborúról és békéről")
	c := mk("M3-C0003", "Zöld mező", 2003, "Szabó Zoltán", "Dokumentum filmek", "Természetfilm")
	return []model.Program{a, b, c}
}

func TestFiltersFromTheURL(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	cases := []struct {
		name, query string
		want        []string // titles that must be present
		not         []string
	}{
		{"creators", "keszitok=Nagy+Istv%C3%A1n", []string{"Árvíztűrő", "Béke szigete"}, []string{"Zöld mező"}},
		{"creators and genre", "keszitok=Nagy&mufaj=TV-film%2C+TV-j%C3%A1t%C3%A9k", []string{"Béke szigete"}, []string{"Árvíztűrő", "Zöld mező"}},
		{"title field", "cim=zold", []string{"Zöld mező"}, []string{"Béke"}},
		{"description field", "leiras=mese", []string{"Árvíztűrő"}, []string{"Béke"}},
		{"year range", "ev_tol=1990&ev_ig=2000", []string{"Árvíztűrő"}, []string{"Béke", "Zöld"}},
		{"highlighted", "kiemelt=1", []string{"Árvíztűrő"}, []string{"Béke", "Zöld"}},
		{"term plus field", "kereses=nagy&cim=b%C3%A9ke", []string{"Béke szigete"}, []string{"Árvíztűrő"}},
	}
	for _, c := range cases {
		resp, body := e.get(t, "/?"+c.query)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", c.name, resp.StatusCode)
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: %q missing", c.name, w)
			}
		}
		for _, n := range c.not {
			if strings.Contains(body, "<span class=\"mdc-typography--headline6 cell__title--title\">"+n) {
				t.Errorf("%s: %q must be filtered out", c.name, n)
			}
		}
	}
}

func TestSortOrdersAndTheirSelection(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	pos := func(body, title string) int { return strings.Index(body, ">"+title) }
	_, body := e.get(t, "/?rendezes=cim_n")
	if !(pos(body, "Árvíztűrő") < pos(body, "Béke") && pos(body, "Béke") < pos(body, "Zöld")) {
		t.Error("title A-Z: diacritics must be folded (Árvíz before Béke before Zöld)")
	}
	_, body = e.get(t, "/?rendezes=ev_cs")
	if !(pos(body, "Zöld") < pos(body, "Árvíz") && pos(body, "Árvíz") < pos(body, "Béke")) {
		t.Error("year descending")
	}
	_, body = e.get(t, "/?rendezes=regi")
	if !(pos(body, "Árvíz") < pos(body, "Béke") && pos(body, "Béke") < pos(body, "Zöld")) {
		t.Error("oldest first")
	}
	if !strings.Contains(body, `<option value="regi" selected>`) {
		t.Error("the chosen order is not selected in the form")
	}
	// an unknown order falls back to the default instead of failing or reaching the database
	for _, bad := range []string{"id%3B+DROP+TABLE+programs", "ev_cs%00", "year_desc", "%27"} {
		resp, body := e.get(t, "/?rendezes="+bad)
		if resp.StatusCode != 200 || !strings.Contains(body, "Zöld mező") {
			t.Errorf("rendezes=%s: %d", bad, resp.StatusCode)
		}
	}
}

func TestInvalidFilterValuesAreHandledNotTrusted(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	for query, want := range map[string]string{
		"ev_tol=abc":       "Érvénytelen évszám",
		"ev_ig=99999":      "Érvénytelen évszám",
		"ev_tol=-5":        "Érvénytelen évszám",
		"ev_tol=1990.5":    "Érvénytelen évszám",
		"cim=ab":           "legalább 3 karakter",
		"keszitok=%00xyz":  "Érvénytelen karakter",
		"mufaj=%ff%fe":     "Érvénytelen karakter",
		"alcim=x":          "legalább 3 karakter",
		"cim=%22+OR+1%3D1": "", // a quote is just text: no error, no match
	} {
		resp, body := e.get(t, "/?"+query)
		if resp.StatusCode >= 500 {
			t.Errorf("%s: status %d", query, resp.StatusCode)
		}
		if want != "" && !strings.Contains(body, want) {
			t.Errorf("%s: notice %q missing", query, want)
		}
		assertSecure(t, query, resp)
	}
	// every field name that is not a form field is simply ignored
	resp, _ := e.get(t, "/?program_id=x&rowid=1&programs_fts=1&title=x")
	if resp.StatusCode != 200 {
		t.Errorf("unknown parameters: %d", resp.StatusCode)
	}
}

func TestPagerKeepsEveryFilter(t *testing.T) {
	var ps []model.Program
	for i := 0; i < 25; i++ {
		p := prog(fmt.Sprintf("M3-P%04d", i), fmt.Sprintf("Film %03d", i))
		p.Year, p.Genre, p.Creators = 1990, "Dokumentum filmek", "Nagy Béla"
		ps = append(ps, p)
	}
	e := newEnv(t, nil, ps...)
	_, body := e.get(t, "/?kereses=Film&keszitok=Nagy&mufaj=Dokumentum+filmek&ev_tol=1980&ev_ig=2000&rendezes=cim_n&elerheto=1")
	_, body2 := e.get(t, "/?kereses=Film&keszitok=Nagy&mufaj=Dokumentum+filmek&ev_tol=1980&ev_ig=2000&rendezes=cim_n")
	if strings.Contains(body, `class="pager"`) {
		t.Error("nothing is observed as playable, so the playable filter must leave no results")
	}
	m := strings.Index(body2, `href="/2?`)
	if m < 0 {
		t.Fatal("no link to page 2")
	}
	link := body2[m : m+400]
	link = html.UnescapeString(link[:strings.Index(link[6:], `"`)+7]) // &amp; and &#43; as a browser reads them
	for _, want := range []string{"kereses=Film", "keszitok=Nagy", "mufaj=Dokumentum+filmek", "ev_tol=1980", "ev_ig=2000", "rendezes=cim_n"} {
		if !strings.Contains(link, want) {
			t.Errorf("the page link drops %q: %s", want, link)
		}
	}
	// following the link keeps the search: page 3 holds the last 5 of 25 matches
	_, p3 := e.get(t, strings.TrimSuffix(strings.TrimPrefix(link, `href="`), `"`))
	if !strings.Contains(p3, "25 találat") {
		t.Error("the second page lost the search")
	}
}

func TestFormShowsGenresYearsAndSortChoices(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/")
	for _, want := range []string{
		`<option value="Animáció gyerekeknek">Animáció gyerekeknek (1)</option>`,
		`<option value="Dokumentum filmek">Dokumentum filmek (1)</option>`,
		`name="ev_tol" min="1980" max="2003"`, `name="ev_ig" min="1980" max="2003"`,
		`<option value="cim_n">Cím szerint A–Z</option>`, `name="kiemelt"`, `name="keszitok"`, `name="kozremukodok"`,
		`<details class="advanced">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	// the detailed panel opens by itself when a detailed filter is active
	_, body = e.get(t, "/?cim=Zöld")
	if !strings.Contains(body, `<details class="advanced" open>`) {
		t.Error("the panel must be open when a detailed filter is set")
	}
	if !strings.Contains(body, `name="cim" value="Zöld"`) {
		t.Error("the filter value is not shown in its field")
	}
}

func TestFragmentIsOnlyTheResults(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	resp, body := e.get(t, "/?kereses=Béke&reszlet=1")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("%d", resp.StatusCode)
	}
	assertSecure(t, "fragment", resp)
	for _, bad := range []string{"<html", "<form", "<head", "<script", "Lekérdezés:", "<footer"} {
		if strings.Contains(body, bad) {
			t.Errorf("the fragment contains %q", bad)
		}
	}
	if !strings.Contains(body, "1 találat") || !strings.Contains(body, "Béke szigete") {
		t.Error("the fragment lacks the results")
	}
	// the fragment of a hostile search is escaped just like the page
	_, body = e.get(t, "/?kereses=%3Cscript%3Ealert(1)%3C%2Fscript%3E&reszlet=1")
	if strings.Contains(body, "<script") {
		t.Error("unescaped input in the fragment")
	}
	// paging links of a page never carry the fragment flag
	var many []model.Program
	for i := 0; i < 25; i++ {
		many = append(many, prog(fmt.Sprintf("M3-F%04d", i), fmt.Sprintf("Töredék %03d", i)))
	}
	e2 := newEnv(t, nil, many...)
	_, body = e2.get(t, "/?kereses=Töredék&reszlet=1")
	if !strings.Contains(body, `href="/2?`) || strings.Contains(body, "reszlet") {
		t.Error("the pager links of a fragment must not contain the fragment flag")
	}
}

func TestFooterShowsTheQueryTimeOnPagesOnly(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/")
	if !strings.Contains(body, "Lekérdezés: 0.") || !strings.Contains(body, " mp") {
		t.Errorf("no query time in the footer")
	}
}

func TestHighlightedBadgeAndNewFieldsInDetails(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/?kereses=Árvíztűrő")
	if !strings.Contains(body, `badge--highlight`) || !strings.Contains(body, ">Kiemelt<") {
		t.Error("the highlighted badge is missing")
	}
	if !strings.Contains(body, ">highlighted<") {
		t.Error("the details do not list the new field")
	}
	_, body = e.get(t, "/?kereses=Zöld")
	if strings.Contains(body, ">Kiemelt<") {
		t.Error("badge on a program that is not highlighted")
	}
}

func TestAvailabilityHistoryInTheDetails(t *testing.T) {
	dir := t.TempDir()
	p := prog("M3-H0001", "Előzményes film")
	p.PlayableKnown, p.Playable = true, true
	publish := func(at int64, playable bool) {
		p.Playable = playable
		pub := &store.Publisher{Dir: dir, Now: func() time.Time { return time.Unix(at, 0) }}
		if _, err := pub.Run(context.Background(), func(ctx context.Context, w *store.Writer) error {
			if _, err := w.InsertPrograms(ctx, []model.Program{p}); err != nil {
				return err
			}
			_, _, err := w.RecordObservations(ctx, []model.Program{p})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	publish(1_700_000_000, true)
	publish(1_700_100_000, false)
	publish(1_700_200_000, true)

	src := store.NewSource(dir, discardLogger())
	if err := src.Refresh(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)
	s, err := New(Options{DataDir: dir, Source: src, Log: discardLogger(), RateLimit: 1000, RateBurst: 1000})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{srv: newTestServer(t, s), s: s, src: src, dir: dir}

	_, body := e.get(t, "/")
	for _, want := range []string{"elérhetőségi előzmény", "elérhetővé vált", "elérhetetlenné vált"} {
		if !strings.Contains(body, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	if strings.Count(body, "elérhetővé vált") != 2 || strings.Count(body, "elérhetetlenné vált") != 1 {
		t.Errorf("the history must list each change once (true, false, true)")
	}
	// a program that is not playable right now, but was, says when it stopped being so
	publish(1_700_300_000, false)
	if err := src.Refresh(); err != nil {
		t.Fatal(err)
	}
	_, body = e.get(t, "/")
	if !strings.Contains(body, "Elérhetetlenné válását ekkor vettük észre") {
		t.Error("the tooltip does not say when the program became unavailable")
	}
}

func TestSearchScriptIsServedAndTheCSPAllowsOnlyIt(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	resp, body := e.get(t, "/static/search.js")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	assertSecure(t, "script", resp)
	for _, bad := range []string{"eval(", "innerHTML", "document.write", "outerHTML", "insertAdjacentHTML", "new Function", "setTimeout(\"", "http://", "https://"} {
		if strings.Contains(body, bad) {
			t.Errorf("the script contains %q", bad)
		}
	}
	// the script only talks to the page itself
	if !strings.Contains(body, "fetch('/?'") {
		t.Error("expected a same-origin fetch")
	}
}

// The HTML autofocus attribute focused the search field on every page load, which opened the
// on-screen keyboard on phones at each page turn. Focusing is done by search.js, only for fine
// pointers and only on the bare start page.
func TestSearchFieldHasNoAutofocusAttribute(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	for _, path := range []string{"/", "/?kereses=zold", "/?kereses=zold&db=10", "/?nyers"} {
		_, body := e.get(t, path)
		if !strings.Contains(body, `id="search-field"`) {
			t.Fatalf("%s: no search field in the page", path)
		}
		if strings.Contains(strings.ToLower(body), "autofocus") {
			t.Errorf("%s: the page still has an autofocus attribute", path)
		}
	}
	_, script := e.get(t, "/static/search.js")
	for _, want := range []string{"(pointer: fine)", "location.search === ''"} {
		if !strings.Contains(script, want) {
			t.Errorf("search.js does not limit the focusing: %q is missing", want)
		}
	}
}

func TestShortSearchOnThePage(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/?kereses=zo")
	if !strings.Contains(body, "Zöld mező") || strings.Contains(body, "Béke szigete") {
		t.Error("a 2 character search must find titles (folded) and only titles")
	}
	_, body = e.get(t, "/?kereses=%25")
	if strings.Contains(body, "Zöld mező") {
		t.Error("a LIKE wildcard in the term must be literal")
	}
}

func TestJumpToPageRedirectsToTheCanonicalAddress(t *testing.T) {
	e := newEnv(t, nil, many(3)...)
	for _, c := range []struct{ path, want string }{
		{"/?oldal=3&kereses=Film", "/3?kereses=Film"},
		{"/?kereses=Film&oldal=3", "/3?kereses=Film"},
		{"/?oldal=1&kereses=Film", "/?kereses=Film"},
		{"/?oldal=0", "/"},
		{"/?oldal=-5", "/"},
		{"/?oldal=%2007%20", "/7"},
		{"/?oldal=99999", "/1000"},
		{"/?oldal=2&nyers", "/2?nyers="},
		{"/?oldal=2&kereses=a%26b+c&db=50", "/2?db=50&kereses=a%26b+c"},
	} {
		resp, _ := e.get(t, c.path)
		loc := resp.Header.Get("Location")
		if resp.StatusCode != http.StatusSeeOther || loc != c.want {
			t.Errorf("%s: %d -> %q, want 303 -> %q", c.path, resp.StatusCode, loc, c.want)
		}
		// only ever one of our own page paths
		if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") || strings.Contains(loc, "://") {
			t.Errorf("%s: unsafe redirect target %q", c.path, loc)
		}
		assertSecure(t, c.path, resp)
	}

	// anything that is not a number is simply ignored, never followed
	for _, path := range []string{"/?oldal=abc", "/?oldal=%2F%2Fevil.example", "/?oldal=https://evil.example/", "/?oldal=1e3", "/?oldal=2.5"} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d, Location %q", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}

	// a page number in the path wins over the parameter
	if resp, _ := e.get(t, "/1?oldal=2"); resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		t.Errorf("/1?oldal=2: %d", resp.StatusCode)
	}
}

func TestPageSizeChoice(t *testing.T) {
	e := newEnv(t, nil, many(45)...)
	count := func(path string) int {
		_, body := e.get(t, path)
		return strings.Count(body, `class="card"`)
	}
	if n := count("/?kereses=Film"); n != DefaultPageSize {
		t.Errorf("default page: %d cards, want %d", n, DefaultPageSize)
	}
	if n := count("/?kereses=Film&db=10"); n != 10 {
		t.Errorf("db=10: %d cards", n)
	}
	if n := count("/?kereses=Film&db=100"); n != 45 {
		t.Errorf("db=100: %d cards", n)
	}
	// sizes that are not offered fall back to the default
	for _, bad := range []string{"7", "0", "-1", "101", "abc", "9999999999999999999", "20x", ""} {
		if n := count("/?kereses=Film&db=" + url.QueryEscape(bad)); n != DefaultPageSize {
			t.Errorf("db=%q: %d cards, want the default %d", bad, n, DefaultPageSize)
		}
	}
	// the number of pages follows the size, and the page links carry the choice (but never the default)
	_, body := e.get(t, "/?kereses=Film&db=10")
	if !strings.Contains(body, "45 találat · 1. oldal / 5") || !strings.Contains(body, `href="/2?db=10&amp;kereses=Film"`) {
		t.Errorf("pager with 10 per page is wrong")
	}
	_, body = e.get(t, "/?kereses=Film&db=20")
	if strings.Contains(body, "db=20") || !strings.Contains(body, `href="/2?kereses=Film"`) {
		t.Errorf("the default page size must not show up in links")
	}
	// the form offers the sizes, the default one has an empty value and is selected
	if !strings.Contains(body, `<option value="" selected>20</option>`) || !strings.Contains(body, `<option value="100">100</option>`) {
		t.Errorf("page size select is missing or wrong")
	}
	_, body = e.get(t, "/?kereses=Film&db=50")
	if !strings.Contains(body, `<option value="50" selected>50</option>`) {
		t.Errorf("the chosen page size is not selected")
	}
}

func TestPageAfterTheLastGoesToTheLastPage(t *testing.T) {
	e := newEnv(t, nil, many(45)...)
	for path, want := range map[string]string{
		"/4?kereses=Film":       "/3?kereses=Film",
		"/999?kereses=Film":     "/3?kereses=Film",
		"/7?kereses=Film&db=10": "/5?db=10&kereses=Film",
	} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != want {
			t.Errorf("%s: %d -> %q, want %q", path, resp.StatusCode, resp.Header.Get("Location"), want)
		}
	}
	// an existing last page is shown as it is, and a search without results does not redirect
	if resp, _ := e.get(t, "/3?kereses=Film"); resp.StatusCode != http.StatusOK {
		t.Errorf("last page: %d", resp.StatusCode)
	}
	if resp, body := e.get(t, "/3?kereses=zzzzzz"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Nincs találat") {
		t.Errorf("no results: %d", resp.StatusCode)
	}
}

func TestSortIsVisibleOnTheFormNotHiddenInTheAdvancedPanel(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/")
	sel := strings.Index(body, `name="rendezes"`)
	adv := strings.Index(body, `<details class="advanced"`)
	if sel < 0 || adv < 0 || sel > adv {
		t.Fatalf("the sort select must come before the advanced panel (select at %d, panel at %d)", sel, adv)
	}
	if strings.Count(body, `name="rendezes"`) != 1 {
		t.Error("exactly one sort select expected")
	}
	// choosing a sort order does not force the advanced panel open any more
	_, body = e.get(t, "/?rendezes=cim_n")
	if strings.Contains(body, `<details class="advanced" open>`) {
		t.Error("a sort order alone must not open the advanced panel")
	}
	if !strings.Contains(body, `<option value="cim_n" selected>`) {
		t.Error("the chosen sort order is not selected")
	}
}

func TestPagerJumpFormEscapesTheSearch(t *testing.T) {
	var ps []model.Program
	for i := 0; i < 45; i++ {
		ps = append(ps, prog(fmt.Sprintf("M3-X%03d", i), fmt.Sprintf(`Zold "><i> film %03d`, i)))
	}
	e := newEnv(t, nil, ps...)
	_, body := e.get(t, "/?kereses="+url.QueryEscape(`"><i>`))
	if !strings.Contains(body, `class="pager__jump"`) {
		t.Fatal("no jump form: the hostile search found nothing, adjust the fixture")
	}
	if strings.Contains(body, `"><i>`) {
		t.Error("the search text is not escaped in the page")
	}
	if !strings.Contains(body, `type="hidden" name="kereses" value="&#34;&gt;&lt;i&gt;"`) {
		t.Errorf("the jump form lost or mis-escaped the search term")
	}
	if strings.Contains(body, `name="oldal" value=`) {
		t.Error("the jump form must not prefill a page number as a hidden field")
	}
	if !strings.Contains(body, `name="oldal" min="1" max="3"`) {
		t.Error("the page number input has no limits")
	}
}

func TestCardsAreTheListMarkup(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/")
	for _, want := range []string{`<ul class="cards">`, `class="card__title"`, `class="avail avail--`, `class="m3link"`, `class="card__more"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %s", want)
		}
	}
	// the data tables are gone from the normal view (the raw view keeps one)
	if strings.Contains(body, `class="mdc-data-table mdc-elevation--z2 list__table"`) {
		t.Error("the old table layout is still there")
	}
	if _, raw := e.get(t, "/?nyers"); !strings.Contains(raw, `list__table--raw`) {
		t.Error("the raw view lost its scrollable table")
	}
}

func TestFooterHasTheSourceLink(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, body := e.get(t, "/")
	for _, want := range []string{`class="footer-links__source"`, `href="https://github.com/py-snake/m3-keresheto-archivum"`, `rel="noopener noreferrer"`, "Nyílt forráskód a GitHubon", "Nem hivatalos kereső"} {
		if !strings.Contains(body, want) {
			t.Errorf("the footer lacks %q", want)
		}
	}
}

const (
	yearImmutable = "public, max-age=31536000, immutable"
	oneDay        = "public, max-age=86400"
)

func TestCoverImagesAreCachedForAYearButMissingOnesNever(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "x"))
	if err := images.Save(filepath.Join(e.dir, "images"), "M3-AAA1", []byte("\xff\xd8\xff")); err != nil {
		t.Fatal(err)
	}
	resp, _ := e.get(t, "/img/M3-AAA1.jpg")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != yearImmutable {
		t.Errorf("existing cover: %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("Last-Modified") == "" {
		t.Error("no Last-Modified: conditional requests could not be answered with 304")
	}
	// a cover that is not there yet may arrive a minute later: a cached 404 would hide it for a year
	for _, path := range []string{"/img/M3-NOPE.jpg", "/img/M3-AAA1.png", "/img/not-an-id.jpg", "/img/..%2fm3.sqlite"} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != 404 || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d, Cache-Control %q (want 404, no-store)", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
}

func TestStaticFilesAreVersionedByContentAndCachedForAYear(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	_, page := e.get(t, "/")
	sub, err := fs.Sub(webfs.FS, "static")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.css", "m3.min.css", "search.js", "logo.png", "placeholder.svg"} {
		m := regexp.MustCompile(`/static/` + regexp.QuoteMeta(name) + `\?v=([0-9a-f]{10})`).FindStringSubmatch(page)
		if m == nil {
			t.Errorf("the page does not link %s with a content digest", name)
			continue
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if want := hex.EncodeToString(sum[:])[:10]; m[1] != want {
			t.Errorf("%s: v=%s, the digest of the file is %s", name, m[1], want)
		}
		link := "/static/" + name + "?v=" + m[1]
		if resp, _ := e.get(t, link); resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != yearImmutable {
			t.Errorf("%s: %d, Cache-Control %q", link, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
		// the same file under a made-up or missing version is not trusted to be immutable
		for _, bad := range []string{"/static/" + name, "/static/" + name + "?v=0000000000", "/static/" + name + "?v="} {
			if resp, _ := e.get(t, bad); resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != oneDay {
				t.Errorf("%s: %d, Cache-Control %q (want one day)", bad, resp.StatusCode, resp.Header.Get("Cache-Control"))
			}
		}
	}
	if resp, _ := e.get(t, "/static/nope.css?v=1"); resp.StatusCode != 404 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("missing static file: %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
}

func TestStaticDigestFollowsTheContent(t *testing.T) {
	v, err := staticVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 5 {
		t.Fatalf("expected the embedded static files, got %v", v)
	}
	seen := map[string]string{}
	for name, d := range v {
		if len(d) != 10 {
			t.Errorf("%s: digest %q", name, d)
		}
		if other, dup := seen[d]; dup {
			t.Errorf("%s and %s share a digest", name, other)
		}
		seen[d] = name
	}
}

// A program without a cover links to the shared placeholder, one with a cover to its own image: two
// different addresses. So a long-lived cached placeholder can never be shown for a program that has
// meanwhile got its own cover (its page just starts to link to /img/<id>.jpg).
func TestPlaceholderAndCoverAreDifferentAddresses(t *testing.T) {
	with, without := prog("M3-WITH1", "Van borító"), prog("M3-NONE1", "Nincs borító")
	with.ImageState, without.ImageState = model.ImageStored, model.ImagePending
	e := newEnv(t, nil, with, without)
	_, body := e.get(t, "/")
	if !strings.Contains(body, `src="/img/M3-WITH1.jpg"`) {
		t.Error("the program with a cover does not link to it")
	}
	if strings.Contains(body, "/img/M3-NONE1.jpg") {
		t.Error("the program without a cover links to an image that does not exist")
	}
	if !regexp.MustCompile(`src="/static/placeholder\.svg\?v=[0-9a-f]{10}"`).MatchString(body) {
		t.Error("the program without a cover has no placeholder")
	}
	// the page itself stays short-lived, so the switch to the cover is picked up within a minute
	resp, _ := e.get(t, "/")
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=60" {
		t.Errorf("page Cache-Control: %q", cc)
	}
}
