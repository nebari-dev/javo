package hooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestWebhookSetupShowsTheSecretOnceAndReplacesIt(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	alice := f.Session(apitest.AliceID)
	if !strings.Contains(f.Site.URL, "/hooks/github/") || len(f.Site.Secret) < 32 || f.Site.Notice != reasons.Msg(reasons.MsgWebhookSecretOnce) {
		t.Fatalf("setup answer = %+v", f.Site)
	}
	// The view says a webhook exists and never shows the secret or the address.
	w := f.Do("GET", "/api/workspaces/site", "", alice)
	v := apitest.Decode[apitest.RepoWorkspaceJSON](t, w)
	if !v.Source.WebhookConfigured || strings.Contains(w.Body.String(), f.Site.Secret) || strings.Contains(w.Body.String(), apitest.HookPath(t, f.Site)) {
		t.Fatalf("view: %s", w.Body)
	}
	// Setting it up again replaces both: the old address stops answering.
	again := apitest.Decode[apitest.HookJSON](t, f.Do("POST", "/api/workspaces/site/previews/webhook", "", alice))
	if again.URL == f.Site.URL || again.Secret == f.Site.Secret {
		t.Fatal("a second setup kept the old address or secret")
	}
	if w := f.Deliver(apitest.HookPath(t, f.Site), "ping", "{}", f.Site.Secret); w.Code != http.StatusNotFound {
		t.Fatalf("old address: %d %s", w.Code, w.Body)
	}
	// The secret is stored for the signature check and goes nowhere else.
	events, err := f.Db.Audit().List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	for _, h := range []apitest.HookJSON{f.Site, again} {
		if strings.Contains(string(raw), h.Secret) || strings.Contains(string(raw), apitest.HookPath(t, h)) {
			t.Fatal("a webhook secret or address reached the audit log")
		}
	}
}

func TestWebhookSetupIsRefusedWhereItCannotWork(t *testing.T) {
	t.Parallel()
	home := gatewaytest.Workspace("plain", apitest.AliceID, "Plain")
	f := apitest.New(t, apitest.Options{CodeHosts: apitest.GithubHosts()}, append(apitest.RepoTemplates(), apitest.RepoApp("site"), home)...)
	alice := f.Session(apitest.AliceID)
	child := f.PreviewChild(apitest.RepoApp("site"), 12)
	for name, want := range map[string]int{"plain": 422, "site--pr-12": 409, "nope": 404} {
		if w := f.Do("POST", "/api/workspaces/"+name+"/previews/webhook", "", alice); w.Code != want {
			t.Errorf("%s (%s): %d %s, want %d", name, child.Name, w.Code, w.Body, want)
		}
	}
	if w := f.Do("POST", "/api/workspaces/site/previews/webhook", "", f.Session(apitest.BobID)); w.Code != http.StatusNotFound {
		t.Errorf("bob: %d %s", w.Code, w.Body)
	}
}

// The New app form makes the webhook before the app exists, so it can be
// pasted into GitHub first: the address answers once the app is created with
// it, and the app starts with previews on.
func TestWebhookMadeBeforeTheAppWorksOnceItIsCreated(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{CodeHosts: apitest.GithubHosts(), Previews: true}, apitest.RepoTemplates()...)
	alice := f.Session(apitest.AliceID)
	w := f.Do("POST", "/api/previews/webhook", `{"url":"https://github.com/example-org/site"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("new webhook: %d %s", w.Code, w.Body)
	}
	hook := apitest.Decode[apitest.NewWebhookJSON](t, w)
	if !strings.HasSuffix(hook.URL, "/hooks/github/"+hook.ID) || len(hook.ID) < 22 || len(hook.Secret) < 43 || hook.Notice != reasons.Msg(reasons.MsgWebhookSecretOnce) {
		t.Fatalf("new webhook = %+v", hook)
	}
	path := apitest.HookPath(t, apitest.HookJSON{URL: hook.URL})
	if w := f.Deliver(path, "ping", "{}", hook.Secret); w.Code != http.StatusNotFound {
		t.Fatalf("delivery before the app exists: %d %s", w.Code, w.Body)
	}

	body := `{"displayName":"Site","template":"streamlit","file":"app.py","source":{"url":"https://github.com/example-org/site",
		"previews":{"enabled":true,"max":2},"webhook":{"id":"` + hook.ID + `","secret":"` + hook.Secret + `"}}}`
	if w := f.Do("POST", "/api/workspaces", body, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	} else if strings.Contains(w.Body.String(), hook.Secret) || strings.Contains(w.Body.String(), hook.ID) {
		t.Fatalf("the create answer repeats the webhook: %s", w.Body)
	}
	if v := apitest.Decode[apitest.RepoWorkspaceJSON](t, f.Do("GET", "/api/workspaces/site", "", alice)); !v.Source.WebhookConfigured {
		t.Fatalf("view: %+v", v.Source)
	}
	if p := apitest.Decode[apitest.PreviewsJSON](t, f.Do("GET", "/api/workspaces/site/previews", "", alice)); !p.Enabled || p.Max != 2 || p.Forks {
		t.Fatalf("previews = %+v", p)
	}
	if w := f.Deliver(path, "pull_request", apitest.PullRequestEvent, hook.Secret); w.Code != http.StatusAccepted {
		t.Fatalf("delivery: %d %s", w.Code, w.Body)
	}
	if f.PokedAt("site").IsZero() {
		t.Fatal("the delivery did not poke the app")
	}
}

// What the form needs to offer previews: whether the platform has them, the
// most an app may ask for and the words next to the forks switch.
func TestPreviewOptionsAreThePlatforms(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{true, false} {
		f := apitest.New(t, apitest.Options{CodeHosts: apitest.GithubHosts(), Previews: on, PreviewsMax: 3})
		got := apitest.Decode[struct {
			Available       bool
			Max, MaxAllowed int32
			ForksWarning    string
		}](t, f.Do("GET", "/api/previews", "", f.Session(apitest.AliceID)))
		if got.Available != on || got.MaxAllowed != 3 || got.Max != 3 || got.ForksWarning != reasons.Msg(reasons.MsgPreviewForksWarning) {
			t.Errorf("previews %v: options %+v", on, got)
		}
	}
}

func TestPreviewsAndWebhookAtCreateAreRefusedWhereTheyCannotWork(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{CodeHosts: apitest.GithubHosts(), Previews: true}, apitest.RepoTemplates()...)
	alice := f.Session(apitest.AliceID)
	if w := f.Do("POST", "/api/previews/webhook", `{"url":"https://git.example.org/example-org/site"}`, alice); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("webhook for another host: %d %s", w.Code, w.Body)
	}
	hook := apitest.Decode[apitest.NewWebhookJSON](t, f.Do("POST", "/api/previews/webhook", `{"url":"https://github.com/example-org/site"}`, alice))
	create := func(url, extra string) string {
		return `{"displayName":"Site","template":"streamlit","file":"app.py","source":{"url":"` + url + `",` + extra + `}}`
	}
	for name, c := range map[string]struct{ body, field string }{
		"previews off GitHub": {create("https://git.example.org/example-org/site", `"previews":{"enabled":true,"max":1}`), "source.previews"},
		"too many previews":   {create("https://github.com/example-org/site", `"previews":{"enabled":true,"max":99}`), "source.previews.max"},
		"webhook off GitHub":  {create("https://git.example.org/example-org/site", `"webhook":{"id":"`+hook.ID+`","secret":"`+hook.Secret+`"}`), "source.webhook"},
		"made-up webhook":     {create("https://github.com/example-org/site", `"webhook":{"id":"short","secret":"`+hook.Secret+`"}`), "source.webhook"},
	} {
		w := f.Do("POST", "/api/workspaces", c.body, alice)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"body.`+c.field+`"`) {
			t.Errorf("%s: %d %s, want 422 on %s", name, w.Code, w.Body, c.field)
		}
	}
	if f.WorkspaceCount() != 0 {
		t.Fatalf("%d apps created", f.WorkspaceCount())
	}
}

func TestWebhookSignedWithAnotherAppsSecretIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	before := f.PokedAt("docs")
	w := f.Deliver(apitest.HookPath(t, f.Docs), "pull_request", apitest.PullRequestEvent, f.Site.Secret)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.PokedAt("docs"); !got.Equal(before) {
		t.Fatalf("poked_at moved from %v to %v", before, got)
	}
}

func TestWebhookWithoutOrWithBadSignatureIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	for name, secret := range map[string]string{"missing": "", "wrong": "not-the-secret"} {
		w := f.Deliver(apitest.HookPath(t, f.Site), "pull_request", apitest.PullRequestEvent, secret)
		if w.Code != http.StatusUnauthorized || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgWebhookSignature) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if !f.PokedAt("site").IsZero() {
		t.Fatal("a refused delivery poked the app")
	}
}

func TestWebhookUnknownAddressIsNotFound(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	if w := f.Deliver("/hooks/github/unknown", "ping", "{}", f.Site.Secret); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s", w.Code, w.Body)
	}
	if w := f.Deliver("/hooks/gitlab/"+apitest.HookPath(t, f.Site)[len("/hooks/github/"):], "ping", "{}", f.Site.Secret); w.Code != http.StatusNotFound {
		t.Fatalf("unregistered host: %d %s", w.Code, w.Body)
	}
}

func TestWebhookSignedPullRequestPokesOnceAndAnnouncesIt(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	// On Postgres announcements travel through the server, which needs the feed.
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := f.Db.StartFeed(ctx); err != nil {
		t.Fatal(err)
	}
	w := f.Deliver(apitest.HookPath(t, f.Site), "pull_request", apitest.PullRequestEvent, f.Site.Secret)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if time.Since(f.PokedAt("site")) > time.Minute || f.PokedAt("site").IsZero() {
		t.Fatalf("poked_at = %v", f.PokedAt("site"))
	}
	if !f.PokedAt("docs").IsZero() {
		t.Fatal("another app was poked")
	}
	// Announcements may arrive a moment after the commit (Postgres notifies
	// through the server), and setup changes may still be in flight: count the
	// pokes of previews, which only the delivery makes.
	site := f.GetWorkspace(apitest.AliceID, "site").ID
	pokes := 0
	for timeout := time.After(time.Second); ; {
		select {
		case c := <-f.Db.Changes():
			if c.Table == "app_previews" {
				if c.ID != site {
					t.Fatalf("change for another app: %+v", c)
				}
				pokes++
			}
			continue
		case <-timeout:
		}
		break
	}
	if pokes != 1 {
		t.Fatalf("%d previews changes announced, want 1", pokes)
	}
}

func TestWebhookPingPokesAndOtherEventsAreIgnored(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	if w := f.Deliver(apitest.HookPath(t, f.Site), "push", "{}", f.Site.Secret); w.Code != http.StatusNoContent || !f.PokedAt("site").IsZero() {
		t.Fatalf("push: %d %s, poked %v", w.Code, w.Body, f.PokedAt("site"))
	}
	if w := f.Deliver(apitest.HookPath(t, f.Site), "ping", "{}", f.Site.Secret); w.Code != http.StatusAccepted || f.PokedAt("site").IsZero() {
		t.Fatalf("ping: %d %s", w.Code, w.Body)
	}
}

func TestWebhookBodyOverOneMiBIsTooLarge(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	body := `{"pad":"` + strings.Repeat("x", 2<<20) + `"}`
	// Signed correctly: the size check must come first, not the signature.
	for _, secret := range []string{f.Site.Secret, ""} {
		w := f.Deliver(apitest.HookPath(t, f.Site), "pull_request", body, secret)
		if w.Code != http.StatusRequestEntityTooLarge || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgWebhookTooLarge) {
			t.Fatalf("status %d: %.200s", w.Code, w.Body)
		}
	}
	if !f.PokedAt("site").IsZero() {
		t.Fatal("an oversized delivery poked the app")
	}
	// A body that does not announce its length is cut off the same way.
	r := httptest.NewRequest("POST", apitest.HookPath(t, f.Site), bytes.NewReader([]byte(body)))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Event", "pull_request")
	r.Header.Set("X-Hub-Signature-256", apitest.Signature(body, f.Site.Secret))
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unsized body: %d", w.Code)
	}
}

func TestWebhookIsRateLimitedPerClientAddress(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	post := func(ip string) int {
		r := httptest.NewRequest("POST", "/hooks/github/unknown", strings.NewReader("{}"))
		r.RemoteAddr = ip + ":4000"
		w := httptest.NewRecorder()
		f.Srv.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
		return w.Code
	}
	limited := 0
	for range 200 {
		if post("192.0.2.77") == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("200 deliveries from one address were never limited")
	}
	if got := post("192.0.2.78"); got != http.StatusNotFound {
		t.Fatalf("another address: %d, want 404", got)
	}
}

// Behind an untrusted ingress every delivery comes from one in-cluster
// address: a flood at made-up hook addresses must not spend the bucket the
// real deliveries need, or previews stop syncing.
func TestAFloodThroughASharedIngressDoesNotStopRealDeliveries(t *testing.T) {
	t.Parallel()
	f := apitest.NewHookFixture(t)
	deliver := func(path, secret string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-GitHub-Event", "ping")
		if secret != "" {
			r.Header.Set("X-Hub-Signature-256", apitest.Signature("{}", secret))
		}
		r.RemoteAddr = "10.42.0.9:40000"
		w := httptest.NewRecorder()
		f.Srv.ServeHTTP(w, r)
		return w.Code
	}
	real := apitest.HookPath(t, f.Site)
	bogus := strings.Replace(real, "/hooks/github/", "/hooks/github/x", 1)
	for range 200 {
		deliver(bogus, "")
	}
	if code := deliver(real, f.Site.Secret); code != http.StatusAccepted {
		t.Fatalf("a real delivery after the flood: %d", code)
	}
}
