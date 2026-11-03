package apps_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestPerUserAppLandsInThePersonsNamespace(t *testing.T) {
	f := apitest.PerUserFixture(t)
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)

	if w := f.Do("POST", "/api/workspaces", apitest.NewApp, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	f.WorkspaceIn("javo-"+apitest.AliceID, apitest.AliceID, "sales")

	// Alice sees it, reads it and can stop it from where it lives.
	if got := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", alice)); len(got.Items) != 1 {
		t.Fatalf("alice lists %+v", got.Items)
	}
	if w := f.Do("POST", "/api/workspaces/sales/stop", "", alice); w.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}

	// Bob neither lists it nor reaches it by name.
	if got := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", bob)); len(got.Items) != 0 {
		t.Fatalf("bob lists %+v", got.Items)
	}
	if w := f.Do("GET", "/api/workspaces/sales", "", bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob reads alice's app: %d", w.Code)
	}
	// Bob can use the same name in his own namespace.
	if w := f.Do("POST", "/api/workspaces", apitest.NewApp, bob); w.Code != http.StatusCreated {
		t.Fatalf("bob create: %d %s", w.Code, w.Body)
	}
	f.WorkspaceIn("javo-"+apitest.BobID, apitest.BobID, "sales")
}

func TestPerUserDeleteRemovesFromThePersonsNamespace(t *testing.T) {
	f := apitest.PerUserFixture(t)
	alice := f.Session(apitest.AliceID)
	f.Do("POST", "/api/workspaces", apitest.NewApp, alice)
	if w := f.Do("DELETE", "/api/workspaces/sales", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if n := f.WorkspaceCount(); n != 0 {
		t.Fatalf("%d workspaces left", n)
	}
}

func TestPerTeamCreateAsksForATeamWhenThereAreSeveral(t *testing.T) {
	f := apitest.PerTeamFixture(t)
	alice := f.Session(apitest.AliceID, "/org/ds", "/org/ml")

	w := f.Do("POST", "/api/workspaces", apitest.NewApp, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.team"); msg != reasons.Msg(reasons.MsgPickTeam) {
		t.Fatalf("team message = %q", msg)
	}
	if f.WorkspaceCountAll() != 0 {
		t.Fatal("an app was created without a team")
	}

	w = f.Do("POST", "/api/workspaces", `{"displayName":"Sales","template":"lab","team":"ml"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("with team: %d %s", w.Code, w.Body)
	}
	f.WorkspaceIn("javo-ml", apitest.AliceID, "sales")
}

func TestPerTeamSingleTeamNeedsNoChoice(t *testing.T) {
	f := apitest.PerTeamFixture(t)
	if w := f.Do("POST", "/api/workspaces", apitest.NewApp, f.Session(apitest.AliceID, "/org/ds")); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	f.WorkspaceIn("javo-ds", apitest.AliceID, "sales")
}

func TestPerTeamRefusals(t *testing.T) {
	f := apitest.PerTeamFixture(t)
	cases := []struct {
		name, body string
		groups     []string
		want       reasons.MessageCode
	}{
		{"no team", apitest.NewApp, []string{"/other"}, reasons.MsgNoTeam},
		{"not your team", `{"displayName":"Sales","template":"lab","team":"ml"}`, []string{"/org/ds"}, reasons.MsgTeamNotYours},
	}
	for _, tc := range cases {
		w := f.Do("POST", "/api/workspaces", tc.body, f.Session(apitest.BobID, tc.groups...))
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d: %s", tc.name, w.Code, w.Body)
		}
		if msg, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.team"); msg != reasons.Msg(tc.want) {
			t.Errorf("%s: team message = %q, want %q", tc.name, msg, reasons.Msg(tc.want))
		}
	}
	if f.WorkspaceCountAll() != 0 {
		t.Fatal("a refused create left an app")
	}
}

// The URL names owner and app only, so one person cannot have the same name
// in two teams.
func TestPerTeamNameIsUniqueAcrossTheirTeams(t *testing.T) {
	f := apitest.PerTeamFixture(t)
	alice := f.Session(apitest.AliceID, "/org/ds", "/org/ml")
	f.Do("POST", "/api/workspaces", `{"displayName":"Sales","template":"lab","team":"ds"}`, alice)

	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales","template":"lab","team":"ml"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", alice)); len(got.Items) != 1 {
		t.Fatalf("alice lists %+v", got.Items)
	}
}

// An app shared with bob sits in alice's namespace; bob's list finds it
// whatever namespace it is in.
func TestPerUserSharedListFindsAppsInOtherNamespaces(t *testing.T) {
	ws := gatewaytest.Workspace("report", apitest.AliceID, "Report")
	ws.Namespace = "javo-" + apitest.AliceID
	ws.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{apitest.BobID}}
	tpl := gatewaytest.Template("streamlit", "streamlit")
	tpl.Spec.MaxSharing = model.AccessSignedIn
	f := apitest.New(t, apitest.Options{
		Urls:      apitest.SeparateHosts,
		Placement: placementperuser.New(config.Namespaces{Prefix: "javo"}),
		People:    []apitest.TestPerson{apitest.SignedIn("alice", time.Time{}), apitest.SignedIn("bob", time.Time{})},
	}, ws, tpl, apitest.ManagedNS("javo-"+apitest.AliceID), apitest.ManagedNS("javo-"+apitest.BobID))

	got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)))
	if len(got.Items) != 1 || got.Items[0].Name != "report" || got.Items[0].Owner.ID != apitest.AliceID {
		t.Fatalf("items %+v", got.Items)
	}
}

// Apps shared with bob by several owners, each in their own namespace, are
// all in his shared list.
func TestPerUserSharedListFindsEveryOwnersApp(t *testing.T) {
	tpl := gatewaytest.Template("streamlit", "streamlit")
	tpl.Spec.MaxSharing = model.AccessSignedIn
	objs := []any{tpl}
	people := []apitest.TestPerson{apitest.SignedIn("alice", time.Time{}), apitest.SignedIn("bob", time.Time{})}
	for _, owner := range []string{apitest.AliceID, apitest.PersonID("carol"), apitest.PersonID("dave")} {
		ws := gatewaytest.Workspace("report", owner, "Report")
		ws.Namespace = "javo-" + owner
		ws.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{apitest.BobID}}
		objs = append(objs, ws, apitest.ManagedNS(ws.Namespace))
		if owner != apitest.AliceID {
			people = append(people, apitest.SignedIn(owner[:strings.IndexByte(owner, '-')], time.Time{}))
		}
	}
	f := apitest.New(t, apitest.Options{
		Urls: apitest.SeparateHosts, People: people,
		Placement: placementperuser.New(config.Namespaces{Prefix: "javo"}),
	}, objs...)

	got := apitest.Decode[apitest.SharedJSON](t, f.Do("GET", "/api/shared", "", f.Session(apitest.BobID)))
	if len(got.Items) != 3 {
		t.Fatalf("items %+v, want the 3 shared apps", got.Items)
	}
}

// An app stays its owner's after they move team: alice made "sales" in ds,
// then left ds for ml. She still lists, stops and deletes it, and cannot make
// a second "sales" in ml that the address could not tell apart.
func TestPerTeamOwnerKeepsTheirAppAfterChangingTeam(t *testing.T) {
	f := apitest.PerTeamFixture(t)
	if w := f.Do("POST", "/api/workspaces", apitest.NewApp, f.Session(apitest.AliceID, "/org/ds")); w.Code != http.StatusCreated {
		t.Fatalf("create in ds: %d %s", w.Code, w.Body)
	}
	moved := f.Session(apitest.AliceID, "/org/ml")

	if got := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", moved)); len(got.Items) != 1 {
		t.Fatalf("alice lists %+v after moving team", got.Items)
	}
	if w := f.Do("POST", "/api/workspaces/sales/stop", "", moved); w.Code != http.StatusOK {
		t.Fatalf("stop after moving team: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/workspaces", apitest.NewApp, moved); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second sales in ml: %d %s", w.Code, w.Body)
	}
	if n := f.WorkspaceCountAll(); n != 1 {
		t.Fatalf("%d apps, want the one in ds", n)
	}
	if w := f.Do("DELETE", "/api/workspaces/sales", "", moved); w.Code != http.StatusNoContent {
		t.Fatalf("delete after moving team: %d %s", w.Code, w.Body)
	}
	if n := f.WorkspaceCountAll(); n != 0 {
		t.Fatalf("%d apps left after delete", n)
	}
}

// A first-login user's namespace is labelled by the controller a moment after
// the Identity appears, possibly after the managed-namespace list was cached.
// The create still lands, and the app reads back at once.
func TestPerUserNamespaceLabelledAfterTheCacheFilled(t *testing.T) {
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	f := apitest.New(t, apitest.Options{Placement: pl}, gatewaytest.Template("lab", "jupyterlab"))
	alice := f.Session(apitest.AliceID)
	if got := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", alice)); len(got.Items) != 0 {
		t.Fatalf("before: %+v", got.Items)
	}
	if err := f.Client.Create(context.Background(), apitest.ManagedNS("javo-"+apitest.AliceID)); err != nil {
		t.Fatal(err)
	}
	if w := f.Do("POST", "/api/workspaces", apitest.NewApp, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/workspaces/sales", "", alice); w.Code != http.StatusOK {
		t.Fatalf("read back: %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", alice)); len(got.Items) != 1 {
		t.Fatalf("list: %+v", got.Items)
	}
}

// Before the controller has made or labelled the namespace, a create is
// refused with a reason that says to retry, and nothing is written.
func TestPerUserCreateBeforeTheNamespaceIsReadyIsRetryable(t *testing.T) {
	unlabelled := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "javo-" + apitest.BobID}}
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	f := apitest.New(t, apitest.Options{Placement: pl}, gatewaytest.Template("lab", "jupyterlab"), unlabelled)
	for _, who := range []string{apitest.AliceID, apitest.BobID} { // alice: no namespace; bob: not labelled
		w := f.Do("POST", "/api/workspaces", apitest.NewApp, f.Session(who))
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: %d %v %s", who, w.Code, w.Header(), w.Body)
		}
		var p struct{ Reason string }
		if p = apitest.Decode[struct{ Reason string }](t, w); p.Reason != string(reasons.NamespacePending) {
			t.Fatalf("%s reason %q", who, p.Reason)
		}
	}
	if f.WorkspaceCountAll() != 0 {
		t.Fatal("an app was written")
	}
}
