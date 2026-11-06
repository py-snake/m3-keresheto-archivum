package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/model"
)

// the schema of the first database version, kept verbatim to test the upgrade of a real old file
const schemaV1 = `
CREATE TABLE programs (
  id INTEGER PRIMARY KEY,
  program_id TEXT NOT NULL UNIQUE CHECK (length(program_id) BETWEEN 4 AND 30),
  title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 255),
  subtitle TEXT NOT NULL DEFAULT '', info TEXT NOT NULL DEFAULT '', extended_info TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '', short_description TEXT NOT NULL DEFAULT '', company TEXT NOT NULL DEFAULT '',
  year INTEGER NOT NULL DEFAULT 0, country TEXT NOT NULL DEFAULT '', creators TEXT NOT NULL DEFAULT '',
  contributors TEXT NOT NULL DEFAULT '', genre TEXT NOT NULL DEFAULT '', quality TEXT NOT NULL DEFAULT '',
  pg TEXT NOT NULL DEFAULT '', ratio TEXT NOT NULL DEFAULT '', duration TEXT NOT NULL DEFAULT '',
  has_subtitle INTEGER NOT NULL DEFAULT 0, is_series INTEGER NOT NULL DEFAULT 0, series_id TEXT NOT NULL DEFAULT '',
  episode INTEGER NOT NULL DEFAULT 0, episodes INTEGER NOT NULL DEFAULT 0, added_at INTEGER NOT NULL,
  released_at INTEGER NOT NULL DEFAULT 0, image_state INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX programs_image_state_idx ON programs (image_state) WHERE image_state = 0;
CREATE VIRTUAL TABLE programs_fts USING fts5(
  title, subtitle, info, extended_info, description, creators, contributors, genre,
  content = 'programs', content_rowid = 'id', tokenize = 'trigram case_sensitive 0 remove_diacritics 1');
CREATE TRIGGER programs_ai AFTER INSERT ON programs BEGIN
  INSERT INTO programs_fts (rowid, title, subtitle, info, extended_info, description, creators, contributors, genre)
  VALUES (new.id, new.title, new.subtitle, new.info, new.extended_info, new.description, new.creators, new.contributors, new.genre);
END;
PRAGMA user_version = 1;`

func avail(id, title string, playable bool, first, last int64) model.Program {
	p := prog(id, title)
	p.PlayableKnown, p.Playable, p.WindowFirst, p.WindowLast = true, playable, first, last
	return p
}

func run(t *testing.T, dir string, now time.Time, work func(ctx context.Context, w *Writer) error) {
	t.Helper()
	pub := &Publisher{Dir: dir, Keep: 2, Now: func() time.Time { return now }}
	if _, err := pub.Run(context.Background(), work); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationOfAnOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFile)
	old, err := openWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Régi film egy", "Régi film kettő"} {
		if _, err := old.Exec(`INSERT INTO programs (program_id, title, added_at, image_state) VALUES (?, ?, 100, 1)`, "M3-OLD"+string(rune('A'+i)), title); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	// a reader of the new version refuses the old file instead of failing on missing columns
	if _, err := OpenReader(path); err == nil {
		t.Fatal("a version 1 database was served by the version 2 reader")
	}

	// any ingest run upgrades it: inside the private copy, checked, published atomically
	run(t, dir, time.Unix(1000, 0), func(context.Context, *Writer) error { return nil })

	r := openReader(t, dir)
	res, err := r.Search(context.Background(), "Régi film", 10, 0)
	if err != nil || res.Total != 2 {
		t.Fatalf("data or the full text index lost in the upgrade: %+v %v", res, err)
	}
	for _, p := range res.Items {
		if p.PlayableKnown || p.PlayableCheckedAt != 0 || p.GoneAt != 0 || p.ImageState != 1 || p.AddedAt != 100 {
			t.Fatalf("an upgraded row must be 'never observed' and keep its data: %+v", p)
		}
	}
	// the upgraded database accepts availability observations
	run(t, dir, time.Unix(2000, 0), func(ctx context.Context, w *Writer) error {
		obs, _, err := w.RecordObservations(ctx, []model.Program{avail("M3-OLDA", "x", true, 0, 0)})
		if obs != 1 {
			t.Fatalf("observed %d", obs)
		}
		return err
	})
	if backups, _ := os.ReadDir(filepath.Join(dir, "backups")); len(backups) == 0 {
		t.Fatal("the version 1 file was not kept as a backup")
	}
}

func TestAvailabilityUpdatesOnlyAvailabilityAndLogsChanges(t *testing.T) {
	dir := t.TempDir()
	t1, t2, t3, t4 := time.Unix(1000, 0), time.Unix(2000, 0), time.Unix(3000, 0), time.Unix(4000, 0)

	// first sight: the program is stored and its availability observed (never observed before -> a change)
	run(t, dir, t1, func(ctx context.Context, w *Writer) error {
		first := avail("M3-AAA1", "Eredeti cím", true, 500, 900)
		first.Description = "eredeti leírás"
		if _, err := w.InsertPrograms(ctx, []model.Program{first}); err != nil {
			return err
		}
		obs, chg, err := w.RecordObservations(ctx, []model.Program{first})
		if obs != 1 || chg != 1 {
			t.Fatalf("first observation: %d/%d", obs, chg)
		}
		return err
	})

	// the upstream later says "not playable" and (hypothetically) also changed the title
	run(t, dir, t2, func(ctx context.Context, w *Writer) error {
		later := avail("M3-AAA1", "MEGVÁLTOZOTT cím", false, 500, 900)
		later.Description = "megváltozott leírás"
		if n, err := w.InsertPrograms(ctx, []model.Program{later}); err != nil || n != 0 {
			t.Fatalf("an existing program must not be inserted again: %d %v", n, err)
		}
		obs, chg, err := w.RecordObservations(ctx, []model.Program{later})
		if obs != 1 || chg != 1 {
			t.Fatalf("a flipped flag must count as a change: %d/%d", obs, chg)
		}
		return err
	})

	// an unchanged observation only refreshes the check time
	run(t, dir, t3, func(ctx context.Context, w *Writer) error {
		obs, chg, err := w.RecordObservations(ctx, []model.Program{avail("M3-AAA1", "x", false, 600, 1000)})
		if obs != 1 || chg != 0 {
			t.Fatalf("unchanged: %d/%d", obs, chg)
		}
		return err
	})

	// programs whose item had no playable flag are skipped (not guessed)
	run(t, dir, t4, func(ctx context.Context, w *Writer) error {
		obs, _, err := w.RecordObservations(ctx, []model.Program{prog("M3-AAA1", "x")})
		if obs != 0 {
			t.Fatalf("an item without the flag was observed")
		}
		return err
	})

	res, err := openReader(t, dir).Search(context.Background(), "M3-AAA1", 10, 0)
	if err != nil || len(res.Items) != 1 {
		t.Fatal(err)
	}
	p := res.Items[0]
	if p.Title != "Eredeti cím" || p.Description != "eredeti leírás" {
		t.Fatalf("the descriptive fields must keep the FIRST seen state: %q / %q", p.Title, p.Description)
	}
	if !p.PlayableKnown || p.Playable {
		t.Fatalf("the availability must be the LAST observed state: %+v", p)
	}
	if p.PlayableChangedAt != t2.Unix() || p.PlayableCheckedAt != t3.Unix() {
		t.Fatalf("changed=%d checked=%d, want %d / %d", p.PlayableChangedAt, p.PlayableCheckedAt, t2.Unix(), t3.Unix())
	}
	if p.WindowFirst != 600 || p.WindowLast != 1000 {
		t.Fatalf("window %d..%d", p.WindowFirst, p.WindowLast)
	}
}

func TestAvailabilityLogHoldsOnlyChanges(t *testing.T) {
	dir := t.TempDir()
	pub := &Publisher{Dir: dir}
	var rows [][2]int64
	_, err := pub.Run(context.Background(), func(ctx context.Context, w *Writer) error {
		now := time.Unix(10, 0)
		w.now = func() time.Time { return now }
		a := avail("M3-LOG1", "Napló", true, 0, 0)
		if _, err := w.InsertPrograms(ctx, []model.Program{a}); err != nil {
			return err
		}
		seq := []bool{true, true, false, false, true} // 3 changes: unknown->true, true->false, false->true
		for i, v := range seq {
			now = time.Unix(int64(100*(i+1)), 0)
			a.Playable = v
			if _, _, err := w.RecordObservations(ctx, []model.Program{a}); err != nil {
				return err
			}
		}
		rs, err := w.db.QueryContext(ctx, "SELECT observed_at, playable FROM availability_log WHERE program_id = 'M3-LOG1' ORDER BY id")
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var at, v int64
			if err := rs.Scan(&at, &v); err != nil {
				return err
			}
			rows = append(rows, [2]int64{at, v})
		}
		return rs.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]int64{{100, 1}, {300, 0}, {500, 1}}
	if len(rows) != 3 || rows[0] != want[0] || rows[1] != want[1] || rows[2] != want[2] {
		t.Fatalf("history %v, want %v", rows, want)
	}
}

func TestRecheckOrderGoneAndTouch(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, time.Unix(100, 0), func(ctx context.Context, w *Writer) error {
		ps := []model.Program{prog("M3-NEVER1", "soha"), prog("M3-OLD001", "régi"), prog("M3-NEW001", "friss"), prog("M3-GONE01", "eltűnt")}
		if _, err := w.InsertPrograms(ctx, ps); err != nil {
			return err
		}
		if _, _, err := w.RecordObservations(ctx, []model.Program{avail("M3-OLD001", "x", true, 0, 0)}); err != nil {
			return err
		}
		w.now = func() time.Time { return time.Unix(900, 0) }
		if _, _, err := w.RecordObservations(ctx, []model.Program{avail("M3-NEW001", "x", true, 0, 0)}); err != nil {
			return err
		}
		return w.MarkGone(ctx, "M3-GONE01")
	})
	run(t, dir, time.Unix(1000, 0), func(ctx context.Context, w *Writer) error {
		ids, err := w.StalestForRecheck(ctx, 10)
		if err != nil {
			return err
		}
		// never observed first, then oldest observation; programs the upstream no longer knows are skipped
		if len(ids) != 3 || ids[0] != "M3-NEVER1" || ids[1] != "M3-OLD001" || ids[2] != "M3-NEW001" {
			t.Fatalf("order %v", ids)
		}
		if two, _ := w.StalestForRecheck(ctx, 2); len(two) != 2 {
			t.Fatalf("limit not applied: %v", two)
		}
		// a checked program without a flag moves to the end of the queue
		if err := w.TouchChecked(ctx, []string{"M3-NEVER1"}); err != nil {
			return err
		}
		ids, _ = w.StalestForRecheck(ctx, 10)
		if ids[len(ids)-1] != "M3-NEVER1" {
			t.Fatalf("a touched program must go to the back: %v", ids)
		}
		return nil
	})
}
