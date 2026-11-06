package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/dto"
	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
	webfs "github.com/py-snake/m3-keresheto-archivum/web"
)

// The read-only REST API (JSON, versioned under /api/v1, described by an OpenAPI document).
// It offers exactly what the web page offers, with the same search rules and limits, and nothing
// that writes. Every answer is built with encoding/json, so the stored text is always escaped.

const (
	apiDefaultPerPage = 20
	apiMaxPerPage     = 100
)

type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiJSON writes a JSON answer. The body is built first: an encoding problem becomes a clean 500.
func (s *Server) apiJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		s.o.Log.Error("encoding an API answer failed", "err", err)
		status, buf = http.StatusInternalServerError, *bytes.NewBufferString(`{"error":{"code":"internal","message":"internal error"}}` + "\n")
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if status == http.StatusOK {
		h.Set("Cache-Control", "public, max-age=60")
	} else {
		h.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) apiFail(w http.ResponseWriter, status int, code, message string) {
	s.apiJSON(w, status, map[string]apiErrorBody{"error": {Code: code, Message: message}})
}

type apiList struct {
	Total   int           `json:"total"`
	Capped  bool          `json:"capped"`
	Page    int           `json:"page"`
	PerPage int           `json:"per_page"`
	Pages   int           `json:"pages"`
	Items   []dto.Program `json:"items"`
}

// apiQuery reads and validates the query parameters of the list endpoint.
func apiQuery(r *http.Request) (store.Query, int, int, *apiProblem) {
	v := r.URL.Query()
	var q store.Query
	bad := func(code, msg string) (store.Query, int, int, *apiProblem) {
		return store.Query{}, 0, 0, &apiProblem{code, msg}
	}

	term, ok := store.NormalizeTerm(v.Get("q"))
	if !ok {
		return bad("invalid_query", "q contains characters that cannot be searched")
	}
	q.Term = term
	q.Fields = map[string]string{}
	for _, col := range store.FilterFields {
		t, ok := store.NormalizeTerm(v.Get(col))
		if !ok {
			return bad("invalid_query", col+" contains characters that cannot be searched")
		}
		if t != "" {
			q.Fields[col] = t
		}
	}
	for name, dst := range map[string]*int{"year_from": &q.YearFrom, "year_to": &q.YearTo} {
		if raw := strings.TrimSpace(v.Get(name)); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 2100 {
				return bad("invalid_parameter", name+" must be a year between 1 and 2100")
			}
			*dst = n
		}
	}
	for name, dst := range map[string]*bool{"playable": &q.OnlyPlayable, "highlighted": &q.OnlyHighlighted} {
		switch strings.ToLower(v.Get(name)) {
		case "", "false", "0":
		case "true", "1":
			*dst = true
		default:
			return bad("invalid_parameter", name+" must be true or false")
		}
	}
	sortOrder, ok := store.ParseSort(v.Get("sort"))
	if !ok {
		return bad("invalid_parameter", "unknown sort order")
	}
	q.Sort = sortOrder

	page, perPage := 1, apiDefaultPerPage
	if raw := v.Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > MaxPage {
			return bad("invalid_parameter", "page must be between 1 and "+strconv.Itoa(MaxPage))
		}
		page = n
	}
	if raw := v.Get("per_page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > apiMaxPerPage {
			return bad("invalid_parameter", "per_page must be between 1 and "+strconv.Itoa(apiMaxPerPage))
		}
		perPage = n
	}
	q.Limit, q.Offset = perPage, (page-1)*perPage
	return q, page, perPage, nil
}

type apiProblem struct{ code, message string }

func (s *Server) apiPrograms(w http.ResponseWriter, r *http.Request) {
	q, page, perPage, problem := apiQuery(r)
	if problem != nil {
		s.apiFail(w, http.StatusBadRequest, problem.code, problem.message)
		return
	}
	reader := s.o.Source.Reader()
	if reader == nil {
		s.apiFail(w, http.StatusServiceUnavailable, "unavailable", "the database is not ready yet")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	res, err := reader.SearchQuery(ctx, q)
	switch {
	case errors.Is(err, store.ErrTermTooShort):
		s.apiFail(w, http.StatusBadRequest, "term_too_short", "the text of a field filter needs at least "+strconv.Itoa(store.MinTermRunes)+" characters")
		return
	case errors.Is(err, context.DeadlineExceeded):
		s.apiFail(w, http.StatusServiceUnavailable, "timeout", "the search took too long, please narrow it")
		return
	case err != nil:
		s.o.Log.Error("API search failed", "err", err)
		s.apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := apiList{Total: res.Total, Capped: res.Capped, Page: page, PerPage: perPage, Items: make([]dto.Program, 0, len(res.Items))}
	out.Pages = min((res.Total+perPage-1)/perPage, MaxPage)
	for _, p := range res.Items {
		out.Items = append(out.Items, dto.FromModel(p, "/img/"+p.ProgramID+".jpg"))
	}
	s.apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiProgram(w http.ResponseWriter, r *http.Request) {
	id := strings.ToUpper(r.PathValue("id"))
	if !progid.Valid(id) {
		s.apiFail(w, http.StatusBadRequest, "invalid_id", "not a valid M3 program id (M3-...)")
		return
	}
	reader := s.o.Source.Reader()
	if reader == nil {
		s.apiFail(w, http.StatusServiceUnavailable, "unavailable", "the database is not ready yet")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	res, err := reader.SearchQuery(ctx, store.Query{Term: id, Limit: 1})
	if err != nil {
		s.o.Log.Error("API lookup failed", "err", err)
		s.apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if len(res.Items) == 0 {
		s.apiFail(w, http.StatusNotFound, "not_found", "no such program in this archive")
		return
	}
	out := dto.FromModel(res.Items[0], "/img/"+id+".jpg")
	if hist, err := reader.History(ctx, []string{id}); err == nil {
		for _, c := range hist[id] {
			out.Availability.History = append(out.Availability.History, dto.Change{At: time.Unix(c.At, 0).UTC(), Playable: c.Playable})
		}
	}
	s.apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiRoot(w http.ResponseWriter, r *http.Request) {
	reader := s.o.Source.Reader()
	if reader == nil {
		s.apiFail(w, http.StatusServiceUnavailable, "unavailable", "the database is not ready yet")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	res, err := reader.SearchQuery(ctx, store.Query{Limit: 1})
	if err != nil {
		s.apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	var updated *time.Time
	if t := s.o.Source.UpdatedAt(); !t.IsZero() {
		u := t.UTC()
		updated = &u
	}
	s.apiJSON(w, http.StatusOK, map[string]any{
		"name":                "m3 kereshető archívum API",
		"version":             "v1",
		"programs":            res.Total,
		"database_updated_at": updated,
		"documentation":       "/api/",
		"openapi":             "/api/v1/openapi.json",
		"downloads":           "/export/",
		"limits": map[string]int{
			"max_per_page": apiMaxPerPage, "max_pages": MaxPage, "min_field_text_length": store.MinTermRunes, "max_text_length": store.MaxTermRunes,
		},
	})
}

func (s *Server) apiOpenAPI(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(webfs.FS, "static/openapi.json")
	if err != nil {
		s.apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(b)
}

// apiFallback answers everything below /api/ that is not a known GET route: CORS preflights, wrong
// methods and unknown paths, always as JSON.
func (s *Server) apiFallback(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		h := w.Header()
		h.Set("Allow", "GET, HEAD, OPTIONS")
		h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Accept, Content-Type")
		h.Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		s.apiFail(w, http.StatusNotFound, "not_found", "no such endpoint, see /api/")
	default:
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		s.apiFail(w, http.StatusMethodNotAllowed, "method_not_allowed", "the API is read-only: use GET")
	}
}

// apiDoc is the human readable page of the API (static text, no scripts).
func (s *Server) apiDoc(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "api", pageData{Title: "API - " + siteTitle, ShowAPI: true, ShowExport: s.o.PublicExport})
}
