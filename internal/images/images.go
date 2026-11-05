// Package images validates, sanitizes, stores and opens cover images.
//
// Cover images come from the upstream and are untrusted binary input. Nothing the
// upstream sent is stored or served as-is: an image is only accepted if it is a
// JPEG within sane dimensions, and what is written to disk is a fresh encoding
// of the decoded pixels, so metadata, appended data and polyglot tricks vanish.
package images

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"

	"github.com/py-snake/m3-keresheto-archivum/internal/progid"
)

// Limits protecting the decoder (a "decompression bomb" is a tiny file that expands to gigabytes).
const (
	MaxInputBytes = 2 << 20
	maxSide       = 4096
	maxPixels     = 12_000_000
	jpegQuality   = 82
)

// Errors returned by Sanitize.
var (
	ErrNotJPEG  = errors.New("images: not a JPEG")
	ErrTooLarge = errors.New("images: image too large")
)

// Sanitize validates raw upstream bytes and returns a clean JPEG, scaled down to maxWidth
// pixels of width when it is wider (maxWidth <= 0 keeps the size).
func Sanitize(data []byte, maxWidth int) ([]byte, error) {
	if len(data) > MaxInputBytes {
		return nil, ErrTooLarge
	}
	// The magic bytes, not the Content-Type header or the HTTP status: the upstream
	// answers 200 with an HTML page for unknown ids.
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF {
		return nil, ErrNotJPEG
	}

	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJPEG, err)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxSide || cfg.Height > maxSide || cfg.Width*cfg.Height > maxPixels {
		return nil, ErrTooLarge
	}

	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotJPEG, err)
	}
	if b := img.Bounds(); maxWidth > 0 && b.Dx() > maxWidth {
		img = scaleDown(img, maxWidth, max(1, (b.Dy()*maxWidth+b.Dx()/2)/b.Dx()))
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("images: encoding: %w", err)
	}
	return out.Bytes(), nil
}

// scaleDown shrinks img to w x h by averaging the covered source pixels (box filter).
func scaleDown(src image.Image, w, h int) *image.RGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, b, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					pr, pg, pb, _ := src.At(sb.Min.X+xx, sb.Min.Y+yy).RGBA()
					r, g, b, n = r+uint64(pr), g+uint64(pg), b+uint64(pb), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(b / n >> 8)
			dst.Pix[i+3] = 0xff
		}
	}
	return dst
}

// FileName is the file name of a program's cover image. The id must be valid.
func FileName(id string) (string, error) {
	if !progid.Valid(id) {
		return "", errors.New("images: malformed program id")
	}
	return id + ".jpg", nil
}

// Save writes data as <dir>/<id>.jpg atomically: temp file, fsync, rename, fsync of the
// directory. An existing file is replaced as a whole; it is never modified in place.
func Save(dir, id string, data []byte) error {
	name, err := FileName(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := filepath.Join(dir, ".tmp-"+hex.EncodeToString(rnd[:])+".jpg")

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Open opens a stored cover image through an os.Root, which makes it impossible to leave
// the images directory (no "..", no symbolic link pointing outside). Only regular files
// are returned.
func Open(root *os.Root, id string) (*os.File, os.FileInfo, error) {
	name, err := FileName(id)
	if err != nil {
		return nil, nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxInputBytes {
		_ = f.Close()
		return nil, nil, os.ErrNotExist
	}
	return f, info, nil
}
