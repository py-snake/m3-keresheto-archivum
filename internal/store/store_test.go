package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
)

func prog(id, title string) model.Program {
	return model.Program{ProgramID: id, Title: title, Duration: "00:10:00:00"}
}

func publish(t *testing.T, dir string, ps ...model.Program) Summary {
	t.Helper()
	pub := &Publisher{Dir: dir, Keep: 3}
	sum, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.InsertPrograms(ctx, ps)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func openReader(t *testing.T, dir string) *Reader {
	t.Helper()
	r, err := OpenReader(filepath.Join(dir, DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func titles(res Result) []string {
	var out []string
	for _, p := range res.Items {
		out = append(out, p.Title)
	}
	return out
}

func TestPublishCreatesAndReaderSeesIt(t *testing.T) {
	dir := t.TempDir()
	sum := publish(t, dir, prog("M3-AAA1", "Első"), prog("M3-AAA2", "Második"))
	if !sum.Published || sum.Before != 0 || sum.After != 2 {
		t.Fatalf("%+v", sum)
	}
	r := openReader(t, dir)
	res, err := r.Search(context.Background(), "", 10, 0)
	if err != nil || res.Total != 2 || res.Items[0].Title != "Második" {
		t.Fatalf("newest first expected: %+v %v", res, err)
	}
	// no side files next to the published database
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		switch e.Name() {
		case DBFile, ".ingest.lock":
		default:
			t.Errorf("unexpected file %q", e.Name())
		}
	}
}

func TestInsertKeepsTheFirstSeenState(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "Eredeti"))
	sum := publish(t, dir, prog("M3-AAA1", "Megváltozott"), prog("M3-AAA2", "Új"))
	if sum.After != 2 {
		t.Fatalf("%+v", sum)
	}
	res, _ := openReader(t, dir).Search(context.Background(), "M3-AAA1", 10, 0)
	if len(res.Items) != 1 || res.Items[0].Title != "Eredeti" {
		t.Fatalf("an existing program must not be overwritten: %v", titles(res))
	}
}

func TestFailedRunLeavesThePublishedDatabaseUntouched(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "Marad"))
	before, _ := os.ReadFile(filepath.Join(dir, DBFile))

	pub := &Publisher{Dir: dir}
	_, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		if _, err := w.InsertPrograms(ctx, []model.Program{prog("M3-AAA2", "Fél")}); err != nil {
			return err
		}
		return errors.New("upstream went away halfway")
	})
	if err == nil {
		t.Fatal("expected the error")
	}
	after, _ := os.ReadFile(filepath.Join(dir, DBFile))
	if string(before) != string(after) {
		t.Fatal("the published file changed although the run failed")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".new") {
			t.Fatalf("work file left behind: %s", e.Name())
		}
	}
}

func TestRowCountMayNotShrinkOrExplode(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "a"), prog("M3-AAA2", "b"))
	before, _ := os.ReadFile(filepath.Join(dir, DBFile))

	pub := &Publisher{Dir: dir}
	_, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.db.ExecContext(ctx, "DELETE FROM programs WHERE program_id = 'M3-AAA1'")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("shrinking database published: %v", err)
	}

	pub = &Publisher{Dir: dir, MaxNew: 2}
	_, err = pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.InsertPrograms(ctx, []model.Program{prog("M3-BBB1", "x"), prog("M3-BBB2", "y"), prog("M3-BBB3", "z")})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("a flood of new rows was published: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, DBFile))
	if string(before) != string(after) {
		t.Fatal("published file changed")
	}
}

func TestMalformedProgramIDIsRefused(t *testing.T) {
	dir := t.TempDir()
	pub := &Publisher{Dir: dir}
	_, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.InsertPrograms(ctx, []model.Program{prog("M3-X') OR 1=1 --", "t")})
		return err
	})
	if err == nil {
		t.Fatal("malformed id stored")
	}
	if _, statErr := os.Stat(filepath.Join(dir, DBFile)); statErr == nil {
		t.Fatal("a database was published after a failed run")
	}
}

func TestSchemaConstraintsRejectGarbage(t *testing.T) {
	dir := t.TempDir()
	pub := &Publisher{Dir: dir}
	_, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		bad := prog("M3-AAA1", strings.Repeat("x", 300)) // longer than the CHECK allows
		_, err := w.InsertPrograms(ctx, []model.Program{bad})
		return err
	})
	if err == nil {
		t.Fatal("over-long title accepted by the database")
	}
}

func TestOnlyOneIngestAtATime(t *testing.T) {
	dir := t.TempDir()
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		(&Publisher{Dir: dir}).Run(context.Background(), func(ctx context.Context, w *Writer) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	_, err := (&Publisher{Dir: dir}).Run(context.Background(), func(context.Context, *Writer) error { return nil })
	close(release)
	wg.Wait()
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second ingest must be refused, got %v", err)
	}
}

func TestBackupsAreRotated(t *testing.T) {
	dir := t.TempDir()
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		clock = clock.Add(time.Hour)
		now := clock
		pub := &Publisher{Dir: dir, Keep: 3, Now: func() time.Time { return now }}
		if _, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
			_, err := w.InsertPrograms(ctx, []model.Program{prog(fmt.Sprintf("M3-R%d", i), "r")})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "backups"))
	if len(ents) != 3 {
		t.Fatalf("expected 3 backups, got %d", len(ents))
	}
	// every kept backup is a complete, openable database
	for _, e := range ents {
		r, err := OpenReader(filepath.Join(dir, "backups", e.Name()))
		if err != nil {
			t.Fatalf("backup %s: %v", e.Name(), err)
		}
		r.Close()
	}
}

// TestCrashAtAnyPoint kills a real process (os.Exit, like kill -9: no deferred cleanup runs) at
// every step of an ingest and verifies that the published database is intact and the next run works.
func TestCrashAtAnyPoint(t *testing.T) {
	if point := os.Getenv("M3_CRASH_CHILD"); point != "" {
		crashChild(point)
		return
	}
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "Túlél"))
	before, _ := os.ReadFile(filepath.Join(dir, DBFile))

	for _, point := range []string{"copied", "inserted", "worked", "checked", "synced", "backed-up"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestCrashAtAnyPoint")
		cmd.Env = append(os.Environ(), "M3_CRASH_CHILD="+point, "M3_CRASH_DIR="+dir)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("[%s] the child was supposed to die: %s", point, out)
		}
		after, _ := os.ReadFile(filepath.Join(dir, DBFile))
		if string(after) != string(before) {
			t.Fatalf("[%s] the published database was changed by a crashed ingest", point)
		}
		r := openReader(t, dir)
		if res, err := r.Search(context.Background(), "", 10, 0); err != nil || res.Total != 1 {
			t.Fatalf("[%s] database unusable after the crash: %+v %v", point, res, err)
		}
	}

	// the leftovers of the crashed runs must not block or poison the next run
	sum := publish(t, dir, prog("M3-AAA2", "Új"))
	if sum.After != 2 {
		t.Fatalf("%+v", sum)
	}
	if _, err := os.Stat(filepath.Join(dir, DBFile+".new")); err == nil {
		t.Fatal("work file left after a successful run")
	}
}

func crashChild(point string) {
	pub := &Publisher{Dir: os.Getenv("M3_CRASH_DIR"), Keep: 2}
	pub.stage = func(name string) {
		if name == point {
			os.Exit(3)
		}
	}
	pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		w.InsertPrograms(ctx, []model.Program{prog("M3-CRASH1", "x")})
		pub.at("inserted")
		return nil
	})
	os.Exit(0) // reaching the end would be a failure of the test setup
}

func TestReaderCannotDoAnythingExceptSelect(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "Első"))
	r := openReader(t, dir)
	evil := filepath.Join(t.TempDir(), "evil.db")

	denied := map[string]string{
		"attach":            "ATTACH DATABASE '" + evil + "' AS e",
		"insert":            "INSERT INTO programs (program_id, title, added_at) VALUES ('M3-X', 't', 1)",
		"update":            "UPDATE programs SET title = 'x'",
		"delete":            "DELETE FROM programs",
		"create table":      "CREATE TABLE x (a)",
		"drop":              "DROP TABLE programs",
		"pragma writable":   "PRAGMA writable_schema = ON",
		"pragma journal":    "PRAGMA journal_mode = WAL",
		"pragma query_only": "PRAGMA query_only = 0",
		"pragma table_info": "PRAGMA table_info(programs)",
		"sqlite_master":     "SELECT * FROM sqlite_master",
		"vacuum into":       "VACUUM INTO '" + evil + "'",
		"load_extension":    "SELECT load_extension('x')",
		"readfile":          "SELECT readfile('/etc/passwd')",
		"random function":   "SELECT randomblob(10)",
		"other table":       "SELECT * FROM pragma_table_info('programs')",
	}
	for name, q := range denied {
		rows, err := r.db.Query(q)
		if err == nil {
			for rows.Next() {
			}
			err = rows.Err()
			rows.Close()
		}
		if err == nil {
			t.Errorf("%s: statement succeeded: %s", name, q)
		}
	}
	if _, err := os.Stat(evil); err == nil {
		t.Fatal("ATTACH created a file")
	}

	// and the legitimate queries still work
	if res, err := r.Search(context.Background(), "Első", 10, 0); err != nil || res.Total != 1 {
		t.Fatalf("legit search failed: %+v %v", res, err)
	}
}

func TestSearchSemantics(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir,
		prog("M3-AAA1", "A béke szigete"),
		prog("M3-AAA2", "Vadászkutyák"),
		prog("M3-AAA3", "Százados"),
		model.Program{ProgramID: "M3-AAA4", Title: "Egyéb", Description: "Kolozsvár és Eszék között", Creators: "Nagy István\nKiss Ödön"},
	)
	r := openReader(t, dir)
	ctx := context.Background()

	cases := []struct {
		term string
		want int
	}{
		{"béke", 1}, {"BÉKE", 1}, {"beke", 1}, // case and diacritics folded
		{"zigete", 1},                 // substring, like the old LIKE %x%
		{"vadaszkutya", 1},            // no accents typed
		{"kolozsvar es", 1},           // description, folded
		{"kiss ödön", 1}, {"odon", 1}, // creators, multi-line field
		{"százados", 1},
		{"nincs ilyen", 0},
		{"M3-AAA2", 1}, {"m3-aaa2", 1}, {"M3-AAA1, M3-AAA3 ,M3-NOPE", 2}, // identifier lookup
		{"M3-nonsense text", 0}, // looks like an id query but has no valid id: falls back to text search
	}
	for _, c := range cases {
		res, err := r.Search(ctx, c.term, 10, 0)
		if err != nil || res.Total != c.want {
			t.Errorf("%q: total=%d err=%v, want %d", c.term, res.Total, err, c.want)
		}
	}

	// 1-2 character terms search the titles only; they are no longer refused
	for _, short := range []string{"a", "ab", "é", "ok"} {
		if _, err := r.Search(ctx, short, 10, 0); err != nil {
			t.Errorf("%q: %v", short, err)
		}
	}
}

func TestHostileSearchTermsAreJustText(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "Hello world"), prog("M3-AAA2", `Idézet "szó" benne`))
	r := openReader(t, dir)
	ctx := context.Background()

	hostile := []string{
		`" OR title:* NEAR(a b) AND NOT ^x`, `'; DROP TABLE programs; --`, `*`, `**`, `title:Hello`, `Hello AND world`,
		`NEAR(Hello world)`, `^Hel`, `"`, `""`, `"Hello`, `Hello"`, "Hel\x00lo", `%`, `_`, `\`, `)`, `(`, `-Hello`,
		strings.Repeat("a", 5000), strings.Repeat(`"`, 300), "\xff\xfe\xfd", "Hello\nworld", `{title}: Hello`,
	}
	for _, term := range hostile {
		res, err := r.Search(ctx, term, 10, 0)
		if err != nil && !errors.Is(err, ErrTermTooShort) {
			t.Errorf("%q caused an error: %v", term, err)
		}
		if res.Total > 1 {
			t.Errorf("%q matched %d rows: it was treated as an operator", term, res.Total)
		}
	}
	// a quote inside a real search is searched for literally
	if res, err := r.Search(ctx, `"szó"`, 10, 0); err != nil || res.Total != 1 {
		t.Errorf("literal quote search: %+v %v", res, err)
	}
	// the tables are still there
	if res, _ := r.Search(ctx, "", 10, 0); res.Total != 2 {
		t.Fatal("data damaged by hostile terms")
	}
}

func TestPagingAndCaps(t *testing.T) {
	dir := t.TempDir()
	var ps []model.Program
	for i := 0; i < 25; i++ {
		ps = append(ps, prog(fmt.Sprintf("M3-P%03d", i), fmt.Sprintf("Film %03d", i)))
	}
	publish(t, dir, ps...)
	r := openReader(t, dir)
	ctx := context.Background()

	p1, _ := r.Search(ctx, "Film", 10, 0)
	p3, _ := r.Search(ctx, "Film", 10, 20)
	if p1.Total != 25 || len(p1.Items) != 10 || len(p3.Items) != 5 || p1.Items[0].Title != "Film 024" {
		t.Fatalf("%d %d %d %v", p1.Total, len(p1.Items), len(p3.Items), titles(p1)[:1])
	}
	if res, err := r.Search(ctx, "Film", 10, 1000); err != nil || len(res.Items) != 0 {
		t.Fatalf("offset past the end: %+v %v", res, err)
	}
	for _, bad := range [][2]int{{0, 0}, {101, 0}, {10, -1}} {
		if _, err := r.Search(ctx, "Film", bad[0], bad[1]); err == nil {
			t.Errorf("paging %v accepted", bad)
		}
	}
	long := strings.Repeat("é", 500)
	if n, ok := NormalizeTerm(long); !ok || len([]rune(n)) != MaxTermRunes {
		t.Errorf("term not capped: %d", len([]rune(n)))
	}
}

func TestSourceSwapsToThePublishedReplacement(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "Régi"))
	s := NewSource(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.grace = 50 * time.Millisecond
	defer s.Close()
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	old := s.Reader()
	if res, _ := old.Search(context.Background(), "", 10, 0); res.Total != 1 {
		t.Fatal("first database not served")
	}
	if err := s.Refresh(); err != nil || s.Reader() != old { // unchanged file: same reader
		t.Fatalf("needless reopen: %v", err)
	}

	publish(t, dir, prog("M3-AAA2", "Új"))
	if err := s.Refresh(); err != nil {
		t.Fatal(err)
	}
	cur := s.Reader()
	if cur == old {
		t.Fatal("the replaced database was not picked up")
	}
	if res, _ := cur.Search(context.Background(), "", 10, 0); res.Total != 2 {
		t.Fatal("new database not served")
	}
	// the old reader keeps working during the grace period, then is closed
	if res, err := old.Search(context.Background(), "", 10, 0); err != nil || res.Total != 1 {
		t.Fatalf("old snapshot must stay readable: %+v %v", res, err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := old.Search(context.Background(), "", 10, 0); err == nil {
		t.Fatal("old reader never closed")
	}
}

func TestSourceWithoutDatabaseAndWithBrokenFile(t *testing.T) {
	dir := t.TempDir()
	s := NewSource(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Refresh(); err != nil || s.Reader() != nil {
		t.Fatalf("no database yet must not be an error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, DBFile), []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(); err == nil {
		t.Fatal("a corrupt file was accepted")
	}
	if s.Reader() != nil {
		t.Fatal("reader set for a corrupt file")
	}
}

func TestUnsupportedSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	publish(t, dir, prog("M3-AAA1", "x"))
	pub := &Publisher{Dir: dir}
	_, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.db.ExecContext(ctx, "PRAGMA user_version = 99")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReader(filepath.Join(dir, DBFile)); err == nil {
		t.Fatal("unknown schema version served")
	}
}

// FuzzSearchTerm throws arbitrary bytes at the search. A term must never cause an SQL or FTS5
// syntax error, never be interpreted as an operator, never write, and never match more than exists.
func FuzzSearchTerm(f *testing.F) {
	dir := f.TempDir()
	ps := []model.Program{prog("M3-AAA1", "Hello world"), prog("M3-AAA2", `Idézet "szó" benne`), prog("M3-AAA3", "NEAR AND OR NOT title")}
	if _, err := (&Publisher{Dir: dir}).Run(context.Background(), func(ctx context.Context, w *Writer) error {
		_, err := w.InsertPrograms(ctx, ps)
		return err
	}); err != nil {
		f.Fatal(err)
	}
	r, err := OpenReader(filepath.Join(dir, DBFile))
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { r.Close() })

	for _, seed := range []string{"Hello", `"`, `""`, `" OR title:* NEAR(a b)`, "*", "^x", "title:Hello", "M3-AAA1", "M3-", "a%00b", "(", ")", "-x", "AND", "\xff\xfe"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, term string) {
		res, err := r.Search(context.Background(), term, 10, 0)
		if err != nil && !errors.Is(err, ErrTermTooShort) {
			t.Fatalf("term %q caused an error: %v", term, err)
		}
		if res.Total > 3 || len(res.Items) > 3 {
			t.Fatalf("term %q matched %d rows of 3: treated as an operator", term, res.Total)
		}
		var n int
		if err := r.db.QueryRow("SELECT count(*) FROM programs").Scan(&n); err != nil || n != 3 {
			t.Fatalf("data changed after term %q: %d %v", term, n, err)
		}
	})
}
