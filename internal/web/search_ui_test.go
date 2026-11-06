package web

import (
	"context"
	"fmt"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
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
	if strings.Contains(body, `class="paginator__wrapper"`) {
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
