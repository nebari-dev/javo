// Package branding serves the install's white-label look: GET /api/branding
// for the UI, signed in or not, and the configured image files under
// /branding/.
package branding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// MaxImageBytes caps each image file; a logo or favicon is a few KiB.
const MaxImageBytes = 256 << 10

// imageCSP stops an SVG opened on its own from running script or loading
// anything; inline styles are all a logo needs.
const imageCSP = "default-src 'none'; style-src 'unsafe-inline'"

// The names images are served under, /branding/<name>.
const (
	logoLight = "logo-light"
	logoDark  = "logo-dark"
	favicon   = "favicon"
)

// BrandingView is what GET /api/branding answers. An empty image or
// accent means the UI keeps its built-in one.
type BrandingView struct {
	Name      string `json:"name" doc:"The product name the UI shows."`
	LogoLight string `json:"logoLight,omitempty" doc:"The logo's address for the light theme, and the dark one when logoDark is empty."`
	LogoDark  string `json:"logoDark,omitempty" doc:"The logo's address for the dark theme."`
	Favicon   string `json:"favicon,omitempty" doc:"The browser tab icon's address."`
	Accent    string `json:"accent,omitempty" doc:"The accent colour, #rgb or #rrggbb."`
}

type image struct {
	data  []byte
	ctype string
	etag  string
}

// Branding is the loaded look. The zero value is not usable; build it with
// Load or Default.
type Branding struct {
	view    BrandingView
	images  map[string]image
	origins []string
}

// Default is the built-in look.
func Default() *Branding {
	b, _ := Load(config.BrandingCfg{})
	return b
}

// Load reads and checks the configured image files: each must be at most
// MaxImageBytes and a PNG, JPEG, GIF, WebP, ICO or SVG. The config itself
// was validated by config.Load.
func Load(c config.BrandingCfg) (*Branding, error) {
	b := &Branding{images: map[string]image{}, view: BrandingView{Name: c.Name, Accent: c.Accent}}
	if b.view.Name == "" {
		b.view.Name = config.DefaultBrandName
	}
	for _, s := range []struct {
		key, name string
		img       config.BrandImage
		out       *string
	}{
		{"branding.logo.light", logoLight, c.Logo.Light, &b.view.LogoLight},
		{"branding.logo.dark", logoDark, c.Logo.Dark, &b.view.LogoDark},
		{"branding.favicon", favicon, c.Favicon, &b.view.Favicon},
	} {
		switch {
		case s.img.URL != "":
			*s.out = s.img.URL
			if u, err := url.Parse(s.img.URL); err == nil {
				if o := u.Scheme + "://" + u.Host; !slices.Contains(b.origins, o) {
					b.origins = append(b.origins, o)
				}
			}
		case s.img.File != "":
			img, err := readImage(s.img.File)
			if err != nil {
				return nil, fmt.Errorf("%s.file: %w", s.key, err)
			}
			b.images[s.name] = img
			*s.out = "/branding/" + s.name + "?v=" + img.etag
		}
	}
	return b, nil
}

func readImage(path string) (image, error) {
	f, err := os.Open(path)
	if err != nil {
		return image{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return image{}, err
	}
	if len(data) > MaxImageBytes {
		return image{}, fmt.Errorf("%s is over %d KiB", path, MaxImageBytes>>10)
	}
	ctype, ok := imageType(data)
	if !ok {
		return image{}, fmt.Errorf("%s is not a PNG, JPEG, GIF, WebP, ICO or SVG image", path)
	}
	sum := sha256.Sum256(data)
	return image{data: data, ctype: ctype, etag: hex.EncodeToString(sum[:8])}, nil
}

// imageType tells the type by the bytes, never by a file name.
func imageType(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/x-icon":
		return ct, true
	case "text/xml; charset=utf-8", "text/plain; charset=utf-8":
		if bytes.Contains(data, []byte("<svg")) {
			return "image/svg+xml", true
		}
	}
	return "", false
}

// ImageOrigins are the hosts of images given as a URL, which the UI's
// Content-Security-Policy must allow.
func (b *Branding) ImageOrigins() []string { return b.origins }

type viewOutput struct {
	Body BrandingView
}

// Register adds GET /api/branding to api and the image files to mux.
func (b *Branding) Register(api huma.API, mux *http.ServeMux) {
	huma.Register(api, huma.Operation{
		OperationID: "getBranding", Method: http.MethodGet, Path: "/api/branding",
		Summary: "The install's product name, logo, favicon and accent", Tags: []string{"branding"},
		Security: authn.NoSession,
	}, func(context.Context, *struct{}) (*viewOutput, error) {
		return &viewOutput{Body: b.view}, nil
	})
	mux.HandleFunc("GET /branding/{name}", b.serveImage)
}

func (b *Branding) serveImage(w http.ResponseWriter, r *http.Request) {
	img, ok := b.images[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", img.ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", imageCSP)
	h.Set("ETag", `"`+img.etag+`"`)
	// The address /api/branding hands out names the content, so it never
	// changes; any other may.
	if r.URL.Query().Get("v") == img.etag {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(img.data))
}
