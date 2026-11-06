package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/config"
	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
	"github.com/py-snake/m3-keresheto-archivum/internal/images"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
)

type env struct {
	srv  *httptest.Server
	s    *Server
	src  *store.Source
	dir  string
	logs *bytes.Buffer
}

func newEnv(t testing.TB, tune func(*Options), ps ...model.Program) *env {
	t.Helper()
	dir := t.TempDir()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(&lockedWriter{w: logs}, nil))

	if len(ps) > 0 {
		pub := &store.Publisher{Dir: dir}
		if _, err := pub.Run(context.Background(), func(ctx context.Context, w *store.Writer) error {
			if _, err := w.InsertPrograms(ctx, ps); err != nil {
				return err
			}
			for _, p := range ps {
				if p.ImageState != model.ImagePending {
					if err := w.SetImageState(ctx, p.ProgramID, p.ImageState); err != nil {
						return err
					}
				}
			}
			if _, _, err := w.RecordObservations(ctx, ps); err != nil {
				return err
			}
			for _, p := range ps {
				if p.GoneAt > 0 {
					if err := w.MarkGone(ctx, p.ProgramID); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	src := store.NewSource(dir, log)
	if err := src.Refresh(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(src.Close)

	o := Options{DataDir: dir, Source: src, Log: log, RateLimit: 1000, RateBurst: 1000, MaxInFlight: 64, PublicAPI: true, PublicExport: true}
	if tune != nil {
		tune(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{srv: srv, s: s, src: src, dir: dir, logs: logs}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (e *env) do(t *testing.T, method, path string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *env) get(t *testing.T, path string) (*http.Response, string) {
	return e.do(t, http.MethodGet, path, nil)
}

func prog(id, title string) model.Program {
	return model.Program{ProgramID: id, Title: title, Duration: "00:27:44:00", Year: 1983}
}

func many(n int) []model.Program {
	var ps []model.Program
	for i := 0; i < n; i++ {
		ps = append(ps, prog(fmt.Sprintf("M3-P%03d", i), fmt.Sprintf("Film %03d", i)))
	}
	return ps
}

var requiredHeaders = []string{
	"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy",
	"Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy", "Strict-Transport-Security", "X-Frame-Options",
}

func assertSecure(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	for _, h := range requiredHeaders {
		if resp.Header.Get(h) == "" {
			t.Errorf("%s: missing header %s", what, h)
		}
	}
	if v := resp.Header.Get("Set-Cookie"); v != "" {
		t.Errorf("%s: sets a cookie: %s", what, v)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe") || !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'self';") ||
		!strings.Contains(csp, "connect-src 'self';") || strings.Contains(csp, "http") || strings.Contains(csp, "*") {
		t.Errorf("%s: the CSP must allow only same-origin scripts and fetches: %s", what, csp)
	}
}

func TestIndexRendersTheArchive(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "A béke szigete"), model.Program{
		ProgramID: "M3-AAA2", Title: "Vadászkutyák", Subtitle: "Epizód", IsSeries: true, Episode: 2, Episodes: 5,
		Duration: "01:07:14:08", ShortDescription: "Rövid leírás", Description: "egy;kettő", Year: 1999,
		Creators: "Nagy István\nKiss Ödön", ReleasedAt: time.Now().AddDate(0, 0, 30).Unix(), ImageState: 1,
	})
	resp, body := e.get(t, "/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	assertSecure(t, "index", resp)
	for _, want := range []string{
		"2 találat", "Vadászkutyák", "A béke szigete", "M3-AAA2", "(2. / 5)", "1ó 7p", "Rövid leírás",
		`src="/img/M3-AAA2.jpg"`, `src="/static/placeholder.svg"`, "Nagy István<br>Kiss Ödön", "egy; kettő",
		"📅", "Közzététel dátuma", `lang="hu"`, `action="/"`, `name="kereses"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// no JavaScript and no third-party resources at all
	for _, bad := range []string{"googleapis", "unpkg", "onclick", "onload", "javascript:"} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	// exactly one script, an external file of this site: no inline script anywhere
	if n := strings.Count(body, "<script"); n != 1 || !strings.Contains(body, `<script src="/static/search.js" defer></script>`) {
		t.Errorf("expected only the instant search script, found %d script tags", n)
	}
	// nothing is loaded from another host: only plain links lead there (the M3 page of a program)
	for _, bad := range []string{`src="http`, `src='http`, `<link rel="stylesheet" href="http`, `url(http`} {
		if strings.Contains(body, bad) {
			t.Errorf("the page loads an external resource: %q", bad)
		}
	}
	// newest first
	if strings.Index(body, "Vadászkutyák") > strings.Index(body, "A béke szigete") {
		t.Error("not newest first")
	}
}

func TestSearchAndNotices(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "A béke szigete"), prog("M3-AAA2", "Vadászkutyák"))

	_, body := e.get(t, "/?kereses=BEKE")
	if !strings.Contains(body, "1 találat") || !strings.Contains(body, "A béke szigete") || strings.Contains(body, "Vadászkutyák") {
		t.Error("accent/case insensitive search failed")
	}
	if !strings.Contains(body, `value="BEKE"`) || !strings.Contains(body, "<title>BEKE - m3 kereshető archívum</title>") {
		t.Error("search term not echoed")
	}
	_, body = e.get(t, "/?kereses=ke")
	if strings.Contains(body, "Legalább 3 karakter") || !strings.Contains(body, "A béke szigete") || strings.Contains(body, "Vadászkutyák") {
		t.Error("a 1-2 character term must search the titles, not be refused")
	}
	_, body = e.get(t, "/?cim=ab")
	if !strings.Contains(body, "legalább 3 karakter") {
		t.Error("a field filter under 3 characters needs a notice")
	}
	_, body = e.get(t, "/?kereses=nincsilyen")
	if !strings.Contains(body, "Nincs találat") {
		t.Error("no-result message missing")
	}
	_, body = e.get(t, "/?kereses=m3-aaa2")
	if !strings.Contains(body, "1 találat") || !strings.Contains(body, "Vadászkutyák") {
		t.Error("id lookup failed")
	}
	_, body = e.get(t, "/?kereses=a%00b")
	if !strings.Contains(body, "Érvénytelen karakter a keresőszövegben") {
		t.Error("NUL in the term not rejected")
	}
	_, body = e.get(t, "/?kereses=%ff%fe")
	if !strings.Contains(body, "Érvénytelen karakter a keresőszövegben") {
		t.Error("invalid UTF-8 not rejected")
	}
}

func TestEverythingUserControlledIsEscaped(t *testing.T) {
	xss := `<script>alert('x')</script>"><img src=x onerror=alert(1)>`
	short := `"><i x=1>`
	e := newEnv(t, nil, model.Program{
		ProgramID: "M3-XSS1", Title: "T" + xss, Subtitle: xss, Info: xss + "\n" + xss, ExtendedInfo: xss,
		Description: xss, ShortDescription: xss, Company: xss, Creators: xss, Contributors: xss, Genre: xss,
		SeriesID: xss, Country: "H", Quality: short, PG: short, Ratio: short, Duration: short, IsSeries: true,
	})
	for _, path := range []string{"/", "/?nyers", "/?kereses=M3-XSS1", "/?kereses=" + "%22%3E%3Cscript%3Ealert(1)%3C%2Fscript%3E"} {
		_, body := e.get(t, path)
		for _, raw := range []string{"<script>alert", "<img src=x", "<i x=1>", "onerror=alert(1)>", `"><script`} {
			if strings.Contains(body, raw) {
				t.Errorf("%s: unescaped %q in the page", path, raw)
			}
		}
	}
	_, body := e.get(t, "/")
	if !strings.Contains(body, "&lt;script&gt;alert(&#39;x&#39;)&lt;/script&gt;") {
		t.Error("payload is not shown as escaped text")
	}
}

func TestSecurityHeadersOnEveryKindOfResponse(t *testing.T) {
	e := newEnv(t, nil, many(3)...)

	cases := []struct {
		name, method, path string
		want               int
	}{
		{"index", "GET", "/", 200},
		{"head", "HEAD", "/", 200},
		{"page 2", "GET", "/2", 200},
		{"unknown", "GET", "/nope", 404},
		{"deep unknown", "GET", "/a/b/c", 404},
		{"page zero", "GET", "/0", 404},
		{"page too big", "GET", "/1001", 404},
		{"page leading zero", "GET", "/01", 404},
		{"negative page", "GET", "/-1", 404},
		{"static", "GET", "/static/app.css", 200},
		{"robots", "GET", "/robots.txt", 200},
		{"missing image", "GET", "/img/M3-NOPE.jpg", 404},
		{"post", "POST", "/", 405},
		{"put", "PUT", "/", 405},
		{"delete", "DELETE", "/", 405},
		{"patch", "PATCH", "/", 405},
		{"post on image", "POST", "/img/M3-A.jpg", 405},
		{"long url", "GET", "/?kereses=" + strings.Repeat("a", 3000), 414},
	}
	for _, c := range cases {
		resp, _ := e.do(t, c.method, c.path, nil)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
		assertSecure(t, c.name, resp)
	}

}

func TestRateLimitedResponsesAreSecureToo(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.RateLimit = 0.001; o.RateBurst = 3 }, many(3)...)
	var limited *http.Response
	for i := 0; i < 10 && (limited == nil || limited.StatusCode != 429); i++ {
		limited, _ = e.get(t, "/")
	}
	if limited.StatusCode != 429 || limited.Header.Get("Retry-After") == "" {
		t.Fatalf("no rate limiting: %d", limited.StatusCode)
	}
	assertSecure(t, "429", limited)
}

func TestNoDatabaseYet(t *testing.T) {
	e := newEnv(t, nil)
	resp, body := e.get(t, "/")
	if resp.StatusCode != 503 || !strings.Contains(body, "nem készült el") {
		t.Fatalf("%d %q", resp.StatusCode, body[:min(len(body), 80)])
	}
	assertSecure(t, "503", resp)
	if resp, _ := e.get(t, "/healthz"); resp.StatusCode != 503 {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "x"))
	resp, body := e.get(t, "/healthz")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != "ok" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}

func TestPagingLinks(t *testing.T) {
	e := newEnv(t, nil, many(25)...)
	_, body := e.get(t, "/?kereses=Film")
	if !strings.Contains(body, "25 találat") || !strings.Contains(body, `href="/2?kereses=Film"`) || !strings.Contains(body, `href="/3?kereses=Film"`) {
		t.Error("pager lacks links that keep the search term")
	}
	_, p3 := e.get(t, "/3?kereses=Film")
	if strings.Count(p3, `class="mdc-data-table__row"`) != 5+1+0 && !strings.Contains(p3, "Film 004") {
		t.Error("last page does not hold the oldest 5 items")
	}
	if !strings.Contains(p3, `rel="prev"`) || strings.Contains(p3, `rel="next"`) {
		t.Error("prev/next relations wrong on the last page")
	}
	_, beyond := e.get(t, "/9")
	if !strings.Contains(beyond, "Nincs találat") {
		t.Error("a page past the end should say there is nothing")
	}
}

func TestRawView(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "Cím"))
	_, body := e.get(t, "/?nyers")
	for _, col := range []string{"program_id", "extended_info", "seriesId", "added", "released"} {
		if !strings.Contains(body, ">"+col+"<") {
			t.Errorf("raw view lacks column %s", col)
		}
	}
}

func TestImagesAreServedFromTheDataDirectoryOnly(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "x"))
	jpeg := fakeupstream.Fixture(t, "cover.jpg")
	clean, err := images.Sanitize(jpeg, 0)
	if err != nil {
		t.Fatal(err)
	}
	imgDir := filepath.Join(e.dir, "images")
	if err := images.Save(imgDir, "M3-AAA1", clean); err != nil {
		t.Fatal(err)
	}

	resp, body := e.get(t, "/img/M3-AAA1.jpg")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || body != string(clean) {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	assertSecure(t, "image", resp)
	if resp, _ := e.do(t, "HEAD", "/img/M3-AAA1.jpg", nil); resp.StatusCode != 200 {
		t.Fatalf("HEAD %d", resp.StatusCode)
	}

	// things an attacker might plant or request
	secret := filepath.Join(filepath.Dir(e.dir), "secret-"+filepath.Base(e.dir))
	_ = os.WriteFile(secret, []byte("TOP SECRET"), 0o644)
	t.Cleanup(func() { os.Remove(secret) })
	_ = os.Symlink(secret, filepath.Join(imgDir, "M3-LINK.jpg"))
	_ = os.Symlink("/etc/passwd", filepath.Join(imgDir, "M3-PASS.jpg"))
	_ = os.Mkdir(filepath.Join(imgDir, "M3-ADIR.jpg"), 0o755)
	_ = os.WriteFile(filepath.Join(imgDir, "notes.txt"), []byte("TOP SECRET"), 0o644)

	for _, p := range []string{
		"/img/M3-LINK.jpg", "/img/M3-PASS.jpg", "/img/M3-ADIR.jpg", "/img/notes.txt", "/img/M3-AAA1", "/img/M3-AAA1.jpeg",
		"/img/m3-aaa1.jpg", "/img/M3-AAA1.jpg.txt", "/img/..%2F..%2Fetc%2Fpasswd", "/img/M3-AAA1.jpg%00.png",
		"/img/%2e%2e", "/img/M3-A%2F..%2F..%2Fsecret.jpg", "/img/", "/img//M3-AAA1.jpg", "/img/M3-AAA1.jpg/",
		"/img/.tmp-abc.jpg", "/img/" + strings.Repeat("A", 200) + ".jpg", "/img/M3-AAA1.jpg%20",
	} {
		resp, body := e.get(t, p)
		if resp.StatusCode == 200 || strings.Contains(body, "TOP SECRET") || strings.Contains(body, "root:") {
			t.Errorf("%s -> %d (served something it must not)", p, resp.StatusCode)
		}
	}
}

func TestImagesWithoutAnImagesDirectory(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "x"))
	if resp, _ := e.get(t, "/img/M3-AAA1.jpg"); resp.StatusCode != 404 {
		t.Fatalf("%d", resp.StatusCode)
	}
	// the directory appears later (first ingest): the next request finds it
	_ = images.Save(filepath.Join(e.dir, "images"), "M3-AAA1", []byte("\xff\xd8\xff"))
	if resp, _ := e.get(t, "/img/M3-AAA1.jpg"); resp.StatusCode != 200 {
		t.Fatalf("after creation: %d", resp.StatusCode)
	}
}

func TestStaticFilesOnly(t *testing.T) {
	e := newEnv(t, nil)
	if resp, body := e.get(t, "/static/app.css"); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || !strings.Contains(body, "m3player") {
		t.Fatalf("app.css: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := e.get(t, "/static/placeholder.svg"); resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("svg: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	for _, p := range []string{"/static/", "/static", "/static/..", "/static/../templates/layout.html", "/static/templates/layout.html",
		"/static/%2e%2e/templates/index.html", "/static/layout.html", "/static//etc/passwd", "/templates/layout.html"} {
		resp, body := e.get(t, p)
		if resp.StatusCode == 200 || strings.Contains(body, "{{") {
			t.Errorf("%s -> %d", p, resp.StatusCode)
		}
	}
}

func TestRobots(t *testing.T) {
	e := newEnv(t, nil)
	_, body := e.get(t, "/robots.txt")
	if !strings.Contains(body, "Disallow: /") {
		t.Fatal(body)
	}
}

func TestRateLimitIgnoresForgedHeadersFromUntrustedPeers(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.RateLimit = 0.0001; o.RateBurst = 3 }, prog("M3-AAA1", "x"))
	// no trusted proxies: the forged header must not give every request its own bucket
	got429 := false
	for i := 0; i < 12; i++ {
		resp, _ := e.do(t, "GET", "/", map[string]string{"CF-Connecting-IP": fmt.Sprintf("203.0.113.%d", i+1), "X-Forwarded-For": fmt.Sprintf("198.51.100.%d", i+1)})
		if resp.StatusCode == 429 {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("a forged CF-Connecting-IP / X-Forwarded-For evaded the rate limit")
	}
	// /healthz is never limited
	for i := 0; i < 20; i++ {
		if resp, _ := e.get(t, "/healthz"); resp.StatusCode != 200 {
			t.Fatalf("healthz limited at %d", i)
		}
	}
}

func TestRateLimitSeparatesClientsBehindATrustedProxy(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	e := newEnv(t, func(o *Options) { o.RateLimit = 0.0001; o.RateBurst = 2; o.TrustedProxies = trusted }, prog("M3-AAA1", "x"))

	a := map[string]string{"CF-Connecting-IP": "203.0.113.1"}
	b := map[string]string{"CF-Connecting-IP": "203.0.113.2"}
	for i := 0; i < 2; i++ {
		if resp, _ := e.do(t, "GET", "/", a); resp.StatusCode != 200 {
			t.Fatalf("client A request %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := e.do(t, "GET", "/", a); resp.StatusCode != 429 {
		t.Fatalf("client A exceeded its budget but got %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, "GET", "/", b); resp.StatusCode != 200 {
		t.Fatalf("client B was punished for client A: %d", resp.StatusCode)
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	req := func(remote string, h map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want string
	}{
		{"direct", req("198.51.100.7:1234", nil), "198.51.100.7"},
		{"forged header, untrusted peer", req("198.51.100.7:1234", map[string]string{"CF-Connecting-IP": "1.2.3.4", "X-Forwarded-For": "5.6.7.8"}), "198.51.100.7"},
		{"cloudflare header", req("10.1.2.3:80", map[string]string{"CF-Connecting-IP": "203.0.113.9"}), "203.0.113.9"},
		{"garbage header", req("10.1.2.3:80", map[string]string{"CF-Connecting-IP": "not-an-ip"}), "10.1.2.3"},
		{"xff right-most untrusted", req("10.1.2.3:80", map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.9, 10.9.9.9"}), "203.0.113.9"},
		{"ipv6 mapped", req("[::ffff:198.51.100.7]:1", nil), "198.51.100.7"},
	}
	for _, c := range cases {
		if got := clientIP(c.r, trusted); got.String() != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestLimiterMemoryIsBounded(t *testing.T) {
	now := time.Now()
	l := newLimiter(1, 1, 100, func() time.Time { return now })
	for i := 0; i < 5000; i++ {
		l.allow(netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1}))
	}
	if n := l.size(); n > 100 {
		t.Fatalf("limiter table grew to %d entries", n)
	}
	// IPv6 clients are grouped by /64
	l2 := newLimiter(0.0001, 2, 100, func() time.Time { return now })
	a, b := netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("2001:db8::ffff")
	l2.allow(a)
	l2.allow(b)
	if l2.allow(a) {
		t.Fatal("two addresses of one /64 must share a bucket")
	}
}

func TestInFlightCap(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxInFlight = 1 })
	release, entered := make(chan struct{}), make(chan struct{})
	slow := e.s.guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slow.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}()
	<-entered
	rec := httptest.NewRecorder()
	slow.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	close(release)
	wg.Wait()
	if rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("second concurrent request got %d", rec.Code)
	}
}

func TestPanicBecomesAGenericError(t *testing.T) {
	e := newEnv(t, nil)
	h := e.s.recoverer(e.s.secure(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret internal detail /etc/passwd") })))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "passwd") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("error response without security headers")
	}
}

func TestLogsHoldNeitherSearchTermsNorRawAddresses(t *testing.T) {
	e := newEnv(t, nil, prog("M3-AAA1", "x"))
	e.get(t, "/?kereses=TitkosKeresoszo123")
	e.get(t, "/?kereses=M3-SECRETID")
	logs := e.logs.String()
	if !strings.Contains(logs, `"msg":"request"`) {
		t.Fatalf("no access log:\n%s", logs)
	}
	for _, leak := range []string{"TitkosKeresoszo123", "SECRETID", "127.0.0.1", "kereses"} {
		if strings.Contains(logs, leak) {
			t.Errorf("the log contains %q:\n%s", leak, logs)
		}
	}
}

func TestPagerWindow(t *testing.T) {
	items := buildPager(10, 1000, url.Values{"kereses": {`a b&c`}}, true)
	var labels []string
	for _, it := range items {
		labels = append(labels, it.Label)
	}
	got := strings.Join(labels, " ")
	if got != "« ‹ 6 7 8 9 10 11 12 13 14 › »" {
		t.Fatalf("window: %s", got)
	}
	if items[1].Href != "/9?kereses=a+b%26c&nyers" {
		t.Fatalf("href: %s", items[1].Href)
	}
	if buildPager(1, 5, nil, false) != nil {
		t.Fatal("a single page needs no pager")
	}
	if p := buildPager(1, 50*1000, nil, false); len(p) == 0 || p[len(p)-1].Href != "/1000" {
		t.Fatalf("pager must stop at MaxPage: %+v", p[len(p)-1])
	}
}

func TestDurationLabel(t *testing.T) {
	for in, want := range map[string]string{
		"01:07:14:08": "1ó 7p", "02:00:00:00": "2ó 0p", "00:27:44:00": "27p", "00:00:30:12": "30mp", "00:00:00:05": "", "": "", "garbage": "", "x:y:z": "",
	} {
		if got := durationLabel(in); got != want {
			t.Errorf("durationLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- the real server with its timeouts ----

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func startServe(t *testing.T) (addr string, stop func()) {
	t.Helper()
	dir := t.TempDir()
	if _, err := (&store.Publisher{Dir: dir}).Run(context.Background(), func(ctx context.Context, w *store.Writer) error {
		_, err := w.InsertPrograms(ctx, many(3))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	addr = freePort(t)
	cfg, err := config.FromEnv(func(k string) string {
		return map[string]string{"M3_DATA_DIR": dir, "M3_LISTEN": addr}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return addr, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("graceful shutdown hangs")
		}
	}
}

func TestServeEndToEndAndGracefulShutdown(t *testing.T) {
	addr, stop := startServe(t)
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "3 találat") {
		t.Fatalf("%d", resp.StatusCode)
	}
	stop()
	if _, err := http.Get("http://" + addr + "/"); err == nil {
		t.Fatal("still serving after shutdown")
	}
}

func TestSlowHeaderClientsAreDisconnected(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 6 seconds")
	}
	addr, stop := startServe(t)
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// slowloris: start a request and never finish the headers
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\nX-Slow: "))
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	start := time.Now()
	_, err = io.ReadAll(c) // returns when the server closes the connection
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the server kept a half-finished request open for 8 seconds")
	}
	if d := time.Since(start); d > 7*time.Second {
		t.Fatalf("disconnected only after %s", d)
	}
}

func TestOversizedHeadersAreRefused(t *testing.T) {
	addr, stop := startServe(t)
	defer stop()
	req, _ := http.NewRequest("GET", "http://"+addr+"/", nil)
	req.Header.Set("X-Big", strings.Repeat("a", 20000))
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Fatalf("20 kB header accepted: %d", resp.StatusCode)
		}
	}
}

func TestAvailabilityBadgesLinkAndFilter(t *testing.T) {
	yes := prog("M3-YES1", "Elérhető film")
	yes.PlayableKnown, yes.Playable = true, true
	yes.WindowLast = time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC).Unix()
	no := prog("M3-NO01", "Lejárt film")
	no.PlayableKnown, no.Playable = true, false
	unknown := prog("M3-UNK1", "Ismeretlen film")
	gone := prog("M3-GONE1", "Eltűnt film")
	gone.PlayableKnown, gone.Playable, gone.GoneAt = true, false, 1
	e := newEnv(t, nil, yes, no, unknown, gone)

	resp, body := e.get(t, "/?kereses=film")
	assertSecure(t, "availability page", resp)
	for _, want := range []string{
		`avail--yes" title="Utoljára ellenőrizve`, ">Elérhető<", ">Nem elérhető<", ">Ismeretlen<", ">Eltűnt az M3-ról<",
		"Utolsó ismert lejátszható nap: 2026-11-14", "Az elérhetőség azóta változhatott",
		`href="https://nemzetiarchivum.hu/m3/M3-YES1" target="_blank" rel="noopener noreferrer"`,
		`name="elerheto"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "elerheto\" value=\"1\" checked") {
		t.Error("the filter box is checked without the filter")
	}

	// the filter keeps only programs whose last observed state is playable
	_, body = e.get(t, "/?kereses=film&elerheto=1")
	if !strings.Contains(body, "1 találat") || !strings.Contains(body, "Elérhető film") || strings.Contains(body, "Lejárt film") || strings.Contains(body, "Ismeretlen film") {
		t.Error("the playable filter does not filter")
	}
	if !strings.Contains(body, "checked") {
		t.Error("the filter box lost its state")
	}
	// ... also without a search term, and with an id lookup
	_, body = e.get(t, "/?elerheto=1")
	if !strings.Contains(body, "1 találat") {
		t.Error("filter without a term")
	}
	_, body = e.get(t, "/?kereses=M3-NO01&elerheto=1")
	if !strings.Contains(body, "Nincs találat") {
		t.Error("an id lookup must respect the filter")
	}
}

func TestPagerKeepsTheAvailabilityFilter(t *testing.T) {
	var ps []model.Program
	for i := 0; i < 25; i++ {
		p := prog(fmt.Sprintf("M3-P%03d", i), fmt.Sprintf("Film %03d", i))
		p.PlayableKnown, p.Playable = true, true
		ps = append(ps, p)
	}
	e := newEnv(t, nil, ps...)
	_, body := e.get(t, "/?kereses=Film&elerheto=1")
	if !strings.Contains(body, `href="/2?elerheto=1&amp;kereses=Film"`) && !strings.Contains(body, `href="/2?kereses=Film&amp;elerheto=1"`) {
		t.Errorf("pager links drop the filter")
	}
	if items := buildPager(1, 30, url.Values{"elerheto": {"1"}, "kereses": {"x"}}, false); !strings.Contains(items[1].Href, "elerheto=1") {
		t.Errorf("%+v", items)
	}
}

func TestDetailsShowAvailability(t *testing.T) {
	p := prog("M3-DET1", "Részletes")
	p.PlayableKnown, p.Playable = true, false
	p.PlayableCheckedAt = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix()
	e := newEnv(t, nil, p)
	_, body := e.get(t, "/")
	for _, key := range []string{">playable<", ">playableChecked<"} {
		if !strings.Contains(body, key) {
			t.Errorf("details lack %s", key)
		}
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestServer(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestOnePageViewIsNeverThrottled(t *testing.T) {
	// a real visit loads the page, the style sheets, the script and ten cover images at once: with
	// the default per-visitor limit (3 requests/second, burst 15) that must not produce a single 429
	var ps []model.Program
	for i := 0; i < 10; i++ {
		p := prog(fmt.Sprintf("M3-V%04d", i), fmt.Sprintf("Látogatás %d", i))
		p.ImageState = model.ImageStored
		ps = append(ps, p)
	}
	e := newEnv(t, func(o *Options) { o.RateLimit, o.RateBurst = 3, 15 }, ps...)
	_ = images.Save(filepath.Join(e.dir, "images"), "M3-V0000", []byte("\xff\xd8\xff"))

	if resp, _ := e.get(t, "/"); resp.StatusCode != 200 {
		t.Fatalf("page: %d", resp.StatusCode)
	}
	assets := []string{"/static/m3.min.css", "/static/app.css", "/static/search.js", "/static/logo.png", "/static/placeholder.svg"}
	for i := 0; i < 10; i++ {
		assets = append(assets, fmt.Sprintf("/img/M3-V%04d.jpg", i))
	}
	for round := 0; round < 3; round++ { // three views in a row
		for _, a := range assets {
			if resp, _ := e.get(t, a); resp.StatusCode == 429 {
				t.Fatalf("%s was throttled in round %d: an ordinary visit must not hit the limit", a, round)
			}
		}
	}
	// ... while searches stay limited: the burst is 15 pages
	limited := false
	for i := 0; i < 40 && !limited; i++ {
		resp, _ := e.get(t, "/?kereses=Látogatás")
		limited = resp.StatusCode == 429
	}
	if !limited {
		t.Fatal("searches must still be rate limited")
	}
}

func httptestRequest(rawQuery string) *http.Request {
	if len(rawQuery) > 2000 || strings.ContainsAny(rawQuery, " \r\n\x00#") {
		return nil
	}
	req, err := http.NewRequest("GET", "/api/v1/programs?"+rawQuery, nil)
	if err != nil {
		return nil
	}
	req.RemoteAddr = "192.0.2.1:1234"
	return req
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}
