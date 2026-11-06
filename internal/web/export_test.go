package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/export"
)

func exportEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, nil, catalog()...)
	if _, err := export.Run(context.Background(), e.dir, time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDownloadPageBeforeAndAfterTheFirstExport(t *testing.T) {
	e := newEnv(t, nil, catalog()...)
	resp, body := e.get(t, "/export/")
	if resp.StatusCode != 200 || !strings.Contains(body, "még nem készültek el") || strings.Contains(body, `href="/export/m3-archive.sqlite.gz"`) {
		t.Errorf("before the first export: %d", resp.StatusCode)
	}
	if resp, _ := e.get(t, "/export/m3-archive.sqlite.gz"); resp.StatusCode != 404 {
		t.Errorf("a missing export must be 404: %d", resp.StatusCode)
	}

	e = exportEnv(t)
	resp, body = e.get(t, "/export/")
	assertSecure(t, "downloads", resp)
	for _, want := range []string{
		"m3-archive.sqlite.gz", "m3-programs.csv.gz", "m3-programs.jsonl.gz", "schema.sql", "SHA256SUMS", "manifest.json",
		"2026-11-06 10:30", "<strong>3</strong> műsor", "sha256sum -c", "CSV-injekció", "nemzetiarchivum.hu/m3", "Az adatok eredete",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the download page lacks %q", want)
		}
	}
	if strings.Contains(body, `id="search-form"`) || strings.Contains(body, "<script") {
		t.Error("unexpected form or script on the download page")
	}
}

func TestEveryExportFileCanBeDownloaded(t *testing.T) {
	e := exportEnv(t)
	for name, spec := range exportFiles {
		resp, body := e.get(t, "/export/"+name)
		if resp.StatusCode != 200 {
			t.Errorf("%s: %d", name, resp.StatusCode)
			continue
		}
		disk, _ := os.ReadFile(filepath.Join(e.dir, "export", name))
		if body != string(disk) {
			t.Errorf("%s: the download differs from the file", name)
		}
		if resp.Header.Get("Content-Type") != spec.ctype {
			t.Errorf("%s: content type %q", name, resp.Header.Get("Content-Type"))
		}
		if (resp.Header.Get("Content-Disposition") != "") != spec.attachment {
			t.Errorf("%s: content disposition %q", name, resp.Header.Get("Content-Disposition"))
		}
		if resp.Header.Get("Content-Length") == "" || resp.Header.Get("Accept-Ranges") != "bytes" {
			t.Errorf("%s: no length / range support", name)
		}
		assertSecure(t, name, resp)
		if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
			t.Errorf("%s: the .gz files must be sent as files, not as a content encoding", name)
		}
	}
	// a resumable download works
	resp, body := e.do(t, "GET", "/export/m3-archive.sqlite.gz", map[string]string{"Range": "bytes=0-9"})
	if resp.StatusCode != 206 || len(body) != 10 || !strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-9/") {
		t.Errorf("range request: %d %q", resp.StatusCode, resp.Header.Get("Content-Range"))
	}
	if resp, _ := e.do(t, "HEAD", "/export/schema.sql", nil); resp.StatusCode != 200 {
		t.Errorf("HEAD: %d", resp.StatusCode)
	}
}

func TestOnlyTheFixedFilesAreServed(t *testing.T) {
	e := exportEnv(t)
	dir := filepath.Join(e.dir, "export")
	secret := filepath.Join(e.dir, "secret.txt")
	_ = os.WriteFile(secret, []byte("TOP SECRET"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("TOP SECRET"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".tmp-abc-m3-programs.csv.gz"), []byte("TOP SECRET"), 0o644)
	_ = os.Mkdir(filepath.Join(e.dir, "sub"), 0o755)

	// replace an allowed name with a symbolic link that points outside, and another with a directory
	_ = os.Remove(filepath.Join(dir, export.SumsName))
	_ = os.Symlink(secret, filepath.Join(dir, export.SumsName))
	_ = os.Remove(filepath.Join(dir, export.ManifestName))
	_ = os.Mkdir(filepath.Join(dir, export.ManifestName), 0o755)

	for _, path := range []string{
		"/export/notes.txt", "/export/.tmp-abc-m3-programs.csv.gz", "/export/SHA256SUMS", "/export/manifest.json",
		"/export/..%2Fsecret.txt", "/export/%2e%2e", "/export/m3-archive.sqlite.gz%00.txt", "/export/M3-ARCHIVE.SQLITE.GZ",
		"/export/schema.sql/", "/export/sub/x", "/export//schema.sql", "/export/m3-archive.sqlite",
	} {
		resp, body := e.get(t, path)
		if resp.StatusCode == 200 || strings.Contains(body, "TOP SECRET") {
			t.Errorf("%s -> %d: served a file it must not", path, resp.StatusCode)
		}
	}
	if resp, _ := e.get(t, "/export/schema.sql"); resp.StatusCode != 200 {
		t.Error("the allowed files must keep working")
	}
}

func TestConcurrentDownloadsAreLimited(t *testing.T) {
	e := exportEnv(t)
	for i := 0; i < maxDownloads; i++ {
		e.s.downloads <- struct{}{} // four downloads in progress
	}
	resp, _ := e.get(t, "/export/m3-archive.sqlite.gz")
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("the fifth download must be refused politely: %d", resp.StatusCode)
	}
	assertSecure(t, "503", resp)
	<-e.s.downloads // one finished
	if resp, _ := e.get(t, "/export/m3-archive.sqlite.gz"); resp.StatusCode != 200 {
		t.Fatalf("after a download finished the next one must work: %d", resp.StatusCode)
	}
	// the page itself and the small files are not held up by a busy queue
	for len(e.s.downloads) > 0 {
		<-e.s.downloads
	}
}

func TestDownloadBudgetAndSizes(t *testing.T) {
	if downloadBudget(0) != downloadMinBudget || downloadBudget(1<<10) != downloadMinBudget {
		t.Error("a small file gets the minimum budget")
	}
	if a, b := downloadBudget(10<<20), downloadBudget(20<<20); !(a > downloadMinBudget && b > a && b < downloadMaxBudget) {
		t.Errorf("the budget must grow with the size: %s %s", a, b)
	}
	for _, huge := range []int64{1 << 40, 1 << 50, 1<<63 - 1} {
		if got := downloadBudget(huge); got != downloadMaxBudget {
			t.Errorf("downloadBudget(%d) = %s: the budget must be bounded (and must not overflow)", huge, got)
		}
	}
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 1024: "1.0 KB", 1 << 20: "1.0 MB", 3 << 29: "1.5 GB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}
