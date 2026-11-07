package web

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
)

// Everything in this file is about how the site shows up outside of itself: in search engines (title,
// description, robots, canonical address, sitemap) and in link previews (Open Graph and Twitter card:
// Signal, Messenger, WhatsApp, Telegram, ...).
//
// Absolute addresses are built only from Options.PublicURL, which the operator sets. They are never
// built from the request (the Host header can be forged, and a cache would then keep the forged
// address). Without a PublicURL the site asks search engines not to index it, has no sitemap, and the
// previews carry no image and no address.

const (
	robotsIndex   = "index,follow,max-image-preview:large"
	robotsNoIndex = "noindex,follow"
	robotsPrivate = "noindex,nofollow"

	siteDescription = "Az M3 (Nemzeti Archívum) műsorainak kereshető archívuma: keresés cím, leírás, készítők, műfaj, év és elérhetőség szerint."

	maxTitleRunes       = 70
	maxDescriptionRunes = 155
	maxShareTitleRunes  = 100

	// sitemapMaxURLs is the limit of one sitemap file (sitemaps.org). The home page counts as one.
	sitemapMaxURLs = 50000

	defaultShareImage = "og-default.png"
	defaultShareW     = 1200
	defaultShareH     = 630
)

// pageMeta is everything the head of a page says about the page.
type pageMeta struct {
	Title       string // <title>
	Description string
	Robots      string
	Canonical   string // absolute address of the page, "" = none
	ShareTitle  string // og:title / twitter:title
	Image       string // absolute address of the preview image, "" = none
	ImageAlt    string
	ImageW      int // 0 = unknown
	ImageH      int
}

// absolute turns a path of this site into an absolute address, or "" when no PublicURL is set.
func (s *Server) absolute(path string) string {
	if s.o.PublicURL == "" {
		return ""
	}
	return s.o.PublicURL + path
}

// assetPath is the /static/ path of an embedded file with its content digest (see staticVersions).
func (s *Server) assetPath(name string) string {
	if v := s.versions[name]; v != "" {
		return "/static/" + name + "?v=" + v
	}
	return "/static/" + name
}

// siteMeta is the head of the pages that are about the site as a whole. robots is the indexing
// advice for the page; it is overridden by "noindex,nofollow" while no PublicURL is set.
func (s *Server) siteMeta(title, description, robots, path string) pageMeta {
	m := pageMeta{
		Title: title, Description: description, Robots: robots, ShareTitle: truncate(title, maxShareTitleRunes),
		Image: s.absolute(s.assetPath(defaultShareImage)), ImageAlt: siteTitle, ImageW: defaultShareW, ImageH: defaultShareH,
	}
	if path != "" {
		m.Canonical = s.absolute(path)
	}
	if s.o.PublicURL == "" {
		m.Robots = robotsPrivate
	}
	return m
}

// indexMeta is the head of the home page, the other pages of the list and the search results. Only the
// bare home page is for the index: the pages /2, /3, ... of the list and every search or filter variant
// would be endless thin copies of the program pages, so they are marked "noindex" (their links are
// still followed, so the crawler finds the program pages through them).
func (s *Server) indexMeta(r *http.Request, d pageData, status int) pageMeta {
	home := r.PathValue("page") == "" && r.URL.RawQuery == ""
	title, desc, robots, canonical := d.Title, siteDescription, robotsNoIndex, ""
	if home {
		title = siteTitle + " – az M3 műsorainak kereshető archívuma"
		if status == http.StatusOK {
			robots, canonical = robotsIndex, "/"
		}
	}
	if d.F.Term != "" && d.Total > 0 {
		desc = fmt.Sprintf("%d találat a(z) „%s” keresésre az M3 műsorai között.", d.Total, d.F.Term)
	}
	return s.siteMeta(title, desc, robots, canonical)
}

// programMeta is the head of the page of one program.
func (s *Server) programMeta(p model.Program) pageMeta {
	pageTitle, shareTitle := programTitles(p)
	m := pageMeta{
		Title: pageTitle, Description: programDescription(p), Robots: robotsIndex,
		Canonical: s.absolute("/m3/" + p.ProgramID), ShareTitle: shareTitle,
		Image: s.absolute(s.assetPath(defaultShareImage)), ImageAlt: siteTitle, ImageW: defaultShareW, ImageH: defaultShareH,
	}
	if p.ImageState == model.ImageStored {
		m.Image, m.ImageAlt, m.ImageW, m.ImageH = s.absolute("/img/"+p.ProgramID+".jpg"), p.Title+" borítóképe", 0, 0
	}
	if p.GoneAt != 0 { // no longer on the M3: keep the page for the visitors that have the link, but out of the index
		m.Robots = robotsNoIndex
	}
	if s.o.PublicURL == "" {
		m.Robots = robotsPrivate
	}
	return m
}

// programTitles returns the <title> of a program's page and the shorter title for link previews.
func programTitles(p model.Program) (page, share string) {
	suffix := " | " + siteTitle
	year := ""
	if p.Year > 0 {
		year = " (" + strconv.Itoa(p.Year) + ")"
	}
	title := clean(p.Title)
	base := title
	if sub := clean(p.Subtitle); sub != "" {
		base += " – " + sub
	}
	page = base + year + suffix
	if utf8.RuneCountInString(page) > maxTitleRunes {
		page = title + year + suffix
	}
	if utf8.RuneCountInString(page) > maxTitleRunes {
		page = truncate(title, maxTitleRunes-utf8.RuneCountInString(suffix)) + suffix
	}
	return page, truncate(base, maxShareTitleRunes)
}

// programDescription is the meta description of a program's page: the short description, else the long
// one, else a plain sentence; one line, cut at a word boundary.
func programDescription(p model.Program) string {
	text := clean(p.ShortDescription)
	if text == "" {
		text = clean(p.Description)
	}
	if text == "" {
		text = clean(p.Title) + " – műsor az M3 archívumából."
		if p.Year > 0 {
			text += " (" + strconv.Itoa(p.Year) + ")"
		}
	}
	return truncate(text, maxDescriptionRunes)
}

// clean collapses every run of white space (new lines included) into one space.
func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncate cuts s to at most max runes at a word boundary and marks the cut with an ellipsis.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)[:max-1]
	cut := len(r)
	for i := len(r) - 1; i > len(r)-40 && i > 0; i-- { // back up to the last space, but not too far
		if r[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(r[:cut]), " ,;:.-–—") + "…"
}

// programFacts are the short key facts shown at the top of a program's page.
func programFacts(p model.Program) []kv {
	var out []kv
	add := func(k, v string) {
		if v = clean(v); v != "" {
			out = append(out, kv{k, v})
		}
	}
	if p.Year > 0 {
		add("Év", strconv.Itoa(p.Year))
	}
	add("Ország", p.Country)
	add("Műfaj", p.Genre)
	add("Hossz", durationLabel(p.Duration))
	add("Gyártó", p.Company)
	add("Korhatár", p.PG)
	add("Minőség", p.Quality)
	return out
}

// ---- robots.txt ----

// robots tells crawlers what they may fetch. While no PublicURL is set the site is private as far as
// crawlers are concerned. With one, the program pages, the home page and the cover images are open;
// the search and filter variants (endless combinations), the API and the downloads are not. The query
// patterns name the parameters one by one: a blanket "Disallow: /*?" would also block the style sheet
// and the script (/static/app.css?v=...), which crawlers need to render the pages.
func (s *Server) robots(w http.ResponseWriter, _ *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	if s.o.PublicURL == "" {
		b.WriteString("Disallow: /\n")
	} else {
		b.WriteString("Allow: /\n")
		for _, p := range []string{"/api/", "/export/"} {
			b.WriteString("Disallow: " + p + "\n")
		}
		// every parameter at its exact place: right after the "?" or an "&", followed by its "=" (the
		// bare flag "nyers" has none), so that nothing else in a query, such as the digest in
		// /static/app.css?v=..., can match by accident
		for _, param := range []string{"kereses=", "cim=", "alcim=", "leiras=", "keszitok=", "kozremukodok=", "mufaj=", "ev_tol=", "ev_ig=",
			"elerheto=", "kiemelt=", "rendezes=", "db=", "oldal=", "nyers", "reszlet="} {
			b.WriteString("Disallow: /*?" + param + "\n")
			b.WriteString("Disallow: /*&" + param + "\n")
		}
		b.WriteString("\nSitemap: " + s.absolute("/sitemap.xml") + "\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(b.String()))
}

// ---- sitemap.xml ----

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type sitemapSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// sitemap lists the home page and the page of every program that the M3 still knows. It is only
// served when a PublicURL is set (the addresses in it are absolute).
func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	reader := s.o.Source.Reader()
	if reader == nil {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Az adatbázis még nem készült el.", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	entries, err := reader.SitemapEntries(ctx, sitemapMaxURLs-1)
	if err != nil {
		s.o.Log.Error("building the sitemap failed", "err", err)
		http.Error(w, "Belső hiba.", http.StatusInternalServerError)
		return
	}
	set := sitemapSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9", URLs: make([]sitemapURL, 0, len(entries)+1)}
	set.URLs = append(set.URLs, sitemapURL{Loc: s.absolute("/")})
	for _, e := range entries {
		if !progid.Valid(e.ID) {
			continue
		}
		u := sitemapURL{Loc: s.absolute("/m3/" + e.ID)}
		if e.Modified > 0 {
			u.LastMod = time.Unix(e.Modified, 0).UTC().Format("2006-01-02")
		}
		set.URLs = append(set.URLs, u)
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", " ")
	if err := enc.Encode(set); err != nil {
		s.o.Log.Error("encoding the sitemap failed", "err", err)
		http.Error(w, "Belső hiba.", http.StatusInternalServerError)
		return
	}
	buf.WriteByte('\n')
	h := w.Header()
	h.Set("Content-Type", "application/xml; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(buf.Bytes())
}

// ---- the page of one program ----

// program shows everything the archive knows about one program on a page of its own, so that a search
// engine (and a link preview) has something to point at.
func (s *Server) program(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("id")
	id := strings.ToUpper(raw)
	if !progid.Valid(id) {
		s.notFound(w, r)
		return
	}
	if id != raw { // one address per program
		http.Redirect(w, r, "/m3/"+id, http.StatusMovedPermanently)
		return
	}
	reader := s.o.Source.Reader()
	if reader == nil {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Az adatbázis még nem készült el, kérlek próbáld később.", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	p, found, err := reader.ProgramByID(ctx, id)
	if err != nil {
		s.o.Log.Error("loading a program failed", "err", err)
		http.Error(w, "Belső hiba.", http.StatusInternalServerError)
		return
	}
	if !found {
		s.notFound(w, r)
		return
	}
	data := pageData{
		Title: p.Title + " - " + siteTitle, ProgramPage: true, Program: &p, Today: s.now(),
		ShowAPI: s.o.PublicAPI, ShowExport: s.o.PublicExport, Meta: s.programMeta(p),
		Facts: programFacts(p),
	}
	if hist, err := reader.History(ctx, []string{id}); err == nil {
		data.History = hist
	} else {
		s.o.Log.Error("loading the availability history failed", "err", err)
	}
	var buf bytes.Buffer
	if err := s.pages["program"].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.o.Log.Error("rendering failed", "err", err)
		http.Error(w, "Belső hiba.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(buf.Bytes())
}

// notFound answers 404 and keeps the answer out of every cache: the program may arrive a minute later.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
}
