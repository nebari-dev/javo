package apitest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/features/codehostgithub"
)

const PullRequestEvent = `{"action":"opened","number":12}`

func GithubHosts() *registry.Registry[codehost.Host] {
	r := registry.New[codehost.Host]()
	r.Register(codehostgithub.New(codehostgithub.Config{}))
	return r
}

type HookJSON struct{ URL, Secret, Notice string }

// HookFixture is alice with two repository apps, each with a webhook.
type HookFixture struct {
	*Fixture
	Site, Docs HookJSON
}

func NewHookFixture(t *testing.T) *HookFixture {
	t.Helper()
	f := New(t, Options{CodeHosts: GithubHosts()}, append(RepoTemplates(), RepoApp("site"), RepoApp("docs"))...)
	alice := f.Session(AliceID)
	h := &HookFixture{Fixture: f}
	for name, out := range map[string]*HookJSON{"site": &h.Site, "docs": &h.Docs} {
		w := f.Do("POST", "/api/workspaces/"+name+"/previews/webhook", "", alice)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		*out = Decode[HookJSON](t, w)
	}
	return h
}

func Signature(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Deliver posts a GitHub delivery signed with secret ("" sends no signature).
func (f *Fixture) Deliver(path, event, body, secret string) *httptest.ResponseRecorder {
	f.T.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", event)
	if secret != "" {
		r.Header.Set("X-Hub-Signature-256", Signature(body, secret))
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

func HookPath(t *testing.T, h HookJSON) string {
	t.Helper()
	u, err := url.Parse(h.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Path
}

func (f *Fixture) PokedAt(name string) time.Time {
	f.T.Helper()
	at, err := f.Db.Previews().PokedAt(context.Background(), f.GetWorkspace(AliceID, name).ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return at
}

// NewWebhookJSON is the answer that makes a webhook for an app not yet created.
type NewWebhookJSON struct{ ID, URL, Secret, Notice string }
