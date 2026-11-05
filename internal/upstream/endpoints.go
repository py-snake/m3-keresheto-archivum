package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
)

const (
	acceptJSON  = "application/json, text/plain, */*"
	acceptHTML  = "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8"
	acceptImage = "image/avif,image/webp,image/apng,image/*,*/*;q=0.8"

	// MaxImageBytes is the largest cover image accepted (the real ones are 8-41 KB).
	MaxImageBytes = 2 << 20
	// MaxSeriesPages caps the pagination of a series page.
	MaxSeriesPages = 100

	maxHTMLBytes = 4 << 20
	maxTokenLen  = 300
)

// ErrLayoutChanged means a scraped HTML page no longer looks like it used to
// (the series page yielded no program ids at all).
var ErrLayoutChanged = errors.New("upstream: page layout changed")

// DailyProgram returns the raw program items of today's program.
func (c *Client) DailyProgram(ctx context.Context) ([]json.RawMessage, error) {
	body, err := c.get(ctx, c.url("/api/m3/v3/daily_program", nil), acceptJSON, 0)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Program []json.RawMessage `json:"program"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Program == nil {
		return nil, fmt.Errorf("upstream: unexpected daily_program response shape")
	}
	return doc.Program, nil
}

// ProgramGuide returns the raw program items of the whole program guide (all days).
func (c *Client) ProgramGuide(ctx context.Context) ([]json.RawMessage, error) {
	body, err := c.get(ctx, c.url("/api/m3/v3/program_guide", url.Values{"days": {"30"}}), acceptJSON, 0)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Guides map[string][]json.RawMessage `json:"program_guides"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Guides == nil {
		return nil, fmt.Errorf("upstream: unexpected program_guide response shape")
	}
	var out []json.RawMessage
	for _, day := range doc.Guides {
		out = append(out, day...)
	}
	return out, nil
}

// CollectionIDs lists the open-archive collections.
func (c *Client) CollectionIDs(ctx context.Context) ([]string, error) {
	body, err := c.get(ctx, c.url("/m3/get-open", nil), acceptJSON, 0)
	if err != nil {
		return nil, err
	}
	var list []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("upstream: unexpected get-open response shape")
	}
	var ids []string
	for _, e := range list {
		if safeToken(e.ID) {
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}

// CollectionDoc is the part of a collection item the ingest needs.
type CollectionDoc struct {
	ID       string // program id (when it is not a series)
	SeriesID string // raw (base64) series id, empty for single programs
}

// Collection returns the items of one collection.
func (c *Client) Collection(ctx context.Context, id string) ([]CollectionDoc, error) {
	if !safeToken(id) {
		return nil, errors.New("upstream: malformed collection id")
	}
	body, err := c.get(ctx, c.url("/m3/get-open", url.Values{"collection": {id}}), acceptJSON, 0)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Docs []struct {
			ID       string `json:"id"`
			SeriesID string `json:"seriesId"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Docs == nil {
		return nil, fmt.Errorf("upstream: unexpected collection response shape")
	}
	out := make([]CollectionDoc, 0, len(doc.Docs))
	for _, d := range doc.Docs {
		cd := CollectionDoc{}
		if d.SeriesID != "" && safeToken(d.SeriesID) {
			cd.SeriesID = d.SeriesID
		} else if progid.Valid(d.ID) {
			cd.ID = d.ID
		} else {
			continue
		}
		out = append(out, cd)
	}
	return out, nil
}

// Markers of the series HTML page (scraped, so fragile: see ErrLayoutChanged).
const (
	showBgPrefix = `<div class="show-bg" style="background-image: url(https://nemzetiarchivum.hu/images/m3/`
	showBgSuffix = `)"></div>`
)

// SeriesProgramIDs returns the ids of every program of a series, following its pagination.
func (c *Client) SeriesProgramIDs(ctx context.Context, seriesID string) ([]string, error) {
	if !safeToken(seriesID) {
		return nil, errors.New("upstream: malformed series id")
	}
	var ids []string
	seen := map[string]bool{}
	pages := 1
	for page := 1; page <= pages && page <= MaxSeriesPages; page++ {
		q := url.Values{"series": {seriesID}, "page": {strconv.Itoa(page)}}
		body, err := c.get(ctx, c.url("/m3/open", q), acceptHTML, maxHTMLBytes)
		if err != nil {
			return ids, err
		}
		if page == 1 {
			// the pager links look like  open?series=<urlencoded>&page=N
			for _, n := range extractBetween(body, "open?series="+url.QueryEscape(seriesID)+"&page=", `"`) {
				if v, err := strconv.Atoi(n); err == nil && v > pages && v <= MaxSeriesPages {
					pages = v
				}
			}
		}
		found := extractBetween(body, showBgPrefix, showBgSuffix)
		if page == 1 && len(found) == 0 {
			return nil, ErrLayoutChanged
		}
		for _, id := range found {
			if progid.Valid(id) && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// Item returns the raw details of one program. ErrNotFound when the upstream answers `null`.
func (c *Client) Item(ctx context.Context, id string) (json.RawMessage, error) {
	if !progid.Valid(id) {
		return nil, errors.New("upstream: malformed program id")
	}
	body, err := c.get(ctx, c.url("/api/m3/v3/item", url.Values{"id": {id}}), acceptJSON, 0)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	if string(trimmed) == "null" || string(trimmed) == "false" || len(trimmed) == 0 {
		return nil, ErrNotFound
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("upstream: unexpected item response")
	}
	return json.RawMessage(trimmed), nil
}

// Image downloads the raw cover image bytes of a program. The caller must validate them:
// the upstream answers HTTP 200 with a small HTML page for unknown ids.
func (c *Client) Image(ctx context.Context, id string) ([]byte, error) {
	if !progid.Valid(id) {
		return nil, errors.New("upstream: malformed program id")
	}
	u := c.url("/images/m3/"+id, nil)
	return c.get(ctx, u, acceptImage, MaxImageBytes)
}

// extractBetween returns every substring found between prefix and suffix.
// A prefix without a closing suffix ends the scan quietly.
func extractBetween(s []byte, prefix, suffix string) []string {
	var out []string
	pre, suf := []byte(prefix), []byte(suffix)
	for {
		i := bytes.Index(s, pre)
		if i < 0 {
			return out
		}
		s = s[i+len(pre):]
		j := bytes.Index(s, suf)
		if j < 0 {
			return out
		}
		out = append(out, string(s[:j]))
		s = s[j+len(suf):]
	}
}

// safeToken accepts the opaque ids the upstream hands out (base64-like) and nothing
// that could change the meaning of a URL.
func safeToken(s string) bool {
	if s == "" || len(s) > maxTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '=', c == '+', c == '/':
		default:
			return false
		}
	}
	return true
}
