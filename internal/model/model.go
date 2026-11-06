// Package model holds the data types shared by the upstream parser, the store and the web layer.
package model

// Image states of a program (the image_state column).
const (
	ImagePending = 0 // no image yet; the ingest tries again
	ImageStored  = 1 // images/<program_id>.jpg exists
	ImageMissing = 2 // the upstream has no image for this program
)

// Program is one M3 program. All text is already sanitized.
type Program struct {
	ID               int64 // database row id (0 before insertion)
	ProgramID        string
	Title            string
	Subtitle         string
	Info             string
	ExtendedInfo     string
	Description      string
	ShortDescription string
	Company          string
	Year             int
	Country          string
	Creators         string
	Contributors     string
	Genre            string
	Quality          string
	PG               string
	Ratio            string
	Duration         string // HH:MM:SS:FF, the last part is frames
	HasSubtitle      bool
	IsSeries         bool
	SeriesID         string // base64-decoded, as the PHP version stored it
	Episode          int
	Episodes         int
	AddedAt          int64 // unix seconds (UTC), set by the store
	ReleasedAt       int64 // unix seconds (UTC), 0 = unknown
	ImageState       int

	// Availability: the LAST OBSERVED state, unlike the descriptive fields (first seen).
	PlayableKnown     bool  // false = never observed
	Playable          bool  // meaningful only when PlayableKnown
	PlayableCheckedAt int64 // unix seconds: when the value was last observed (0 = never)
	PlayableChangedAt int64 // unix seconds: when the value last changed
	WindowFirst       int64 // earliest / latest date of the upstream's playable dates (0 = none)
	WindowLast        int64
	GoneAt            int64 // unix seconds: the upstream stopped knowing this program (0 = still known)
}
