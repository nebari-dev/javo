//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

type sharingBody struct {
	Access string   `json:"access"`
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

func (s *session) share(name string, b sharingBody) {
	s.t.Helper()
	if b.Users == nil {
		b.Users = []string{}
	}
	if b.Groups == nil {
		b.Groups = []string{}
	}
	s.json(http.MethodPut, "/api/workspaces/"+name+"/sharing", b, http.StatusOK, nil)
}

// status requests url with the session's cookies, following redirects
// (the ticket flow), and returns the final status. A 200 from another host
// than url's (a sign-in page, say) counts as 0: the app was not reached.
func (s *session) status(url string) int {
	s.t.Helper()
	resp, _ := s.do(http.MethodGet, url, nil)
	if resp.StatusCode == http.StatusOK && resp.Request.URL.Host != mustURL(s.t, s.abs(url)).Host {
		return 0
	}
	return resp.StatusCode
}

// eventually polls url until it answers want or within passes.
func (s *session) eventually(url string, want int, within time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(within)
	got := 0
	for time.Now().Before(deadline) {
		if got = s.status(url); got == want {
			return
		}
		time.Sleep(time.Second)
	}
	s.t.Fatalf("GET %s: %d, want %d within %s", url, got, want, within)
}

// anonymous requests url with no cookies and no redirects followed, and
// returns the status and the host a redirect points at.
func anonymous(t *testing.T, raw string) (int, string) {
	t.Helper()
	hc := newClient(t)
	hc.Jar = nil
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r, err := hc.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	loc, _ := r.Location()
	if loc == nil {
		return r.StatusCode, ""
	}
	return r.StatusCode, loc.Host
}

// TestSharing has alice (Keycloak) share a Streamlit app with bob, with
// everyone, and with a group bob then leaves in Keycloak. TestCustomAmounts,
// which needs bob in that group, runs alone before it.
func TestSharing(t *testing.T) {
	t.Parallel()
	requireKeycloak(t)
	alice := newSession(t)
	alice.login(keycloakLogin, "alice")
	bob := newSession(t)
	bob.login(keycloakLogin, "bob")
	bobID := bob.me().ID

	var a app
	alice.json(http.MethodPost, "/api/samples/streamlit", nil, http.StatusCreated, &a)
	t.Cleanup(func() { alice.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
	a = alice.waitFor(a.Name, 5*time.Minute, func(a app) bool {
		if a.Phase == "Failed" {
			t.Fatalf("streamlit failed: %+v", a.Steps)
		}
		return a.Phase == "Running"
	})
	if got := alice.status(a.URL); got != http.StatusOK {
		t.Fatalf("alice opens her app: %d", got)
	}

	t.Run("bob opens it once shared and loses it once removed", func(t *testing.T) {
		alice.t, bob.t = t, t
		if got := bob.status(a.URL); got != http.StatusNotFound {
			t.Fatalf("bob before sharing: %d, want 404", got)
		}
		alice.share(a.Name, sharingBody{Access: "Restricted", Users: []string{bobID}})
		bob.eventually(a.URL, http.StatusOK, 10*time.Second)
		alice.share(a.Name, sharingBody{Access: "Private"})
		bob.eventually(a.URL, http.StatusNotFound, 10*time.Second)
	})

	t.Run("public app opens without signing in", func(t *testing.T) {
		alice.t = t
		alice.share(a.Name, sharingBody{Access: "Public"})
		deadline := time.Now().Add(10 * time.Second)
		for got, _ := anonymous(t, a.URL); got != http.StatusOK; got, _ = anonymous(t, a.URL) {
			if time.Now().After(deadline) {
				t.Fatalf("anonymous GET of a public app: %d, want 200 with no redirect", got)
			}
			time.Sleep(time.Second)
		}
		// A private app looks to someone not signed in exactly like a missing
		// one: they are sent to sign in on the UI host.
		alice.share(a.Name, sharingBody{Access: "Private"})
		ui := mustURL(t, alice.base).Host
		deadline = time.Now().Add(10 * time.Second)
		for {
			got, to := anonymous(t, a.URL)
			if got == http.StatusFound && to == ui {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("anonymous GET of a private app: %d to %q, want 302 to sign in on %s", got, to, ui)
			}
			time.Sleep(time.Second)
		}
	})

	t.Run("bob loses a group share when Keycloak drops him from the group", func(t *testing.T) {
		alice.t, bob.t = t, t
		alice.share(a.Name, sharingBody{Access: "Restricted", Groups: []string{dsGroup}})
		bob.eventually(a.URL, http.StatusOK, 10*time.Second)
		kc := newKeycloakAdmin(t)
		kc.setMember("bob", dsGroup, false)
		t.Cleanup(func() { kc.setMember("bob", dsGroup, true) })
		// Dev values revalidate every 30 s.
		bob.eventually(a.URL, http.StatusNotFound, 60*time.Second)
	})
}
