//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// bearer is a client that sends only an API token: no cookie jar.
type bearer struct {
	t     *testing.T
	base  string
	token string
	http  *http.Client
}

func newBearer(t *testing.T, token string) *bearer {
	t.Helper()
	c := newClient(t)
	c.Jar = nil
	return &bearer{t: t, base: uiBase(), token: token, http: c}
}

func (b *bearer) do(method, path string, body any) (int, []byte) {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, b.base+path, r)
	if err != nil {
		b.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

type createdToken struct {
	ID, Token, Scope string
}

// newAPIToken creates a token through the API with the signed-in session s
// and revokes it when the test ends.
func (s *session) newAPIToken(name, scope string) createdToken {
	s.t.Helper()
	var tok createdToken
	s.json(http.MethodPost, "/api/tokens", map[string]any{"name": name, "scope": scope, "expiresInDays": 1}, http.StatusCreated, &tok)
	s.t.Cleanup(func() { s.do(http.MethodDelete, "/api/tokens/"+tok.ID, nil) })
	return tok
}

// A token made in Settings creates, stops, starts and deletes an app and makes
// and runs a job with plain HTTP; a read token is refused on POST; a revoked
// token stops at once.
func TestAPITokens(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)

	rw := s.newAPIToken("e2e write", "read-write")
	w := newBearer(t, rw.Token)
	if code, body := w.do(http.MethodGet, "/api/me", nil); code != http.StatusOK || !strings.Contains(string(body), me.ID) {
		t.Fatalf("me with a token: %d %s", code, body)
	}
	name := fmt.Sprintf("tok-%d", time.Now().Unix()%100000)
	code, body := w.do(http.MethodPost, "/api/workspaces", map[string]any{"displayName": name, "template": "jupyterlab"})
	if code != http.StatusCreated {
		t.Fatalf("create with a token: %d %s", code, body)
	}
	var a app
	if err := json.Unmarshal(body, &a); err != nil {
		t.Fatal(err)
	}
	if code, body := w.do(http.MethodGet, "/api/workspaces/"+a.Name, nil); code != http.StatusOK {
		t.Fatalf("get with a token: %d %s", code, body)
	}
	for _, verb := range []string{"stop", "start"} {
		if code, body := w.do(http.MethodPost, "/api/workspaces/"+a.Name+"/"+verb, nil); code != http.StatusOK {
			t.Fatalf("%s with a token: %d %s", verb, code, body)
		}
	}
	// A job made and run with the token.
	code, body = w.do(http.MethodPost, "/api/jobs", map[string]any{
		"displayName": name + " job", "template": "jobs-python", "engine": "command", "command": "echo from a token",
	})
	if code != http.StatusCreated {
		t.Fatalf("create a job with a token: %d %s", code, body)
	}
	var job struct{ Name string }
	if err := json.Unmarshal(body, &job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(2 * time.Second) {
		code, body = w.do(http.MethodPost, "/api/jobs/"+job.Name+"/run", map[string]any{})
		if code == http.StatusCreated {
			break
		}
		if code != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("run a job with a token: %d %s", code, body)
		}
	}
	if code, body := w.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil); code != http.StatusNoContent {
		t.Fatalf("delete with a token: %d %s", code, body)
	}

	ro := newBearer(t, s.newAPIToken("e2e read", "read").Token)
	if code, _ := ro.do(http.MethodGet, "/api/workspaces", nil); code != http.StatusOK {
		t.Fatalf("list with a read token: %d", code)
	}
	if code, body := ro.do(http.MethodPost, "/api/workspaces", map[string]any{"displayName": name + "-ro", "template": "jupyterlab"}); code != http.StatusForbidden {
		t.Fatalf("create with a read token: %d %s", code, body)
	}

	// A token cannot make more tokens, even with a cookie beside it.
	if code, _ := w.do(http.MethodPost, "/api/tokens", map[string]any{"name": "more", "scope": "read"}); code != http.StatusForbidden {
		t.Fatalf("token creating a token: %d", code)
	}

	s.json(http.MethodDelete, "/api/tokens/"+rw.ID, nil, http.StatusNoContent, nil)
	if code, _ := w.do(http.MethodGet, "/api/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
}

// A coding-agent app's pod has JAVO_URL and a JAVO_TOKEN that works against
// /api/me from inside the pod; the token is listed as used by the app and is
// revoked with it. The agent reads the API guide where it looks for
// instructions.
func TestCodingAgentGetsAToken(t *testing.T) {
	if !devValues() {
		t.Skip("needs kubectl exec on a dev cluster")
	}
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	a := s.create(fmt.Sprintf("agent %d", time.Now().Unix()%100000), "claude-code", "")
	s.waitFor(a.Name, 5*time.Minute, running(t))

	c := kube(t)
	pod := appPod(t, c, me.ID, a.Name)
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "JAVO_TOKEN" && (e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil) {
			t.Fatalf("JAVO_TOKEN is not read from a Secret: %+v", e)
		}
	}
	var svc corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: gatewayNS, Name: "javo-gateway"}, &svc); err != nil {
		t.Fatal(err)
	}
	// JAVO_URL is the public address; the dev host name does not resolve in
	// a pod, so the call from inside goes to the gateway's Service with the
	// UI's host in the Host header, as a request to JAVO_URL would carry.
	gw := fmt.Sprintf("http://javo-gateway.%s.svc:%d", gatewayNS, svc.Spec.Ports[0].Port)
	script := `const u = new URL(process.argv[1]);
require('http').get({host: u.hostname, port: u.port, path: '/api/me', headers: {
  Host: new URL(process.env.JAVO_URL).host, Authorization: 'Bearer ' + process.env.JAVO_TOKEN}}, (r) => {
  let b = ''; r.on('data', (d) => (b += d)); r.on('end', () => console.log(r.statusCode, process.env.JAVO_URL, b))
})`
	out := kubectl(t, "-n", appsNS, "exec", pod.Name, "-c", "app", "--", "node", "-e", script, gw)
	if !strings.HasPrefix(out, "200 "+uiBase()+" ") || !strings.Contains(out, me.ID) {
		t.Fatalf("/api/me from the agent's pod: %s", out)
	}
	for _, f := range []string{"/opt/javo/AGENTS.md", "/etc/claude-code/CLAUDE.md"} {
		if guide := kubectl(t, "-n", appsNS, "exec", pod.Name, "-c", "app", "--", "cat", f); !strings.Contains(guide, "JAVO_TOKEN") {
			t.Fatalf("%s in the pod: %q", f, guide)
		}
	}

	var tokens []struct{ ID, App string }
	s.json(http.MethodGet, "/api/tokens", nil, http.StatusOK, &tokens)
	found := false
	for _, tok := range tokens {
		found = found || tok.App == a.Name
	}
	if !found {
		t.Fatalf("no token used by app %s in %+v", a.Name, tokens)
	}
	s.json(http.MethodDelete, "/api/workspaces/"+a.Name, nil, http.StatusNoContent, nil)
	s.json(http.MethodGet, "/api/tokens", nil, http.StatusOK, &tokens)
	for _, tok := range tokens {
		if tok.App == a.Name {
			t.Fatalf("app %s deleted, its token %s remains", a.Name, tok.ID)
		}
	}
}

// The API docs page is served (the kind values turn it on) with its assets
// and a CSP that allows only this origin.
func TestAPIDocsPage(t *testing.T) {
	if !devValues() {
		t.Skip("the docs page is on in the dev values only")
	}
	t.Parallel()
	c := newClient(t)
	for _, p := range []string{"/api/docs/", "/api/docs/swagger-ui-bundle.js", "/api/docs/swagger-ui.css", "/api/docs/init.js"} {
		resp, err := c.Get(uiBase() + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
			t.Fatalf("%s: %d %v", p, resp.StatusCode, resp.Header)
		}
	}
}

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("kubectl", args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// apiCaller calls /api/me and then a write (POST) through the gateway
// Service given as its argument, with the app's JAVO_TOKEN and JAVO_URL's
// host, prints both answers, and then serves 200 on $PORT so the app is
// ready. One line, double quotes only, no braces or dollar signs, so it
// passes as a typed command.
const apiCaller = `import os,sys,urllib.request as u,urllib.parse as p,http.server as h; ` +
	`H=dict(Authorization="Bearer "+os.environ["JAVO_TOKEN"],Host=p.urlsplit(os.environ["JAVO_URL"]).netloc); ` +
	`o=u.build_opener(type("N",(u.HTTPErrorProcessor,),dict(http_response=lambda s,q,r:r))); ` +
	`me=o.open(u.Request(sys.argv[1]+"/api/me",headers=H)); print("API me",me.status,me.read().decode(),flush=True); ` +
	`w=o.open(u.Request(sys.argv[1]+"/api/workspaces/none/stop",method="POST",headers=H)); print("API write",w.status,flush=True); ` +
	`h.HTTPServer(("",int(os.environ["PORT"])),type("S",(h.BaseHTTPRequestHandler,),dict(do_GET=lambda s:(s.send_response(200),s.end_headers())))).serve_forever()`

// Any app can ask for API access. A custom-command app with Read access
// calls /api/me at start (200, as its owner) and is refused a write (403);
// its token is listed with the read scope. Turning access off restarts it
// without a token.
func TestAppWithReadAPIAccess(t *testing.T) {
	if !devValues() {
		t.Skip("needs the gateway Service address inside a dev cluster")
	}
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)
	var svc corev1.Service
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: gatewayNS, Name: "javo-gateway"}, &svc); err != nil {
		t.Fatal(err)
	}
	gw := fmt.Sprintf("http://javo-gateway.%s.svc:%d", gatewayNS, svc.Spec.Ports[0].Port)
	a := s.createApp(map[string]any{
		"displayName": fmt.Sprintf("reader %d", time.Now().Unix()%100000), "template": "custom",
		"command": "python -c '" + apiCaller + "' " + gw, "apiAccess": "Read",
	})
	s.waitFor(a.Name, 5*time.Minute, running(t))

	var logs struct{ Lines []struct{ Text string } }
	deadline := time.Now().Add(time.Minute)
	var all string
	for {
		s.json(http.MethodGet, "/api/workspaces/"+a.Name+"/logs", nil, http.StatusOK, &logs)
		all = ""
		for _, l := range logs.Lines {
			all += l.Text + "\n"
		}
		if strings.Contains(all, "API write") || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !strings.Contains(all, "API me 200 ") || !strings.Contains(all, me.ID) {
		t.Fatalf("/api/me from the app:\n%s", all)
	}
	if !strings.Contains(all, "API write 403") {
		t.Fatalf("a write with read access:\n%s", all)
	}
	var tokens []struct{ App, Scope string }
	s.json(http.MethodGet, "/api/tokens", nil, http.StatusOK, &tokens)
	scopes := []string{}
	for _, tok := range tokens {
		if tok.App == a.Name {
			scopes = append(scopes, tok.Scope)
		}
	}
	if len(scopes) != 1 || scopes[0] != "read" {
		t.Fatalf("tokens of app %s: %v", a.Name, scopes)
	}

	old := appPod(t, c, me.ID, a.Name)
	s.json(http.MethodPut, "/api/workspaces/"+a.Name+"/api-access", map[string]any{"apiAccess": "None"}, http.StatusOK, nil)
	deadline = time.Now().Add(3 * time.Minute)
	for {
		var p corev1.Pod
		err := c.Get(context.Background(), client.ObjectKeyFromObject(old), &p)
		if err == nil && p.UID != old.UID {
			for _, e := range p.Spec.Containers[0].Env {
				if e.Name == "JAVO_TOKEN" {
					t.Fatal("the restarted app still gets JAVO_TOKEN")
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the app did not restart after its API access was turned off")
		}
		time.Sleep(2 * time.Second)
	}
	s.json(http.MethodGet, "/api/tokens", nil, http.StatusOK, &tokens)
	for _, tok := range tokens {
		if tok.App == a.Name {
			t.Fatalf("API access off, the app's token remains: %+v", tok)
		}
	}
}
