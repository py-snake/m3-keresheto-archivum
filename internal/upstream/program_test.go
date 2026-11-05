package upstream

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
)

func TestParseRealItem(t *testing.T) {
	raw := fakeupstream.Fixture(t, "item.json")
	p, trunc, err := ParseProgram(raw)
	if err != nil || trunc {
		t.Fatalf("%v trunc=%v", err, trunc)
	}
	if p.ProgramID != "M3-59945851A60999998" || p.Title == "" {
		t.Fatalf("%+v", p)
	}
	// released = the earliest of start_playable_dts and start_startTime_dts (2025-10-23T16:00:19Z)
	if want := time.Date(2025, 10, 23, 16, 0, 19, 0, time.UTC).Unix(); p.ReleasedAt != want {
		t.Fatalf("ReleasedAt = %d, want %d", p.ReleasedAt, want)
	}
	if p.Duration == "" || p.Year == 0 {
		t.Fatalf("fields missing: %+v", p)
	}
}

func TestSeriesIDIsBase64Decoded(t *testing.T) {
	raw := `{"id":"M3-ABC1","title":"t","seriesId":"RsWRdMOpcg=="}`
	p, _, err := ParseProgram([]byte(raw))
	if err != nil || p.SeriesID != "Főtér" {
		t.Fatalf("%q %v", p.SeriesID, err)
	}
	// undecodable series ids become empty instead of garbage
	p, _, _ = ParseProgram([]byte(`{"id":"M3-ABC1","title":"t","seriesId":"%%%not base64%%%"}`))
	if p.SeriesID != "" {
		t.Fatalf("garbage series id kept: %q", p.SeriesID)
	}
}

func TestSubtitleFalseAndTypeConfusion(t *testing.T) {
	// the real upstream sends `false` for a missing subtitle; other fields may have odd types
	raw := `{"id":"M3-ABC1","title":"t","subtitle":false,"year":"1999","episode":7.0,"hasSubtitle":"true","isSeries":1,
	"info":["a",null,3,{"x":1},"b"],"genre":"not-a-list","quality":null,"pg":["x"]}`
	p, _, err := ParseProgram([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.Subtitle != "" || p.Year != 1999 || p.Episode != 7 || !p.HasSubtitle || !p.IsSeries {
		t.Fatalf("%+v", p)
	}
	if p.Info != "a\n3\nb" {
		t.Fatalf("list scalars only: %q", p.Info)
	}
	if p.Genre != "" || p.Quality != "" || p.PG != "" {
		t.Fatalf("wrongly typed fields must become empty: %+v", p)
	}
}

func TestInvalidItemsAreRejected(t *testing.T) {
	bad := []string{
		`null`, `false`, `"text"`, `[]`, `42`, ``, `{`, `{}`,
		`{"title":"no id"}`,
		`{"id":"M3-ABC1"}`, // no title
		`{"id":"M3-ABC1","title":"   "}`,
		`{"id":"M3-X') OR 1=1 --","title":"t"}`,
		`{"id":"m3-abc1","title":"t"}`,
		`{"id":["M3-ABC1"],"title":"t"}`,
		`{"id":"M3-ABC1/../../etc","title":"t"}`,
	}
	for _, in := range bad {
		if _, _, err := ParseProgram([]byte(in)); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}

func TestHostileTextIsCleaned(t *testing.T) {
	hostile := "a\x00b\x07c‮d⁦e​f\ufeffg h\x1bi" + "\r\n" + "j\xff\xfek"
	raw, _ := json.Marshal(map[string]any{"id": "M3-ABC1", "title": "T" + hostile, "description": hostile})
	p, _, err := ParseProgram(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.Title + p.Description {
		if r < 0x20 && r != '\n' && r != '\t' {
			t.Fatalf("control character %U survived", r)
		}
		if (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200b || r == 0xfeff || r == 0x2028 || r == 0xfffd {
			t.Fatalf("dangerous character %U survived", r)
		}
	}
	if !strings.Contains(p.Title, "Tabcdefghi") {
		t.Fatalf("legitimate text damaged: %q", p.Title)
	}
}

func TestLengthLimitsAndTruncationFlag(t *testing.T) {
	long := strings.Repeat("é", 400) // multi-byte characters: limits are in characters
	raw, _ := json.Marshal(map[string]any{
		"id": "M3-ABC1", "title": long, "country": "HUNGARY", "quality": strings.Repeat("x", 99),
		"description": strings.Repeat("d", maxBigText+500),
	})
	p, trunc, err := ParseProgram(raw)
	if err != nil || !trunc {
		t.Fatalf("err=%v trunc=%v", err, trunc)
	}
	if n := len([]rune(p.Title)); n != maxTitle {
		t.Fatalf("title %d runes", n)
	}
	if p.Country != "HUN" || len(p.Quality) != maxShortText || len([]rune(p.Description)) != maxBigText {
		t.Fatalf("limits not applied: %q %q", p.Country, p.Quality)
	}
}

func TestParseProgramsReport(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"id":"M3-AAA1","title":"ok"}`),
		json.RawMessage(`null`),
		json.RawMessage(`{"id":"bad","title":"x"}`),
		json.RawMessage(`{"id":"M3-AAA2","title":"ok2"}`),
	}
	got, rep := ParsePrograms(items)
	if len(got) != 2 || rep.Total != 4 || rep.Invalid != 2 || len(rep.Samples) != 2 {
		t.Fatalf("%d programs, report %+v", len(got), rep)
	}
}

func TestEarliestIgnoresGarbage(t *testing.T) {
	got := earliest(json.RawMessage(`["nonsense","2026-01-02T03:04:05Z","0001-01-01T00:00:00Z"]`), json.RawMessage(`"not a list"`), nil)
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Unix(); got != want {
		t.Fatalf("%d != %d", got, want)
	}
	if earliest(nil) != 0 {
		t.Fatal("no dates must give 0")
	}
}

func FuzzParseProgram(f *testing.F) {
	f.Add([]byte(`{"id":"M3-ABC1","title":"t","info":["a"],"year":2000}`))
	f.Add([]byte(`null`))
	f.Add(fakeupstreamItem(f))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, _, err := ParseProgram(data) // must never panic
		if err == nil && (p.ProgramID == "" || p.Title == "") {
			t.Fatalf("accepted an item without id/title: %q", data)
		}
	})
}

func fakeupstreamItem(f *testing.F) []byte {
	return fakeupstream.Fixture(f, "item.json")
}
