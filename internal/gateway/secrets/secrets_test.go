package secrets_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
)

func TestSecretValueIsWriteOnly(t *testing.T) {
	logs := apitest.CaptureLogs(t)
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	opener := f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	var answers []string

	w := f.Do("POST", "/api/secrets", `{"name":"OPENAI_KEY","value":"`+apitest.SecretValue+`","note":"billing account"}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())
	if got := apitest.Decode[apitest.SecretJSON](t, w); got.Name != "OPENAI_KEY" || got.Note != "billing account" {
		t.Fatalf("created %+v", got)
	}
	w = f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"app.py","env":[{"name":"OPENAI_API_KEY","secret":"OPENAI_KEY"}]}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create app: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())
	for _, path := range []string{"/api/secrets", "/api/workspaces", "/api/workspaces/dash", "/api/workspaces/export"} {
		w := f.Do("GET", path, "", alice)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		answers = append(answers, w.Body.String())
	}
	if export := answers[len(answers)-1]; !strings.Contains(export, "OPENAI_API_KEY") || !strings.Contains(export, "OPENAI_KEY") {
		t.Fatalf("the export does not list the app's secret by name:\n%s", export)
	}
	const newValue = "sk-test-PLACEHOLDER-rotated-0452"
	w = f.Do("PUT", "/api/secrets/OPENAI_KEY", `{"value":"`+newValue+`"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())
	// A value that is refused is not echoed or logged either.
	tooLong := "sk-test-PLACEHOLDER-toolong-" + strings.Repeat("x", model.MaxSecretValue)
	w = f.Do("PUT", "/api/secrets/OPENAI_KEY", `{"value":"`+tooLong+`"}`, alice)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("too long: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())

	events, err := f.Db.Audit().List(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	audit := fmt.Sprintf("%+v", events)
	for _, v := range []string{apitest.SecretValue, newValue, "sk-test-PLACEHOLDER"} {
		for i, a := range answers {
			if strings.Contains(a, v) {
				t.Fatalf("answer %d holds the value: %s", i, a)
			}
		}
		if strings.Contains(logs.String(), v) {
			t.Fatalf("a log line holds the value:\n%s", logs)
		}
		if strings.Contains(audit, v) {
			t.Fatalf("an audit row holds the value: %s", audit)
		}
	}
	// What is stored is the new value, sealed for its row.
	sec, err := f.Db.Secrets().Get(context.Background(), "alice-aaaaaaaa", "OPENAI_KEY")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := f.Db.Secrets().Sealed(context.Background(), sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(newValue)) {
		t.Fatal("the database holds the value in clear")
	}
	got, err := opener.Open(sealed, secrets.AD{ID: sec.ID, Owner: sec.Owner, Name: sec.Name})
	if err != nil || string(got) != newValue || sec.Version != 2 {
		t.Fatalf("stored value %q (%v), version %d", got, err, sec.Version)
	}
}

func TestSecretsArePrivateToTheirOwner(t *testing.T) {
	t.Parallel()
	bobs := gatewaytest.Workspace("bobs", "bob-bbbbbbbb", "Bobs")
	alices := gatewaytest.Workspace("dash", "alice-aaaaaaaa", "Dash")
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), bobs, alices)
	f.WithSecretKeys()
	alice, bob := f.Session("alice-aaaaaaaa"), f.Session("bob-bbbbbbbb")
	admin := f.Session("root-cccccccc", "javo-admins")
	f.CreateSecret(alice, "ALICE_KEY", apitest.SecretValue)

	if list := apitest.Decode[[]apitest.SecretJSON](t, f.Do("GET", "/api/secrets", "", bob)); len(list) != 0 {
		t.Fatalf("bob lists %+v", list)
	}
	if list := apitest.Decode[[]apitest.SecretJSON](t, f.Do("GET", "/api/secrets", "", admin)); len(list) != 0 {
		t.Fatalf("a platform admin lists %+v", list)
	}
	for _, c := range []*http.Cookie{bob, admin} {
		if w := f.Do("PUT", "/api/secrets/ALICE_KEY", `{"value":"x"}`, c); w.Code != http.StatusNotFound {
			t.Fatalf("replace someone else's secret: %d %s", w.Code, w.Body)
		}
		if w := f.Do("DELETE", "/api/secrets/ALICE_KEY", "", c); w.Code != http.StatusNotFound {
			t.Fatalf("delete someone else's secret: %d %s", w.Code, w.Body)
		}
	}
	// Bob cannot bind it to his app: to him there is no such secret.
	w := f.Do("PUT", "/api/workspaces/bobs/env", `{"env":[{"name":"STOLEN","secret":"ALICE_KEY"}]}`, bob)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bob binds alice's secret: %d %s", w.Code, w.Body)
	}
	// A platform admin manages alice's app but cannot choose its secrets.
	w = f.Do("PUT", "/api/workspaces/dash/env?owner=alice-aaaaaaaa", `{"env":[{"name":"KEY","secret":"ALICE_KEY"}]}`, admin)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsOwnerOnly) {
		t.Fatalf("admin binds into alice's app: %d %s", w.Code, w.Body)
	}
	// Alice cannot bind her secret into bob's app either: it is not hers.
	if w := f.Do("PUT", "/api/workspaces/bobs/env?owner=bob-bbbbbbbb", `{"env":[{"name":"KEY","secret":"ALICE_KEY"}]}`, alice); w.Code != http.StatusNotFound {
		t.Fatalf("alice binds into bob's app: %d %s", w.Code, w.Body)
	}
	if got, _ := f.Db.Secrets().Bindings(context.Background(), f.GetWorkspace("bob-bbbbbbbb", "bobs").ID); len(got) != 0 {
		t.Fatalf("bob's app got %+v", got)
	}
}

func TestBindingChecksTheVariableNames(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Workspace("dash", "alice-aaaaaaaa", "Dash"))
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	many := make([]string, model.MaxEnvVars+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"name":"K%d","secret":"API_KEY"}`, i)
	}
	for _, tc := range []struct{ name, body, loc, msg string }{
		{"reserved token", `[{"name":"JAVO_TOKEN","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "JAVO_TOKEN")},
		{"reserved port", `[{"name":"PORT","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "PORT")},
		{"reserved home", `[{"name":"HOME","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "HOME")},
		{"not a name", `[{"name":"MY-KEY","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameInvalid)},
		{"twice", `[{"name":"A","secret":"API_KEY"},{"name":"A","secret":"API_KEY"}]`, "body.env[1].name", reasons.Msg(reasons.MsgEnvNameRepeated, "A")},
		{"unknown secret", `[{"name":"A","secret":"NOPE"}]`, "body.env[0].secret", reasons.Msg(reasons.MsgSecretUnknown, "NOPE")},
		{"too many", "[" + strings.Join(many, ",") + "]", "body.env", reasons.Msg(reasons.MsgEnvTooMany)},
	} {
		for _, req := range []struct{ method, path, body string }{
			{"PUT", "/api/workspaces/dash/env", `{"env":` + tc.body + `}`},
			{"POST", "/api/workspaces", `{"displayName":"New","template":"streamlit","file":"app.py","env":` + tc.body + `}`},
		} {
			w := f.Do(req.method, req.path, req.body, alice)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s: %d %s", tc.name, req.method, w.Code, w.Body)
			}
			if msg, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage(tc.loc); !ok || msg != tc.msg {
				t.Fatalf("%s %s: %s", tc.name, req.method, w.Body)
			}
		}
	}
	if f.WorkspaceCount() != 1 {
		t.Fatal("a refused create stored an app")
	}
	w := f.Do("PUT", "/api/workspaces/dash/env", `{"env":[{"name":"OPENAI_API_KEY","secret":"API_KEY"},{"name":"BACKUP_KEY","secret":"API_KEY"}]}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.BoundJSON](t, w).Env; len(got) != 2 || got[0] != (apitest.EnvJSON{Name: "BACKUP_KEY", Secret: "API_KEY"}) {
		t.Fatalf("view %+v", got)
	}
	list := apitest.Decode[[]apitest.SecretJSON](t, f.Do("GET", "/api/secrets", "", alice))
	if len(list) != 1 || len(list[0].Apps) != 1 || list[0].Apps[0].Name != "dash" || list[0].Apps[0].DisplayName != "Dash" {
		t.Fatalf("list %+v", list)
	}
}

func TestPreviewsGetNoSecrets(t *testing.T) {
	t.Parallel()
	parent := gatewaytest.Workspace("site", "alice-aaaaaaaa", "Site")
	parent.Source = &model.Source{URL: "https://github.com/example-org/site"}
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), parent)
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	child := f.PreviewChild(f.GetWorkspace("alice-aaaaaaaa", "site"), 7)
	w := f.Do("PUT", "/api/workspaces/"+child.Name+"/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`, alice)
	if w.Code != http.StatusConflict {
		t.Fatalf("bind into a preview: %d %s", w.Code, w.Body)
	}
}

func TestDeletingASecretAnAppUsesIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Workspace("dash", "alice-aaaaaaaa", "Dash"))
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	if w := f.Do("PUT", "/api/workspaces/dash/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", w.Code, w.Body)
	}
	w := f.Do("DELETE", "/api/secrets/API_KEY", "", alice)
	if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretInUse, "Dash") {
		t.Fatalf("delete a used secret: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/dash/env", `{"env":[]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("unbind: %d %s", w.Code, w.Body)
	}
	if w := f.Do("DELETE", "/api/secrets/API_KEY", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
}

func TestSecretFieldsAreChecked(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "TAKEN", "v")
	for _, tc := range []struct{ body, loc string }{
		{`{"name":"1BAD","value":"v"}`, "body.name"},
		{`{"name":"JAVO_TOKEN","value":"v"}`, "body.name"},
		{`{"name":"TAKEN","value":"v"}`, "body.name"},
		{`{"name":"OK","value":""}`, "body.value"},
		{`{"name":"OK","value":"a\u0000b"}`, "body.value"},
		{`{"name":"OK","value":"v","note":"` + strings.Repeat("n", model.MaxSecretNote+1) + `"}`, "body.note"},
	} {
		w := f.Do("POST", "/api/secrets", tc.body, alice)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d %s", tc.body, w.Code, w.Body)
		}
		if _, ok := apitest.Decode[apitest.Problem](t, w).FieldMessage(tc.loc); !ok {
			t.Fatalf("%s: no error on %s: %s", tc.body, tc.loc, w.Body)
		}
	}
}

// An import binds the importer's own secrets of the names the file gives; a
// name they have no secret for refuses that app.
func TestImportBindsSecretsByName(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	doc := func(display, secret string) string {
		return "---\napiVersion: javo.nebari.dev/v1alpha1\nkind: App\nmetadata: {name: x}\nspec: {displayName: " + display +
			", template: streamlit, file: app.py, secrets: [{env: MY_KEY, secret: " + secret + "}]}\n"
	}
	w := f.Do("POST", "/api/workspaces/import", apitest.ImportBody(doc("Known", "API_KEY")+doc("Unknown", "NOPE"), "skip"), alice)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	rows := apitest.Decode[apitest.TransferJSON](t, w).Rows
	if len(rows) != 2 || rows[0].Outcome != apps.OutcomeCreated || rows[1].Outcome != apps.OutcomeInvalid ||
		rows[1].Message != reasons.Msg(reasons.MsgSecretUnknown, "NOPE") {
		t.Fatalf("rows %+v", rows)
	}
	got, err := f.Db.Secrets().Bindings(context.Background(), f.GetWorkspace("alice-aaaaaaaa", "known").ID)
	if err != nil || len(got) != 1 || got[0] != (model.SecretBinding{Env: "MY_KEY", Secret: "API_KEY"}) {
		t.Fatalf("bindings %+v, %v", got, err)
	}
}

func TestSecretsNeedTheKeyset(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	w := f.Do("POST", "/api/secrets", `{"name":"K","value":"v"}`, f.Session("alice-aaaaaaaa"))
	if w.Code != http.StatusServiceUnavailable || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsUnavailable) {
		t.Fatalf("create without a keyset: %d %s", w.Code, w.Body)
	}
}

// An app's own API token reaches every person the app is shared with, so it
// cannot touch secrets: it could otherwise give itself all of its owner's.
func TestAppTokensCannotUseSecrets(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.New(t, apitest.Options{}, apitest.AgentTemplate(), ws)
	f.WithSecretKeys()
	c := f.LoginAlice()
	f.CreateSecret(c, "API_KEY", apitest.SecretValue)
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/api/secrets", ""},
		{"POST", "/api/secrets", `{"name":"NEW","value":"v"}`},
		{"PUT", "/api/secrets/API_KEY", `{"value":"v"}`},
		{"DELETE", "/api/secrets/API_KEY", ""},
		{"PUT", "/api/workspaces/agent/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`},
		{"POST", "/api/workspaces", `{"displayName":"Leak","template":"agent","env":[{"name":"K","secret":"API_KEY"}]}`},
	} {
		w := f.Bearer(req.method, req.path, req.body, text, nil)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != apitest.WorkloadRefusal(req.method) {
			t.Fatalf("%s %s with an app token: %d %s", req.method, req.path, w.Code, w.Body)
		}
	}
	// A personal read-write token of the owner may.
	personal := f.NewToken(c, `{"name":"laptop","scope":"read-write"}`).Token
	if w := f.Bearer("PUT", "/api/workspaces/agent/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`, personal, nil); w.Code != http.StatusOK {
		t.Fatalf("bind with a personal token: %d %s", w.Code, w.Body)
	}
}
