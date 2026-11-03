// Package thumbnail turns an uploaded picture of an app into the image the
// gateway stores and serves on the app's card. Only PNG and JPEG are
// accepted, recognised by their leading bytes; the pixels are decoded and
// encoded again, so nothing but image data (no script, markup or metadata of
// a crafted file) is ever stored or served.
package thumbnail

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
)

const (
	// MaxBytes caps an upload.
	MaxBytes = 4 << 20
	// MaxSide is the longest side a stored thumbnail has; larger pictures
	// are shrunk to fit. Cards show it at about a third of this, so it stays
	// sharp on high-density screens.
	MaxSide = 960
	// maxPixels caps what an upload may decode to (64 MB of RGBA), checked
	// from the header before any pixel is decoded.
	maxPixels = 16_000_000
)

var (
	// ErrUnsupported is anything that is not a readable PNG or JPEG.
	ErrUnsupported = errors.New("thumbnail: not a PNG or JPEG image")
	// ErrTooLarge is an upload over MaxBytes or with too many pixels.
	ErrTooLarge = errors.New("thumbnail: image too large")
)

type format struct {
	magic        string
	contentType  string
	decodeConfig func([]byte) (image.Config, error)
	decode       func([]byte) (image.Image, error)
	encode       func(image.Image) ([]byte, error)
}

var formats = []format{
	{
		magic: "\x89PNG\r\n\x1a\n", contentType: "image/png",
		decodeConfig: func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) },
		decode:       func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) },
		encode: func(img image.Image) ([]byte, error) {
			var out bytes.Buffer
			err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img)
			return out.Bytes(), err
		},
	},
	{
		magic: "\xff\xd8\xff", contentType: "image/jpeg",
		decodeConfig: func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) },
		decode:       func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) },
		encode: func(img image.Image) ([]byte, error) {
			var out bytes.Buffer
			err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85})
			return out.Bytes(), err
		},
	},
}

// Normalize checks that in is a PNG or JPEG of acceptable size and returns
// it encoded afresh in the same format, shrunk to fit MaxSide, with its
// content type.
func Normalize(in []byte) (out []byte, contentType string, err error) {
	if len(in) > MaxBytes {
		return nil, "", ErrTooLarge
	}
	var f *format
	for i := range formats {
		if bytes.HasPrefix(in, []byte(formats[i].magic)) {
			f = &formats[i]
		}
	}
	if f == nil {
		return nil, "", ErrUnsupported
	}
	cfg, err := f.decodeConfig(in)
	if err != nil {
		return nil, "", ErrUnsupported
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", ErrUnsupported
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, "", ErrTooLarge
	}
	img, err := f.decode(in)
	if err != nil {
		return nil, "", ErrUnsupported
	}
	out, err = f.encode(fit(img, MaxSide))
	if err != nil {
		return nil, "", err
	}
	return out, f.contentType, nil
}

// fit shrinks img so neither side exceeds side, averaging each box of source
// pixels into one; a picture that already fits is returned as it is.
func fit(img image.Image, side int) image.Image {
	sb := img.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= side && sh <= side {
		return img
	}
	dw, dh := side, sh*side/sw
	if sh > sw {
		dw, dh = sw*side/sh, side
	}
	dw, dh = max(dw, 1), max(dh, 1)
	src := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(src, src.Bounds(), img, sb.Min, draw.Src)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := range dh {
		y0, y1 := dy*sh/dh, max((dy+1)*sh/dh, dy*sh/dh+1)
		for dx := range dw {
			x0, x1 := dx*sw/dw, max((dx+1)*sw/dw, dx*sw/dw+1)
			var r, g, b, a, n uint64
			for y := y0; y < y1; y++ {
				row := src.Pix[y*src.Stride:]
				for x := x0; x < x1; x++ {
					p := row[x*4 : x*4+4]
					r, g, b, a = r+uint64(p[0]), g+uint64(p[1]), b+uint64(p[2]), a+uint64(p[3])
					n++
				}
			}
			o := dst.Pix[dy*dst.Stride+dx*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n)
		}
	}
	return dst
}
