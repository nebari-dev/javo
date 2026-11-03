package environments_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

func TestWithoutAnEnvironmentManagerNoEnvironmentsAreOffered(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	got := apitest.Decode[apitest.EnvListJSON](t, f.Do("GET", "/api/environments", "", alice))
	if got.Available || len(got.Items) != 0 {
		t.Fatalf("offered %+v", got)
	}
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"app.py","environment":{"id":"ws-1"}}`, alice)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "environment") {
		t.Fatalf("create with an environment: %d %s", w.Code, w.Body)
	}
}

func TestEnvironmentsAreListedAsThePerson(t *testing.T) {
	t.Parallel()
	fresh := apitest.IdTokenAt("alice", time.Now().Add(time.Hour))
	src := &apitest.FakeSource{Want: fresh}
	f := apitest.New(t, apitest.Options{Environments: src, Extra: []authprovider.Provider{apitest.RefreshingProvider{}}})
	got := apitest.Decode[apitest.EnvListJSON](t, f.Do("GET", "/api/environments", "", f.OidcSession(fresh)))
	if !got.Available || len(got.Items) != 1 || got.Items[0].Name != "sales" {
		t.Fatalf("listed %+v", got)
	}
}

// An ID token about to expire is refreshed with the identity provider first,
// so the manager is never handed one it will refuse.
func TestAnExpiredIDTokenIsRefreshedBeforeItIsUsed(t *testing.T) {
	t.Parallel()
	renewed := apitest.IdTokenAt("alice-renewed", time.Now().Add(time.Hour))
	src := &apitest.FakeSource{Want: renewed}
	f := apitest.New(t, apitest.Options{Environments: src, Extra: []authprovider.Provider{apitest.RefreshingProvider{Token: renewed}}})
	c := f.OidcSession(apitest.IdTokenAt("alice", time.Now().Add(-time.Minute)))
	got := apitest.Decode[apitest.EnvListJSON](t, f.Do("GET", "/api/environments", "", c))
	if !got.Available || len(got.Items) != 1 {
		t.Fatalf("listed %+v (manager saw %d tokens)", got, len(src.Seen))
	}
	s, err := f.Store.Get(context.Background(), c.Value)
	if err != nil || s.IDToken != renewed {
		t.Fatalf("the refreshed token was not kept: %v", err)
	}
}

// A local account has no ID token: the manager is not asked, and the person
// is told why the list is empty.
func TestALocalAccountIsToldToSignInWithTheIdentityProvider(t *testing.T) {
	t.Parallel()
	src := &apitest.FakeSource{Want: "never"}
	f := apitest.New(t, apitest.Options{Environments: src})
	got := apitest.Decode[apitest.EnvListJSON](t, f.Do("GET", "/api/environments", "", f.LoginAlice()))
	if !got.Available || len(got.Items) != 0 || got.Message == "" {
		t.Fatalf("listed %+v", got)
	}
	if len(src.Seen) != 0 {
		t.Fatal("the manager was asked without an ID token")
	}
}

func TestAnAppCreatedInAnEnvironmentKeepsItsFiles(t *testing.T) {
	t.Parallel()
	fresh := apitest.IdTokenAt("alice", time.Now().Add(time.Hour))
	f := apitest.New(t, apitest.Options{Environments: &apitest.FakeSource{Want: fresh}, Extra: []authprovider.Provider{apitest.RefreshingProvider{}}},
		gatewaytest.Template("streamlit", "streamlit"))
	c := f.OidcSession(fresh)
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"app.py","environment":{"id":"ws-1"}}`, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if e := apitest.Decode[apitest.EnvViewJSON](t, w).Environment; e == nil || e.Source != "nebi" || e.Name != "sales" || e.Version != "3" {
		t.Fatalf("view %+v", e)
	}
	app := f.GetWorkspace("alice-aaaaaaaa", "dash")
	files, err := f.Db.Environments().ForApp(context.Background(), app.ID)
	if err != nil || files != apitest.SalesEnv || app.Environment.Digest != apitest.SalesEnv.Digest() {
		t.Fatalf("stored %+v, %v", files, err)
	}
	// One the person cannot see is refused on its field.
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Other","template":"streamlit","file":"app.py","environment":{"id":"ws-9"}}`, c)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "environment") {
		t.Fatalf("an unknown environment: %d %s", w.Code, w.Body)
	}
}

func TestAJobCreatedInAnEnvironmentKeepsItAcrossEdits(t *testing.T) {
	t.Parallel()
	fresh := apitest.IdTokenAt("alice", time.Now().Add(time.Hour))
	f := apitest.JobFixture(t, apitest.Options{Environments: &apitest.FakeSource{Want: fresh}, Extra: []authprovider.Provider{apitest.RefreshingProvider{}}})
	c := f.OidcSession(fresh)
	w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"environment":{"id":"ws-1"}`), c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if e := apitest.Decode[apitest.EnvViewJSON](t, w).Environment; e == nil || e.Name != "sales" {
		t.Fatalf("view %+v", e)
	}
	job := f.Jobs()[0]
	if files, err := f.Db.Environments().ForJob(context.Background(), job.ID); err != nil || files != apitest.SalesEnv {
		t.Fatalf("stored %+v, %v", files, err)
	}
	if w := f.Do("PUT", "/api/jobs/nightly-sales", fmt.Sprintf(apitest.NewJob, ""), c); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if f.Jobs()[0].Spec.Environment == nil {
		t.Fatal("editing the job dropped its environment")
	}
}
