package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata" // the scratch image has no zoneinfo

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	webfs "github.com/py-snake/m3-keresheto-archivum/web"
)

// Paging.
const (
	PageSize = 10
	// MaxPage bounds how deep a visitor can page (10 000 results).
	MaxPage = 1000
)

var budapest = mustLocation("Europe/Budapest")

func mustLocation(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return l
}

type kv struct{ Key, Value string }

type pagerItem struct {
	Label   string
	Href    string
	Rel     string
	Current bool
}

type pageData struct {
	Title  string
	Search string
	Raw    bool
	Notice string

	OnlyPlayable bool
	Items        []model.Program
	Total        int
	Capped       bool
	Pager        []pagerItem
	Today        time.Time
}

// parseTemplates compiles the embedded templates. Only html/template is used and nothing is
// ever marked as trusted HTML, so every value is escaped for the context it appears in.
func parseTemplates() (*template.Template, error) {
	sub, err := fs.Sub(webfs.FS, "templates")
	if err != nil {
		return nil, err
	}
	return template.New("").Funcs(template.FuncMap{
		"lines":      splitLines,
		"duration":   durationLabel,
		"futureDate": futureDate,
		"details":    details,
		"rawColumns": func() []string { return rawColumns },
		"rawRow":     rawRow,
		"checked":    checkedLabel,
	}).ParseFS(sub, "*.html")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// durationLabel renders HH:MM:SS:FF (FF = frames) as "1ó 7p" / "27p" / "30mp".
func durationLabel(d string) string {
	var n [3]int
	for i, part := range strings.SplitN(d, ":", 4) {
		if i > 2 {
			break
		}
		n[i], _ = strconv.Atoi(part)
	}
	h, m, s := n[0], n[1], n[2]
	switch {
	case h > 0:
		return fmt.Sprintf("%dó %dp", h, m)
	case m > 0:
		return fmt.Sprintf("%dp", m)
	case s > 0:
		return fmt.Sprintf("%dmp", s)
	}
	return ""
}

// futureDate returns the release date when it lies after today (Budapest time), else "".
func futureDate(p model.Program, today time.Time) string {
	if p.ReleasedAt <= 0 {
		return ""
	}
	r := time.Unix(p.ReleasedAt, 0).In(budapest)
	t := today.In(budapest)
	ry, rm, rd := r.Date()
	ty, tm, td := t.Date()
	if time.Date(ry, rm, rd, 0, 0, 0, 0, time.UTC).After(time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC)) {
		return r.Format("2006.01.02.")
	}
	return ""
}

var rawColumns = []string{
	"program_id", "title", "subtitle", "info", "extended_info", "short_description", "description", "company",
	"year", "country", "creators", "contributors", "genre", "quality", "pg", "duration", "ratio",
	"hasSubtitle", "isSeries", "seriesId", "episode", "episodes", "added", "released",
	"playable", "playableChecked", "playableChanged", "windowFirst", "windowLast", "gone",
}

func fmtTime(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).In(budapest).Format("2006-01-02 15:04:05")
}

func rawRow(p model.Program) []string {
	return []string{
		p.ProgramID, p.Title, p.Subtitle, p.Info, p.ExtendedInfo, semi(p.ShortDescription), semi(p.Description), p.Company,
		strconv.Itoa(p.Year), p.Country, p.Creators, p.Contributors, p.Genre, p.Quality, p.PG, p.Duration, p.Ratio,
		b2s(p.HasSubtitle), b2s(p.IsSeries), p.SeriesID, strconv.Itoa(p.Episode), strconv.Itoa(p.Episodes),
		fmtTime(p.AddedAt), fmtTime(p.ReleasedAt),
		playableText(p), fmtTime(p.PlayableCheckedAt), fmtTime(p.PlayableChangedAt),
		fmtTime(p.WindowFirst), fmtTime(p.WindowLast), fmtTime(p.GoneAt),
	}
}

// playableText is "" while the availability was never observed.
func playableText(p model.Program) string {
	switch {
	case !p.PlayableKnown:
		return ""
	case p.Playable:
		return "1"
	}
	return "0"
}

// checkedLabel explains how fresh the availability information is, for the badge tooltip.
func checkedLabel(p model.Program) string {
	if p.GoneAt > 0 {
		return "Az M3 már nem ismeri ezt a műsort (" + fmtTime(p.GoneAt) + " óta). Az adatai itt megmaradtak."
	}
	if !p.PlayableKnown {
		return ""
	}
	s := "Utoljára ellenőrizve: " + fmtTime(p.PlayableCheckedAt) + ". Az elérhetőség azóta változhatott."
	if p.WindowLast > 0 {
		s += " Utolsó ismert lejátszható nap: " + time.Unix(p.WindowLast, 0).In(budapest).Format("2006-01-02") + "."
	}
	return s
}

// details lists every non-empty field of a program, like the PHP version did.
func details(p model.Program) []kv {
	row := rawRow(p)
	var out []kv
	for i, name := range rawColumns {
		v := strings.TrimSpace(row[i])
		if v == "" || v == "0" && (name == "year" || name == "episode" || name == "episodes" || name == "hasSubtitle" || name == "isSeries") {
			continue
		}
		out = append(out, kv{Key: name, Value: v})
	}
	return out
}

// semi puts a space after semicolons in the long descriptions (they are ';' separated lists).
func semi(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "; ", ";"), ";", "; ")
}

// buildPager returns the page links around the current page. Links keep the search term.
func buildPager(current, total int, search string, raw, onlyPlayable bool) []pagerItem {
	pages := (total + PageSize - 1) / PageSize
	pages = min(pages, MaxPage)
	if pages <= 1 {
		return nil
	}
	href := func(page int) string {
		q := url.Values{}
		if search != "" {
			q.Set("kereses", search)
		}
		path := "/"
		if page > 1 {
			path = "/" + strconv.Itoa(page)
		}
		if onlyPlayable {
			q.Set("elerheto", "1")
		}
		s := q.Encode()
		if raw {
			if s != "" {
				s += "&"
			}
			s += "nyers"
		}
		if s != "" {
			path += "?" + s
		}
		return path
	}

	var items []pagerItem
	add := func(label string, page int, rel string) {
		items = append(items, pagerItem{Label: label, Href: href(page), Rel: rel})
	}
	if current > 1 {
		add("«", 1, "start")
		add("‹", current-1, "prev")
	}
	for p := max(1, current-4); p <= min(pages, current+4); p++ {
		if p == current {
			items = append(items, pagerItem{Label: strconv.Itoa(p), Current: true})
		} else {
			add(strconv.Itoa(p), p, "")
		}
	}
	if current < pages {
		add("›", current+1, "next")
		add("»", pages, "")
	}
	return items
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
