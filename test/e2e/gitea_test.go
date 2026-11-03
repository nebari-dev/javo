//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
)

// The hermetic Git side of the suite (scripts/test/e2e-git-up.sh, JAVO_E2E_GIT=1):
// Gitea serves every repository under https://github.com/<owner>/<repo>
// inside the cluster, and fakegithub answers the pull request list. The
// tests reach both through kubectl port-forward, so nothing is published on
// the host.
const (
	e2eNS      = "javo-e2e"
	giteaAdmin = "e2e-admin"
	// repoOwner is the organization every fixture repository lives in.
	repoOwner = "example-org"
	// repoHost is the address every app clones from; the cluster's DNS sends it to Gitea.
	repoHost = "https://github.com/"
)

// repoURL is the address an app clones the fixture repository name from.
func repoURL(name string) string { return repoHost + repoOwner + "/" + name }

// requireHermeticGit skips the test unless the cluster runs the Git side.
func requireHermeticGit(t *testing.T) {
	t.Helper()
	var d appsv1.Deployment
	err := kube(t).Get(context.Background(), client.ObjectKey{Namespace: e2eNS, Name: "gitea"}, &d)
	if err != nil {
		t.Skipf("no Gitea in namespace %s: run make e2e with JAVO_E2E_GIT=1 (%v)", e2eNS, err)
	}
}

// portForward forwards a local port to port of target in namespace javo-e2e
// until the test ends, and returns its address.
func portForward(t *testing.T, target string, port int) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "-n", e2eNS, "port-forward", target, fmt.Sprintf(":%d", port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	re := regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if m := re.FindStringSubmatch(sc.Text()); m != nil {
				found <- "127.0.0.1:" + m[1]
				break
			}
		}
		_, _ = io.Copy(io.Discard, out)
	}()
	select {
	case addr := <-found:
		return addr
	case <-time.After(30 * time.Second):
		t.Fatalf("kubectl port-forward %s did not report a port", target)
		return ""
	}
}

// gitea is the in-cluster Git server, seen through its REST API as its admin.
type gitea struct {
	t    *testing.T
	base string
	pass string
	http *http.Client
}

func newGitea(t *testing.T) *gitea {
	t.Helper()
	requireHermeticGit(t)
	g := &gitea{
		t:    t,
		base: "https://" + portForward(t, "deploy/gitea", 3000),
		pass: readSecret(t, devFile(t, "gitea-admin.password")),
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}, //nolint:gosec // the test server's self-signed certificate
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(time.Second) {
		if resp, err := g.http.Get(g.base + "/api/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Gitea does not answer through the port-forward")
		}
	}
	g.call(http.MethodPost, "/orgs", map[string]any{"username": repoOwner}, http.StatusCreated, http.StatusUnprocessableEntity)
	return g
}

// call sends an API request as the admin and returns the body; the status
// must be one of want (default 200).
func (g *gitea) call(method, path string, body any, want ...int) []byte {
	g.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			g.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, g.base+"/api/v1"+path, r)
	if err != nil {
		g.t.Fatal(err)
	}
	req.SetBasicAuth(giteaAdmin, g.pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.http.Do(req)
	if err != nil {
		g.t.Fatalf("gitea %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if len(want) == 0 {
		want = []int{http.StatusOK}
	}
	for _, w := range want {
		if resp.StatusCode == w {
			return data
		}
	}
	g.t.Fatalf("gitea %s %s: status %d, want %v: %s", method, path, resp.StatusCode, want, data)
	return nil
}

// commitOf is the commit a contents call made.
func commitOf(t *testing.T, data []byte) string {
	t.Helper()
	var out struct{ Commit struct{ SHA string } }
	if err := json.Unmarshal(data, &out); err != nil || out.Commit.SHA == "" {
		t.Fatalf("no commit in %s (%v)", data, err)
	}
	return out.Commit.SHA
}

// seedRepo makes repository name under the organization with the files of
// testdata/repos/<fixture> on branch main, and returns the commit.
func (g *gitea) seedRepo(name, fixture string, private bool) string {
	g.t.Helper()
	g.call(http.MethodPost, "/orgs/"+repoOwner+"/repos",
		map[string]any{"name": name, "private": private, "auto_init": true, "default_branch": "main"}, http.StatusCreated)
	root := filepath.Join(repoRoot(g.t), "test", "e2e", "testdata", "repos", fixture)
	var files []map[string]string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, map[string]string{"operation": "create", "path": filepath.ToSlash(rel), "content": base64.StdEncoding.EncodeToString(b)})
		return nil
	})
	if err != nil {
		g.t.Fatal(err)
	}
	data := g.call(http.MethodPost, fmt.Sprintf("/repos/%s/%s/contents", repoOwner, name),
		map[string]any{"message": "add " + fixture, "branch": "main", "files": files}, http.StatusCreated)
	return commitOf(g.t, data)
}

// write puts content at path on branch (creating or replacing the file) and
// returns the new commit.
func (g *gitea) write(repo, branch, path, content string) string {
	g.t.Helper()
	op := "create"
	var existing struct{ SHA string }
	api := fmt.Sprintf("/repos/%s/%s/contents/%s", repoOwner, repo, path)
	if resp, err := g.get(api + "?ref=" + url.QueryEscape(branch)); err == nil && json.Unmarshal(resp, &existing) == nil && existing.SHA != "" {
		op = "update"
	}
	file := map[string]string{"operation": op, "path": path, "content": base64.StdEncoding.EncodeToString([]byte(content))}
	if op == "update" {
		file["sha"] = existing.SHA
	}
	data := g.call(http.MethodPost, fmt.Sprintf("/repos/%s/%s/contents", repoOwner, repo),
		map[string]any{"message": "change " + path, "branch": branch, "files": []map[string]string{file}}, http.StatusCreated)
	return commitOf(g.t, data)
}

// get is a GET that returns the body of a 200 answer and an error otherwise.
func (g *gitea) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, g.base+"/api/v1"+path, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(giteaAdmin, g.pass)
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return data, nil
}

// branch creates branch from main and returns nothing; write then commits to it.
func (g *gitea) branch(repo, name string) {
	g.t.Helper()
	g.call(http.MethodPost, fmt.Sprintf("/repos/%s/%s/branches", repoOwner, repo),
		map[string]any{"new_branch_name": name, "old_branch_name": "main"}, http.StatusCreated)
}

// readToken makes a token that may read repositories. It is returned, never
// logged, and deleted with the test.
func (g *gitea) readToken() string {
	g.t.Helper()
	name := "e2e-" + randomHex(g.t, 6)
	data := g.call(http.MethodPost, "/users/"+giteaAdmin+"/tokens", map[string]any{"name": name, "scopes": []string{"read:repository"}}, http.StatusCreated)
	var out struct{ Sha1 string }
	if err := json.Unmarshal(data, &out); err != nil || out.Sha1 == "" {
		g.t.Fatalf("token answer has no token (%v)", err)
	}
	g.t.Cleanup(func() {
		g.call(http.MethodDelete, "/users/"+giteaAdmin+"/tokens/"+name, nil, http.StatusNoContent, http.StatusNotFound)
	})
	return out.Sha1
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// fakeGitHub sets what the pull request list of the given repository (a
// name under the organization) answers; each call replaces all state.
type fakePull struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	HeadSHA   string    `json:"headSha"`
	Fork      bool      `json:"fork,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type fakeGitHub struct {
	t    *testing.T
	base string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	requireHermeticGit(t)
	return &fakeGitHub{t: t, base: "http://" + portForward(t, "deploy/fakegithub", 8080)}
}

// set replaces the open pull requests of repo (one repository at a time).
func (f *fakeGitHub) set(repo string, pulls ...fakePull) {
	f.t.Helper()
	b, err := json.Marshal(map[string][]fakePull{repoOwner + "/" + repo: pulls})
	if err != nil {
		f.t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, f.base+"/_state", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		f.t.Fatalf("PUT /_state: %d", resp.StatusCode)
	}
}

// hold makes the pull request list of repo answer only after d (0 clears it).
func (f *fakeGitHub) hold(repo string, d time.Duration) {
	f.t.Helper()
	b, err := json.Marshal(map[string]string{repoOwner + "/" + repo: d.String()})
	if err != nil {
		f.t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPut, f.base+"/_delay", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		f.t.Fatalf("PUT /_delay: %d", resp.StatusCode)
	}
}

// waitHeld returns once a list call is waiting in the hold.
func (f *fakeGitHub) waitHeld(within time.Duration) {
	f.t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(time.Second) {
		resp, err := http.Get(f.base + "/_waiting")
		if err != nil {
			f.t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.TrimSpace(string(b)) != "0" {
			return
		}
	}
	f.t.Fatalf("no pull request list call was held within %s", within)
}
