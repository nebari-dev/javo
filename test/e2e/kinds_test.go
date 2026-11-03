//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// assetRef finds the first local script or stylesheet a page references.
// The extension must end the path, so manifest.json is not read as a script.
var assetRef = regexp.MustCompile(`(?:src|href)="([^"#?]+\.(?:js|css))(?:[?#][^"]*)?"`)

// TestKinds starts each kind on its own sample copied into alice's home as a
// file (or, for the folder kinds, a folder) the user "uploaded", and checks
// what a browser needs through the real gateway: the app is Running, its page
// answers 200 and is not a sign-in page, one static asset the page references
// answers 200, and (where the kind has one) its websocket upgrades. VS Code,
// RStudio and the coding agents are the basePath Strip kinds: the gateway
// removes the prefix and follows RStudio's cookie-scoped redirects, which only
// this test exercises.
func TestKinds(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)
	ensureHomeClaim(t, c, me.ID)
	ownerUID(t, s, c, me.ID) // once, before the subtests run side by side

	kinds := []struct {
		template, file string
		command        string         // the typed command, for templates that take one
		folder         bool           // the kind opens a folder: seed every file of the sample folder, open the folder
		noAsset        bool           // the page references no script or stylesheet
		ws             string         // websocket path under the app URL; empty skips
		wsProtocol     string         // Sec-WebSocket-Protocol the app's page asks for, if any
		wsID           *regexp.Regexp // when set, its first group is read from the page body and replaces @ID@ in ws
	}{
		{template: "marimo", file: "sample.py", ws: "ws?session_id=s_e2e"},
		// Pluto's editor opens its websocket on the prefix itself.
		{template: "pluto", file: "sample.jl", ws: "?ws=e2e"},
		{template: "panel", file: "sample.py", ws: "sample/ws"},
		{template: "bokeh", file: "sample.py", ws: "sample/ws"},
		{template: "dash", file: "sample.py"},
		{template: "gradio", file: "sample.py"},
		{template: "voila", file: "sample.ipynb", ws: "api/kernels/@ID@/channels", wsID: regexp.MustCompile(`kernelId": "([^"]+)"`)},
		{template: "fastapi", file: "sample.py", noAsset: true},
		// Runs in the folder of the chosen file.
		{template: "custom", file: "sample.py", command: "python sample.py --port={{.Port}} --base-url={{.BaseURL}}", noAsset: true},
		// VS Code's reconnection websocket is the one a workbench opens first.
		{template: "vscode", folder: true, ws: "?reconnectionToken=e2e&reconnection=false&skipWebSocketFrames=false"},
		// RStudio long-polls and has no websocket on its main page.
		{template: "rstudio", folder: true},
		// ttyd's page carries its script inline. Its terminal websocket needs
		// the "tty" subprotocol (without it ttyd answers 101 but runs nothing)
		// and an Origin that matches the Host the gateway passes on.
		{template: "claude-code", folder: true, noAsset: true, ws: "ws", wsProtocol: "tty"},
		{template: "pi", folder: true, noAsset: true, ws: "ws", wsProtocol: "tty"},
		{template: "herdr", folder: true, noAsset: true, ws: "ws", wsProtocol: "tty"},
		{template: "terminal", folder: true, noAsset: true, ws: "ws", wsProtocol: "tty"},
	}
	for _, k := range kinds {
		t.Run(k.template, func(t *testing.T) {
			t.Parallel()
			s := s.with(t)
			if slices.Contains(heavyTemplates, k.template) && os.Getenv("JAVO_E2E_HEAVY") != "1" {
				t.Skip("heavy template; set JAVO_E2E_HEAVY=1")
			}
			sampleDir := filepath.Join(repoRoot(t), "pod", "files", "samples", k.template)
			files := []string{k.file}
			if k.folder {
				entries, err := os.ReadDir(sampleDir)
				if err != nil {
					t.Fatal(err)
				}
				files = files[:0]
				for _, e := range entries {
					files = append(files, e.Name())
				}
			}
			dir := "e2e/kinds/" + k.template
			for _, f := range files {
				src, err := os.ReadFile(filepath.Join(sampleDir, f))
				if err != nil {
					t.Fatal(err)
				}
				runHomeJob(t, s, c, me.ID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+dir+"/"+f, string(src))
			}
			t.Cleanup(func() { runHomeJob(t, s, c, me.ID, "rm", "-rf", "/home/user/e2e/kinds/"+k.template) })
			home := dir + "/" + k.file
			if k.folder {
				home = dir
			}

			req := map[string]any{"displayName": "e2e " + k.template + " " + time.Now().Format("150405"), "template": k.template, "file": home}
			if k.command != "" {
				req["command"] = k.command
			}
			a := s.createApp(req)
			got := s.waitFor(a.Name, 8*time.Minute, func(a app) bool {
				if a.Phase == "Failed" {
					t.Fatalf("%s failed: %+v", k.template, a.Steps)
				}
				return a.Phase == "Running"
			})

			resp, body := s.do(http.MethodGet, got.URL, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
			}
			if strings.Contains(resp.Request.URL.Path, "sign-in") {
				t.Fatalf("GET %s ended on a sign-in page: %s", got.URL, resp.Request.URL)
			}
			if !k.noAsset {
				m := assetRef.FindSubmatch(body)
				if m == nil {
					t.Fatalf("%s: page references no script or stylesheet: %.300s", got.URL, body)
				}
				// Resolve against the page the redirects ended on.
				ref, err := resp.Request.URL.Parse(string(m[1]))
				if err != nil {
					t.Fatal(err)
				}
				// Absolute: apps may be on their own host.
				asset := ref.String()
				if r, _ := s.do(http.MethodGet, asset, nil); r.StatusCode != http.StatusOK {
					t.Fatalf("GET %s: %d", asset, r.StatusCode)
				}
			}
			if k.ws != "" {
				ws := k.ws
				if k.wsID != nil {
					m := k.wsID.FindSubmatch(body)
					if m == nil {
						t.Fatalf("%s: page has no id matching %s: %.300s", got.URL, k.wsID, body)
					}
					ws = strings.ReplaceAll(ws, "@ID@", string(m[1]))
				}
				var extra []string
				if k.wsProtocol != "" {
					extra = append(extra, "Sec-WebSocket-Protocol: "+k.wsProtocol)
				}
				if code := websocketStatus(t, s, got.URL+ws, extra...); code != http.StatusSwitchingProtocols {
					t.Fatalf("websocket %s%s: %d, want 101", got.URL, ws, code)
				}
			}
		})
	}
}
