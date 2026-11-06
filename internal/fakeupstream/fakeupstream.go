// Package fakeupstream is a local stand-in for the M3 site, built from recorded real
// responses (tests/fixtures). It exists for tests only.
package fakeupstream

import (
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// PagedSeries has two pages in the fixtures, SingleSeries one.
const (
	PagedSeries  = "RsWRdMOpcg=="
	SingleSeries = "RGV2aWN0dXMgVmluY2l0IOKAkyBMZWd5xZF6dmUgZ3nFkXo="
	// MissingImageID is answered with HTTP 200 and an HTML page, like the real site does.
	MissingImageID = "M3-DOESNOTEXIST"
)

// Server is the fake site. Tweak its fields before sending requests.
type Server struct {
	*httptest.Server
	CAs *x509.CertPool

	mu       sync.Mutex
	Requests []Request
	// Items answers /item for these ids (id -> JSON); filled from the daily and guide fixtures.
	Items map[string]json.RawMessage
	// Override lets a test replace the answer for a path: return true when it handled the request.
	Override func(w http.ResponseWriter, r *http.Request) bool
}

// Request is a recorded request.
type Request struct {
	Path, Query, UserAgent, Accept string
}

// FixtureDir returns the absolute path of tests/fixtures.
func FixtureDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "tests", "fixtures")
}

// Fixture reads a fixture file.
func Fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(FixtureDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// New starts the fake site (TLS, like the real one).
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{Items: map[string]json.RawMessage{}}

	var daily struct {
		Program []json.RawMessage `json:"program"`
	}
	var guide struct {
		Guides map[string][]json.RawMessage `json:"program_guides"`
	}
	_ = json.Unmarshal(Fixture(t, "daily_program.json"), &daily)
	_ = json.Unmarshal(Fixture(t, "program_guide.json"), &guide)
	all := append([]json.RawMessage{}, daily.Program...)
	for _, d := range guide.Guides {
		all = append(all, d...)
	}
	for _, raw := range all {
		var h struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &h)
		s.Items[h.ID] = raw
	}

	mux := http.NewServeMux()
	serve := func(name, ctype string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			_, _ = w.Write(Fixture(t, name))
		}
	}
	// the real site serves JSON as text/html
	mux.HandleFunc("/api/m3/v3/daily_program", serve("daily_program.json", "text/html; charset=UTF-8"))
	mux.HandleFunc("/api/m3/v3/program_guide", serve("program_guide.json", "text/html; charset=UTF-8"))
	mux.HandleFunc("/m3/get-open", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		if r.URL.Query().Get("collection") != "" {
			_, _ = w.Write(Fixture(t, "collection.json"))
			return
		}
		_, _ = w.Write(Fixture(t, "get_open.json"))
	})
	mux.HandleFunc("/m3/open", func(w http.ResponseWriter, r *http.Request) {
		series, page := r.URL.Query().Get("series"), r.URL.Query().Get("page")
		switch {
		case series == PagedSeries && page == "1":
			_, _ = w.Write(Fixture(t, "series_paged_1.html"))
		case series == PagedSeries && page == "2":
			_, _ = w.Write(Fixture(t, "series_paged_2.html"))
		case series == PagedSeries:
			// past the last page the real site answers with an empty body
		default:
			_, _ = w.Write(Fixture(t, "series_single.html"))
		}
	})
	mux.HandleFunc("/api/m3/v3/item", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		s.mu.Lock()
		raw, ok := s.Items[r.URL.Query().Get("id")]
		s.mu.Unlock()
		if !ok {
			_, _ = w.Write([]byte("null")) // unknown id: HTTP 200 + null
			return
		}
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/images/m3/", func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == MissingImageID {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = w.Write(Fixture(t, "cover_notfound.html"))
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(Fixture(t, "cover.jpg"))
	})

	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.Requests = append(s.Requests, Request{
			Path: r.URL.Path, Query: r.URL.RawQuery, UserAgent: r.UserAgent(), Accept: r.Header.Get("Accept"),
		})
		override := s.Override
		s.mu.Unlock()
		if override != nil && override(w, r) {
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Server.Close)

	s.CAs = x509.NewCertPool()
	s.CAs.AddCert(s.Server.Certificate())
	return s
}

// Each calls f for every recorded request, in order.
func (s *Server) Each(f func(Request)) {
	s.mu.Lock()
	reqs := append([]Request(nil), s.Requests...)
	s.mu.Unlock()
	for _, r := range reqs {
		f(r)
	}
}

// Count returns how many recorded requests had the given path.
func (s *Server) Count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.Requests {
		if r.Path == path {
			n++
		}
	}
	return n
}

// Paths returns the recorded request paths in order.
func (s *Server) Paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.Requests))
	for i, r := range s.Requests {
		out[i] = r.Path
	}
	return out
}

// UserAgents returns the distinct User-Agent values seen.
func (s *Server) UserAgents() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[string]int{}
	for _, r := range s.Requests {
		m[r.UserAgent]++
	}
	return m
}

// ItemIDs returns the ids the fake /item endpoint knows.
func (s *Server) ItemIDs() []string {
	var ids []string
	for id := range s.Items {
		ids = append(ids, id)
	}
	return ids
}
