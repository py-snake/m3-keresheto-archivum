// Package web is the HTTP side of m3 serve.
//
// It is deliberately small: three kinds of GET routes, no request bodies, no cookies, no
// sessions, no JavaScript. The database is opened read-only behind an authorizer (internal/store)
// and the container it runs in has no outbound network and a read-only file system.
package web

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/config"
	"github.com/py-snake/m3-keresheto-archivum/internal/images"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
	webfs "github.com/py-snake/m3-keresheto-archivum/web"
)

const (
	siteTitle    = "m3 kereshető archívum"
	maxURLLength = 2048
	queryTimeout = 5 * time.Second
	maxTracked   = 20000

	// csp allows nothing but same-origin styles and images (the CSS embeds small data: SVGs) and
	// forbids scripts, frames, plugins, base tag changes and cross-origin form posts.
	csp = "default-src 'none'; style-src 'self'; img-src 'self' data:; font-src 'self'; form-action 'self'; " +
		"base-uri 'none'; frame-ancestors 'none'; object-src 'none'"
)

// Options configures a Server.
type Options struct {
	DataDir        string
	Source         *store.Source
	Log            *slog.Logger
	TrustedProxies []netip.Prefix
	RateLimit      float64
	RateBurst      int
	MaxInFlight    int
	Now            func() time.Time
}

// Server is the m3 web application.
type Server struct {
	o        Options
	tpl      *template.Template
	static   http.Handler
	limiter  *limiter
	anon     *anonymizer
	inflight chan struct{}
	now      func() time.Time

	imgMu   sync.Mutex
	imgRoot *os.Root
}

// New builds the server.
func New(o Options) (*Server, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.RateLimit <= 0 {
		o.RateLimit = 3
	}
	if o.RateBurst <= 0 {
		o.RateBurst = 15
	}
	if o.MaxInFlight <= 0 {
		o.MaxInFlight = 64
	}
	tpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(webfs.FS, "static")
	if err != nil {
		return nil, err
	}
	return &Server{
		o:        o,
		tpl:      tpl,
		static:   http.StripPrefix("/static/", http.FileServerFS(noDirs{sub})),
		limiter:  newLimiter(o.RateLimit, o.RateBurst, maxTracked, o.Now),
		anon:     &anonymizer{now: o.Now},
		inflight: make(chan struct{}, o.MaxInFlight),
		now:      o.Now,
	}, nil
}

// Close releases the images directory handle.
func (s *Server) Close() {
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if s.imgRoot != nil {
		_ = s.imgRoot.Close()
		s.imgRoot = nil
	}
}

// Handler returns the complete handler chain.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /{page}", s.index)
	mux.HandleFunc("GET /img/{file}", s.image)
	mux.HandleFunc("GET /static/", s.staticFile)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /healthz", s.health)

	// access sits outside guard so that rejected requests (429/503) are logged too
	return s.recoverer(s.secure(s.access(s.guard(mux))))
}

// noDirs hides directory listings of the embedded static files.
type noDirs struct{ fs fs.FS }

func (n noDirs) Open(name string) (fs.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		_ = f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}

// ---- handlers ----

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := r.PathValue("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > MaxPage || strconv.Itoa(n) != p {
			http.NotFound(w, r)
			return
		}
		page = n
	}

	q := r.URL.Query()
	_, raw := q["nyers"]
	term, valid := store.NormalizeTerm(q.Get("kereses"))
	onlyPlayable := q.Get("elerheto") == "1"

	data := pageData{Title: siteTitle, Search: term, Raw: raw, OnlyPlayable: onlyPlayable, Today: s.now()}
	status := http.StatusOK

	switch reader := s.o.Source.Reader(); {
	case !valid:
		data.Search = ""
		data.Notice = "Érvénytelen keresőszó."
	case reader == nil:
		data.Notice = "Az adatbázis még nem készült el, kérlek próbáld később."
		status = http.StatusServiceUnavailable
	default:
		ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
		defer cancel()
		res, err := reader.SearchQuery(ctx, store.Query{Term: term, OnlyPlayable: onlyPlayable, Limit: PageSize, Offset: (page - 1) * PageSize})
		switch {
		case errors.Is(err, store.ErrTermTooShort):
			data.Notice = "Legalább " + strconv.Itoa(store.MinTermRunes) + " karakter szükséges a kereséshez."
		case errors.Is(err, context.DeadlineExceeded):
			data.Notice = "A keresés túl sokáig tartott, kérlek pontosítsd."
			status = http.StatusServiceUnavailable
		case err != nil:
			s.o.Log.Error("search failed", "err", err)
			http.Error(w, "Belső hiba.", http.StatusInternalServerError)
			return
		default:
			data.Items, data.Total, data.Capped = res.Items, res.Total, res.Capped
			data.Pager = buildPager(page, res.Total, term, raw, onlyPlayable)
			if term != "" {
				data.Title = term + " - " + siteTitle
			}
		}
	}

	var buf bytes.Buffer // render completely first: a template error must not leave a half page
	if err := s.tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.o.Log.Error("rendering failed", "err", err)
		http.Error(w, "Belső hiba.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	if status == http.StatusOK {
		h.Set("Cache-Control", "public, max-age=60")
	} else {
		h.Set("Cache-Control", "no-store")
		h.Set("Retry-After", "30")
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("file"), ".jpg")
	root := s.imagesRoot()
	if !ok || root == nil {
		http.NotFound(w, r)
		return
	}
	f, info, err := images.Open(root, id) // validates the id; cannot leave the directory
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// imagesRoot opens <data>/images lazily: it may not exist before the first ingest.
func (s *Server) imagesRoot() *os.Root {
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if s.imgRoot == nil {
		root, err := os.OpenRoot(filepath.Join(s.o.DataDir, "images"))
		if err != nil {
			return nil
		}
		s.imgRoot = root
	}
	return s.imgRoot
}

func (s *Server) staticFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	s.static.ServeHTTP(w, r)
}

func (s *Server) robots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	reader := s.o.Source.Reader()
	if reader == nil {
		http.Error(w, "no database", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := reader.Ping(ctx); err != nil {
		http.Error(w, "database error", http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ok\n"))
}

// ---- configuration glue ----

// OptionsFrom derives Options from the program configuration.
func OptionsFrom(cfg *config.Config, src *store.Source, log *slog.Logger) Options {
	return Options{
		DataDir:        cfg.DataDir,
		Source:         src,
		Log:            log,
		TrustedProxies: cfg.TrustedProxies,
		RateLimit:      cfg.RateLimit,
		RateBurst:      cfg.RateBurst,
		MaxInFlight:    cfg.MaxInFlight,
	}
}
