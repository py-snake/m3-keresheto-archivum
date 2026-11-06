package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/store"
)

// change rewrites one field of a recorded item, as if the upstream had edited it.
func change(t *testing.T, raw json.RawMessage, field string, value any) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m[field] = value
	out, _ := json.Marshal(m)
	return out
}

func TestRefreshIsOffUnlessEnabled(t *testing.T) {
	g, fake, _ := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.Requests = nil
	res, err := g.Refresh(context.Background())
	if err != nil || res.Summary.Published || len(fake.Requests) != 0 {
		t.Fatalf("a disabled refresh must do nothing: %+v %v %d", res, err, len(fake.Requests))
	}
}

func TestRefreshUpdatesTheDescriptiveFieldsOnly(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	now := time.Unix(5_000_000, 0)
	g.Now = func() time.Time { return now }
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := all(t, g, dir)

	id := "M3-59945851A60999998"
	fake.Items[id] = change(t, change(t, fake.Items[id], "title", "JAVÍTOTT CÍM"), "description", "Javított leírás.")
	g.RefreshPerRun, g.RefreshDelay = 100, time.Millisecond
	now = now.Add(time.Hour)
	fake.Requests = nil
	res, err := g.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Refreshed != 13 || res.Updated != 1 {
		t.Fatalf("13 programs re-fetched, exactly 1 changed: %+v", res)
	}
	if n := len(itemRequests(fake)); n != 13 {
		t.Fatalf("%d item requests", n)
	}
	after := all(t, g, dir)
	if after[id].Title != "JAVÍTOTT CÍM" || after[id].Description != "Javított leírás." {
		t.Fatalf("the corrected data was not taken over: %+v", after[id])
	}
	if after[id].AddedAt != before[id].AddedAt || after[id].ID != before[id].ID || after[id].RefreshedAt != now.Unix() {
		t.Fatalf("a refresh must keep the row id and added_at and stamp refreshed_at: %+v", after[id])
	}
	// the new title is findable, the old one is not
	if res, _ := reader(t, dir).Search(context.Background(), "JAVÍTOTT CÍM", 10, 0); res.Total != 1 {
		t.Fatalf("the full text index is stale: %+v", res)
	}
	// unchanged programs are untouched apart from the refresh stamp
	for other, p := range after {
		if other != id && (p.Title != before[other].Title || p.AddedAt != before[other].AddedAt) {
			t.Fatalf("%s changed unexpectedly", other)
		}
	}
}

func TestRefreshWorksThroughTheQueueAndNeverExceedsTheLimit(t *testing.T) {
	g, fake, _ := setup(t)
	g.ImagesPerRun = 0
	now := time.Unix(6_000_000, 0)
	g.Now = func() time.Time { return now }
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.RefreshPerRun, g.RefreshDelay = 5, time.Millisecond
	seen := map[string]int{}
	for i := 0; i < 2; i++ {
		fake.Requests = nil
		now = now.Add(time.Hour)
		if _, err := g.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := itemRequests(fake)
		if len(got) != 5 {
			t.Fatalf("run %d sent %d requests, the limit is 5", i+1, len(got))
		}
		for _, id := range got {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("%s was refreshed twice before the others had their turn", id)
		}
	}
	if len(seen) != 10 {
		t.Fatalf("two runs of 5 must cover 10 programs, got %d", len(seen))
	}
}

func TestRefreshRefusesToPublishWhenTheUpstreamWouldEraseTheText(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, store.DBFile))
	// the upstream is degraded: every item answers without description, creators and genre
	for id, raw := range fake.Items {
		raw = change(t, raw, "description", "")
		raw = change(t, raw, "short_description", "")
		raw = change(t, raw, "creators", []string{})
		fake.Items[id] = change(t, raw, "genre", []string{})
	}
	g.RefreshPerRun, g.RefreshDelay = 100, time.Millisecond
	g.Now = func() time.Time { return time.Now().Add(time.Hour) }
	res, err := g.Refresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to publish") {
		t.Fatalf("the degraded answers were accepted: %+v %v", res, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, store.DBFile))
	if string(before) != string(after) {
		t.Fatal("the database changed although the refresh was refused")
	}
}

func TestRefreshMarksVanishedProgramsAndPushesBadAnswersBack(t *testing.T) {
	g, fake, dir := setup(t)
	g.ImagesPerRun = 0
	now := time.Unix(7_000_000, 0)
	g.Now = func() time.Time { return now }
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone, broken := "M3-59945851A60999998", "M3-87A98999A60999894"
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/m3/v3/item" {
			return false
		}
		switch r.URL.Query().Get("id") {
		case gone:
			_, _ = w.Write([]byte("null"))
			return true
		case broken:
			_, _ = w.Write([]byte(`{"id":"M3-87A98999A60999894"}`)) // no title: invalid
			return true
		}
		return false
	}
	g.RefreshPerRun, g.RefreshDelay = 100, time.Millisecond
	now = now.Add(time.Hour)
	res, err := g.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Gone != 1 || res.Refreshed != 11 {
		t.Fatalf("%+v", res)
	}
	m := all(t, g, dir)
	if m[gone].GoneAt == 0 || m[gone].Title == "" {
		t.Fatalf("a vanished program must be marked and keep its data: %+v", m[gone])
	}
	if m[broken].Title == "" || m[broken].RefreshedAt != now.Unix() {
		t.Fatalf("an unusable answer must not damage the data, and the program goes to the back of the queue: %+v", m[broken])
	}
}

func TestRefreshStopsAtTheRequestBudget(t *testing.T) {
	g, fake, _ := setup(t)
	g.ImagesPerRun = 0
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	g.Up = clientWith(t, fake, 3)
	g.RefreshPerRun, g.RefreshDelay = 50, time.Millisecond
	g.Now = func() time.Time { return time.Now().Add(time.Hour) }
	res, err := g.Refresh(context.Background())
	if err != nil || res.Refreshed != 3 || !res.BudgetStopped {
		t.Fatalf("%+v %v", res, err)
	}
}
