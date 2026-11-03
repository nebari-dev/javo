package thumbnail_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/nebari-dev/javo/internal/thumbnail"
)

func picture(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	return img
}

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestNormalizeKeepsPNGAndJPEGAsTheirOwnType(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{pngOf(t, picture(64, 40)), "image/png"},
		{jpegOf(t, picture(64, 40)), "image/jpeg"},
	} {
		out, typ, err := thumbnail.Normalize(tc.in)
		if err != nil || typ != tc.want {
			t.Fatalf("Normalize = %q, %v; want %q", typ, err, tc.want)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
		if err != nil || "image/"+format != tc.want || cfg.Width != 64 || cfg.Height != 40 {
			t.Fatalf("output decodes as %s %dx%d, %v", format, cfg.Width, cfg.Height, err)
		}
	}
}

func TestNormalizeRefusesWhatIsNotPNGOrJPEG(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, picture(8, 8), nil); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]byte{
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`),
		"html":  []byte(`<html><script>alert(1)</script></html>`),
		"gif":   g.Bytes(),
		"empty": nil,
		// The PNG signature with nothing readable after it.
		"truncated png": []byte("\x89PNG\r\n\x1a\n<script>alert(1)</script>"),
	} {
		if _, _, err := thumbnail.Normalize(in); !errors.Is(err, thumbnail.ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", name, err)
		}
	}
}

// Whatever rides along after the image data (a polyglot's HTML) is gone:
// the stored bytes are a fresh encoding of the pixels.
func TestNormalizeDropsBytesThatAreNotPixels(t *testing.T) {
	payload := []byte("<script>alert(document.cookie)</script>")
	in := append(pngOf(t, picture(16, 16)), payload...)
	out, _, err := thumbnail.Normalize(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, payload) {
		t.Fatal("the appended script survived")
	}
}

func TestNormalizeShrinksLargeImagesToFit(t *testing.T) {
	out, _, err := thumbnail.Normalize(pngOf(t, picture(thumbnail.MaxSide*2, thumbnail.MaxSide)))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != thumbnail.MaxSide || cfg.Height != thumbnail.MaxSide/2 {
		t.Fatalf("output %dx%d, %v; want %dx%d", cfg.Width, cfg.Height, err, thumbnail.MaxSide, thumbnail.MaxSide/2)
	}
}

// An image whose header claims huge dimensions is refused before its pixels
// are decoded, so a small upload cannot ask for gigabytes of memory.
func TestNormalizeRefusesHugeDimensions(t *testing.T) {
	// A PNG signature and a valid IHDR chunk claiming 100000x100000 RGB
	// pixels, and no pixel data.
	ihdr := []byte("IHDR\x00\x01\x86\xa0\x00\x01\x86\xa0\x08\x02\x00\x00\x00")
	in := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0d")
	in = append(in, ihdr...)
	in = binary.BigEndian.AppendUint32(in, crc32.ChecksumIEEE(ihdr))
	if _, _, err := thumbnail.Normalize(in); !errors.Is(err, thumbnail.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
