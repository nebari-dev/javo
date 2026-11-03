package apps_test

import (
	"github.com/nebari-dev/javo/internal/gateway/apitest"
)

import (
	"bytes"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestThumbnailsOffChangesNothing(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts})
	alice := f.Session(apitest.AliceID)
	if w := f.PutThumb(apitest.ThumbPath, "image/png", apitest.TestPNG(t), alice); w.Code != http.StatusNotFound {
		t.Fatalf("PUT with thumbnails off = %d %s", w.Code, w.Body)
	}
	if w := f.Do("GET", apitest.ThumbPath, "", alice); w.Code != http.StatusNotFound {
		t.Fatalf("GET with thumbnails off = %d", w.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(f.Do("GET", "/api/workspaces/report", "", alice).Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["thumbnail"]; ok {
		t.Fatal("the app view carries a thumbnail while the feature is off")
	}
}

func TestOwnerSetsThumbnailAndItIsServedAsAnImageOnly(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts, Thumbnails: true})
	alice := f.Session(apitest.AliceID)

	if th := apitest.ThumbOf(t, f.Do("GET", "/api/workspaces/report", "", alice)); th == nil || th.URL != "" {
		t.Fatalf("before upload: %+v (want present, no url)", th)
	}
	w := f.PutThumb(apitest.ThumbPath, "image/png", apitest.TestPNG(t), alice)
	th := apitest.ThumbOf(t, w)
	if th == nil || th.URL == "" {
		t.Fatalf("after upload: %+v", th)
	}
	if list := f.Do("GET", "/api/workspaces", "", alice); !bytes.Contains(list.Body.Bytes(), []byte(th.URL)) {
		t.Fatalf("the apps list does not carry %s: %s", th.URL, list.Body)
	}

	got := f.Do("GET", th.URL, "", alice)
	if got.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", th.URL, got.Code, got.Body)
	}
	for k, want := range map[string]string{
		"Content-Type":           "image/png",
		"X-Content-Type-Options": "nosniff",
		"Content-Disposition":    "inline",
	} {
		if v := got.Header().Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
	if csp := got.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("no Content-Security-Policy")
	}
	if _, err := png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
		t.Fatalf("served body is not a PNG: %v", err)
	}

	// A browser that holds this picture is told it is still current.
	r := httptest.NewRequest("GET", th.URL, nil)
	r.Header.Set("If-None-Match", got.Header().Get("ETag"))
	r.AddCookie(alice)
	again := httptest.NewRecorder()
	f.Srv.ServeHTTP(again, r)
	if again.Code != http.StatusNotModified {
		t.Fatalf("revalidation = %d", again.Code)
	}

	if w := f.Do("DELETE", apitest.ThumbPath, "", alice); w.Code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", w.Code, w.Body)
	} else if th := apitest.ThumbOf(t, w); th == nil || th.URL != "" {
		t.Fatalf("after delete: %+v", th)
	}
	if w := f.Do("GET", th.URL, "", alice); w.Code != http.StatusNotFound {
		t.Fatalf("GET after delete = %d", w.Code)
	}
}

func TestThumbnailRefusesWhatIsNotAPNGOrJPEG(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts, Thumbnails: true})
	alice := f.Session(apitest.AliceID)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	// The content type the browser claims does not matter: the bytes do.
	for _, ct := range []string{"image/svg+xml", "image/png"} {
		if w := f.PutThumb(apitest.ThumbPath, ct, svg, alice); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("SVG as %s = %d %s", ct, w.Code, w.Body)
		}
	}
	if th := apitest.ThumbOf(t, f.Do("GET", "/api/workspaces/report", "", alice)); th == nil || th.URL != "" {
		t.Fatalf("a refused upload left %+v", th)
	}
	big := make([]byte, 5<<20)
	copy(big, apitest.TestPNG(t))
	if w := f.PutThumb(apitest.ThumbPath, "image/png", big, alice); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("5 MB upload = %d", w.Code)
	}
}

func TestThumbnailFollowsTheAppsSharing(t *testing.T) {
	t.Parallel()
	f := apitest.NewSharingFixture(t, apitest.Options{Urls: apitest.SeparateHosts, Thumbnails: true})
	alice, bob, carol := f.Session(apitest.AliceID), f.Session(apitest.BobID), f.Session(apitest.CarolID)
	th := apitest.ThumbOf(t, f.PutThumb(apitest.ThumbPath, "image/png", apitest.TestPNG(t), alice))

	// Private: nobody else sees the picture, and nobody else may change it.
	if w := f.Do("GET", th.URL, "", bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob GET a private app's picture = %d", w.Code)
	}
	f.Do("PUT", apitest.SharePath, `{"access":"Restricted","users":["`+apitest.BobID+`"],"groups":[]}`, alice)
	shared := apitest.Decode[struct {
		Items []struct{ Thumbnail *struct{ URL string } }
	}](t, f.Do("GET", "/api/shared", "", bob))
	if len(shared.Items) != 1 || shared.Items[0].Thumbnail == nil || shared.Items[0].Thumbnail.URL != th.URL {
		t.Fatalf("bob's shared list = %+v, want the picture %s", shared.Items, th.URL)
	}
	if w := f.Do("GET", th.URL, "", bob); w.Code != http.StatusOK {
		t.Fatalf("bob GET a shared app's picture = %d", w.Code)
	}
	if w := f.Do("GET", th.URL, "", carol); w.Code != http.StatusNotFound {
		t.Fatalf("carol GET a picture not shared with her = %d", w.Code)
	}
	owned := apitest.ThumbPath + "?owner=" + apitest.AliceID
	if w := f.PutThumb(owned, "image/png", apitest.TestPNG(t), bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob PUT on alice's app = %d", w.Code)
	}
	if w := f.Do("DELETE", owned, "", bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob DELETE on alice's app = %d", w.Code)
	}
	// A platform admin may change it.
	if w := f.PutThumb(owned, "image/png", apitest.TestPNG(t), f.Session(apitest.DaveID, "javo-admins")); w.Code != http.StatusOK {
		t.Fatalf("admin PUT = %d %s", w.Code, w.Body)
	}
}
