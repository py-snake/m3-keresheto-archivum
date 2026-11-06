package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/dto"
	"github.com/py-snake/m3-keresheto-archivum/internal/images"
	"github.com/py-snake/m3-keresheto-archivum/internal/model"
)

func apiEnv(t *testing.T) *env {
	t.Helper()
	ps := catalog()
	ps[0].ImageState = model.ImageStored
	ps[0].AiredFirst, ps[0].AiredLast = 1_600_000_000, 1_650_000_000
	e := newEnv(t, nil, ps...)
	_ = images.Save(e.dir+"/images", "M3-C0001", []byte("\xff\xd8\xff"))
	return e
}

func decode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
}

func apiGet(t *testing.T, e *env, path string) (*http.Response, string) {
	t.Helper()
	resp, body := e.get(t, path)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: content type %q", path, ct)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("%s: no CORS header", path)
	}
	return resp, body
}

func TestAPIList(t *testing.T) {
	e := apiEnv(t)
	resp, body := apiGet(t, e, "/api/v1/programs")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	assertSecure(t, "api list", resp)
	var list struct {
		Total   int
		Page    int
		PerPage int `json:"per_page"`
		Pages   int
		Capped  bool
		Items   []dto.Program
	}
	decode(t, body, &list)
	if list.Total != 3 || list.Page != 1 || list.PerPage != 20 || list.Pages != 1 || list.Capped || len(list.Items) != 3 {
		t.Fatalf("%+v", list)
	}
	// newest added first, lists are arrays (never null), the image path only for stored covers
	if list.Items[0].ID != "M3-C0003" {
		t.Errorf("order: %s", list.Items[0].ID)
	}
	var a dto.Program
	for _, p := range list.Items {
		if p.ID == "M3-C0001" {
			a = p
		}
		if p.Info == nil || p.Creators == nil || p.Genre == nil || p.Collection == nil {
			t.Errorf("%s: a list field is null instead of []", p.ID)
		}
	}
	if a.ImageURL == nil || *a.ImageURL != "/img/M3-C0001.jpg" || !a.HasImage || a.M3URL != "https://nemzetiarchivum.hu/m3/M3-C0001" {
		t.Errorf("%+v", a)
	}
	if len(a.Creators) != 2 || a.Creators[0] != "Kiss Ödön" || !a.Highlighted || a.AiredFirst == nil || a.Availability.Playable == nil || !*a.Availability.Playable {
		t.Errorf("%+v", a)
	}
	for _, p := range list.Items {
		if p.ID == "M3-C0002" && (p.Availability.Playable != nil || p.ImageURL != nil || p.ReleasedAt != nil) {
			t.Errorf("never observed must be null: %+v", p.Availability)
		}
	}
	if !strings.Contains(body, `"playable":null`) {
		t.Error("an unobserved availability must be an explicit null")
	}
}

func TestAPIFilters(t *testing.T) {
	e := apiEnv(t)
	cases := []struct{ query, want string }{
		{"q=b%C3%A9ke", "M3-C0002"},
		{"q=ke", "M3-C0002"}, // 1-2 characters: titles
		{"creators=Nagy&genre=TV-film", "M3-C0002"},
		{"creators=nagy+istvan", "M3-C0002 M3-C0001"},
		{"year_from=1990", "M3-C0003 M3-C0001"},
		{"year_from=1980&year_to=1995&sort=year_asc", "M3-C0002 M3-C0001"},
		{"playable=true", "M3-C0001"},
		{"highlighted=true", "M3-C0001"},
		{"sort=title_asc", "M3-C0001 M3-C0002 M3-C0003"},
		{"sort=oldest", "M3-C0001 M3-C0002 M3-C0003"},
		{"q=M3-C0002,m3-c0003", "M3-C0003 M3-C0002"},
		{"description=mese", "M3-C0001"},
		{"q=nincsilyen", ""},
	}
	for _, c := range cases {
		resp, body := apiGet(t, e, "/api/v1/programs?"+c.query)
		if resp.StatusCode != 200 {
			t.Errorf("%s: %d %s", c.query, resp.StatusCode, body)
			continue
		}
		var list struct{ Items []dto.Program }
		decode(t, body, &list)
		var got []string
		for _, p := range list.Items {
			got = append(got, p.ID)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: %v, want %q", c.query, got, c.want)
		}
	}
}

func TestAPIPaging(t *testing.T) {
	var ps []model.Program
	for i := 0; i < 25; i++ {
		ps = append(ps, prog(fmt.Sprintf("M3-AP%04d", i), fmt.Sprintf("Film %03d", i)))
	}
	e := newEnv(t, nil, ps...)
	var list struct {
		Total   int
		Page    int
		PerPage int `json:"per_page"`
		Pages   int
		Items   []dto.Program
	}
	_, body := apiGet(t, e, "/api/v1/programs?per_page=10&page=3&sort=title_asc")
	decode(t, body, &list)
	if list.Total != 25 || list.Page != 3 || list.Pages != 3 || len(list.Items) != 5 || list.Items[0].Title != "Film 020" {
		t.Fatalf("%+v", list)
	}
	_, body = apiGet(t, e, "/api/v1/programs?page=9")
	decode(t, body, &list)
	if len(list.Items) != 0 || list.Total != 25 {
		t.Fatalf("a page past the end is empty, not an error: %+v", list)
	}
}

func TestAPIRejectsBadInputWithJSONErrors(t *testing.T) {
	e := apiEnv(t)
	payload := "SUPER_SECRET_PAYLOAD"
	cases := map[string]string{
		"q=a%00b":                        "invalid_query",
		"q=%ff%fe":                       "invalid_query",
		"creators=%00":                   "invalid_query",
		"creators=ab":                    "term_too_short",
		"per_page=0":                     "invalid_parameter",
		"per_page=101":                   "invalid_parameter",
		"per_page=" + payload:            "invalid_parameter",
		"page=0":                         "invalid_parameter",
		"page=1001":                      "invalid_parameter",
		"page=-1":                        "invalid_parameter",
		"year_from=abc":                  "invalid_parameter",
		"year_to=99999":                  "invalid_parameter",
		"playable=maybe":                 "invalid_parameter",
		"highlighted=" + payload:         "invalid_parameter",
		"sort=" + payload:                "invalid_parameter",
		"sort=id%3B+DROP+TABLE+programs": "invalid_parameter",
	}
	for query, code := range cases {
		resp, body := apiGet(t, e, "/api/v1/programs?"+query)
		if resp.StatusCode != 400 {
			t.Errorf("%s: status %d", query, resp.StatusCode)
			continue
		}
		var errBody struct {
			Error struct{ Code, Message string }
		}
		decode(t, body, &errBody)
		if errBody.Error.Code != code || errBody.Error.Message == "" {
			t.Errorf("%s: %+v", query, errBody)
		}
		if strings.Contains(body, payload) || strings.Contains(body, "DROP") {
			t.Errorf("%s: the error echoes the input: %s", query, body)
		}
		if resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: errors must not be cached", query)
		}
	}
}

func TestAPISingleProgram(t *testing.T) {
	e := apiEnv(t)
	resp, body := apiGet(t, e, "/api/v1/programs/m3-c0001") // case insensitive
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var p dto.Program
	decode(t, body, &p)
	if p.ID != "M3-C0001" || len(p.Availability.History) != 1 || !p.Availability.History[0].Playable {
		t.Fatalf("a single program carries its availability history: %+v", p.Availability)
	}
	if resp, body := apiGet(t, e, "/api/v1/programs/M3-NOPE1"); resp.StatusCode != 404 || !strings.Contains(body, `"not_found"`) {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	for _, bad := range []string{"nonsense", "M3-A%27B", "M3-A%2F..%2Fb", "M3-" + strings.Repeat("A", 40), "M3-A%00"} {
		if resp, _ := apiGet(t, e, "/api/v1/programs/"+bad); resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
	// lists carry no history
	_, body = apiGet(t, e, "/api/v1/programs")
	if strings.Contains(body, `"history"`) {
		t.Error("the list must not include histories")
	}
}

func TestAPIRoot(t *testing.T) {
	e := apiEnv(t)
	_, body := apiGet(t, e, "/api/v1/")
	var root struct {
		Programs          int
		DatabaseUpdatedAt *time.Time `json:"database_updated_at"`
		Openapi           string
		Limits            map[string]int
	}
	decode(t, body, &root)
	if root.Programs != 3 || root.DatabaseUpdatedAt == nil || root.Openapi != "/api/v1/openapi.json" || root.Limits["max_per_page"] != 100 {
		t.Fatalf("%+v", root)
	}
}

func TestAPIResponsesAreEscapedJSON(t *testing.T) {
	hostile := prog("M3-XSS99", `</script><img src=x onerror=alert(1)>"\`)
	hostile.Description = "<script>alert('x')</script>"
	e := newEnv(t, nil, hostile)
	resp, body := apiGet(t, e, "/api/v1/programs")
	if strings.Contains(body, "<script") || strings.Contains(body, "<img") {
		t.Errorf("raw HTML in a JSON answer: %s", body)
	}
	if !strings.Contains(body, `\u003cscript`) {
		t.Errorf("the angle brackets must be escaped inside the JSON: %.400s", body)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff is what stops a browser from treating JSON as HTML")
	}
	var list struct{ Items []dto.Program }
	decode(t, body, &list)
	if list.Items[0].Title != hostile.Title {
		t.Error("the JSON must round trip the text exactly")
	}
}

func TestAPICorsAndMethods(t *testing.T) {
	e := apiEnv(t)
	// CORS on successes, errors, unknown paths and preflights; not on the HTML site
	for _, path := range []string{"/api/v1/programs", "/api/v1/programs?per_page=0", "/api/v1/nope", "/api/v1/programs/M3-NOPE1"} {
		resp, _ := e.get(t, path)
		if resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
			t.Errorf("%s: CORS/CORP headers missing", path)
		}
	}
	if resp, _ := e.get(t, "/"); resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Error("the HTML pages must stay same-origin")
	}
	resp, _ := e.do(t, "OPTIONS", "/api/v1/programs", map[string]string{"Origin": "https://other.example", "Access-Control-Request-Method": "GET"})
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "GET") || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		resp, body := e.do(t, m, "/api/v1/programs", nil)
		if resp.StatusCode != 405 || !strings.Contains(body, "method_not_allowed") || !strings.Contains(resp.Header.Get("Allow"), "GET") {
			t.Errorf("%s: %d %s", m, resp.StatusCode, body)
		}
	}
	if resp, body := apiGet(t, e, "/api/v1/nothing/here"); resp.StatusCode != 404 || !strings.Contains(body, "not_found") {
		t.Errorf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, "HEAD", "/api/v1/programs", nil); resp.StatusCode != 200 {
		t.Errorf("HEAD: %d", resp.StatusCode)
	}
}

func TestAPIIsRateLimitedAndCanBeSwitchedOff(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.RateLimit = 0.0001; o.RateBurst = 3 }, catalog()...)
	limited := false
	for i := 0; i < 10 && !limited; i++ {
		resp, _ := e.get(t, "/api/v1/programs")
		if resp.StatusCode == 429 {
			limited = true
			if resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Retry-After") == "" {
				t.Error("a 429 must be readable by browser clients and say when to retry")
			}
		}
	}
	if !limited {
		t.Error("the API must share the visitor rate limit")
	}

	off := newEnv(t, func(o *Options) { o.PublicAPI, o.PublicExport = false, false }, catalog()...)
	for _, path := range []string{"/api/", "/api/v1/programs", "/api/v1/openapi.json", "/export/", "/export/schema.sql"} {
		if resp, _ := off.get(t, path); resp.StatusCode != 404 {
			t.Errorf("%s with the API and downloads switched off: %d", path, resp.StatusCode)
		}
	}
	if _, body := off.get(t, "/"); strings.Contains(body, `href="/api/"`) || strings.Contains(body, `href="/export/"`) {
		t.Error("the footer links to pages that are switched off")
	}
}

// the OpenAPI document must describe what the server really returns and accepts
func TestOpenAPIDescribesTheRealTypes(t *testing.T) {
	e := apiEnv(t)
	resp, body := e.get(t, "/api/v1/openapi.json")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	var doc struct {
		Openapi string
		Paths   map[string]map[string]struct {
			Parameters []struct{ Name string }
		}
		Components struct {
			Schemas map[string]struct {
				Properties map[string]json.RawMessage
			}
		}
	}
	decode(t, body, &doc)
	if doc.Openapi != "3.1.0" {
		t.Fatal(doc.Openapi)
	}
	tags := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for i := 0; i < rt.NumField(); i++ {
			if tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
				out = append(out, tag)
			}
		}
		sort.Strings(out)
		return out
	}
	keys := func(m map[string]json.RawMessage) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	for name, typ := range map[string]any{"Program": dto.Program{}, "Availability": dto.Availability{}, "Change": dto.Change{}} {
		if got, want := keys(doc.Components.Schemas[name].Properties), tags(typ); !reflect.DeepEqual(got, want) {
			t.Errorf("OpenAPI schema %s drifted from the Go type:\n spec: %v\n  go:  %v", name, got, want)
		}
	}
	// every query parameter the server reads is documented, and nothing else is
	want := []string{"q", "title", "subtitle", "description", "creators", "contributors", "genre", "info",
		"year_from", "year_to", "playable", "highlighted", "sort", "page", "per_page"}
	var got []string
	for _, p := range doc.Paths["/api/v1/programs"]["get"].Parameters {
		got = append(got, p.Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("documented parameters %v, implemented %v", got, want)
	}
	for _, field := range want[:0] {
		_ = field
	}
	for _, path := range []string{"/api/v1/", "/api/v1/programs", "/api/v1/programs/{id}", "/api/v1/openapi.json"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("path %s is not documented", path)
		}
	}
}

func TestAPIDocumentationPage(t *testing.T) {
	e := apiEnv(t)
	resp, body := e.get(t, "/api/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("%d", resp.StatusCode)
	}
	assertSecure(t, "api docs", resp)
	for _, want := range []string{"/api/v1/programs", "openapi.json", "playable", "capped", `href="/export/"`, "CORS"} {
		if !strings.Contains(body, want) {
			t.Errorf("the documentation lacks %q", want)
		}
	}
	// the documentation page has no search form and no script of its own
	if strings.Contains(body, `id="search-form"`) || strings.Contains(body, "<script") {
		t.Error("unexpected form or script on the documentation page")
	}
}

// FuzzAPIQuery sends arbitrary query strings to the list endpoint: whatever arrives, the answer is
// valid JSON with a 2xx/4xx status, never a server error and never a panic.
func FuzzAPIQuery(f *testing.F) {
	for _, seed := range []string{"q=abc", "q=%00", "per_page=999999999999999999999", "sort=title_asc&page=2", "year_from=-1",
		"creators=%22+OR+1%3D1", "playable=true&highlighted=1", "q=%ff%fe&title=%25&genre=_", "=&=&", "q=" + strings.Repeat("a", 3000)} {
		f.Add(seed)
	}
	// no rate limit here: the fuzzer sends thousands of requests per second (the limiter has its own tests)
	srv := newEnv(f, func(o *Options) { o.RateLimit, o.RateBurst = 1e9, 1_000_000_000 }, catalog()...).s
	f.Fuzz(func(t *testing.T, raw string) {
		req := httptestRequest(raw)
		if req == nil {
			return
		}
		rec := serve(srv, req)
		if rec.Code >= 500 {
			t.Fatalf("%q: status %d", raw, rec.Code)
		}
		if !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("%q: the answer is not valid JSON: %.200s", raw, rec.Body.String())
		}
	})
}
