//go:build e2e

package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// A coding agent deploys a folder the way /llms.txt tells it: a device
// login the person approves in the browser, the folder uploaded as a tar.gz,
// an app made on the HTML page template, and a redeploy over the same folder.
func TestAgentDeploysAFolder(t *testing.T) {
	t.Parallel()
	s := newUser(t)

	// The agent starts a login without any credentials: a session never
	// signed in.
	agent := newSession(t)
	var start struct{ DeviceCode, UserCode string }
	agent.json(http.MethodPost, "/api/auth/device", nil, http.StatusOK, &start)
	poll := map[string]string{"deviceCode": start.DeviceCode}
	if resp, body := agent.do(http.MethodPost, "/api/auth/device/token", poll); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "authorization_pending") {
		t.Fatalf("poll before approval: %d %s", resp.StatusCode, body)
	}
	// The person approves it, signed in.
	s.json(http.MethodPost, "/api/auth/device/"+start.UserCode+"/approve", nil, http.StatusNoContent, nil)
	var tok struct{ Token string }
	agent.json(http.MethodPost, "/api/auth/device/token", poll, http.StatusOK, &tok)
	w := newBearer(t, tok.Token)

	folder := fmt.Sprintf("deploys/page-%d", time.Now().UnixNano()%100000)
	w.upload(folder, "<h1>first version</h1>", false, http.StatusCreated)
	code, body := w.do(http.MethodPost, "/api/workspaces", map[string]any{
		"displayName": "Agent page", "template": "static-site", "file": folder + "/index.html",
	})
	if code != http.StatusCreated {
		t.Fatalf("create with the agent's token: %d %s", code, body)
	}
	var a app
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
	s.waitFor(a.Name, 5*time.Minute, func(a app) bool { return a.Phase == "Running" })
	s.waitPage(a.URL, "first version", time.Minute)

	// Redeploy: the same folder replaced, then a restart.
	w.upload(folder, "<h1>second version</h1>", true, http.StatusOK)
	if code, body := w.do(http.MethodPost, "/api/workspaces/"+a.Name+"/restart", nil); code != http.StatusOK {
		t.Fatalf("restart with the agent's token: %d %s", code, body)
	}
	s.waitFor(a.Name, 5*time.Minute, func(a app) bool { return a.Phase == "Running" })
	s.waitPage(a.URL, "second version", time.Minute)
}

// upload sends a folder holding one index.html to path in the home, as
// PUT /api/files/archive, and expects want. A first upload waits while the
// home opens (503).
func (b *bearer) upload(path, page string, replace bool, want int) {
	b.t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: int64(len(page)), Typeflag: tar.TypeReg}); err != nil {
		b.t.Fatal(err)
	}
	if _, err := tw.Write([]byte(page)); err != nil {
		b.t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		b.t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		b.t.Fatal(err)
	}
	u := b.base + "/api/files/archive?path=" + path
	if replace {
		u += "&replace=true"
	}
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(3 * time.Second) {
		req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(buf.Bytes()))
		if err != nil {
			b.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+b.token)
		req.Header.Set(authn.RequestHeader, "1")
		req.Header.Set("Content-Type", "application/gzip")
		resp, err := b.http.Do(req)
		if err != nil {
			b.t.Fatalf("upload %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == want {
			return
		}
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			b.t.Fatalf("upload %s: %d, want %d: %s", path, resp.StatusCode, want, body)
		}
	}
}
