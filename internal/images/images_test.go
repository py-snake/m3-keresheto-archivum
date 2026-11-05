package images

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
)

func cover(t *testing.T) []byte { return fakeupstream.Fixture(t, "cover.jpg") }

func dims(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

func TestSanitizeRealCover(t *testing.T) {
	in := cover(t) // 280x210, as served by the real site
	w, h := dims(t, in)

	out, err := Sanitize(in, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ow, oh := dims(t, out); ow != w || oh != h {
		t.Fatalf("size changed without scaling: %dx%d -> %dx%d", w, h, ow, oh)
	}
	if bytes.Equal(in, out) {
		t.Fatal("the upstream bytes must not be stored as they are")
	}
	// the comment segment the upstream encoder writes must be gone
	if bytes.Contains(out, []byte("CREATOR")) {
		t.Fatal("metadata survived the re-encoding")
	}

	small, err := Sanitize(in, 140)
	if err != nil {
		t.Fatal(err)
	}
	if sw, sh := dims(t, small); sw != 140 || sh != 105 {
		t.Fatalf("scaled to %dx%d, want 140x105", sw, sh)
	}
}

func TestSanitizeDoesNotUpscaleAndKeepsWideImages(t *testing.T) {
	out, err := Sanitize(cover(t), 4000)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := dims(t, out); w != 280 {
		t.Fatalf("image was upscaled to %d", w)
	}
}

func TestSanitizeRejectsNonImages(t *testing.T) {
	notFound := fakeupstream.Fixture(t, "cover_notfound.html") // the real "HTTP 200 but not an image" answer
	cases := map[string][]byte{
		"html 200 page":   notFound,
		"empty":           nil,
		"png header":      {0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0},
		"gif":             []byte("GIF89a......"),
		"jpeg magic only": {0xFF, 0xD8, 0xFF, 0xE0},
		"svg":             []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`),
	}
	for name, data := range cases {
		if _, err := Sanitize(data, 480); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := Sanitize(notFound, 480); !errors.Is(err, ErrNotJPEG) {
		t.Errorf("html page: %v", err)
	}
}

func TestSanitizeRejectsTruncatedAndOversized(t *testing.T) {
	in := cover(t)
	if _, err := Sanitize(in[:len(in)/2], 0); err == nil {
		t.Error("truncated JPEG accepted")
	}
	if _, err := Sanitize(make([]byte, MaxInputBytes+1), 0); !errors.Is(err, ErrTooLarge) {
		t.Error("oversized input accepted")
	}

	// a small file that claims to be 60000x60000 pixels (decompression bomb)
	bomb := bytes.Clone(in)
	i := bytes.Index(bomb, []byte{0xFF, 0xC0}) // SOF0: marker, length(2), precision, height(2), width(2)
	if i < 0 {
		t.Skip("fixture has no baseline SOF0 marker")
	}
	bomb[i+5], bomb[i+6], bomb[i+7], bomb[i+8] = 0xEA, 0x60, 0xEA, 0x60
	if _, err := Sanitize(bomb, 480); !errors.Is(err, ErrTooLarge) {
		t.Errorf("decompression bomb: %v", err)
	}
}

func TestSanitizeStripsAppendedData(t *testing.T) {
	polyglot := append(bytes.Clone(cover(t)), []byte("<script>alert('x')</script>PK\x03\x04")...)
	out, err := Sanitize(polyglot, 0)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("<script>")) || bytes.Contains(out, []byte("PK\x03\x04")) {
		t.Fatal("appended data survived")
	}
}

func TestScaleDownKeepsColors(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 100, 50, 255
	}
	dst := scaleDown(src, 10, 10)
	if c := dst.RGBAAt(5, 5); c.R != 200 || c.G != 100 || c.B != 50 {
		t.Fatalf("color changed: %+v", c)
	}
}

func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "images")
	if err := Save(dir, "M3-ABC1", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, "M3-ABC1", []byte("two")); err != nil { // replaced as a whole
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "M3-ABC1.jpg"))
	if string(got) != "two" {
		t.Fatalf("content %q", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "M3-ABC1.jpg")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode())
	}
	for _, bad := range []string{"", "../x", "M3-A/B", "M3-A\x00", "m3-abc"} {
		if err := Save(dir, bad, []byte("x")); err == nil {
			t.Errorf("Save accepted id %q", bad)
		}
	}
}

func TestOpenCannotEscapeTheDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "images")
	secret := filepath.Join(base, "secret.txt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(secret, []byte("TOP SECRET"), 0o644)
	_ = Save(dir, "M3-GOOD1", []byte("jpeg bytes"))

	// symbolic links an attacker could plant: outside the root, and to a directory
	_ = os.Symlink(secret, filepath.Join(dir, "M3-LINK1.jpg"))
	_ = os.Symlink(base, filepath.Join(dir, "M3-DIR1.jpg"))
	_ = os.Mkdir(filepath.Join(dir, "M3-ISDIR.jpg"), 0o755)

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	f, _, err := Open(root, "M3-GOOD1")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	for _, id := range []string{"M3-LINK1", "M3-DIR1", "M3-ISDIR", "M3-MISSING", "../secret.txt", "M3-A/../../secret", strings.Repeat("A", 100), "M3-A\x00"} {
		if f, _, err := Open(root, id); err == nil {
			f.Close()
			t.Errorf("Open(%q) succeeded", id)
		}
	}
}

func FuzzSanitize(f *testing.F) {
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F'})
	f.Add([]byte("<html>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := Sanitize(data, 480) // must never panic; an accepted result must be a real JPEG
		if err == nil {
			if _, e := jpeg.Decode(bytes.NewReader(out)); e != nil {
				t.Fatalf("output is not decodable: %v", e)
			}
		}
	})
}
