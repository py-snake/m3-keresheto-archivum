package upstream

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
)

func direct(t *testing.T, fake *fakeupstream.Server) *Client {
	t.Helper()
	c, err := NewClient(Options{BaseURL: fake.URL, UserAgent: "test-agent", RootCAs: fake.CAs, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(Options{BaseURL: "http://nemzetiarchivum.hu", UserAgent: "x"}); err == nil {
		t.Error("plain http base URL accepted")
	}
	if _, err := NewClient(Options{BaseURL: "https://user:pw@nemzetiarchivum.hu", UserAgent: "x"}); err == nil {
		t.Error("credentials in base URL accepted")
	}
	if _, err := NewClient(Options{UserAgent: ""}); err == nil {
		t.Error("empty User-Agent accepted")
	}
	if _, err := NewClient(Options{UserAgent: "x"}); err != nil {
		t.Errorf("default base URL rejected: %v", err)
	}
}

func TestEndpointsAgainstRecordedResponses(t *testing.T) {
	fake := fakeupstream.New(t)
	c := direct(t, fake)
	ctx := context.Background()

	daily, err := c.DailyProgram(ctx)
	if err != nil || len(daily) != 6 {
		t.Fatalf("DailyProgram: %d items, %v", len(daily), err)
	}
	guide, err := c.ProgramGuide(ctx)
	if err != nil || len(guide) != 8 {
		t.Fatalf("ProgramGuide: %d items, %v", len(guide), err)
	}
	progs, rep := ParsePrograms(append(daily, guide...))
	if rep.Invalid != 0 || len(progs) != 14 {
		t.Fatalf("recorded real items must all parse: %+v", rep)
	}

	ids, err := c.CollectionIDs(ctx)
	if err != nil || len(ids) != 8 {
		t.Fatalf("CollectionIDs: %v %v", ids, err)
	}
	docs, err := c.Collection(ctx, ids[0])
	if err != nil || len(docs) != 11 {
		t.Fatalf("Collection: %d docs, %v", len(docs), err)
	}
	series := 0
	for _, d := range docs {
		if d.SeriesID != "" {
			series++
		}
	}
	if series != 1 {
		t.Fatalf("expected 1 series doc, got %d", series)
	}

	raw, err := c.Item(ctx, "M3-59945851A60999998")
	if err != nil {
		t.Fatal(err)
	}
	if p, _, err := ParseProgram(raw); err != nil || p.ProgramID != "M3-59945851A60999998" || p.ReleasedAt == 0 {
		t.Fatalf("Item parse: %+v %v", p, err)
	}
}

func TestItemNullIsNotFound(t *testing.T) {
	fake := fakeupstream.New(t)
	c := direct(t, fake)
	_, err := c.Item(context.Background(), "M3-DOESNOTEXIST")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("the 200 + null answer must be ErrNotFound, got %v", err)
	}
}

func TestMalformedIDsNeverReachTheNetwork(t *testing.T) {
	fake := fakeupstream.New(t)
	c := direct(t, fake)
	ctx := context.Background()
	for _, bad := range []string{"", "m3-abc", "M3-A/../x", "M3-A?x=1", "M3-A\x00", "M3-A B"} {
		if _, err := c.Item(ctx, bad); err == nil {
			t.Errorf("Item(%q) accepted", bad)
		}
		if _, err := c.Image(ctx, bad); err == nil {
			t.Errorf("Image(%q) accepted", bad)
		}
	}
	for _, bad := range []string{"", "a b", "a&b=c", "a#b", "../x", "a\nb", strings.Repeat("A", 400)} {
		if _, err := c.SeriesProgramIDs(ctx, bad); err == nil {
			t.Errorf("SeriesProgramIDs(%q) accepted", bad)
		}
		if _, err := c.Collection(ctx, bad); err == nil {
			t.Errorf("Collection(%q) accepted", bad)
		}
	}
	if len(fake.Requests) != 0 {
		t.Fatalf("invalid identifiers produced requests: %v", fake.Paths())
	}
}

func TestSeriesPagination(t *testing.T) {
	fake := fakeupstream.New(t)
	c := direct(t, fake)

	ids, err := c.SeriesProgramIDs(context.Background(), fakeupstream.PagedSeries)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 27 { // 20 on page 1, 7 on page 2 (recorded)
		t.Fatalf("expected 27 ids over 2 pages, got %d", len(ids))
	}
	if n := fake.Count("/m3/open"); n != 2 {
		t.Fatalf("expected 2 page requests (1..max), got %d", n)
	}

	single, err := c.SeriesProgramIDs(context.Background(), fakeupstream.SingleSeries)
	if err != nil || len(single) != 2 {
		t.Fatalf("single page series: %v %v", single, err)
	}
}

func TestLayoutChangeIsAnError(t *testing.T) {
	fake := fakeupstream.New(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/m3/open" {
			_, _ = w.Write([]byte("<html><body>totally different markup</body></html>"))
			return true
		}
		return false
	}
	c := direct(t, fake)
	_, err := c.SeriesProgramIDs(context.Background(), fakeupstream.SingleSeries)
	if !errors.Is(err, ErrLayoutChanged) {
		t.Fatalf("expected ErrLayoutChanged, got %v", err)
	}
}

func TestStatusAndSizeLimits(t *testing.T) {
	fake := fakeupstream.New(t)
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/m3/get-open":
			http.Error(w, "boom", http.StatusInternalServerError)
			return true
		case "/api/m3/v3/daily_program":
			_, _ = w.Write([]byte(strings.Repeat("A", 5000)))
			return true
		}
		return false
	}
	c, _ := NewClient(Options{BaseURL: fake.URL, UserAgent: "x", RootCAs: fake.CAs, MaxBody: 1000})

	var se *StatusError
	if _, err := c.CollectionIDs(context.Background()); !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("expected a 500 StatusError, got %v", err)
	}
	if _, err := c.DailyProgram(context.Background()); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized body accepted: %v", err)
	}
}

func TestRedirectsStayOnTheAllowedHost(t *testing.T) {
	fake := fakeupstream.New(t)
	other := fakeupstream.New(t) // a second TLS server = a different host:port
	fake.Override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/m3/get-open" {
			http.Redirect(w, r, other.URL+"/m3/get-open", http.StatusFound)
			return true
		}
		return false
	}
	c := direct(t, fake)
	if _, err := c.CollectionIDs(context.Background()); err == nil {
		t.Fatal("redirect to another host followed")
	}
	if len(other.Requests) != 0 {
		t.Fatal("the other host was contacted")
	}
}

func TestUserAgentIsAlwaysTheConfiguredOne(t *testing.T) {
	fake := fakeupstream.New(t)
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"
	c, _ := NewClient(Options{BaseURL: fake.URL, UserAgent: ua, RootCAs: fake.CAs})
	ctx := context.Background()
	_, _ = c.DailyProgram(ctx)
	_, _ = c.CollectionIDs(ctx)
	_, _ = c.Item(ctx, "M3-59945851A60999998")
	_, _ = c.Image(ctx, "M3-59945851A60999998")
	if got := fake.UserAgents(); len(got) != 1 || got[ua] != 4 {
		t.Fatalf("every request must carry the browser User-Agent, saw %v", got)
	}
}

func TestThrottle(t *testing.T) {
	fake := fakeupstream.New(t)
	c, _ := NewClient(Options{BaseURL: fake.URL, UserAgent: "x", RootCAs: fake.CAs, Delay: 120 * time.Millisecond})
	start := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := c.CollectionIDs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 230*time.Millisecond {
		t.Fatalf("3 requests with a 120 ms delay took only %s", d)
	}
}
