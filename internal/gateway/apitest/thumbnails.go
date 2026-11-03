package apitest

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/authn"
)

const ThumbPath = "/api/workspaces/report/thumbnail"

func TestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 32, 18))
	for x := range 32 {
		img.Set(x, x%18, color.NRGBA{200, 40, 40, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// PutThumb uploads body as the caller's picture for the app at path.
func (f *Fixture) PutThumb(path, contentType string, body []byte, c *http.Cookie) *httptest.ResponseRecorder {
	f.T.Helper()
	r := httptest.NewRequest("PUT", path, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set(authn.RequestHeader, "1")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

type ThumbView struct {
	Thumbnail *struct{ URL string }
}

func ThumbOf(t *testing.T, w *httptest.ResponseRecorder) *struct{ URL string } {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return Decode[ThumbView](t, w).Thumbnail
}
