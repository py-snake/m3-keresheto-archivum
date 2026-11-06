package export

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"

	"github.com/py-snake/m3-keresheto-archivum/internal/dto"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
)

func seed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mk := func(id, title string) model.Program {
		return model.Program{ProgramID: id, Title: title, Duration: "00:10:00:00", Year: 1990}
	}
	a := mk("M3-EXP001", "Első film")
	a.Description, a.Creators, a.Genre = "Leírás\nkét sorban", "Kiss Ödön\nNagy István", "Dokumentum filmek"
	a.PlayableKnown, a.Playable, a.IsHighlighted = true, true, true
	a.ReleasedAt, a.AiredFirst, a.AiredLast = 1_700_000_000, 1_600_000_000, 1_650_000_000
	a.WindowFirst, a.WindowLast = 1_700_000_000, 1_700_500_000
	b := mk("M3-EXP002", `=HYPERLINK("http://evil.example","katt ide")`) // formula injection
	b.Subtitle, b.Description = "+cmd|' /C calc'!A0", "@SUM(1+1)*cmd|' /C calc'!A0"
	b.Creators, b.Company = "-2+3+cmd|' /C calc'!A0", "\t=1+1"
	c := mk("M3-EXP003", "Árvíztűrő, \"idézőjeles\", vesszős cím")
	c.Info = "<script>alert(1)</script>"
	d := mk("M3-EXP004", "Eltűnt műsor")
	pub := &store.Publisher{Dir: dir, Now: func() time.Time { return time.Unix(1_700_000_000, 0) }}
	if _, err := pub.Run(context.Background(), func(ctx context.Context, w *store.Writer) error {
		if _, err := w.InsertPrograms(ctx, []model.Program{a, b, c, d}); err != nil {
			return err
		}
		if _, _, err := w.RecordObservations(ctx, []model.Program{a}); err != nil {
			return err
		}
		return w.MarkGone(ctx, "M3-EXP004")
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func gunzip(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExportProducesConsistentFiles(t *testing.T) {
	dir := seed(t)
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	m, err := Run(context.Background(), dir, at)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "export")
	if m.Programs != 4 || m.SchemaVersion != store.SchemaVersion || !m.GeneratedAt.Equal(at) || len(m.Files) != 4 {
		t.Fatalf("%+v", m)
	}
	if !strings.Contains(m.Notice, "M3") || m.Source == "" {
		t.Fatal("the manifest lacks the origin notice")
	}

	// every listed file exists, has the stated size and checksum, and SHA256SUMS agrees
	sums, _ := os.ReadFile(filepath.Join(out, SumsName))
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(out, f.Name))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		if int64(len(data)) != f.Bytes || hex.EncodeToString(h[:]) != f.SHA256 {
			t.Errorf("%s: manifest and file disagree", f.Name)
		}
		if !strings.Contains(string(sums), f.SHA256+"  "+f.Name+"\n") {
			t.Errorf("%s: missing from SHA256SUMS", f.Name)
		}
		if f.Description == "" {
			t.Errorf("%s: no description", f.Name)
		}
	}
	// the manifest on disk is the same and is the LAST thing written
	raw, _ := os.ReadFile(filepath.Join(out, ManifestName))
	back, err := ReadManifest(raw)
	if err != nil || back.Programs != 4 || len(back.Files) != 4 {
		t.Fatalf("%+v %v", back, err)
	}
	ents, _ := os.ReadDir(out)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestTheDumpIsAUsableDatabase(t *testing.T) {
	dir := seed(t)
	if _, err := Run(context.Background(), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), store.DBFile)
	if err := os.WriteFile(restored, gunzip(t, filepath.Join(dir, "export", ArchiveName)), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := store.OpenReader(restored)
	if err != nil {
		t.Fatalf("the dump does not open: %v", err)
	}
	defer r.Close()
	res, err := r.Search(context.Background(), "Árvíztűrő", 10, 0) // the full text index travels with it
	if err != nil || res.Total != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if all, _ := r.Search(context.Background(), "", 10, 0); all.Total != 4 {
		t.Fatalf("%d programs", all.Total)
	}
}

func TestCSVIsComplete(t *testing.T) {
	dir := seed(t)
	if _, err := Run(context.Background(), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(gunzip(t, filepath.Join(dir, "export", CSVName)))).ReadAll()
	if err != nil {
		t.Fatalf("the CSV is not valid CSV: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("%d rows, want header + 4", len(rows))
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	if len(col) != len(csvHeader) {
		t.Fatal("header")
	}
	byID := map[string][]string{}
	for _, r := range rows[1:] {
		if len(r) != len(csvHeader) {
			t.Fatalf("a row has %d cells", len(r))
		}
		byID[r[col["id"]]] = r
	}
	a := byID["M3-EXP001"]
	if a[col["description"]] != "Leírás\nkét sorban" || a[col["creators"]] != "Kiss Ödön\nNagy István" {
		t.Errorf("multi-line cells must survive: %q %q", a[col["description"]], a[col["creators"]])
	}
	if a[col["playable"]] != "true" || a[col["highlighted"]] != "true" || a[col["gone_at"]] != "" || a[col["year"]] != "1990" {
		t.Errorf("%v", a)
	}
	if a[col["released_at"]] != "2023-11-14T22:13:20Z" || a[col["m3_url"]] != "https://nemzetiarchivum.hu/m3/M3-EXP001" {
		t.Errorf("%q %q", a[col["released_at"]], a[col["m3_url"]])
	}
	if byID["M3-EXP004"][col["gone_at"]] == "" || byID["M3-EXP002"][col["playable"]] != "" {
		t.Error("gone / never observed")
	}
	if c := byID["M3-EXP003"]; c[col["title"]] != `Árvíztűrő, "idézőjeles", vesszős cím` {
		t.Errorf("commas and quotes must round trip: %q", c[col["title"]])
	}
}

func TestCSVNeverStartsACellWithAFormulaCharacter(t *testing.T) {
	dir := seed(t)
	if _, err := Run(context.Background(), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, _ := csv.NewReader(bytes.NewReader(gunzip(t, filepath.Join(dir, "export", CSVName)))).ReadAll()
	for _, row := range rows[1:] {
		for i, cell := range row {
			if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
				t.Errorf("column %s starts with %q: %q (spreadsheet formula injection)", rows[0][i], cell[0], cell)
			}
		}
	}
	// the dangerous cells are still there, as plain text with an apostrophe in front
	var all string
	for _, row := range rows {
		all += strings.Join(row, "|")
	}
	for _, want := range []string{`'=HYPERLINK(`, `'+cmd|`, `'@SUM(`, `'-2+3+cmd`, "'\t=1+1"} {
		if !strings.Contains(all, want) {
			t.Errorf("the protected cell %q is missing", want)
		}
	}
	// the header and ordinary text are untouched
	if rows[0][0] != "id" || safeCell("Normál cím") != "Normál cím" || safeCell("") != "" || safeCell("12") != "12" {
		t.Error("safeCell must only touch cells that start with a formula character")
	}
	for _, c := range []string{"=a", "+a", "-a", "@a", "\ta", "\ra"} {
		if safeCell(c) != "'"+c {
			t.Errorf("safeCell(%q) = %q", c, safeCell(c))
		}
	}
}

func TestJSONLMatchesTheAPIShape(t *testing.T) {
	dir := seed(t)
	if _, err := Run(context.Background(), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(gunzip(t, filepath.Join(dir, "export", JSONLName))))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	byID := map[string]dto.Program{}
	lines := 0
	for sc.Scan() {
		lines++
		var p dto.Program
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("line %d does not fit the API type: %v", lines, err)
		}
		byID[p.ID] = p
	}
	if lines != 4 {
		t.Fatalf("%d lines", lines)
	}
	a := byID["M3-EXP001"]
	if len(a.Creators) != 2 || a.Creators[1] != "Nagy István" || a.Availability.Playable == nil || !*a.Availability.Playable {
		t.Fatalf("%+v", a)
	}
	if byID["M3-EXP002"].Availability.Playable != nil || !byID["M3-EXP004"].Availability.Gone || a.ImageURL != nil {
		t.Error("never observed must be null, gone must be true, the dump has no image URL")
	}
	// HTML in the data is escaped inside the JSON
	if raw := gunzip(t, filepath.Join(dir, "export", JSONLName)); bytes.Contains(raw, []byte("<script>")) {
		t.Error("raw < > in the JSON lines")
	}
}

func TestSchemaFileIsAValidSchema(t *testing.T) {
	dir := seed(t)
	if _, err := Run(context.Background(), dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	schema, _ := os.ReadFile(filepath.Join(dir, "export", SchemaName))
	if !strings.Contains(string(schema), "CREATE TABLE programs") || !strings.Contains(string(schema), "availability_log") ||
		!strings.Contains(string(schema), "Column notes") || !strings.Contains(string(schema), "FIRST seen") {
		t.Fatal("the schema file lacks the tables or the explanation")
	}
	// it really creates a database: the file is executed on an empty one
	db, err := driver.Open("file:"+filepath.Join(t.TempDir(), "empty.db"), func(c *sqlite3.Conn) error { return fts5.Register(c) })
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("schema.sql does not run: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('programs','programs_fts','availability_log')").Scan(&n); err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	// and a row of the real data fits it
	if _, err := db.Exec("INSERT INTO programs (program_id, title, added_at) VALUES ('M3-X1', 't', 1)"); err != nil {
		t.Fatal(err)
	}
}

func TestExportIsRepeatableAndSafeToInterrupt(t *testing.T) {
	dir := seed(t)
	if _, err := Run(context.Background(), dir, time.Unix(1000, 0)); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "export")
	first, _ := os.ReadFile(filepath.Join(out, CSVName))

	// leftovers of a crashed run are cleaned up, a second run replaces the files as a whole
	_ = os.WriteFile(filepath.Join(out, ".tmp-deadbeef-m3-programs.csv.gz"), []byte("half"), 0o644)
	if _, err := Run(context.Background(), dir, time.Unix(2000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, ".tmp-deadbeef-m3-programs.csv.gz")); err == nil {
		t.Error("a stale temporary file was not removed")
	}
	second, _ := os.ReadFile(filepath.Join(out, CSVName))
	if !bytes.Equal(first, second) {
		t.Error("the same data must give the same file (reproducible gzip)")
	}
	// while an ingest holds the lock, the export refuses instead of reading a moving target
	unlock, err := store.Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := Run(context.Background(), dir, time.Now()); err == nil {
		t.Fatal("the export ran while the data directory was locked")
	}
}

func TestExportWithoutADatabase(t *testing.T) {
	if _, err := Run(context.Background(), t.TempDir(), time.Now()); err == nil {
		t.Fatal("nothing to export must be an error")
	}
}
