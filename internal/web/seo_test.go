package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
)

const testPublicURL = "https://m3.example.test"

func public(o *Options) { o.PublicURL = testPublicURL }

// metaContent returns the content of the first <meta> tag with the given name or property.
func metaContent(t *testing.T, body, key string) string {
	t.Helper()
	re := regexp.MustCompile(`<meta (?:name|property)="` + regexp.QuoteMeta(key) + `" content="([^"]*)">`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func linkHref(body, rel string) string {
	m := regexp.MustCompile(`<link rel="` + rel + `" href="([^"]*)">`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

func seoProgram() model.Program {
	p := prog("M3-SEO001", "Kuckókirály")
	p.Subtitle, p.Year, p.Country, p.Genre = "Egy gyerekfilm", 1970, "HUN", "TV-film"
	p.ShortDescription = "Nincs életerős férfi a faluban,\nhisz mind a csatában harcol."
	p.Description = "Hosszú leírás.\nMásodik sor."
	p.Creators, p.Contributors = "Katkics Ilona (Rendező)\nMestyán Tibor (Operatőr)", "Blahó László (Szereplő)"
	p.ImageState = model.ImageStored
	return p
}

func TestProgramPageHasItsOwnAddressTitleAndDescription(t *testing.T) {
	e := newEnv(t, public, seoProgram())
	resp, body := e.get(t, "/m3/M3-SEO001")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	assertSecure(t, "program page", resp)
	if got := resp.Header.Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control %q", got)
	}
	if !strings.Contains(body, "<title>Kuckókirály – Egy gyerekfilm (1970) | m3 kereshető archívum</title>") {
		t.Errorf("title: %s", regexp.MustCompile(`<title>[^<]*</title>`).FindString(body))
	}
	if d := metaContent(t, body, "description"); d != "Nincs életerős férfi a faluban, hisz mind a csatában harcol." {
		t.Errorf("description %q", d)
	}
	if got := metaContent(t, body, "robots"); got != robotsIndex {
		t.Errorf("robots %q", got)
	}
	if got := linkHref(body, "canonical"); got != testPublicURL+"/m3/M3-SEO001" {
		t.Errorf("canonical %q", got)
	}
	// the program's own heading is the only <h1>; the site name above it is not a heading
	if n := strings.Count(body, "<h1"); n != 1 || !strings.Contains(body, `<h1 class="program__title">`) {
		t.Errorf("expected exactly one h1, the program's: %d", n)
	}
	for _, want := range []string{"Kuckókirály", "Egy gyerekfilm", "Leírás", "Hosszú leírás.<br>Második sor.", "Készítők", "Katkics Ilona (Rendező)",
		"Közreműködők", "Blahó László (Szereplő)", "Ország", "HUN", "Műfaj", "TV-film", `alt="Kuckókirály borítóképe"`, `src="/img/M3-SEO001.jpg"`,
		"Az M3 oldalán", "https://nemzetiarchivum.hu/m3/M3-SEO001"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// no script at all on this page
	if strings.Contains(body, "<script") {
		t.Error("the program page needs no script")
	}
}

func TestLinkPreviewTags(t *testing.T) {
	e := newEnv(t, public, seoProgram(), prog("M3-SEO002", "Képtelen"))
	_, body := e.get(t, "/m3/M3-SEO001")
	for k, want := range map[string]string{
		"og:site_name": "m3 kereshető archívum", "og:type": "website", "og:locale": "hu_HU", "og:title": "Kuckókirály – Egy gyerekfilm",
		"og:description": "Nincs életerős férfi a faluban, hisz mind a csatában harcol.", "og:url": testPublicURL + "/m3/M3-SEO001",
		"og:image": testPublicURL + "/img/M3-SEO001.jpg", "og:image:alt": "Kuckókirály borítóképe",
		"twitter:card": "summary_large_image", "twitter:title": "Kuckókirály – Egy gyerekfilm", "twitter:image": testPublicURL + "/img/M3-SEO001.jpg",
	} {
		if got := metaContent(t, body, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if metaContent(t, body, "og:image:width") != "" {
		t.Error("the size of a cover is not known and must not be claimed")
	}

	// a program without a cover gets the site's share image, with its size, as an absolute address
	_, body = e.get(t, "/m3/M3-SEO002")
	img := metaContent(t, body, "og:image")
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(testPublicURL) + `/static/og-default\.png\?v=[0-9a-f]{10}$`).MatchString(img) {
		t.Errorf("fallback image %q", img)
	}
	if metaContent(t, body, "og:image:width") != "1200" || metaContent(t, body, "og:image:height") != "630" {
		t.Error("the share image's size is missing")
	}
	// the image is really served, and it is a PNG that a link preview can use
	path := strings.TrimPrefix(img, testPublicURL)
	resp, data := e.get(t, path)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(data, "\x89PNG") || len(data) > 300<<10 {
		t.Errorf("share image: %d %s %d bytes", resp.StatusCode, resp.Header.Get("Content-Type"), len(data))
	}
	// the home page carries the previews too
	_, home := e.get(t, "/")
	if metaContent(t, home, "og:title") == "" || metaContent(t, home, "og:image") == "" || metaContent(t, home, "og:url") != testPublicURL+"/" {
		t.Error("the home page has no link preview")
	}
}

// Absolute addresses come from the configuration only. A forged Host (or forwarding) header must not
// get into the page, the sitemap or the robots.txt: a cache would keep the forged address.
func TestAbsoluteAddressesNeverComeFromTheRequest(t *testing.T) {
	e := newEnv(t, public, seoProgram())
	hdr := map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "http", "Forwarded": "host=evil.example", "X-Original-URL": "http://evil.example/"}
	for _, path := range []string{"/", "/m3/M3-SEO001", "/sitemap.xml", "/robots.txt"} {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		req.Host = "evil.example"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || strings.Contains(sb.String(), "evil.example") {
			t.Errorf("%s: %d, the forged host is in the answer", path, resp.StatusCode)
		}
		if !strings.Contains(sb.String(), testPublicURL) {
			t.Errorf("%s: the configured address is missing", path)
		}
	}
}

func TestWithoutAPublicURLTheSiteIsPrivateToSearchEngines(t *testing.T) {
	e := newEnv(t, nil, seoProgram())
	for _, path := range []string{"/", "/m3/M3-SEO001", "/2?kereses=x"} {
		resp, body := e.get(t, path)
		if resp.StatusCode != 200 && resp.StatusCode != 303 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if resp.StatusCode == 200 {
			if got := metaContent(t, body, "robots"); got != robotsPrivate {
				t.Errorf("%s: robots %q, want %q", path, got, robotsPrivate)
			}
			if linkHref(body, "canonical") != "" || metaContent(t, body, "og:url") != "" || metaContent(t, body, "og:image") != "" {
				t.Errorf("%s: absolute addresses without a configured public URL", path)
			}
		}
	}
	_, robots := e.get(t, "/robots.txt")
	if robots != "User-agent: *\nDisallow: /\n" {
		t.Errorf("robots.txt: %q", robots)
	}
	if resp, _ := e.get(t, "/sitemap.xml"); resp.StatusCode != 404 {
		t.Errorf("sitemap without a public URL: %d", resp.StatusCode)
	}
}

func TestOnlyTheHomePageAndTheProgramsAreForTheIndex(t *testing.T) {
	var ps []model.Program
	for i := 0; i < 45; i++ {
		ps = append(ps, prog(fmt.Sprintf("M3-IDX%03d", i), fmt.Sprintf("Film %03d", i)))
	}
	e := newEnv(t, public, ps...)
	for path, want := range map[string]string{
		"/": robotsIndex, "/2": robotsNoIndex, "/3": robotsNoIndex, "/?kereses=Film": robotsNoIndex,
		"/?rendezes=cim_n": robotsNoIndex, "/?db=10": robotsNoIndex, "/?nyers": robotsNoIndex, "/m3/M3-IDX001": robotsIndex,
	} {
		_, body := e.get(t, path)
		if got := metaContent(t, body, "robots"); got != want {
			t.Errorf("%s: robots %q, want %q", path, got, want)
		}
	}
	_, home := e.get(t, "/")
	if got := linkHref(home, "canonical"); got != testPublicURL+"/" {
		t.Errorf("home canonical %q", got)
	}
	// pages that are not for the index have no canonical address
	_, page2 := e.get(t, "/2")
	if got := linkHref(page2, "canonical"); got != "" {
		t.Errorf("page 2 canonical %q", got)
	}
	if !strings.Contains(home, "<title>m3 kereshető archívum – az M3 műsorainak kereshető archívuma</title>") {
		t.Error("the home page title does not describe the site")
	}
	_, search := e.get(t, "/?kereses=Film")
	if d := metaContent(t, search, "description"); d != "45 találat a(z) „Film” keresésre az M3 műsorai között." {
		t.Errorf("search description %q", d)
	}
}

func TestGoneProgramsStayReachableButOutOfTheIndex(t *testing.T) {
	gone := prog("M3-GONE01", "Eltűnt műsor")
	gone.GoneAt = 1_700_000_000
	e := newEnv(t, public, gone, prog("M3-HERE01", "Itt van"))
	resp, body := e.get(t, "/m3/M3-GONE01")
	if resp.StatusCode != 200 || metaContent(t, body, "robots") != robotsNoIndex || !strings.Contains(body, "Eltűnt az M3-ról") {
		t.Errorf("gone program: %d robots %q", resp.StatusCode, metaContent(t, body, "robots"))
	}
	_, sm := e.get(t, "/sitemap.xml")
	if strings.Contains(sm, "M3-GONE01") || !strings.Contains(sm, "M3-HERE01") {
		t.Errorf("the sitemap must list the known programs only: %s", sm)
	}
}

func TestProgramPageAnswersOnlyForRealPrograms(t *testing.T) {
	e := newEnv(t, public, seoProgram())
	for _, path := range []string{"/m3/M3-NOPE", "/m3/x", "/m3/..", "/m3/M3-SEO001.jpg", "/m3/%00", "/m3/M3-" + strings.Repeat("A", 200), "/m3/%3Cscript%3E", "/m3/M3-SEO001/extra", "/m3/"} {
		resp, _ := e.get(t, path)
		if resp.StatusCode != 404 && resp.StatusCode != 307 && resp.StatusCode != 301 {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
		// the 404 of the program handler itself is never cached (the router's own 404 for a path with more
		// segments is the same as everywhere else on the site)
		if resp.StatusCode == 404 && !strings.Contains(strings.TrimPrefix(path, "/m3/"), "/") && path != "/m3/" && resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: a missing program must not be cached (%q)", path, resp.Header.Get("Cache-Control"))
		}
		assertSecure(t, path, resp)
	}
	// one address per program: another spelling is redirected to the canonical one
	resp, _ := e.get(t, "/m3/m3-seo001")
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/m3/M3-SEO001" {
		t.Errorf("lower case: %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := e.do(t, "POST", "/m3/M3-SEO001", nil); resp.StatusCode != 405 {
		t.Errorf("POST: %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "HEAD", "/m3/M3-SEO001", nil); resp.StatusCode != 200 {
		t.Errorf("HEAD: %d", resp.StatusCode)
	}
}

func TestEverythingInTheHeadIsEscaped(t *testing.T) {
	p := prog("M3-XSS001", `"><script>alert(1)</script><b onmouseover=x>`)
	p.Subtitle = `'"><img src=x onerror=alert(2)>`
	p.ShortDescription = `"><svg onload=alert(3)> és még sok szöveg, hogy hosszú legyen és vágni kelljen, hosszú hosszú hosszú hosszú hosszú hosszú`
	p.Creators = `<script>alert(4)</script>`
	e := newEnv(t, public, p)
	_, body := e.get(t, "/m3/M3-XSS001")
	head := body[:strings.Index(body, "</head>")]
	for _, bad := range []string{"<script", "<svg", "<img", "<b "} {
		if strings.Contains(head, bad) {
			t.Errorf("the head contains %q", bad)
		}
	}
	// every attribute value in the head is closed properly: no raw quote, "<" or ">" inside a value
	attrs := regexp.MustCompile(`(?:content|href)="([^"]*)"`).FindAllStringSubmatch(head, -1)
	if len(attrs) < 15 {
		t.Fatalf("expected the meta tags, found %d attribute values", len(attrs))
	}
	for _, a := range attrs {
		if strings.ContainsAny(a[1], "<>") {
			t.Errorf("a raw angle bracket inside an attribute value: %q", a[1])
		}
	}
	// the tags are exactly as many as expected: nothing was split by the quotes in the data
	if n := strings.Count(head, "<meta "); n != 20 {
		t.Errorf("%d meta tags, want 20: a hostile value must not add or break tags", n)
	}
	if strings.Contains(body, "<script") || strings.Contains(body, "<svg onload") || strings.Contains(body, "<img src=x") {
		t.Error("hostile markup got into the page body")
	}
}

func TestTitleAndDescriptionLengthsAreBounded(t *testing.T) {
	long := strings.Repeat("Hosszúcím ", 40)
	p := prog("M3-LONG01", long)
	p.Subtitle = long
	p.Year = 1999
	p.ShortDescription = strings.Repeat("szó ", 200)
	page, share := programTitles(p)
	if n := utf8.RuneCountInString(page); n > maxTitleRunes {
		t.Errorf("title %d runes: %s", n, page)
	}
	if !strings.HasSuffix(page, " | "+siteTitle) {
		t.Errorf("the site name must stay at the end: %s", page)
	}
	if n := utf8.RuneCountInString(share); n > maxShareTitleRunes {
		t.Errorf("share title %d runes", n)
	}
	d := programDescription(p)
	if n := utf8.RuneCountInString(d); n > maxDescriptionRunes || !strings.HasSuffix(d, "…") || strings.Contains(d, "szó…") && strings.Contains(d, "szó …") {
		t.Errorf("description %d runes: %q", n, d)
	}
	// without any text there is still a sentence, and the title's parts degrade in order
	if got := programDescription(model.Program{Title: "Csak cím", Year: 1980}); got != "Csak cím – műsor az M3 archívumából. (1980)" {
		t.Errorf("fallback description %q", got)
	}
	short := model.Program{Title: "Rövid", Subtitle: "Alcím", Year: 2001}
	if page, share := programTitles(short); page != "Rövid – Alcím (2001) | "+siteTitle || share != "Rövid – Alcím" {
		t.Errorf("%q / %q", page, share)
	}
	if got := truncate("egy kettő három négy", 12); got != "egy kettő…" {
		t.Errorf("truncate at a word boundary: %q", got)
	}
	if got := truncate("rövid", 12); got != "rövid" {
		t.Errorf("truncate must not touch short text: %q", got)
	}
}

type sitemapDoc struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
}

func TestSitemap(t *testing.T) {
	var ps []model.Program
	for i := 0; i < 30; i++ {
		ps = append(ps, prog(fmt.Sprintf("M3-MAP%03d", i), fmt.Sprintf("Térkép %03d", i)))
	}
	e := newEnv(t, public, ps...)
	resp, body := e.get(t, "/sitemap.xml")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/xml; charset=utf-8" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	assertSecure(t, "sitemap", resp)
	if resp.Header.Get("Cache-Control") != "public, max-age=3600" {
		t.Errorf("Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
	if !strings.HasPrefix(body, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Errorf("no XML declaration: %.60s", body)
	}
	var doc sitemapDoc
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("not well-formed XML: %v", err)
	}
	if !strings.Contains(body, `xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`) {
		t.Error("the sitemap namespace is missing")
	}
	if len(doc.URLs) != 31 || doc.URLs[0].Loc != testPublicURL+"/" {
		t.Fatalf("%d urls, first %q", len(doc.URLs), doc.URLs[0].Loc)
	}
	seen := map[string]bool{}
	for _, u := range doc.URLs[1:] {
		if !regexp.MustCompile(`^` + regexp.QuoteMeta(testPublicURL) + `/m3/M3-MAP\d{3}$`).MatchString(u.Loc) {
			t.Errorf("odd address %q", u.Loc)
		}
		if u.LastMod != "" && !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(u.LastMod) {
			t.Errorf("lastmod %q is not a W3C date", u.LastMod)
		}
		if seen[u.Loc] {
			t.Errorf("duplicate %s", u.Loc)
		}
		seen[u.Loc] = true
	}
	if len(body) > 50<<20 || len(doc.URLs) > sitemapMaxURLs {
		t.Error("the sitemap is over the protocol's limits")
	}
}

// robotsRule is one Allow/Disallow line, matched the way the crawlers do: "*" is any text and a
// trailing "$" anchors the end; the pattern is a prefix of the path with its query.
func robotsBlocked(rules []string, target string) bool {
	for _, pat := range rules {
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(strings.TrimSuffix(pat, "$")), `\*`, ".*")
		if strings.HasSuffix(pat, "$") {
			re += "$"
		}
		if regexp.MustCompile(re).MatchString(target) {
			return true
		}
	}
	return false
}

func TestRobotsOpensThePagesAndClosesTheEndlessVariants(t *testing.T) {
	e := newEnv(t, public, seoProgram())
	resp, body := e.get(t, "/robots.txt")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, "\nSitemap: "+testPublicURL+"/sitemap.xml\n") {
		t.Errorf("no Sitemap line:\n%s", body)
	}
	if strings.Contains(body, "Disallow: /\n") {
		t.Error("the whole site is still disallowed")
	}
	var disallow []string
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "Disallow: "); ok {
			disallow = append(disallow, v)
		}
	}
	for _, ok := range []string{"/", "/2", "/m3/M3-SEO001", "/img/M3-SEO001.jpg", "/sitemap.xml", "/robots.txt",
		// the style sheet and the script must stay open, whatever their digest looks like
		"/static/app.css?v=1adb4cim9f", "/static/search.js?v=db0ldal000", "/static/m3.min.css?v=0123456789", "/static/og-default.png?v=ffffffffff"} {
		if robotsBlocked(disallow, ok) {
			t.Errorf("%s is blocked, it must be open", ok)
		}
	}
	for _, blocked := range []string{"/?kereses=a", "/?db=10&kereses=a", "/2?rendezes=ev_cs", "/?x=1&elerheto=1", "/?nyers", "/3?nyers=", "/?oldal=2",
		"/?cim=a", "/?mufaj=Film", "/?ev_tol=1980", "/?kiemelt=1", "/?reszlet=1", "/api/", "/api/v1/programs", "/export/", "/export/m3.csv"} {
		if !robotsBlocked(disallow, blocked) {
			t.Errorf("%s is open, it must be blocked", blocked)
		}
	}
}

func TestListLinksToTheProgramPages(t *testing.T) {
	e := newEnv(t, nil, seoProgram(), prog("M3-SEO003", "Másik"))
	_, body := e.get(t, "/")
	for _, id := range []string{"M3-SEO001", "M3-SEO003"} {
		if !strings.Contains(body, `<a class="card__link" href="/m3/`+id+`">`) {
			t.Errorf("the list does not link %s to its page", id)
		}
	}
}

func TestPublicURLIsAValidatedOrigin(t *testing.T) {
	// the configuration side lives in the config package; here: the pages never print a path of it
	e := newEnv(t, func(o *Options) { o.PublicURL = "https://m3.example.test" }, seoProgram())
	_, body := e.get(t, "/m3/M3-SEO001")
	if strings.Contains(body, "https://m3.example.test//") {
		t.Error("double slash in an absolute address")
	}
}
