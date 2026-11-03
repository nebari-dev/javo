//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// The dev Keycloak (deploy/dev/keycloak): realm javo, alice in /javo-admins
// and /org/data-science, bob in /org/data-science. scripts/dev/keycloak-up.sh
// writes .dev/<user>.keycloak<suffix>.password and
// .dev/keycloak-admin<suffix>.password, where the suffix is
// JAVO_SECRETS_SUFFIX: empty on kind, ".<site>" (JAVO_SITE) for
// scripts/install/deploy-single-node.sh.
const (
	realm       = "javo"
	adminsGroup = "/javo-admins"
	dsGroup     = "/org/data-science"
)

// keycloakBase is the Keycloak URL up to /auth: JAVO_KEYCLOAK_URL, or the
// kind cluster's.
func keycloakBase() string {
	if u := os.Getenv("JAVO_KEYCLOAK_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://keycloak.localhost:" + kindPort("JAVO_KEYCLOAK_PORT", "8081") + "/auth"
}

// requireKeycloak skips the test on a deployment that offers no Keycloak
// sign-in, except on kind, where dev-up always deploys it, and with
// JAVO_E2E_KEYCLOAK=1 (make e2e-full deploys it).
func requireKeycloak(t *testing.T) {
	t.Helper()
	s := newSession(t)
	var opts []struct {
		ID       string
		Disabled bool
		Reason   string
	}
	s.json(http.MethodGet, "/api/auth/options", nil, http.StatusOK, &opts)
	for _, o := range opts {
		if o.ID == "oidc" {
			if o.Disabled {
				t.Fatalf("Keycloak sign-in is offered but disabled: %s", o.Reason)
			}
			return
		}
	}
	if onKind() || os.Getenv("JAVO_E2E_KEYCLOAK") == "1" {
		t.Fatal("the deployment offers no Keycloak sign-in; run JAVO_KEYCLOAK=1 make up")
	}
	t.Skip("this deployment offers no Keycloak sign-in")
}

// loginKeycloak signs in the way a browser does: the gateway sends it to
// Keycloak, the login form is filled in, and Keycloak sends it back to the
// callback, which sets the session cookie.
func (s *session) loginKeycloak(user, password string) {
	s.t.Helper()
	resp, body := s.do(http.MethodGet, "/auth/oidc/start?next=/", nil)
	if resp.StatusCode != http.StatusOK {
		s.t.Fatalf("Keycloak login page: %d %.300s", resp.StatusCode, body)
	}
	action := formAction(s.t, body, "kc-form-login")
	target, err := resp.Request.URL.Parse(action)
	if err != nil {
		s.t.Fatalf("form action %q: %v", action, err)
	}
	form := url.Values{"username": {user}, "password": {password}, "credentialId": {""}}
	r, err := s.http.PostForm(target.String(), form)
	if err != nil {
		s.t.Fatalf("submit the Keycloak login form: %v", err)
	}
	data, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || r.Request.URL.Host != mustURL(s.t, s.base).Host {
		s.t.Fatalf("after the Keycloak login form: %d at %s: %.300s", r.StatusCode, r.Request.URL.Redacted(), data)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// formAction returns the action of the form with the given id.
func formAction(t *testing.T, page []byte, id string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(string(page)))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "form" {
			continue
		}
		var gotID, action string
		for _, a := range n.Attr {
			switch a.Key {
			case "id":
				gotID = a.Val
			case "action":
				action = a.Val
			}
		}
		if gotID == id {
			return action
		}
	}
	t.Fatalf("no form %q on the page: %.300s", id, page)
	return ""
}

type me struct {
	ID       string
	Username string
	Groups   []string
	Roles    []string
}

func (s *session) me() me {
	s.t.Helper()
	var m me
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &m)
	return m
}

func TestOIDC(t *testing.T) {
	t.Parallel()
	requireKeycloak(t)
	s := newSession(t)
	s.login(keycloakLogin, "alice")
	m := s.me()
	if !strings.HasPrefix(m.ID, "alice-") {
		t.Fatalf("identity %q, want alice-<hash>", m.ID)
	}
	if !slices.Contains(m.Groups, adminsGroup) {
		t.Fatalf("groups %v lack %s verbatim", m.Groups, adminsGroup)
	}
	if !slices.Contains(m.Roles, "platform-admin") {
		t.Fatalf("roles %v lack platform-admin", m.Roles)
	}
}

// TestPasswordReset replaces a local account's password with
// `javo user add --force` and checks its open session ends. It uses its own
// account so alice's password file stays valid.
func TestPasswordReset(t *testing.T) {
	t.Parallel()
	const user = "e2e-reset"
	// The password comes back on stdout from the pod that holds the database.
	password := javoCLI(t, "user", "add", user, "--password-file", "-", "--force")
	t.Cleanup(func() { javoCLI(t, "user", "remove", user) })

	s := newSession(t)
	s.loginLocal(user, password)
	s.me()
	javoCLI(t, "user", "add", user, "--password-file", "-", "--force")
	if resp, body := s.do(http.MethodGet, "/api/me", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/me after the reset: %d %s, want 401", resp.StatusCode, body)
	}
}

// keycloakAdmin calls the Keycloak admin API as the bootstrap admin. It signs
// in again for every call: admin-cli tokens live 60 s, shorter than an app
// start on a slow cluster, and cleanups run after the test body.
type keycloakAdmin struct {
	t    *testing.T
	http *http.Client
}

func newKeycloakAdmin(t *testing.T) *keycloakAdmin {
	t.Helper()
	k := &keycloakAdmin{t: t, http: newClient(t)}
	k.token()
	return k
}

func (k *keycloakAdmin) token() string {
	k.t.Helper()
	form := url.Values{
		"grant_type": {"password"}, "client_id": {"admin-cli"},
		"username": {"admin"}, "password": {readSecret(k.t, devFile(k.t, "keycloak-admin"+os.Getenv("JAVO_SECRETS_SUFFIX")+".password"))},
	}
	r, err := k.http.PostForm(keycloakBase()+"/realms/master/protocol/openid-connect/token", form)
	if err != nil {
		k.t.Fatal(err)
	}
	defer r.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if r.StatusCode != http.StatusOK || json.NewDecoder(r.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		k.t.Fatalf("Keycloak admin token: status %d", r.StatusCode)
	}
	return tok.AccessToken
}

func (k *keycloakAdmin) call(method, path string, out any) {
	k.t.Helper()
	req, err := http.NewRequest(method, keycloakBase()+"/admin/realms/"+realm+path, nil)
	if err != nil {
		k.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token())
	r, err := k.http.Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	if r.StatusCode/100 != 2 {
		k.t.Fatalf("Keycloak admin %s %s: %d %s", method, path, r.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			k.t.Fatalf("Keycloak admin %s %s: %v", method, path, err)
		}
	}
}

// id returns the id of the one user or group the search path finds.
func (k *keycloakAdmin) id(path string) string {
	k.t.Helper()
	var found []struct{ ID string }
	k.call(http.MethodGet, path, &found)
	if len(found) != 1 {
		k.t.Fatalf("Keycloak admin GET %s: %d matches, want 1", path, len(found))
	}
	return found[0].ID
}

// setMember adds user to or removes them from the group at path.
func (k *keycloakAdmin) setMember(user, groupPath string, member bool) {
	k.t.Helper()
	uid := k.id("/users?exact=true&username=" + url.QueryEscape(user))
	var g struct{ ID string }
	k.call(http.MethodGet, "/group-by-path"+groupPath, &g)
	method := http.MethodPut
	if !member {
		method = http.MethodDelete
	}
	k.call(method, "/users/"+uid+"/groups/"+g.ID, nil)
}
