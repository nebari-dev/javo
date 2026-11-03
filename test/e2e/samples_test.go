//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/controller/workspace"
)

// heavy templates take long on a first start (RStudio's large image, Pluto's
// precompile); they run only with JAVO_E2E_HEAVY=1.
var heavyTemplates = []string{"rstudio", "pluto"}

// editMarker is what the test appends to a sample's first file. It is
// whitespace so every sample stays valid (a notebook is JSON, where a
// comment line would break it).
const editMarker = "\n\n"

// cellEdits are sample edits for apps that rewrite their file on open in
// their own format, which drops added whitespace: a sed program on the
// first file that changes a cell and makes the file one byte longer.
var cellEdits = map[string]string{
	// Pluto keeps each cell's code as written.
	"pluto": `s/^principal = 1000$/principal = 10000/`,
}

type homeFolder struct {
	Stale   bool
	Entries []struct {
		Name string
		Kind string
		Size int64
	}
}

// firstFile returns the first regular file of the user's home folder dir. The
// first listing answers 503 HomeStarting while the file pod starts, and a
// repeat one may be the gateway's last listing (stale), so it asks again until
// the folder opens and the listing is fresh.
func (s *session) firstFile(dir string) (name string, size int64) {
	s.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		resp, data := s.do(http.MethodGet, "/api/files?path="+dir, nil)
		if resp.StatusCode == http.StatusServiceUnavailable && time.Now().Before(deadline) {
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			s.t.Fatalf("GET /api/files?path=%s: %d %s", dir, resp.StatusCode, data)
		}
		var l homeFolder
		if err := json.Unmarshal(data, &l); err != nil {
			s.t.Fatalf("%v: %s", err, data)
		}
		if l.Stale && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		for _, e := range l.Entries {
			if e.Kind == "file" {
				return e.Name, e.Size
			}
		}
		s.t.Fatalf("%s has no file: %s", dir, data)
	}
}

// TestSamples starts every template's sample as a user with an empty
// sample folder: one click, a running app that answers 200 through the
// gateway, and an edit to the copied file that survives a restart.
func TestSamples(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)

	var templates struct {
		Items []struct {
			Name   string
			Sample *struct{ Title, Image string }
		}
	}
	s.json(http.MethodGet, "/api/templates", nil, http.StatusOK, &templates)
	ensureHomeClaim(t, c, me.ID)
	ownerUID(t, s, c, me.ID) // once, before the subtests run side by side

	for _, tpl := range templates.Items {
		// An image sample has no files to copy; TestImageSample starts it.
		if tpl.Sample == nil || tpl.Sample.Image != "" {
			continue
		}
		t.Run(tpl.Name, func(t *testing.T) {
			if slices.Contains(heavyTemplates, tpl.Name) && os.Getenv("JAVO_E2E_HEAVY") != "1" {
				t.Skip("heavy template; set JAVO_E2E_HEAVY=1")
			}
			t.Parallel()
			s := s.with(t)
			dir := workspace.SampleDir + "/" + tpl.Name
			// Start from a home with no copy of this sample.
			runHomeJob(t, s, c, me.ID, "rm", "-rf", "/home/user/"+dir)
			t.Cleanup(func() { runHomeJob(t, s, c, me.ID, "rm", "-rf", "/home/user/"+dir) })

			var a app
			s.json(http.MethodPost, "/api/samples/"+tpl.Name, nil, http.StatusCreated, &a)
			t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })

			running := func(a app) bool {
				if a.Phase == "Failed" {
					t.Fatalf("%s sample failed: %+v", tpl.Name, a.Steps)
				}
				return a.Phase == "Running"
			}
			start := time.Now()
			got := s.waitFor(a.Name, 8*time.Minute, running)
			// The first start installs the template's environment unless the
			// home has it already; the restart below reuses it.
			t.Logf("%s: first start running after %s", tpl.Name, time.Since(start).Round(time.Second))
			if resp, body := s.do(http.MethodGet, got.URL, nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
			}

			// Edit the copied file while the app is stopped, then start again:
			// the copy must not overwrite the edit.
			s.json(http.MethodPost, "/api/workspaces/"+a.Name+"/stop", nil, http.StatusOK, nil)
			s.waitFor(a.Name, 2*time.Minute, func(a app) bool { return a.Phase == "Stopped" })
			file, before := s.firstFile(dir)
			want := before + int64(len(editMarker))
			if prog, ok := cellEdits[tpl.Name]; ok {
				runHomeJob(t, s, c, me.ID, "sed", "-i", prog, "/home/user/"+dir+"/"+file)
				want = before + 1
			} else {
				runHomeJob(t, s, c, me.ID, "sh", "-c", `printf '%s' "$2" >> "$1"`, "edit", "/home/user/"+dir+"/"+file, editMarker)
			}
			s.json(http.MethodPost, "/api/workspaces/"+a.Name+"/start", nil, http.StatusOK, nil)
			start = time.Now()
			s.waitFor(a.Name, 8*time.Minute, running)
			t.Logf("%s: second start running after %s", tpl.Name, time.Since(start).Round(time.Second))
			// A listing up to a few seconds old counts as current, so the
			// edit can take one refresh to show.
			deadline := time.Now().Add(30 * time.Second)
			for {
				_, after := s.firstFile(dir)
				if after == want {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s/%s is %d bytes after restart, want %d: the edit did not survive", dir, file, after, want)
				}
				time.Sleep(time.Second)
			}
		})
	}
}
