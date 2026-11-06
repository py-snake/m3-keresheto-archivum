// Package dto is the public JSON representation of a program. The REST API and the JSONL
// download use the very same type, so what the API returns and what the dump contains cannot drift apart.
package dto

import (
	"strings"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
)

// M3PageURL is the page of a program on the M3 site.
const M3PageURL = "https://nemzetiarchivum.hu/m3/"

// Program is the JSON form of a program.
type Program struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Subtitle         string   `json:"subtitle"`
	Description      string   `json:"description"`
	ShortDescription string   `json:"short_description"`
	Info             []string `json:"info"`
	ExtendedInfo     []string `json:"extended_info"`
	Company          string   `json:"company"`
	Year             int      `json:"year"`
	Country          string   `json:"country"`
	Creators         []string `json:"creators"`
	Contributors     []string `json:"contributors"`
	Genre            []string `json:"genre"`
	Quality          string   `json:"quality"`
	PG               string   `json:"pg"`
	Ratio            string   `json:"ratio"`
	// Duration is HH:MM:SS:FF, the last part counts frames.
	Duration    string `json:"duration"`
	HasSubtitle bool   `json:"has_subtitle"`
	IsSeries    bool   `json:"is_series"`
	// Series is the name of the series (empty when the program is not part of one).
	Series   string `json:"series"`
	Episode  int    `json:"episode"`
	Episodes int    `json:"episodes"`
	// AddedAt is when this archive first saw the program; ReleasedAt is the earliest date the
	// upstream gives for it (null = unknown).
	AddedAt    time.Time  `json:"added_at"`
	ReleasedAt *time.Time `json:"released_at"`
	// HasImage tells whether this archive stores a cover image. ImageURL is set by the API only
	// (a path on the same host).
	// More fields of the upstream, last observed. Rank, Collection, CollectionID and Catchup are always
	// empty at the moment: the upstream does not use them yet.
	Highlighted  bool       `json:"highlighted"`
	Live         bool       `json:"live"`
	Catchup      bool       `json:"catchup"`
	Rank         int        `json:"rank"`
	Collection   []string   `json:"collection"`
	CollectionID string     `json:"collection_id"`
	AiredFirst   *time.Time `json:"aired_first"`
	AiredLast    *time.Time `json:"aired_last"`
	// RefreshedAt is when the descriptive fields were last updated from the upstream (null = never:
	// they hold the first seen state).
	RefreshedAt *time.Time `json:"refreshed_at"`

	HasImage     bool         `json:"has_image"`
	ImageURL     *string      `json:"image_url,omitempty"`
	M3URL        string       `json:"m3_url"`
	Availability Availability `json:"availability"`
}

// Availability is the LAST OBSERVED playability of a program; checked_at says how fresh it is.
type Availability struct {
	// Playable is null until the availability has been observed.
	Playable    *bool      `json:"playable"`
	CheckedAt   *time.Time `json:"checked_at"`
	ChangedAt   *time.Time `json:"changed_at"`
	WindowFirst *time.Time `json:"window_first"`
	WindowLast  *time.Time `json:"window_last"`
	// Gone is true when the upstream no longer knows the program (the data here is kept).
	Gone   bool       `json:"gone"`
	GoneAt *time.Time `json:"gone_at"`
	// History lists the changes of the playable flag, oldest first. The single program endpoint fills
	// it; lists and the downloads leave it out.
	History []Change `json:"history,omitempty"`
}

// Change is one change of the playable flag.
type Change struct {
	At       time.Time `json:"at"`
	Playable bool      `json:"playable"`
}

// FromModel converts a stored program. imageURL is the cover image path to publish ("" = none).
func FromModel(p model.Program, imageURL string) Program {
	out := Program{
		ID:               p.ProgramID,
		Title:            p.Title,
		Subtitle:         p.Subtitle,
		Description:      p.Description,
		ShortDescription: p.ShortDescription,
		Info:             Lines(p.Info),
		ExtendedInfo:     Lines(p.ExtendedInfo),
		Company:          p.Company,
		Year:             p.Year,
		Country:          p.Country,
		Creators:         Lines(p.Creators),
		Contributors:     Lines(p.Contributors),
		Genre:            Lines(p.Genre),
		Quality:          p.Quality,
		PG:               p.PG,
		Ratio:            p.Ratio,
		Duration:         p.Duration,
		HasSubtitle:      p.HasSubtitle,
		IsSeries:         p.IsSeries,
		Series:           p.SeriesID,
		Episode:          p.Episode,
		Episodes:         p.Episodes,
		AddedAt:          time.Unix(p.AddedAt, 0).UTC(),
		ReleasedAt:       Time(p.ReleasedAt),
		Highlighted:      p.IsHighlighted,
		Live:             p.IsLive,
		Catchup:          p.IsCatchup,
		Rank:             p.Rank,
		Collection:       Lines(p.Collection),
		CollectionID:     p.CollectionID,
		AiredFirst:       Time(p.AiredFirst),
		AiredLast:        Time(p.AiredLast),
		RefreshedAt:      Time(p.RefreshedAt),
		HasImage:         p.ImageState == model.ImageStored,
		M3URL:            M3PageURL + p.ProgramID,
		Availability: Availability{
			CheckedAt:   Time(p.PlayableCheckedAt),
			ChangedAt:   Time(p.PlayableChangedAt),
			WindowFirst: Time(p.WindowFirst),
			WindowLast:  Time(p.WindowLast),
			Gone:        p.GoneAt > 0,
			GoneAt:      Time(p.GoneAt),
		},
	}
	if p.PlayableKnown {
		v := p.Playable
		out.Availability.Playable = &v
	}
	if imageURL != "" && out.HasImage {
		out.ImageURL = &imageURL
	}
	return out
}

// Lines splits a newline separated stored field into a list (never nil, so JSON shows []).
func Lines(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}

// Time converts unix seconds to a UTC time, or nil for 0.
func Time(unix int64) *time.Time {
	if unix <= 0 {
		return nil
	}
	t := time.Unix(unix, 0).UTC()
	return &t
}
