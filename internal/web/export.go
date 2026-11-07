package web

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/export"
)

// The public downloads: the files `m3 export` writes into <data>/export. Only the fixed names below
// are ever served, through an os.Root (no path from the request reaches the file system).

const (
	maxDownloads      = 4 // concurrent downloads; more get 503 so slow clients cannot occupy the server
	downloadMinBudget = 60 * time.Second
	downloadMaxBudget = 30 * time.Minute
	downloadMinRate   = 32 << 10 // bytes per second a client must sustain, else the transfer ends
)

var exportFiles = map[string]struct {
	ctype      string
	attachment bool
}{
	export.ArchiveName:  {"application/gzip", true},
	export.CSVName:      {"application/gzip", true},
	export.JSONLName:    {"application/gzip", true},
	export.SchemaName:   {"text/plain; charset=utf-8", false},
	export.SumsName:     {"text/plain; charset=utf-8", false},
	export.ManifestName: {"application/json; charset=utf-8", false},
}

type exportFileView struct {
	Name, Description, Size, SHA256 string
	Attachment                      bool
}

type exportView struct {
	Available bool
	Generated string
	Programs  int
	Files     []exportFileView
	Notice    string
	Source    string
}

// downloadBudget is how long the response of a file of the given size may take to write: the
// server's 15 second write timeout is far too short for a large file, but it must not be unbounded.
func downloadBudget(size int64) time.Duration {
	secs := size / downloadMinRate
	if secs < 0 || secs >= int64(downloadMaxBudget/time.Second) { // also guards the conversion below against overflow
		return downloadMaxBudget
	}
	return min(downloadMinBudget+time.Duration(secs)*time.Second, downloadMaxBudget)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// exportRoot opens <data>/export lazily: it does not exist before the first `m3 export`.
func (s *Server) exportRoot() *os.Root {
	s.expMu.Lock()
	defer s.expMu.Unlock()
	if s.expRoot == nil {
		root, err := os.OpenRoot(filepath.Join(s.o.DataDir, "export"))
		if err != nil {
			return nil
		}
		s.expRoot = root
	}
	return s.expRoot
}

func (s *Server) exportIndex(w http.ResponseWriter, r *http.Request) {
	view := exportView{Notice: export.Notice, Source: "https://nemzetiarchivum.hu/m3"}
	if root := s.exportRoot(); root != nil {
		if f, err := root.Open(export.ManifestName); err == nil {
			raw, _ := io.ReadAll(io.LimitReader(f, 1<<20))
			f.Close()
			if m, err := export.ReadManifest(raw); err == nil {
				view.Available = true
				view.Generated = m.GeneratedAt.In(budapest).Format("2006-01-02 15:04")
				view.Programs = m.Programs
				for _, fi := range m.Files {
					if spec, ok := exportFiles[fi.Name]; ok { // only files the server would serve
						view.Files = append(view.Files, exportFileView{
							Name: fi.Name, Description: fi.Description, Size: humanSize(fi.Bytes), SHA256: fi.SHA256, Attachment: spec.attachment,
						})
					}
				}
			}
		}
	}
	s.renderPage(w, "export", pageData{Title: "Letöltések - " + siteTitle, ShowAPI: s.o.PublicAPI, ShowExport: true, Export: &view})
}

func (s *Server) exportFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	spec, ok := exportFiles[name]
	root := s.exportRoot()
	if !ok || root == nil {
		http.NotFound(w, r)
		return
	}
	f, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}

	select {
	case s.downloads <- struct{}{}:
		defer func() { <-s.downloads }()
	default:
		w.Header().Set("Retry-After", "30")
		http.Error(w, "Túl sok egyidejű letöltés, kérlek próbáld később.", http.StatusServiceUnavailable)
		return
	}
	// a large file needs more than the usual write timeout, within a bound
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadBudget(info.Size())))

	h := w.Header()
	h.Set("Content-Type", spec.ctype)
	h.Set("Cache-Control", "public, max-age=3600")
	if spec.attachment {
		h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	http.ServeContent(w, r, "", info.ModTime(), f) // handles Range requests (resumable downloads)
}

// renderPage renders a whole page of the given template set.
func (s *Server) renderPage(w http.ResponseWriter, page string, data pageData) {
	if data.Meta.Title == "" { // the downloads and the API page: for people, not for the index
		data.Meta = s.siteMeta(data.Title, siteDescription, robotsNoIndex, "")
	}
	var buf strings.Builder
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		s.o.Log.Error("rendering failed", "page", page, "err", err)
		http.Error(w, "Belső hiba.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300")
	_, _ = io.WriteString(w, buf.String())
}
