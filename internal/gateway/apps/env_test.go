package apps_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// An app's environment holds plain values, shown again as typed, beside
// values from secrets, which are only ever named.
func TestAppEnvHoldsPlainValuesAndSecrets(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"app.py",`+
		`"env":[{"name":"LOG_LEVEL","value":"debug"},{"name":"OPENAI_API_KEY","secret":"API_KEY"}]}`, alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	want := []apitest.EnvJSON{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "OPENAI_API_KEY", Secret: "API_KEY"}}
	if got := apitest.Decode[apitest.BoundJSON](t, w).Env; !slices.Equal(got, want) {
		t.Fatalf("created app lists %+v", got)
	}
	app := f.GetWorkspace("alice-aaaaaaaa", "dash")
	if !slices.Equal(app.Env, []model.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}) {
		t.Fatalf("stored plain values %+v", app.Env)
	}
	w = f.Do("PUT", "/api/workspaces/dash/env", `{"env":[{"name":"LOG_LEVEL","value":"info"}]}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.BoundJSON](t, w).Env; !slices.Equal(got, []apitest.EnvJSON{{Name: "LOG_LEVEL", Value: "info"}}) {
		t.Fatalf("changed app lists %+v", got)
	}
	if got := f.GetWorkspace("alice-aaaaaaaa", "dash"); got.Version != app.Version+1 || got.Env[0].Value != "info" {
		t.Fatalf("stored %+v at version %d", got.Env, got.Version)
	}
	if got, _ := f.Db.Secrets().Bindings(context.Background(), app.ID); len(got) != 0 {
		t.Fatalf("the secret is still bound: %+v", got)
	}
}

func TestJobEnvHoldsPlainValuesAndSecrets(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":[{"name":"REGION","value":"emea"},{"name":"KEY","secret":"API_KEY"}]`), alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	want := []apitest.EnvJSON{{Name: "KEY", Secret: "API_KEY"}, {Name: "REGION", Value: "emea"}}
	if got := apitest.Decode[apitest.JobSecretsJSON](t, w).Env; !slices.Equal(got, want) {
		t.Fatalf("created job lists %+v", got)
	}
	job := f.Jobs()[0]
	if !slices.Equal(job.Spec.Env, []model.EnvVar{{Name: "REGION", Value: "emea"}}) {
		t.Fatalf("stored %+v", job.Spec.Env)
	}
	w = f.Do("PUT", "/api/jobs/nightly-sales/env", `{"env":[{"name":"REGION","value":"apac"}]}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	if got := apitest.Decode[apitest.JobSecretsJSON](t, w).Env; !slices.Equal(got, []apitest.EnvJSON{{Name: "REGION", Value: "apac"}}) {
		t.Fatalf("changed job lists %+v", got)
	}
	// Editing the job keeps its variables.
	body := fmt.Sprintf(apitest.NewJob, "")
	if w := f.Do("PUT", "/api/jobs/nightly-sales", body, alice); w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	if got := f.Jobs()[0].Spec.Env; !slices.Equal(got, []model.EnvVar{{Name: "REGION", Value: "apac"}}) {
		t.Fatalf("an edit dropped the variables: %+v", got)
	}
}

func TestPlainValuesAreChecked(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"), gatewaytest.Workspace("dash", "alice-aaaaaaaa", "Dash"))
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	for _, tc := range []struct{ name, body, loc, msg string }{
		{"reserved", `[{"name":"PORT","value":"1"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "PORT")},
		{"not a name", `[{"name":"A B","value":"1"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameInvalid)},
		{"twice across kinds", `[{"name":"A","value":"1"},{"name":"A","secret":"API_KEY"}]`, "body.env[1].name", reasons.Msg(reasons.MsgEnvNameRepeated, "A")},
		{"too long", `[{"name":"A","value":"` + strings.Repeat("x", model.MaxEnvValue+1) + `"}]`, "body.env[0].value", reasons.Msg(reasons.MsgEnvValueInvalid)},
		{"NUL", `[{"name":"A","value":"a\u0000b"}]`, "body.env[0].value", reasons.Msg(reasons.MsgEnvValueInvalid)},
		{"value and secret", `[{"name":"A","value":"1","secret":"API_KEY"}]`, "body.env[0].value", reasons.Msg(reasons.MsgEnvValueOrSecret)},
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
}

// Only the owner changes an app's or a job's variables: a platform admin who
// manages them, and an app's own API token, are refused even for plain values.
func TestOnlyTheOwnerChangesTheEnvironment(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.JobFixture(t, apitest.Options{}, apitest.AgentTemplate(), ws, apitest.JobObj(apitest.LocalAlice(), "nightly"))
	f.WithSecretKeys()
	c := f.LoginAlice()
	admin := f.Session("root-cccccccc", "javo-admins")
	for _, path := range []string{"/api/workspaces/agent/env?owner=" + apitest.LocalAlice(), "/api/jobs/nightly/env?owner=" + apitest.LocalAlice()} {
		w := f.Do("PUT", path, `{"env":[{"name":"A","value":"1"}]}`, admin)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsOwnerOnly) {
			t.Fatalf("admin PUT %s: %d %s", path, w.Code, w.Body)
		}
	}
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(),
		Scope: token.ScopeReadWrite, App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/workspaces/agent/env", "/api/jobs/nightly/env"} {
		w := f.Bearer("PUT", path, `{"env":[{"name":"A","value":"1"}]}`, text, nil)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != apitest.WorkloadRefusal("PUT") {
			t.Fatalf("app token PUT %s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := f.Do("PUT", "/api/workspaces/agent/env", `{"env":[{"name":"A","value":"1"}]}`, c); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
}

// An export holds plain values and the names of secrets, never a secret's
// value; an import brings both back, binding the importer's own secrets.
func TestExportAndImportCarryTheEnvironment(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{}, gatewaytest.Template("streamlit", "streamlit"))
	f.WithSecretKeys()
	alice := f.Session("alice-aaaaaaaa")
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	if w := f.Do("POST", "/api/workspaces", `{"displayName":"Dash","template":"streamlit","file":"app.py",`+
		`"env":[{"name":"LOG_LEVEL","value":"debug"},{"name":"OPENAI_API_KEY","secret":"API_KEY"}]}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w := f.Do("GET", "/api/workspaces/export", "", alice)
	export := w.Body.String()
	if !strings.Contains(export, "LOG_LEVEL") || !strings.Contains(export, "debug") || !strings.Contains(export, "API_KEY") || strings.Contains(export, apitest.SecretValue) {
		t.Fatalf("export:\n%s", export)
	}
	yaml := strings.Replace(export, "Dash", "Copy", 1)
	if w := f.Do("POST", "/api/workspaces/import", apitest.ImportBody(yaml, "rename"), alice); w.Code != http.StatusOK {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	cp := f.GetWorkspace("alice-aaaaaaaa", "copy")
	if !slices.Equal(cp.Env, []model.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}}) {
		t.Fatalf("imported plain values %+v", cp.Env)
	}
	if got, _ := f.Db.Secrets().Bindings(context.Background(), cp.ID); len(got) != 1 || got[0].Env != "OPENAI_API_KEY" {
		t.Fatalf("imported bindings %+v", got)
	}
}
