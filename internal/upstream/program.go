package upstream

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
)

// Program is the stored form of an M3 program.
type Program = model.Program

// Column limits, in characters. They match the SQL CHECK constraints.
const (
	maxTitle     = 255
	maxSubtitle  = 255
	maxCompany   = 255
	maxCountry   = 3
	maxShortText = 15 // quality, pg, ratio, duration
	maxSeriesID  = 255
	maxBigText   = 20000 // description and the list fields
)

// ErrInvalidItem marks an upstream item that cannot be stored.
var ErrInvalidItem = errors.New("invalid upstream item")

// ParseProgram converts one upstream JSON object into a Program. The second result tells
// whether any value had to be truncated.
func ParseProgram(raw []byte) (Program, bool, error) {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(raw, &f); err != nil || f == nil {
		// covers `null` (upstream answer for unknown ids), arrays, scalars and broken JSON
		return Program{}, false, fmt.Errorf("%w: not a JSON object", ErrInvalidItem)
	}

	var trunc bool
	text := func(key string, max int) string {
		s, t := clean(jsonText(f[key]), max)
		trunc = trunc || t
		return s
	}
	lines := func(key string) string {
		s, t := clean(jsonLines(f[key]), maxBigText)
		trunc = trunc || t
		return s
	}

	p := Program{
		ProgramID:        strings.TrimSpace(jsonText(f["id"])),
		Title:            text("title", maxTitle),
		Subtitle:         text("subtitle", maxSubtitle), // the upstream sends `false` when there is none
		Info:             lines("info"),
		ExtendedInfo:     lines("extended_info"),
		Description:      text("description", maxBigText),
		ShortDescription: text("short_description", maxBigText),
		Company:          text("company", maxCompany),
		Year:             jsonInt(f["year"], 0, 2100),
		Country:          text("country", maxCountry),
		Creators:         lines("creators"),
		Contributors:     lines("contributors"),
		Genre:            lines("genre"),
		Quality:          text("quality", maxShortText),
		PG:               text("pg", maxShortText),
		Ratio:            text("ratio", maxShortText),
		Duration:         text("duration", maxShortText),
		HasSubtitle:      jsonBool(f["hasSubtitle"]),
		IsSeries:         jsonBool(f["isSeries"]),
		Episode:          jsonInt(f["episode"], 0, 100000),
		Episodes:         jsonInt(f["episodes"], 0, 100000),
		ReleasedAt:       earliest(f["start_playable_dts"], f["start_startTime_dts"]),
	}
	p.WindowFirst, p.WindowLast = bounds(f["start_playable_dts"])
	p.AiredFirst, p.AiredLast = bounds(f["start_startTime_dts"])
	p.IsHighlighted = jsonBool(f["isHighlighted_b"])
	p.IsLive = jsonBool(f["is_live"])
	p.IsCatchup = jsonBool(f["isBroadcastCatchup_b"])
	p.Rank = jsonInt(f["rank"], 0, 1_000_000_000)
	p.Collection = lines("collection")
	p.CollectionID = text("collectionId", maxSeriesID)
	p.PlayableKnown, p.Playable = jsonStrictBool(f["playable"])

	if !progid.Valid(p.ProgramID) {
		return Program{}, false, fmt.Errorf("%w: malformed program id", ErrInvalidItem)
	}
	if p.Title == "" {
		return Program{}, false, fmt.Errorf("%w: %s has no title", ErrInvalidItem, p.ProgramID)
	}

	sid, t := clean(decodeSeriesID(jsonText(f["seriesId"])), maxSeriesID)
	p.SeriesID = sid
	trunc = trunc || t

	return p, trunc, nil
}

// Report summarizes a batch parse; the ingest uses it as a circuit breaker.
type Report struct {
	Total     int
	Invalid   int
	Truncated int
	Samples   []string // a few error messages, for the log
}

// ParsePrograms parses a batch, skipping (and counting) invalid items.
func ParsePrograms(items []json.RawMessage) ([]Program, Report) {
	var rep Report
	out := make([]Program, 0, len(items))
	for _, it := range items {
		rep.Total++
		p, trunc, err := ParseProgram(it)
		if err != nil {
			rep.Invalid++
			if len(rep.Samples) < 5 {
				rep.Samples = append(rep.Samples, err.Error())
			}
			continue
		}
		if trunc {
			rep.Truncated++
		}
		out = append(out, p)
	}
	return out, rep
}

// clean makes untrusted text safe to store and show: valid UTF-8, no control or
// bidirectional-override characters, trimmed, at most max characters.
// The second result reports a truncation.
func clean(s string, max int) (string, bool) {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return -1
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		case r >= 0x202a && r <= 0x202e, // bidi embeddings and overrides
			r >= 0x2066 && r <= 0x2069,            // bidi isolates
			r == 0x061c, r == 0x200e, r == 0x200f, // bidi marks
			r == 0x200b, r == 0xfeff, r == 0xfffd, // zero width space / BOM / replacement character
			r == 0x2028, r == 0x2029: // line / paragraph separators
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)

	if utf8.RuneCountInString(s) > max {
		return strings.TrimSpace(string([]rune(s)[:max])), true
	}
	return s, false
}

// jsonText returns strings and numbers as text. null, booleans, arrays and objects give "".
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

// jsonLines joins the scalar elements of a JSON array with newlines.
func jsonLines(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, e := range arr {
		if s := strings.TrimSpace(jsonText(e)); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

func jsonInt(raw json.RawMessage, min, max int) int {
	s := strings.TrimSpace(jsonText(raw))
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < float64(min) || f > float64(max) {
		return min
	}
	return int(f)
}

func jsonBool(raw json.RawMessage) bool {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x == "1" || strings.EqualFold(x, "true")
	}
	return false
}

// jsonStrictBool reports whether the value is a real JSON boolean, and its value.
func jsonStrictBool(raw json.RawMessage) (known, value bool) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return false, false // json.Unmarshal would turn null into a silent false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, false
	}
	return true, b
}

// bounds returns the earliest and the latest valid RFC 3339 timestamp (unix seconds) of a JSON
// string array, or 0, 0.
func bounds(raw json.RawMessage) (first, last int64) {
	var dts []string
	if len(raw) == 0 || json.Unmarshal(raw, &dts) != nil {
		return 0, 0
	}
	for _, s := range dts {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil || t.Unix() <= 0 {
			continue
		}
		u := t.Unix()
		if first == 0 || u < first {
			first = u
		}
		if u > last {
			last = u
		}
	}
	return first, last
}

// earliest returns the smallest valid RFC 3339 timestamp (unix seconds) found in the given
// JSON string arrays, or 0 when there is none.
func earliest(lists ...json.RawMessage) int64 {
	var best int64
	for _, raw := range lists {
		var dts []string
		if len(raw) == 0 || json.Unmarshal(raw, &dts) != nil {
			continue
		}
		for _, s := range dts {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				continue
			}
			if u := t.Unix(); u > 0 && (best == 0 || u < best) {
				best = u
			}
		}
	}
	return best
}

// decodeSeriesID turns the base64 series id into text; undecodable input gives "".
func decodeSeriesID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b)
		}
	}
	return ""
}
