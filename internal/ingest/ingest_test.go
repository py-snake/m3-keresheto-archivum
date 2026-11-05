package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
	"github.com/py-snake/m3-keresheto-archivum/internal/images"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
	"github.com/py-snake/m3-keresheto-archivum/internal/upstream"
)

func setup(t *testing.T) (*Ingestor, *fakeupstream.Server, string) {
	t.Helper()
	fake := fakeupstream.New(t)
	up, err := upstream.NewClient(upstream.Options{BaseURL: fake.URL, UserAgent: "test-agent", RootCAs: fake.CAs, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	g := &Ingestor{
		Dir: dir, Keep: 2, Up: up, ImagesPerRun: 100, ImageMaxWidth: 200,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return g, fake, dir
}

func reader(t *testing.T, dir string) *store.Reader {
	t.Helper()
	r, err := store.OpenReader(filepath.Join(dir, store.DBFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestDailyStoresProgramsAndImages(t *testing.T) {
	g, fake, dir := setup(t)
	res, err := g.Daily(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 6 daily + 8 guide items, one id appears twice in the guide
	if res.New != 13 || res.Summary.After != 13 || !res.Summary.Published {
		t.Fatalf("%+v", res)
	}
	if res.Images != 13 {
		t.Fatalf("expected 13 cover images, got %d", res.Images)
	}

	r := reader(t, dir)
	list, _ := r.Search(context.Background(), "", 100, 0)
	if list.Total != 13 {
		t.Fatalf("stored %d", list.Total)
	}
	root, err := os.OpenRoot(filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, p := range list.Items {
		if p.ImageState != 1 {
			t.Errorf("%s: image_state %d", p.ProgramID, p.ImageState)
		}
		f, _, err := images.Open(root, p.ProgramID)
		if err != nil {
			t.Errorf("%s: %v", p.ProgramID, err)
			continue
		}
		f.Close()
	}

	// every request used the configured User-Agent
	if ua := fake.UserAgents(); len(ua) != 1 || ua["test-agent"] == 0 {
		t.Fatalf("user agents: %v", ua)
	}
}

func TestDailyIsIdempotentAndDoesNotRedownloadImages(t *testing.T) {
	g, fake, dir := setup(t)
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	imageRequests := len(imageHits(fake))

	res, err := g.Daily(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 || res.Images != 0 || res.Summary.After != 13 {
		t.Fatalf("second run: %+v", res)
	}
	if got := len(imageHits(fake)); got != imageRequests {
		t.Fatalf("images downloaded again: %d -> %d requests", imageRequests, got)
	}
	// the first state is kept as a backup
	if ents, _ := os.ReadDir(filepath.Join(dir, "backups")); len(ents) != 1 {
		t.Fatalf("backups: %v", ents)
	}
}

func imageHits(f *fakeupstream.Server) []string {
	var out []string
	for _, p := range f.Paths() {
		if strings.HasPrefix(p, "/images/") {
			out = append(out, p)
		}
	}
	return out
}

func TestImageBudgetAndBackfill(t *testing.T) {
	g, _, dir := setup(t)
	g.ImagesPerRun = 3
	res, err := g.Daily(context.Background())
	if err != nil || res.Images != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	g.ImagesPerRun = 100
	res, err = g.Images(context.Background())
	if err != nil || res.Images != 10 || res.New != 0 {
		t.Fatalf("backfill: %+v %v", res, err)
	}
	r := reader(t, dir)
	list, _ := r.Search(context.Background(), "", 100, 0)
	for _, p := range list.Items {
		if p.ImageState != 1 {
			t.Fatalf("%s still pending", p.ProgramID)
		}
	}
}

func TestMissingImageIsRecordedNotFatal(t *testing.T) {
	g, fake, dir := setup(t)
	missing := "M3-59945851A60999998"
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/images/m3/"+missing { // HTTP 200 + HTML, like the real site
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(fakeupstream.Fixture(t, "cover_notfound.html"))
			return true
		}
		return false
	}
	res, err := g.Daily(context.Background())
	if err != nil {
		t.Fatalf("a missing image must not fail the run: %v", err)
	}
	if res.Images != 12 {
		t.Fatalf("images stored: %d", res.Images)
	}
	list, _ := reader(t, dir).Search(context.Background(), missing, 10, 0)
	if len(list.Items) != 1 || list.Items[0].ImageState != 2 {
		t.Fatalf("%+v", list.Items)
	}
	if _, err := os.Stat(filepath.Join(dir, "images", missing+".jpg")); err == nil {
		t.Fatal("the HTML page was saved as an image")
	}
}

func TestHostileImagesAreNotStored(t *testing.T) {
	g, fake, dir := setup(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/images/") {
			_, _ = w.Write([]byte("<svg onload=alert(1)>")) // 200, not an image
			return true
		}
		return false
	}
	res, err := g.Daily(context.Background())
	if err != nil || res.Images != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "images")); len(ents) != 0 {
		t.Fatalf("files stored: %v", ents)
	}
}

func TestPartialFailurePublishesWhatItGotAndReportsAnError(t *testing.T) {
	g, fake, dir := setup(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/m3/v3/program_guide" {
			http.Error(w, "down", http.StatusBadGateway)
			return true
		}
		return false
	}
	res, err := g.Daily(context.Background())
	if err == nil {
		t.Fatal("a partial failure must surface as an error (exit code) for the scheduler")
	}
	if !res.Summary.Published || res.New != 6 {
		t.Fatalf("the daily part must still be published: %+v", res)
	}
	if list, _ := reader(t, dir).Search(context.Background(), "", 100, 0); list.Total != 6 {
		t.Fatalf("stored %d", list.Total)
	}
}

func TestTotalFailurePublishesNothing(t *testing.T) {
	g, fake, dir := setup(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return true
	}
	res, err := g.Daily(context.Background())
	if err == nil || res.Summary.Published {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFile)); err == nil {
		t.Fatal("a database was published")
	}
}

func TestCircuitBreakerOnBrokenUpstreamData(t *testing.T) {
	g, fake, dir := setup(t)
	if _, err := g.Daily(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, store.DBFile))

	// the upstream suddenly sends 30 items, half of them malformed (a schema change)
	var items []json.RawMessage
	for i := 0; i < 30; i++ {
		if i%2 == 0 {
			items = append(items, json.RawMessage(fmt.Sprintf(`{"id":"M3-OK%d","title":"fine"}`, i)))
		} else {
			items = append(items, json.RawMessage(`{"id":"???","titel":"renamed field"}`))
		}
	}
	body, _ := json.Marshal(map[string]any{"program": items})
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/m3/v3/daily_program" {
			_, _ = w.Write(body)
			return true
		}
		return false
	}
	res, err := g.Daily(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to publish") {
		t.Fatalf("breaker did not trip: %+v %v", res, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, store.DBFile))
	if string(before) != string(after) {
		t.Fatal("the database changed although the breaker tripped")
	}
}

func TestAllInvalidItemsTripTheBreakerEvenWhenFew(t *testing.T) {
	g, fake, _ := setup(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/api/m3/v3/daily_program":
			_, _ = w.Write([]byte(`{"program":[null,{"x":1}]}`))
			return true
		case "/api/m3/v3/program_guide":
			_, _ = w.Write([]byte(`{"program_guides":{}}`))
			return true
		}
		return false
	}
	if _, err := g.Daily(context.Background()); err == nil {
		t.Fatal("garbage-only response accepted")
	}
}

func TestCancelledRunPublishesNothing(t *testing.T) {
	g, _, dir := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.Daily(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFile)); err == nil {
		t.Fatal("database published after cancellation")
	}
}

func TestWeeklyFollowsSeriesAndPagination(t *testing.T) {
	g, fake, dir := setup(t)
	// teach the fake /item endpoint every id that the two series pages mention
	ids, err := g.Up.SeriesProgramIDs(context.Background(), fakeupstream.PagedSeries)
	if err != nil || len(ids) != 27 {
		t.Fatalf("%d %v", len(ids), err)
	}
	template := fake.Items["M3-59945851A60999998"]
	for _, id := range ids {
		fake.Items[id] = json.RawMessage(strings.Replace(string(template), "M3-59945851A60999998", id, 1))
	}
	// every collection holds two single programs and the two-page series; the series is listed
	// in all 8 collections but must be walked only once
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/m3/get-open" && r.URL.Query().Get("collection") != "" {
			_, _ = w.Write([]byte(`{"docs":[{"id":"M3-59945851A60999998"},{"id":"M3-87A98999A60999894"},{"id":"M3-X","seriesId":"` + fakeupstream.PagedSeries + `"}]}`))
			return true
		}
		return false
	}
	fake.Requests = nil

	res, err := g.Weekly(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 29 || !res.Summary.Published { // 2 single + 27 from the series
		t.Fatalf("%+v", res)
	}
	if fake.Count("/m3/get-open") != 1+8 {
		t.Fatalf("expected the list + 8 collections, got %d", fake.Count("/m3/get-open"))
	}
	if fake.Count("/m3/open") != 2 {
		t.Fatalf("the series must be walked once (2 pages), got %d page requests", fake.Count("/m3/open"))
	}
	list, _ := reader(t, dir).Search(context.Background(), "", 100, 0)
	if list.Total != 29 {
		t.Fatalf("stored %d", list.Total)
	}

	// a second weekly run finds nothing missing and fetches no items again
	fake.Requests = nil
	res, err = g.Weekly(context.Background())
	if err != nil || res.New != 0 || fake.Count("/api/m3/v3/item") != 0 {
		t.Fatalf("second run: %+v %v item requests: %d", res, err, fake.Count("/api/m3/v3/item"))
	}
}

func TestWeeklyWithoutCollectionsFails(t *testing.T) {
	g, fake, dir := setup(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/m3/get-open" {
			_, _ = w.Write([]byte(`[]`))
			return true
		}
		return false
	}
	if _, err := g.Weekly(context.Background()); err == nil {
		t.Fatal("an empty collection list must be an error")
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFile)); err == nil {
		t.Fatal("published")
	}
}

func TestItemsAdminCommand(t *testing.T) {
	g, _, dir := setup(t)
	if _, err := g.Items(context.Background(), []string{"nonsense"}); err == nil {
		t.Fatal("malformed id accepted")
	}
	res, err := g.Items(context.Background(), []string{"M3-59945851A60999998", "M3-DOESNOTEXIST"})
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 1 {
		t.Fatalf("%+v", res)
	}
	list, _ := reader(t, dir).Search(context.Background(), "M3-59945851A60999998", 10, 0)
	if len(list.Items) != 1 {
		t.Fatal("not stored")
	}
}

func TestItemLookupBudget(t *testing.T) {
	g, fake, _ := setup(t)
	g.MaxItems = 2
	var ids []string
	for id := range fake.Items {
		ids = append(ids, id)
	}
	res, err := g.Items(context.Background(), ids[:5])
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 2 {
		t.Fatalf("budget not applied: %+v", res)
	}
}
