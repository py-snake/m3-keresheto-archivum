package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
	"github.com/py-snake/m3-keresheto-archivum/internal/upstream"
)

func clientWith(t *testing.T, fake *fakeupstream.Server, max int) *upstream.Client {
	t.Helper()
	up, err := upstream.NewClient(upstream.Options{BaseURL: fake.URL, UserAgent: "test-agent", RootCAs: fake.CAs, MaxRequests: max})
	if err != nil {
		t.Fatal(err)
	}
	return up
}

func itemRequests(f *fakeupstream.Server) []string {
	var ids []string
	f.Each(func(r fakeupstream.Request) {
		if r.Path == "/api/m3/v3/item" {
			v, _ := url.ParseQuery(r.Query)
			ids = append(ids, v.Get("id"))
		}
	})
	return ids
}

func all(t *testing.T, g *Ingestor, dir string) map[string]model.Program {
	t.Helper()
	res, err := reader(t, dir).Search(context.Background(), "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]model.Program{}
	for _, p := range res.Items {
		m[p.ProgramID] = p
	}
	return m
}

func TestDailyRecordsAvailabilityFromRequestsItAlreadySends(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	res, err := g.Daily(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Observed != 13 || res.Changed != 13 {
		t.Fatalf("every program carries a playable flag in the recorded data: %+v", res)
	}
	// no request besides the two list downloads: the observation is free
	if got := fake.Paths(); len(got) != 2 {
		t.Fatalf("availability must not cost extra requests: %v", got)
	}

	// compare with the recorded upstream flags
	want := map[string]bool{}
	for id, raw := range fake.Items {
		var f struct {
			Playable *bool `json:"playable"`
		}
		_ = json.Unmarshal(raw, &f)
		if f.Playable != nil {
			want[id] = *f.Playable
		}
	}
	trues := 0
	for id, p := range all(t, g, dir) {
		if !p.PlayableKnown || p.Playable != want[id] {
			t.Errorf("%s: stored %v/%v, upstream says %v", id, p.PlayableKnown, p.Playable, want[id])
		}
		if p.Playable {
			trues++
		}
	}
	if trues == 0 || trues == 13 {
		t.Fatalf("the recorded data should contain both playable and non-playable programs, got %d playable", trues)
	}
}

func TestAvailabilityChangeIsPickedUpWithoutTouchingTheDescription(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := all(t, g, dir)

	// next time the upstream flips the first daily item and (hypothetically) renames it
	var doc struct {
		Program []map[string]any `json:"program"`
	}
	_ = json.Unmarshal(fakeupstream.Fixture(t, "daily_program.json"), &doc)
	id := doc.Program[0]["id"].(string)
	was := doc.Program[0]["playable"].(bool)
	doc.Program[0]["playable"] = !was
	doc.Program[0]["title"] = "ÁTNEVEZETT"
	body, _ := json.Marshal(doc)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/m3/v3/daily_program" {
			_, _ = w.Write(body)
			return true
		}
		return false
	}
	g.Now = func() time.Time { return time.Now().Add(time.Hour) }
	res, err := g.Daily(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 1 || res.New != 0 {
		t.Fatalf("exactly one flag changed: %+v", res)
	}
	after := all(t, g, dir)
	if after[id].Playable == was {
		t.Fatal("the new availability was not stored")
	}
	if after[id].Title != before[id].Title {
		t.Fatalf("the title must keep its first seen value, got %q", after[id].Title)
	}
	if after[id].PlayableChangedAt <= before[id].PlayableChangedAt {
		t.Fatal("the change time was not updated")
	}
}

func TestRecheckIsOffUnlessEnabled(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Requests = nil
	res, err := g.Recheck(context.Background())
	if err != nil || res.Summary.Published || len(fake.Requests) != 0 {
		t.Fatalf("a disabled recheck must do nothing: %+v %v, %d requests", res, err, len(fake.Requests))
	}
	_ = dir
}

func TestRecheckIsGentle(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	clock := time.Unix(1_000_000, 0)
	g.Now = func() time.Time { return clock }
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Requests = nil

	g.RecheckPerRun, g.RecheckDelay = 3, 60*time.Millisecond
	clock = clock.Add(time.Hour)
	start := time.Now()
	res, err := g.Recheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Rechecked != 3 {
		t.Fatalf("exactly the configured number of programs must be re-queried: %+v", res)
	}
	if n := len(itemRequests(fake)); n != 3 {
		t.Fatalf("%d item requests, want 3", n)
	}
	if d := time.Since(start); d < 120*time.Millisecond {
		t.Fatalf("3 requests with a 60 ms pause took only %s", d)
	}
	first := itemRequests(fake)

	// the next run continues with the programs that were checked longest ago, not the same ones
	fake.Requests = nil
	clock = clock.Add(time.Hour)
	if _, err := g.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range itemRequests(fake) {
		for _, f := range first {
			if id == f {
				t.Fatalf("%s was checked again before the stale ones", id)
			}
		}
	}
	_ = dir
}

func TestRecheckMarksVanishedProgramsAndKeepsTheirData(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone := ""
	for id := range fake.Items {
		gone = id
		break
	}
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/m3/v3/item" && r.URL.Query().Get("id") == gone {
			_, _ = w.Write([]byte("null"))
			return true
		}
		return false
	}
	before := all(t, g, dir)[gone]
	g.RecheckPerRun, g.RecheckDelay = 100, time.Millisecond
	g.Now = func() time.Time { return time.Now().Add(time.Hour) }
	res, err := g.Recheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Gone != 1 {
		t.Fatalf("%+v", res)
	}
	p := all(t, g, dir)[gone]
	if p.GoneAt == 0 || p.Title != before.Title || p.Description != before.Description {
		t.Fatalf("a vanished program must be marked and keep its data: %+v", p)
	}
	// and it is not asked about again
	fake.Requests = nil
	if _, err := g.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range itemRequests(fake) {
		if id == gone {
			t.Fatal("a vanished program is re-queried every run")
		}
	}
}

func TestRecheckDoesNotRequeryItemsThatCarryNoFlag(t *testing.T) {
	g, fake, _ := setup(t)
	g.ImagesPerRun = 0
	clock := time.Unix(2_000_000, 0)
	g.Now = func() time.Time { return clock }
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	// every item answer lacks the playable flag: without special care the same programs would be asked forever
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/m3/v3/item" {
			raw := string(fake.Items[r.URL.Query().Get("id")])
			raw = strings.Replace(raw, `"playable":`, `"playable_":`, 1)
			raw = strings.Replace(raw, `"playable" :`, `"playable_" :`, 1)
			_, _ = w.Write([]byte(raw))
			return true
		}
		return false
	}
	g.RecheckPerRun, g.RecheckDelay = 5, time.Millisecond
	seen := map[string]int{}
	for i := 0; i < 2; i++ {
		fake.Requests = nil
		clock = clock.Add(time.Hour)
		if _, err := g.Recheck(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, id := range itemRequests(fake) {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("%s was re-queried %d times in two runs although it had just been checked", id, n)
		}
	}
	if len(seen) != 10 {
		t.Fatalf("two runs of 5 must cover 10 different programs, got %d", len(seen))
	}
}

func TestRequestBudgetStopsTheWorkGracefully(t *testing.T) {
	g, fake, dir := setup(t)
	g.Up = clientWith(t, fake, 4) // 2 list downloads + room for only 2 cover images
	res, err := g.Daily(context.Background())
	if err != nil {
		t.Fatalf("running into the budget is not an error: %v", err)
	}
	if !res.Summary.Published || res.New != 13 || res.Images != 2 || !res.BudgetStopped {
		t.Fatalf("%+v", res)
	}
	if g.Up.Requests() != 4 || len(fake.Requests) != 4 {
		t.Fatalf("the ceiling was exceeded: %d / %d", g.Up.Requests(), len(fake.Requests))
	}
	if list := all(t, g, dir); len(list) != 13 {
		t.Fatalf("%d stored", len(list))
	}
}

func TestBudgetEndsARecheckEarly(t *testing.T) {
	g, fake, _ := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.Up = clientWith(t, fake, 2)
	g.RecheckPerRun, g.RecheckDelay = 10, time.Millisecond
	g.Now = func() time.Time { return time.Now().Add(time.Hour) }
	res, err := g.Recheck(context.Background())
	if err != nil || res.Rechecked != 2 || !res.BudgetStopped {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestWeeklyRunningOutOfBudgetWhileScanningPublishesNothing(t *testing.T) {
	g, fake, dir := setup(t)
	g.Up = clientWith(t, fake, 3)
	if _, err := g.Weekly(context.Background()); err == nil {
		t.Fatal("a scan cut short by the budget must not be published as a complete run")
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFile)); err == nil {
		t.Fatal("a database was published after an incomplete scan")
	}
}

func TestMigrateCommandNeedsNoUpstreamRequests(t *testing.T) {
	g, fake, _ := setup(t)
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Requests = nil
	res, err := g.Migrate(context.Background())
	if err != nil || !res.Summary.Published || len(fake.Requests) != 0 {
		t.Fatalf("%+v %v %d", res, err, len(fake.Requests))
	}
}
