package apps_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestUnauthenticatedIsSignInRequired(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	for _, c := range []*http.Cookie{nil, {Name: model.SessionCookieName, Value: "bogus"}} {
		w := f.Do("GET", "/api/workspaces", "", c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(reasons.MsgSignInRequired) {
			t.Fatalf("title %q", p.Title)
		}
	}
}

func TestLoginSetsSessionCookieAndLogoutEndsIt(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Secure: true})

	w := f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"wrong"}`, nil)
	if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: status %d cookies %v", w.Code, w.Result().Cookies())
	}

	w = f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"alice-password"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login status %d: %s", w.Code, w.Body)
	}
	var c *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == model.SessionCookieName {
			c = ck
		}
	}
	if c == nil || c.Value == "" {
		t.Fatal("no javo_session cookie")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie attributes: %+v", c)
	}

	me := f.Do("GET", "/api/me", "", c)
	if me.Code != http.StatusOK {
		t.Fatalf("me status %d", me.Code)
	}
	if u := apitest.Decode[struct{ ID, Username string }](t, me); u.ID != local.UserID("alice") || u.Username != "alice" {
		t.Fatalf("me %+v", u)
	}

	out := f.Do("POST", "/api/auth/logout", "", c)
	if out.Code >= 300 {
		t.Fatalf("logout status %d", out.Code)
	}
	cleared := false
	for _, ck := range out.Result().Cookies() {
		cleared = cleared || (ck.Name == model.SessionCookieName && ck.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}
	if w := f.Do("GET", "/api/me", "", c); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token still valid: %d", w.Code)
	}
}

// The session cookie is Secure whenever the platform's address is https
// (urls.scheme, https by default), whatever --secure-cookies says; only an
// http address gets a plain cookie, unless the flag asks for Secure.
func TestSessionCookieFollowsTheURLScheme(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		scheme string
		flag   bool
		secure bool
	}{
		{"https by default", "", false, true},
		{"https", "https", false, true},
		{"http", "http", false, false},
		{"http with the flag", "http", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := apitest.New(t, apitest.Options{Secure: c.flag, Urls: config.URLs{Scheme: c.scheme}})
			w := f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"alice-password"}`, nil)
			cs := w.Result().Cookies()
			if len(cs) != 1 || cs[0].Secure != c.secure {
				t.Fatalf("cookies %+v", cs)
			}
		})
	}
}

func TestOtherUsersWorkspaceIsNotFound(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Workspace("alices-app", "alice-aaaaaaaa", "Alice's app"))
	bob := f.Session("bob-bbbbbbbb")

	got := f.Do("GET", "/api/workspaces/alices-app", "", bob)
	missing := f.Do("GET", "/api/workspaces/no-such-app", "", bob)
	if got.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("status %d / %d", got.Code, missing.Code)
	}
	if got.Body.String() != missing.Body.String() {
		t.Fatalf("bodies differ:\n%s\n%s", got.Body, missing.Body)
	}
	for _, act := range []string{"stop", "start"} {
		if w := f.Do("POST", "/api/workspaces/alices-app/"+act, "", bob); w.Code != http.StatusNotFound {
			t.Fatalf("%s status %d", act, w.Code)
		}
	}
	if w := f.Do("DELETE", "/api/workspaces/alices-app", "", bob); w.Code != http.StatusNotFound {
		t.Fatalf("delete status %d", w.Code)
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("bob deleted alice's workspace")
	}
	if f.GetWorkspace("alice-aaaaaaaa", "alices-app").Paused {
		t.Fatal("bob stopped alice's workspace")
	}

	list := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", bob))
	if len(list.Items) != 0 {
		t.Fatalf("bob lists %+v", list.Items)
	}
}

func TestWorkspaceStepsComeFromCatalog(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app")
	ws.File = "sales/app.py"
	ws.Status = model.AppStatus{
		Phase: "Failed",
		Conditions: []metav1.Condition{
			{Type: model.ConditionReady, Status: metav1.ConditionUnknown, Reason: "Starting", Message: "Starting"},
			{Type: model.ConditionScheduled, Status: metav1.ConditionTrue, Reason: "Ready", Message: "Ready"},
			{Type: model.ConditionImageReady, Status: metav1.ConditionFalse, Reason: "ImagePullFailed",
				Message: "container app waiting: ErrImagePull: not found\nevent Failed: pull access denied"},
		},
	}
	f := apitest.New(t, apitest.Options{}, ws, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")

	w := f.Do("GET", "/api/workspaces/sales-app", "", alice)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	got := apitest.Decode[apitest.WorkspaceJSON](t, w)
	if got.Name != "sales-app" || got.DisplayName != "Sales app" || got.Kind != "streamlit" ||
		got.File != "sales/app.py" || got.Phase != "Failed" || got.URL != "/u/"+"alice-aaaaaaaa"+"/sales-app/" {
		t.Fatalf("workspace %+v", got)
	}
	if !got.CanShare {
		t.Fatalf("owner's app has canShare false: %+v", got)
	}
	var types []string
	for _, s := range got.Steps {
		types = append(types, s.Type)
	}
	if strings.Join(types, ",") != "Scheduled,HomeReady,ImageReady,Started,Ready" {
		t.Fatalf("step order %v", types)
	}
	pull := got.Steps[2]
	r, _ := reasons.Lookup(reasons.ImagePullFailed)
	if pull.Status != "False" || pull.Reason != string(r.Code) || pull.Title != r.Title || pull.What != r.What ||
		pull.Todo != r.Todo || pull.Blame != r.Blame || pull.Who != r.Who {
		t.Fatalf("pull step %+v", pull)
	}
	if len(pull.Evidence) != 2 || pull.Evidence[1] != "event Failed: pull access denied" {
		t.Fatalf("evidence %q", pull.Evidence)
	}
	if got.Steps[0].Evidence != nil && len(got.Steps[0].Evidence) != 0 {
		t.Fatalf("catalog title repeated as evidence: %q", got.Steps[0].Evidence)
	}
	if home := got.Steps[1]; home.Status != "Unknown" || home.Reason != "" {
		t.Fatalf("missing condition step %+v", home)
	}

	list := apitest.Decode[struct{ Items []apitest.WorkspaceJSON }](t, f.Do("GET", "/api/workspaces", "", alice))
	if len(list.Items) != 1 || list.Items[0].Name != "sales-app" || !list.Items[0].CanShare {
		t.Fatalf("list %+v", list.Items)
	}
}

func TestTwoUsersCanUseTheSameName(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	body := `{"displayName":"Sales app","template":"streamlit","file":"app.py"}`
	for _, who := range []string{"alice-aaaaaaaa", "bob-bbbbbbbb"} {
		c := f.Session(who)
		if w := f.Do("POST", "/api/workspaces", body, c); w.Code != http.StatusCreated {
			t.Fatalf("%s create %d: %s", who, w.Code, w.Body)
		}
		got := apitest.Decode[apitest.WorkspaceJSON](t, f.Do("GET", "/api/workspaces/sales-app", "", c))
		if got.Name != "sales-app" || got.URL != "/u/"+who+"/sales-app/" {
			t.Fatalf("%s sees %+v", who, got)
		}
		if app := f.GetWorkspace(who, "sales-app"); app.Owner != who {
			t.Fatalf("%s row: %+v", who, app)
		}
	}
	if f.WorkspaceCount() != 2 {
		t.Fatalf("%d workspaces", f.WorkspaceCount())
	}
}

func TestStepsCarryTransitionTimes(t *testing.T) {
	t.Parallel()
	ready := time.Date(2026, 10, 30, 12, 0, 42, 0, time.UTC)
	ws := gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app")
	ws.Status = model.AppStatus{Phase: "Running", Conditions: []metav1.Condition{
		{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.NewTime(ready)},
	}}
	f := apitest.New(t, apitest.Options{}, ws, gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("GET", "/api/workspaces/sales-app", "", f.Session("alice-aaaaaaaa"))
	var got struct {
		Steps []struct {
			Type  string
			Since *time.Time
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Steps) != 5 {
		t.Fatalf("%v: %s", err, w.Body)
	}
	for _, s := range got.Steps {
		switch {
		case s.Type == model.ConditionReady && (s.Since == nil || !s.Since.Equal(ready)):
			t.Fatalf("ready since %v", s.Since)
		case s.Type != model.ConditionReady && s.Since != nil:
			t.Fatalf("%s has a time without a condition: %v", s.Type, s.Since)
		}
	}
}

func TestCreateWithTakenNameIs422AndCreatesNothing(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Workspace("sales-app", "alice-aaaaaaaa", "Sales app"), gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"app.py"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.displayName")
	if !ok || msg != `You already have an app called "Sales app".` || msg != reasons.Msg(reasons.MsgNameTakenByYou, "Sales app") {
		t.Fatalf("errors %s", w.Body)
	}
	if n := f.WorkspaceCount(); n != 1 {
		t.Fatalf("%d workspaces", n)
	}
}

func TestCreateWithTemplateOutsideGroupsIs422(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("gpu-lab", "jupyterlab", "admins"))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Big lab","template":"gpu-lab"}`, f.Session("alice-aaaaaaaa", "analysts"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.template"); !ok {
		t.Fatalf("errors %s", w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestCreateRejectsFileOutsideHome(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	for _, file := range []string{"/etc/passwd", "../other/app.py", "sales/../../app.py", `sales\u0000/app.py`, `sales\napp.py`} {
		w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"`+file+`"}`, alice)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d", file, w.Code)
		}
		if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.file"); !ok {
			t.Fatalf("%s: errors %s", file, w.Body)
		}
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

// A rejection by model validation reaches the form as catalog text, not as the
// validator's words, and creates nothing.
func TestCreateMapsModelValidationAndCreatesNothing(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	// An owner that is not a DNS label is rejected by the model, on a field
	// the form has no message for.
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"app.py"}`, f.Session("Not_A_Label"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgInvalidRequest)) {
		t.Fatalf("unmapped field not generic: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "owner") || strings.Contains(w.Body.String(), "DNS") {
		t.Fatalf("validator text leaked: %s", w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestCreateStopStartDelete(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")

	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"sales/app.py"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w); got.Name != "sales-app" || got.Kind != "streamlit" || got.FilePath != "/home/user/sales/app.py" {
		t.Fatalf("created %+v", got)
	}
	ws := f.GetWorkspace("alice-aaaaaaaa", "sales-app")
	if ws.Owner != "alice-aaaaaaaa" || ws.DisplayName != "Sales app" || ws.File != "sales/app.py" || ws.Namespace != gatewaytest.Namespace {
		t.Fatalf("stored %+v", ws)
	}

	if w := f.Do("POST", "/api/workspaces/sales-app/stop", "", alice); w.Code != http.StatusOK {
		t.Fatalf("stop %d: %s", w.Code, w.Body)
	}
	if !f.GetWorkspace("alice-aaaaaaaa", "sales-app").Paused {
		t.Fatal("not paused")
	}
	if w := f.Do("POST", "/api/workspaces/sales-app/start", "", alice); w.Code != http.StatusOK {
		t.Fatalf("start %d", w.Code)
	}
	if f.GetWorkspace("alice-aaaaaaaa", "sales-app").Paused {
		t.Fatal("still paused")
	}
	if w := f.Do("DELETE", "/api/workspaces/sales-app", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete %d", w.Code)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("not deleted")
	}
}

func TestTemplatesFilteredByGroups(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{},
		gatewaytest.Template("streamlit", "streamlit"),
		gatewaytest.Template("gpu-lab", "jupyterlab", "admins"),
		gatewaytest.Template("team-lab", "jupyterlab", "analysts"))
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa", "analysts"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var Names []string
	for _, it := range apitest.Decode[struct{ Items []struct{ Name, Kind string } }](t, w).Items {
		Names = append(Names, it.Name)
	}
	if strings.Join(Names, ",") != "streamlit,team-lab" {
		t.Fatalf("templates %v", Names)
	}
}

func TestOpenAPIDocument(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Files: &apitest.StubFilesLister{}})
	w := f.Do("GET", "/api/openapi.json", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	doc := apitest.Decode[struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}](t, w)
	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Fatalf("openapi %q", doc.OpenAPI)
	}
	for _, p := range []string{"/api/auth/local/login", "/api/me", "/api/templates", "/api/workspaces", "/api/workspaces/{name}/stop", "/api/files"} {
		if _, ok := doc.Paths[p]; !ok {
			t.Fatalf("missing path %s", p)
		}
	}
}

func TestSchemaValidationUsesCatalogCopy(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"","template":"streamlit","file":"app.py"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.displayName"); !ok || msg != reasons.Msg(reasons.MsgNameInvalid) {
		t.Fatalf("errors %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "expected") {
		t.Fatalf("validator text leaked: %s", w.Body)
	}
}

func TestCreateChecksFileAgainstTemplate(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Template("lab", "jupyterlab"))
	alice := f.Session("alice-aaaaaaaa")
	for _, file := range []string{"notes.txt", "", "sales"} {
		w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"`+file+`"}`, alice)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%q: status %d", file, w.Code)
		}
		if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.file"); !ok || msg != reasons.Msg(reasons.MsgFileWrongType, ".py") {
			t.Fatalf("%q: errors %s", file, w.Body)
		}
	}
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"sales/./App.PY"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w); got.File != "sales/App.PY" {
		t.Fatalf("file not normalised: %q", got.File)
	}
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Lab","template":"lab"}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("folder template without file: %d %s", w.Code, w.Body)
	}
}

func TestCreateReleasesTheFilePickerHome(t *testing.T) {
	t.Parallel()
	Homes := &apitest.StubFilesLister{}
	f := apitest.New(t, apitest.Options{Files: Homes}, gatewaytest.Template("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	_ = f.Do("POST", "/api/workspaces", `{"displayName":"","template":"streamlit","file":"app.py"}`, alice)
	if len(Homes.Released) != 0 {
		t.Fatal("released on a failed create")
	}
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Sales app","template":"streamlit","file":"app.py"}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("status %d", w.Code)
	}
	if len(Homes.Released) != 1 || Homes.Released[0] != "alice-aaaaaaaa" {
		t.Fatalf("released %v", Homes.Released)
	}
}

func TestAuthIsDefaultOnForAPI(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	huma.Register(f.Srv.API, huma.Operation{OperationID: "newThing", Method: http.MethodGet, Path: "/api/new-thing"},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	if w := f.Do("GET", "/api/new-thing", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("new operation without session: %d", w.Code)
	}
	if w := f.Do("GET", "/api/new-thing", "", f.Session("alice-aaaaaaaa")); w.Code != http.StatusNoContent {
		t.Fatalf("new operation with session: %d", w.Code)
	}
	if w := f.Do("GET", "/api/nope", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown api path without session: %d", w.Code)
	}
	if w := f.Do("GET", "/api/openapi.json", "", nil); w.Code != http.StatusOK {
		t.Fatalf("openapi: %d", w.Code)
	}
	if w := f.Do("POST", "/api/auth/logout", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
}

func TestAPIResponsesAreNoSniff(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	for _, path := range []string{"/api/workspaces", "/api/openapi.json"} {
		if w := f.Do("GET", path, "", nil); w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: headers %v", path, w.Header())
		}
	}
}

// Guessing alice's password from one address locks out that address only:
// alice still signs in from her own.
func TestLoginGuessesLimitOnlyTheGuessingAddress(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	limited := false
	for i := 0; i < 20 && !limited; i++ {
		limited = f.LoginFrom("192.0.2.66", "alice", "wrong").Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("guessing address never limited")
	}
	if w := f.LoginFrom("198.51.100.1", "alice", "alice-password"); w.Code != http.StatusOK {
		t.Fatalf("alice from her own address: %d", w.Code)
	}
}

// Wrong passwords spread over many addresses slow the next attempts at that
// username instead of refusing them, so the right password still signs in.
func TestGuessesFromManyAddressesSlowButDoNotLockOut(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	for i := 0; i < 6; i++ {
		if w := f.LoginFrom(fmt.Sprintf("192.0.2.%d", i+1), "alice", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, w.Code)
		}
	}
	start := time.Now()
	if w := f.LoginFrom("198.51.100.1", "alice", "alice-password"); w.Code != http.StatusOK {
		t.Fatalf("alice after the guesses: %d", w.Code)
	}
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("alice waited only %v after six wrong guesses", waited)
	}
	start = time.Now()
	if w := f.LoginFrom("198.51.100.2", "bob", "wrong"); w.Code != http.StatusUnauthorized || time.Since(start) > 400*time.Millisecond {
		t.Fatalf("other user: %d after %v", w.Code, time.Since(start))
	}
}

func TestLoginIsRateLimitedPerClientIP(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	limited := false
	for i := 0; i < 60 && !limited; i++ {
		limited = f.LoginFrom("192.0.2.7", fmt.Sprintf("user%d", i), "wrong").Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("one client IP was never limited")
	}
	if w := f.LoginFrom("192.0.2.8", "carol", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("other IP: %d", w.Code)
	}
}

// Behind a trusted proxy each client is limited on its own address; without
// trust every client shares the proxy's address.
func TestLoginLimitUsesClientBehindTrustedProxy(t *testing.T) {
	t.Parallel()
	const proxyIP = "10.42.0.1"
	exhaust := func(f *apitest.Fixture) {
		t.Helper()
		for i := 0; i < 60; i++ {
			if f.LoginVia(proxyIP, "192.0.2.7", fmt.Sprintf("user%d", i), "wrong").Code == http.StatusTooManyRequests {
				return
			}
		}
		t.Fatal("client never limited")
	}

	f := apitest.New(t, apitest.Options{Trusted: []netip.Prefix{netip.MustParsePrefix(proxyIP + "/32")}})
	exhaust(f)
	if w := f.LoginVia(proxyIP, "192.0.2.8", "carol", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("other client behind the proxy: %d", w.Code)
	}
	// A spoofed entry left of the proxy's own does not escape the limit.
	if w := f.LoginVia(proxyIP, "203.0.113.5, 192.0.2.7", "dave", "wrong"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed header escaped the limit: %d", w.Code)
	}

	untrusted := apitest.New(t, apitest.Options{})
	exhaust(untrusted)
	if w := untrusted.LoginVia(proxyIP, "192.0.2.8", "carol", "wrong"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("untrusted proxy header was believed: %d", w.Code)
	}
}

func TestLoginEndsThePreviousSession(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	old := f.Session("alice-aaaaaaaa")
	r := httptest.NewRequest("POST", "/api/auth/local/login", strings.NewReader(`{"username":"alice","password":"alice-password"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(authn.RequestHeader, "1")
	r.AddCookie(old)
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login %d", w.Code)
	}
	if _, err := f.Store.Get(context.Background(), old.Value); err == nil {
		t.Fatal("previous session still valid")
	}
}

func TestCreateAcceptsProductPrefixInName(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("lab", "jupyterlab"))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"javo files","template":"lab"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestUnknownAPIRouteAndMethodUseCatalogText(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	alice := f.Session("alice-aaaaaaaa")
	for _, tc := range []struct {
		method, path string
		status       int
		msg          reasons.MessageCode
	}{
		{"GET", "/api/no-such-thing", http.StatusNotFound, reasons.MsgNoSuchAddress},
		{"PUT", "/api/workspaces", http.StatusMethodNotAllowed, reasons.MsgMethodNotAllowed},
	} {
		w := f.Do(tc.method, tc.path, "", alice)
		if w.Code != tc.status {
			t.Fatalf("%s %s: status %d", tc.method, tc.path, w.Code)
		}
		if p := apitest.Decode[apitest.Problem](t, w); p.Title != reasons.Msg(tc.msg) || p.Status != tc.status {
			t.Fatalf("%s %s: body %s", tc.method, tc.path, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("%s %s: content type %q", tc.method, tc.path, ct)
		}
	}
	if w := f.Do("PUT", "/api/workspaces", "", alice); w.Header().Get("Allow") == "" {
		t.Fatal("405 lost its Allow header")
	}
}

func TestMePicture(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Secure: true})
	tok, err := f.Store.Create(context.Background(), session.Session{
		Principal: authz.Principal{ID: "bob-bbbbbbbb", Username: "bob", Picture: "https://example.com/bob.png"},
		Provider:  "oidc", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	w := f.Do("GET", "/api/me", "", &http.Cookie{Name: model.SessionCookieName, Value: tok})
	if got := apitest.Decode[map[string]any](t, w)["picture"]; got != "https://example.com/bob.png" {
		t.Fatalf("picture %v in %s", got, w.Body)
	}

	w = f.Do("GET", "/api/me", "", f.Session("alice-aaaaaaaa"))
	if _, ok := apitest.Decode[map[string]any](t, w)["picture"]; ok {
		t.Fatalf("local user has a picture: %s", w.Body)
	}
}

func TestStartSampleCreatesNumberedWorkspacePerUser(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.SampleTemplateObj("streamlit", "streamlit"))
	alice, bob := f.Session("alice-aaaaaaaa"), f.Session("bob-bbbbbbbb")

	w := f.Do("POST", "/api/samples/streamlit", "", alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w); got.Name != "streamlit-sample" || got.DisplayName != "Streamlit sample" {
		t.Fatalf("view = %+v", got)
	}
	ws := f.GetWorkspace("alice-aaaaaaaa", "streamlit-sample")
	if ws.DisplayName != "Streamlit sample" || !ws.Sample || ws.File != "samples/streamlit/app.py" ||
		ws.Template != "streamlit" {
		t.Fatalf("stored = %+v", ws)
	}

	w = f.Do("POST", "/api/samples/streamlit", "", alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("second: status %d: %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w); got.DisplayName != "Streamlit sample 2" || got.Name != "streamlit-sample-2" {
		t.Fatalf("second view = %+v", got)
	}

	w = f.Do("POST", "/api/samples/streamlit", "", bob)
	if got := apitest.Decode[apitest.WorkspaceJSON](t, w); w.Code != http.StatusCreated || got.DisplayName != "Streamlit sample" {
		t.Fatalf("bob: status %d view %+v", w.Code, got)
	}
}

// A name that is taken, whoever took it and whenever, is skipped: the create
// itself reports it, so two sample starts at once get two names.
func TestStartSampleSkipsTakenNamesAndTwoAtOnceGetTwo(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.SampleTemplateObj("streamlit", "streamlit"),
		gatewaytest.Workspace("streamlit-sample", "alice-aaaaaaaa", "Streamlit sample"))
	alice := f.Session("alice-aaaaaaaa")
	var wg sync.WaitGroup
	got := make([]string, 2)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := f.Do("POST", "/api/samples/streamlit", "", alice)
			if w.Code != http.StatusCreated {
				t.Errorf("status %d: %s", w.Code, w.Body)
				return
			}
			got[i] = apitest.Decode[apitest.WorkspaceJSON](t, w).DisplayName
		}()
	}
	wg.Wait()
	slices.Sort(got)
	if got[0] != "Streamlit sample 2" || got[1] != "Streamlit sample 3" {
		t.Fatalf("names = %v", got)
	}
	if f.WorkspaceCount() != 3 {
		t.Fatalf("%d workspaces", f.WorkspaceCount())
	}
}

func TestStartSampleGivesUpAfterTwentyNames(t *testing.T) {
	t.Parallel()
	objs := []any{apitest.SampleTemplateObj("streamlit", "streamlit")}
	for i := 1; i <= 20; i++ {
		short, display := "streamlit-sample", "Streamlit sample"
		if i > 1 {
			short, display = fmt.Sprintf("streamlit-sample-%d", i), fmt.Sprintf("Streamlit sample %d", i)
		}
		objs = append(objs, gatewaytest.Workspace(short, "alice-aaaaaaaa", display))
	}
	f := apitest.New(t, apitest.Options{}, objs...)
	w := f.Do("POST", "/api/samples/streamlit", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgSampleNameTaken)) {
		t.Fatalf("body %s", w.Body)
	}
	if f.WorkspaceCount() != 20 {
		t.Fatalf("%d workspaces", f.WorkspaceCount())
	}
}

func TestStartSampleRefusals(t *testing.T) {
	t.Parallel()
	gpu := apitest.SampleTemplateObj("gpu-lab", "jupyterlab")
	gpu.Spec.AllowedGroups = []string{"admins"}
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("plain", "jupyterlab"), gpu)
	alice := f.Session("alice-aaaaaaaa", "analysts")

	w := f.Do("POST", "/api/samples/plain", "", alice)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgNoSample, "plain")) {
		t.Fatalf("no sample: status %d: %s", w.Code, w.Body)
	}
	// Same answer as createWorkspace gives for a template the user may not use.
	w = f.Do("POST", "/api/samples/gpu-lab", "", alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("group: status %d: %s", w.Code, w.Body)
	}
	if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.template"); !ok {
		t.Fatalf("errors %s", w.Body)
	}
	w = f.Do("POST", "/api/samples/missing", "", alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing: status %d: %s", w.Code, w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestTemplatesReportTheirSample(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.SampleTemplateObj("streamlit", "streamlit"), gatewaytest.Template("plain", "jupyterlab"))
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))
	out := apitest.Decode[struct {
		Items []struct {
			Name   string
			Sample *struct{ Title string }
		}
	}](t, w)
	got := map[string]string{}
	for _, it := range out.Items {
		if it.Sample != nil {
			got[it.Name] = it.Sample.Title
		} else {
			got[it.Name] = "<none>"
		}
	}
	if got["streamlit"] != "Sales explorer" || got["plain"] != "<none>" {
		t.Fatalf("samples = %v (%s)", got, w.Body)
	}
}

// A template gated by an identity provider role is listed for a person whose
// session holds the role and not for another member.
func TestTemplatesGatedByAProviderRole(t *testing.T) {
	t.Parallel()
	gpu := gatewaytest.Template("gpu-lab", "jupyterlab")
	gpu.Spec.AllowedProviderRoles = []string{"gpu-users"}
	f := apitest.New(t, apitest.Options{}, gpu, gatewaytest.Template("plain", "jupyterlab"))
	tok, err := f.Store.Create(context.Background(), session.Session{
		Principal: authz.Principal{ID: "bob-bbbbbbbb", Username: "bob", ProviderRoles: []string{"gpu-users"}},
		Provider:  "oidc", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := func(c *http.Cookie) []string {
		out := apitest.Decode[struct{ Items []struct{ Name string } }](t, f.Do("GET", "/api/templates", "", c))
		var Names []string
		for _, it := range out.Items {
			Names = append(Names, it.Name)
		}
		slices.Sort(Names)
		return Names
	}
	if got := listed(&http.Cookie{Name: model.SessionCookieName, Value: tok}); !slices.Equal(got, []string{"gpu-lab", "plain"}) {
		t.Fatalf("bob sees %v", got)
	}
	if got := listed(f.Session("alice-aaaaaaaa")); !slices.Equal(got, []string{"plain"}) {
		t.Fatalf("alice sees %v", got)
	}
}

// Each template carries its category, a template saved without one is in
// Other, and the list names every category with its title in the order New
// app shows them.
func TestTemplatesReportTheirCategory(t *testing.T) {
	t.Parallel()
	lab := gatewaytest.Template("lab", "jupyterlab")
	lab.Spec.Category = "notebooks"
	f := apitest.New(t, apitest.Options{}, lab, gatewaytest.Template("plain", "streamlit"))
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))
	out := apitest.Decode[struct {
		Items      []struct{ Name, Category string }
		Categories []struct{ ID, Title string }
	}](t, w)
	got := map[string]string{}
	for _, it := range out.Items {
		got[it.Name] = it.Category
	}
	if got["lab"] != "notebooks" || got["plain"] != "other" {
		t.Fatalf("categories = %v (%s)", got, w.Body)
	}
	if len(out.Categories) != 6 || out.Categories[0] != (struct{ ID, Title string }{"notebooks", "Notebooks and IDEs"}) || out.Categories[5].ID != "other" {
		t.Fatalf("category list = %+v", out.Categories)
	}
}

func TestCreateWithSampleRunsTheSampleUnderTheChosenName(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.SampleTemplateObj("streamlit", "streamlit"))
	alice := f.Session("alice-aaaaaaaa")
	w := f.Do("POST", "/api/workspaces", `{"displayName":"My demo","template":"streamlit","sample":true}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/samples/streamlit", "", alice); w.Code != http.StatusCreated {
		t.Fatalf("one-click: status %d: %s", w.Code, w.Body)
	}
	got, oneClick := *f.GetWorkspace("alice-aaaaaaaa", "my-demo"), *f.GetWorkspace("alice-aaaaaaaa", "streamlit-sample")
	if got.DisplayName != "My demo" {
		t.Fatalf("display name %q", got.DisplayName)
	}
	// What differs between two apps is who they are, not what they run.
	got.ID, got.Name, got.DisplayName, got.CreatedAt = oneClick.ID, oneClick.Name, oneClick.DisplayName, oneClick.CreatedAt
	if !reflect.DeepEqual(got, oneClick) {
		t.Fatalf("spec differs from the one-click sample:\n got %+v\nwant %+v", got, oneClick)
	}
}

func TestCreateWithSampleOnTemplateWithoutOneIs422(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("plain", "jupyterlab"))
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Lab","template":"plain","sample":true}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), reasons.Msg(reasons.MsgNoSample, "plain")) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestTemplatesReportTheSampleFile(t *testing.T) {
	t.Parallel()
	lab := apitest.SampleTemplateObj("jupyterlab", "jupyterlab")
	lab.Spec.Sample.Open = ""
	f := apitest.New(t, apitest.Options{}, apitest.SampleTemplateObj("streamlit", "streamlit"), lab)
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))
	out := apitest.Decode[struct {
		Items []struct {
			Name   string
			Sample struct{ File string }
		}
	}](t, w)
	got := map[string]string{}
	for _, it := range out.Items {
		got[it.Name] = it.Sample.File
	}
	if got["streamlit"] != "samples/streamlit/app.py" || got["jupyterlab"] != "samples/jupyterlab" {
		t.Fatalf("files = %v (%s)", got, w.Body)
	}
}

func TestCreateLoginlessAppNeedsEnforcedNetworkPolicy(t *testing.T) {
	t.Parallel()
	for _, st := range []netprobe.State{netprobe.NotEnforced, netprobe.Unknown, ""} {
		f := apitest.New(t, apitest.Options{Policy: st}, apitest.LoginlessTemplate())
		w := f.Do("POST", "/api/workspaces", `{"displayName":"My code","template":"code"}`, f.Session("alice-aaaaaaaa"))
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("policy %q: status %d: %s", st, w.Code, w.Body)
		}
		msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.template")
		if !ok || msg != reasons.Msg(reasons.MsgAppLoginNeedsNetworkPolicy) {
			t.Fatalf("policy %q: errors %s", st, w.Body)
		}
		if n := f.WorkspaceCount(); n != 0 {
			t.Fatalf("policy %q: %d workspaces created", st, n)
		}
	}
}

func TestCreateLoginlessAppWorksWhenNetworkPolicyIsEnforced(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Policy: netprobe.Enforced}, apitest.LoginlessTemplate())
	w := f.Do("POST", "/api/workspaces", `{"displayName":"My code","template":"code"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("workspace not created")
	}
}

func TestCreateAppWithItsOwnLoginIgnoresNetworkPolicy(t *testing.T) {
	t.Parallel()
	tpl := gatewaytest.Template("code", "jupyterlab")
	tpl.Spec.AppLogin = "Enabled"
	f := apitest.New(t, apitest.Options{Policy: netprobe.NotEnforced}, tpl)
	w := f.Do("POST", "/api/workspaces", `{"displayName":"My code","template":"code"}`, f.Session("alice-aaaaaaaa"))
	if w.Code >= 300 || f.WorkspaceCount() != 1 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestStartSampleOfLoginlessAppNeedsEnforcedNetworkPolicy(t *testing.T) {
	t.Parallel()
	tpl := apitest.SampleTemplateObj("streamlit", "streamlit")
	tpl.Spec.AppLogin = "Disabled"
	f := apitest.New(t, apitest.Options{Policy: netprobe.NotEnforced}, tpl)
	w := f.Do("POST", "/api/samples/streamlit", "", f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity || f.WorkspaceCount() != 0 {
		t.Fatalf("status %d, %d workspaces: %s", w.Code, f.WorkspaceCount(), w.Body)
	}
}

func TestCreateLoginlessAppFailsClosedWhenPolicyCannotBeRead(t *testing.T) {
	t.Parallel()
	funcs := interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.ConfigMap); ok {
			return errors.New("api server unavailable")
		}
		return c.Get(ctx, key, obj, opts...)
	}}
	f := apitest.New(t, apitest.Options{Funcs: &funcs}, apitest.LoginlessTemplate())
	w := f.Do("POST", "/api/workspaces", `{"displayName":"My code","template":"code"}`, f.Session("alice-aaaaaaaa"))
	if w.Code < 400 || f.WorkspaceCount() != 0 {
		t.Fatalf("status %d, %d workspaces: %s", w.Code, f.WorkspaceCount(), w.Body)
	}
}

func TestCreateOnJobOnlyTemplateIs422OnTemplate(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.JobOnlyTemplate())
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Py","template":"jobs-py"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.template"); !ok {
		t.Fatalf("errors %s", w.Body)
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

func TestTemplatesMarkJobOnlyAndKeepJobs(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, apitest.JobOnlyTemplate(), gatewaytest.Template("lab", "jupyterlab"))
	w := f.Do("GET", "/api/templates", "", f.Session("alice-aaaaaaaa"))
	out := apitest.Decode[struct {
		Items []struct {
			Name    string
			JobOnly bool
			Jobs    *struct{ Engines []string }
		}
	}](t, w)
	got := map[string]bool{}
	for _, it := range out.Items {
		got[it.Name] = it.JobOnly
		if it.Name == "jobs-py" && (it.Jobs == nil || len(it.Jobs.Engines) != 1) {
			t.Fatalf("job-only template lost its jobs: %s", w.Body)
		}
	}
	if len(got) != 2 || !got["jobs-py"] || got["lab"] {
		t.Fatalf("jobOnly = %v (%s)", got, w.Body)
	}
}
