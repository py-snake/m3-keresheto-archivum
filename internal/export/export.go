// Package export writes the downloadable copies of the archive into <data>/export:
//
//	m3-archive.sqlite.gz   the published SQLite database, gzip compressed (a complete, usable backup)
//	m3-programs.csv.gz     every program as CSV (formula-injection safe, see safeCell)
//	m3-programs.jsonl.gz   every program as one JSON object per line (same shape as the REST API)
//	schema.sql             the schema of the database with a description of the columns
//	SHA256SUMS             checksums of the four files above
//	manifest.json          what was generated and when (written last)
//
// The job holds the same lock as the ingest, so nothing is published while it reads. Every file is
// written to a temporary name, flushed and renamed, so a download never sees a half written file.
package export

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/dto"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
)

// File names inside the export directory. They are fixed: the web server serves exactly these.
const (
	ArchiveName  = "m3-archive.sqlite.gz"
	CSVName      = "m3-programs.csv.gz"
	JSONLName    = "m3-programs.jsonl.gz"
	SchemaName   = "schema.sql"
	SumsName     = "SHA256SUMS"
	ManifestName = "manifest.json"
)

// Notice is part of the manifest and shown on the download page.
const Notice = "Az adatok az M3 (Nemzeti Archívum) nyilvános felületéről származnak; az újrafelhasználás feltételeit az M3, " +
	"illetve a Nemzeti Archívum határozza meg. A program forráskódja AGPL-3.0 licencű. / The data originates from the public " +
	"M3 site of the Nemzeti Archivum; the terms of reuse are set by them. The program code is AGPL-3.0."

// File describes one generated file.
type File struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	Description string `json:"description"`
}

// Manifest describes a whole export.
type Manifest struct {
	GeneratedAt   time.Time `json:"generated_at"`
	SchemaVersion int       `json:"schema_version"`
	Programs      int       `json:"programs"`
	Source        string    `json:"source"`
	Notice        string    `json:"notice"`
	Files         []File    `json:"files"`
}

// csvHeader is the column list of the CSV file.
var csvHeader = []string{
	"id", "title", "subtitle", "description", "short_description", "info", "extended_info", "company", "year", "country",
	"creators", "contributors", "genre", "quality", "pg", "ratio", "duration", "has_subtitle", "is_series", "series",
	"episode", "episodes", "added_at", "released_at", "has_image", "playable", "playable_checked_at", "playable_changed_at",
	"playable_window_first", "playable_window_last", "gone_at", "highlighted", "live", "aired_first", "aired_last", "m3_url",
}

// Run generates the export of the database in dataDir. now is the generation time. It fails at
// once when an ingest holds the lock of the data directory.
func Run(ctx context.Context, dataDir string, now time.Time) (Manifest, error) {
	return RunWait(ctx, dataDir, now, 0)
}

// RunWait is Run, but it waits up to lockWait for a running ingest to finish.
func RunWait(ctx context.Context, dataDir string, now time.Time, lockWait time.Duration) (Manifest, error) {
	var m Manifest
	unlock, err := store.LockWait(ctx, dataDir, lockWait)
	if err != nil {
		return m, err
	}
	defer unlock()

	dbPath := filepath.Join(dataDir, store.DBFile)
	if _, err := os.Stat(dbPath); err != nil {
		return m, fmt.Errorf("no published database to export: %w", err)
	}
	reader, err := store.OpenReader(dbPath)
	if err != nil {
		return m, err
	}
	defer reader.Close()

	dir := filepath.Join(dataDir, "export")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m, err
	}
	removeTemps(dir)

	m = Manifest{GeneratedAt: now.UTC().Truncate(time.Second), SchemaVersion: store.SchemaVersion,
		Source: "https://nemzetiarchivum.hu/m3", Notice: Notice}

	add := func(name, desc string, write func(io.Writer) error) error {
		f, err := writeAtomic(dir, name, write)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		f.Description = desc
		m.Files = append(m.Files, f)
		return nil
	}

	if err := add(ArchiveName, "A teljes SQLite adatbázis tömörítve (használható mentés). / The whole SQLite database, gzip compressed.",
		func(w io.Writer) error { return gzipFile(w, dbPath) }); err != nil {
		return m, err
	}
	count := 0
	if err := add(CSVName, "Minden műsor CSV-ben (UTF-8, vesszővel elválasztva). / Every program as CSV (UTF-8, comma separated).",
		func(w io.Writer) error {
			var err error
			count, err = writeCSV(ctx, w, reader)
			return err
		}); err != nil {
		return m, err
	}
	m.Programs = count
	if err := add(JSONLName, "Minden műsor JSON-ként, soronként egy (az API-val azonos szerkezet). / One JSON object per line (same shape as the API).",
		func(w io.Writer) error { return writeJSONL(ctx, w, reader) }); err != nil {
		return m, err
	}
	if err := add(SchemaName, "Az adatbázis sémája az oszlopok leírásával. / The database schema with column notes.",
		func(w io.Writer) error { _, err := io.WriteString(w, SchemaFile(m.GeneratedAt)); return err }); err != nil {
		return m, err
	}

	// checksums of the data files, then the manifest last: a client that reads the manifest finds files that exist
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Name < m.Files[j].Name })
	var sums strings.Builder
	for _, f := range m.Files {
		fmt.Fprintf(&sums, "%s  %s\n", f.SHA256, f.Name)
	}
	if _, err := writeAtomic(dir, SumsName, func(w io.Writer) error { _, err := io.WriteString(w, sums.String()); return err }); err != nil {
		return m, err
	}
	if _, err := writeAtomic(dir, ManifestName, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	}); err != nil {
		return m, err
	}
	return m, syncDir(dir)
}

func gzipFile(w io.Writer, path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	gz := gzip.NewWriter(w)
	if _, err := io.Copy(gz, in); err != nil {
		return err
	}
	return gz.Close()
}

func writeCSV(ctx context.Context, w io.Writer, r *store.Reader) (int, error) {
	gz := gzip.NewWriter(w)
	cw := csv.NewWriter(gz)
	if err := cw.Write(csvHeader); err != nil {
		return 0, err
	}
	n := 0
	err := r.Walk(ctx, func(p model.Program) error {
		n++
		return cw.Write(csvRow(p))
	})
	if err != nil {
		return 0, err
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return 0, err
	}
	return n, gz.Close()
}

func writeJSONL(ctx context.Context, w io.Writer, r *store.Reader) error {
	gz := gzip.NewWriter(w)
	enc := json.NewEncoder(gz) // one compact object per line; < > & are escaped
	err := r.Walk(ctx, func(p model.Program) error { return enc.Encode(dto.FromModel(p, "")) })
	if err != nil {
		return err
	}
	return gz.Close()
}

// csvRow is one CSV record; every text cell goes through safeCell.
func csvRow(p model.Program) []string {
	ts := func(unix int64) string {
		if t := dto.Time(unix); t != nil {
			return t.Format(time.RFC3339)
		}
		return ""
	}
	b := func(v bool) string { return strconv.FormatBool(v) }
	playable := ""
	if p.PlayableKnown {
		playable = b(p.Playable)
	}
	return []string{
		safeCell(p.ProgramID), safeCell(p.Title), safeCell(p.Subtitle), safeCell(p.Description), safeCell(p.ShortDescription),
		safeCell(p.Info), safeCell(p.ExtendedInfo), safeCell(p.Company), strconv.Itoa(p.Year), safeCell(p.Country),
		safeCell(p.Creators), safeCell(p.Contributors), safeCell(p.Genre), safeCell(p.Quality), safeCell(p.PG), safeCell(p.Ratio),
		safeCell(p.Duration), b(p.HasSubtitle), b(p.IsSeries), safeCell(p.SeriesID), strconv.Itoa(p.Episode), strconv.Itoa(p.Episodes),
		ts(p.AddedAt), ts(p.ReleasedAt), b(p.ImageState == model.ImageStored), playable, ts(p.PlayableCheckedAt), ts(p.PlayableChangedAt),
		ts(p.WindowFirst), ts(p.WindowLast), ts(p.GoneAt), b(p.IsHighlighted), b(p.IsLive), ts(p.AiredFirst), ts(p.AiredLast),
		dto.M3PageURL + p.ProgramID,
	}
}

// safeCell protects spreadsheet users from formula injection (CSV injection): a cell that starts
// with = + - @ or a tab / carriage return would be executed as a formula by Excel, LibreOffice and
// Google Sheets, and these cells hold text written by a third party. Such a cell gets a leading
// apostrophe, which spreadsheets show as plain text. The JSONL and SQLite files stay exact.
func safeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// writeAtomic writes through a temporary file: flush, fsync, rename. It reports the size and the
// SHA-256 of what was written.
func writeAtomic(dir, name string, write func(io.Writer) error) (File, error) {
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, ".tmp-"+hex.EncodeToString(rnd[:])+"-"+name)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return File{}, err
	}
	fail := func(err error) (File, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return File{}, err
	}
	h := sha256.New()
	cw := &countWriter{w: io.MultiWriter(f, h)}
	if err := write(cw); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return File{}, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return File{}, err
	}
	return File{Name: name, Bytes: cw.n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func removeTemps(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// ErrNoManifest is returned by ReadManifest when nothing has been exported yet.
var ErrNoManifest = errors.New("export: no manifest")

// ReadManifest parses a manifest.json.
func ReadManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// SchemaFile is schema.sql: a description of the data model followed by the schema itself.
func SchemaFile(at time.Time) string {
	return fmt.Sprintf(schemaHeader, store.SchemaVersion, at.UTC().Format(time.RFC3339)) + store.SchemaSQL()
}

const schemaHeader = `-- m3 kereshető archívum: adatbázis-séma / database schema (version %d)
-- Generated: %s
--
-- HU: Az adatbázis egyetlen SQLite fájl. A "programs" tábla egy sora egy M3 műsor. Az idők unix másodpercek (UTC),
--     a 0 jelentése: ismeretlen / nincs. A lista jellegű mezők (info, extended_info, creators, contributors, genre,
--     collection) sortöréssel (\n) elválasztott szövegek. A leíró oszlopok az ELSŐ látott állapotot őrzik (kivéve, ha a
--     "refreshed_at" nem 0), az "observed" jellegűek (playable*, is_highlighted, is_live, aired_*, ...) az UTOLSÓ
--     megfigyelt állapotot. A "playable" NULL, amíg az elérhetőséget nem figyeltük meg; a "playable_checked_at" mondja meg,
--     mennyire friss. Az "availability_log" csak a "playable" jelző VÁLTOZÁSAIT tartalmazza.
--     A "programs_fts" egy FTS5 teljes szöveges index (trigram tokenizer, kis-nagybetű- és ékezetfüggetlen részszó-keresés,
--     SQLite 3.34+ FTS5 támogatással). Sima olvasáshoz nem kell: a "programs" tábla enélkül is olvasható.
-- EN: One SQLite file. A row of "programs" is one M3 program. Times are unix seconds (UTC), 0 means unknown/none. List
--     fields are newline separated text. The descriptive columns hold the FIRST seen state (unless "refreshed_at" is not 0),
--     the "observed" ones (playable*, is_highlighted, is_live, aired_*, ...) the LAST observed state. "playable" is NULL
--     until the availability has been observed; "playable_checked_at" tells how fresh it is. "availability_log" holds only
--     the CHANGES of the "playable" flag. "programs_fts" is an FTS5 full text index (trigram tokenizer, case and diacritics
--     insensitive substring search, needs SQLite 3.34+ with FTS5); plain reading of "programs" does not need it.
--
-- Column notes / oszlopok:
--   program_id          M3-... azonosító / identifier (unique)
--   title, subtitle     cím, alcím
--   info, extended_info a műsor adatlapjának sorai / lines of the programme sheet
--   description, short_description   leírás, rövid leírás
--   year                a gyártás éve (0 = ismeretlen) / production year
--   duration            HH:MM:SS:FF (FF = képkocka / frames)
--   series_id           a sorozat neve (dekódolva) / the name of the series
--   added_at            mikor került ide először / when this archive first saw it
--   released_at         az M3 által megadott legkorábbi dátum / earliest date the M3 gives
--   image_state         0 még nincs kép, 1 van, 2 az M3-nak nincs / 0 pending, 1 stored, 2 none upstream
--   playable            1 lejátszható, 0 nem, NULL ismeretlen / playable now, not, unknown
--   window_first/last   a lejátszható napok legkorábbija/legkésőbbije / first and last known playable day
--   gone_at             ekkor tűnt el az M3-ról (az adat itt marad) / when the M3 stopped knowing it
--   title_key           belső rendezési kulcs / internal sort key
--
`
