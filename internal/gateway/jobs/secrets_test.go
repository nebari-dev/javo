package jobs_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A job's secrets are names in every answer: the value never comes back, and
// no log line or audit row holds it.
func TestJobSecretValueIsWriteOnly(t *testing.T) {
	logs := apitest.CaptureLogs(t)
	f := apitest.JobFixture(t, apitest.Options{})
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	f.CreateSecret(alice, "WAREHOUSE_PASSWORD", apitest.SecretValue)
	var answers []string

	w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":[{"name":"PGPASSWORD","secret":"WAREHOUSE_PASSWORD"}]`), alice)
	if w.Code != http.StatusCreated {
		t.Fatalf("create job: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())
	if got := apitest.Decode[apitest.JobSecretsJSON](t, w); len(got.Env) != 1 || got.Env[0] != (apitest.EnvJSON{Name: "PGPASSWORD", Secret: "WAREHOUSE_PASSWORD"}) {
		t.Fatalf("created job lists %+v", got.Env)
	}
	for _, path := range []string{"/api/jobs", "/api/jobs/nightly-sales", "/api/secrets"} {
		w := f.Do("GET", path, "", alice)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		answers = append(answers, w.Body.String())
	}
	if got := apitest.Decode[apitest.JobSecretsJSON](t, f.Do("GET", "/api/jobs/nightly-sales", "", alice)).Env; len(got) != 1 || got[0].Name != "PGPASSWORD" {
		t.Fatalf("job page lists %+v", got)
	}
	list := apitest.Decode[[]apitest.SecretUsesJSON](t, f.Do("GET", "/api/secrets", "", alice))
	if len(list) != 1 || len(list[0].Jobs) != 1 || list[0].Jobs[0].Name != "nightly-sales" || list[0].Jobs[0].DisplayName != "Nightly sales" {
		t.Fatalf("secrets list %+v", list)
	}
	w = f.Do("PUT", "/api/jobs/nightly-sales/env", `{"env":[{"name":"DB_PASSWORD","secret":"WAREHOUSE_PASSWORD"}]}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("set secrets: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())
	if got := f.JobBindings(apitest.AliceID, "nightly-sales"); len(got) != 1 || got[0].Env != "DB_PASSWORD" {
		t.Fatalf("stored %+v", got)
	}
	const newValue = "sk-test-PLACEHOLDER-rotated-0452"
	w = f.Do("PUT", "/api/secrets/WAREHOUSE_PASSWORD", `{"value":"`+newValue+`"}`, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", w.Code, w.Body)
	}
	answers = append(answers, w.Body.String())

	events, err := f.Db.Audit().List(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	audit := fmt.Sprintf("%+v", events)
	if !strings.Contains(audit, "job.set_env") {
		t.Fatalf("changing a job's secrets is not audited: %s", audit)
	}
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
}

// Only the owner gives a job secrets, and only their own: another person
// cannot bind into it or bind someone else's, and a platform admin, who
// manages every job, cannot choose its secrets either.
func TestJobSecretsArePrivateToTheirOwner(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.JobObj(apitest.AliceID, "alices"), apitest.JobObj(apitest.BobID, "bobs"))
	f.WithSecretKeys()
	alice, bob := f.Session(apitest.AliceID), f.Session(apitest.BobID)
	admin := f.Session("root-cccccccc", "javo-admins")
	f.CreateSecret(alice, "ALICE_KEY", apitest.SecretValue)

	// To bob there is no such secret, on his job or on a new one.
	w := f.Do("PUT", "/api/jobs/bobs/env", `{"env":[{"name":"STOLEN","secret":"ALICE_KEY"}]}`, bob)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bob binds alice's secret: %d %s", w.Code, w.Body)
	}
	w = f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":[{"name":"STOLEN","secret":"ALICE_KEY"}]`), bob)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bob creates a job with alice's secret: %d %s", w.Code, w.Body)
	}
	// Bob cannot reach alice's job at all.
	if w := f.Do("PUT", "/api/jobs/alices/env?owner="+apitest.AliceID, `{"env":[]}`, bob); w.Code != http.StatusNotFound {
		t.Fatalf("bob changes alice's job: %d %s", w.Code, w.Body)
	}
	// The admin manages alice's job but cannot choose (or clear) its secrets.
	for _, body := range []string{`{"env":[{"name":"KEY","secret":"ALICE_KEY"}]}`, `{"env":[]}`} {
		w = f.Do("PUT", "/api/jobs/alices/env?owner="+apitest.AliceID, body, admin)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsOwnerOnly) {
			t.Fatalf("admin sets alice's job secrets: %d %s", w.Code, w.Body)
		}
	}
	// Alice cannot put her secret into bob's job: it is not hers.
	if w := f.Do("PUT", "/api/jobs/bobs/env?owner="+apitest.BobID, `{"env":[{"name":"KEY","secret":"ALICE_KEY"}]}`, alice); w.Code != http.StatusNotFound {
		t.Fatalf("alice binds into bob's job: %d %s", w.Code, w.Body)
	}
	if got := f.JobBindings(apitest.BobID, "bobs"); len(got) != 0 {
		t.Fatalf("bob's job got %+v", got)
	}
	if got := f.JobBindings(apitest.AliceID, "alices"); len(got) != 0 {
		t.Fatalf("alice's job got %+v", got)
	}
	if len(f.Jobs()) != 2 {
		t.Fatal("a refused create stored a job")
	}
	// Alice herself may.
	if w := f.Do("PUT", "/api/jobs/alices/env", `{"env":[{"name":"KEY","secret":"ALICE_KEY"}]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("alice binds into her job: %d %s", w.Code, w.Body)
	}
}

func TestJobBindingChecksTheVariableNames(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.JobObj(apitest.AliceID, "nightly"))
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	many := make([]string, model.MaxEnvVars+1)
	for i := range many {
		many[i] = fmt.Sprintf(`{"name":"K%d","secret":"API_KEY"}`, i)
	}
	for _, tc := range []struct{ name, body, loc, msg string }{
		{"reserved run", `[{"name":"JAVO_RUN","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "JAVO_RUN")},
		{"reserved parameter", `[{"name":"JAVO_PARAM_REGION","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "JAVO_PARAM_REGION")},
		{"reserved home", `[{"name":"HOME","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameReserved, "HOME")},
		{"not a name", `[{"name":"MY-KEY","secret":"API_KEY"}]`, "body.env[0].name", reasons.Msg(reasons.MsgEnvNameInvalid)},
		{"twice", `[{"name":"A","secret":"API_KEY"},{"name":"A","secret":"API_KEY"}]`, "body.env[1].name", reasons.Msg(reasons.MsgEnvNameRepeated, "A")},
		{"unknown secret", `[{"name":"A","secret":"NOPE"}]`, "body.env[0].secret", reasons.Msg(reasons.MsgSecretUnknown, "NOPE")},
		{"too many", "[" + strings.Join(many, ",") + "]", "body.env", reasons.Msg(reasons.MsgEnvTooMany)},
	} {
		for _, req := range []struct{ method, path, body string }{
			{"PUT", "/api/jobs/nightly/env", `{"env":` + tc.body + `}`},
			{"POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":`+tc.body)},
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
	if len(f.Jobs()) != 1 {
		t.Fatal("a refused create stored a job")
	}
}

func TestDeletingASecretAJobUsesIsRefused(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{}, apitest.JobObj(apitest.AliceID, "nightly"))
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	f.CreateSecret(alice, "API_KEY", apitest.SecretValue)
	if w := f.Do("PUT", "/api/jobs/nightly/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("bind: %d %s", w.Code, w.Body)
	}
	w := f.Do("DELETE", "/api/secrets/API_KEY", "", alice)
	if w.Code != http.StatusConflict || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretInUse, "nightly") {
		t.Fatalf("delete a secret a job uses: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/jobs/nightly/env", `{"env":[]}`, alice); w.Code != http.StatusOK {
		t.Fatalf("unbind: %d %s", w.Code, w.Body)
	}
	if w := f.Do("DELETE", "/api/secrets/API_KEY", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
}

// An app's own API token cannot give its owner's secrets to a job either.
func TestAppTokensCannotGiveJobsSecrets(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.JobFixture(t, apitest.Options{}, apitest.AgentTemplate(), ws, apitest.JobObj(apitest.LocalAlice(), "nightly"))
	f.WithSecretKeys()
	c := f.LoginAlice()
	f.CreateSecret(c, "API_KEY", apitest.SecretValue)
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []struct{ method, path, body string }{
		{"PUT", "/api/jobs/nightly/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`},
		{"PUT", "/api/jobs/nightly/env", `{"env":[]}`},
		{"POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":[{"name":"K","secret":"API_KEY"}]`)},
	} {
		w := f.Bearer(req.method, req.path, req.body, text, nil)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsNotForAppTokens) {
			t.Fatalf("%s %s with an app token: %d %s", req.method, req.path, w.Code, w.Body)
		}
	}
	if got := f.JobBindings(apitest.LocalAlice(), "nightly"); len(got) != 0 {
		t.Fatalf("the job got %+v", got)
	}
}

// rewrite is a change of what nightly-sales runs: a command that prints the
// secret variable K.
const rewrite = `{"displayName":"Nightly sales","template":"lab","engine":"command","command":"sh -c 'echo $K'"}`

// A platform admin may manage alice's job but not choose its secrets, so
// neither may they change what runs next to the secrets she bound: the
// stored job and its next run stay hers. Without secrets, or for a change
// that runs nothing new, the admin's edit goes through.
func TestAdminCannotRewriteAJobThatHoldsSecrets(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	admin := f.Session("root-cccccccc", "javo-admins")
	f.CreateSecret(alice, "ALICE_KEY", apitest.SecretValue)
	if w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":[{"name":"K","secret":"ALICE_KEY"}]`), alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/jobs", `{"displayName":"Plain","template":"lab","engine":"papermill","source":"reports/sales.ipynb"}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("create plain: %d %s", w.Code, w.Body)
	}

	w := f.Do("PUT", "/api/jobs/nightly-sales?owner="+apitest.AliceID, rewrite, admin)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsHeldOwnerOnly) {
		t.Fatalf("admin rewrite of a job with secrets: %d %s", w.Code, w.Body)
	}
	job, err := f.Db.Jobs().GetByOwnerName(context.Background(), apitest.AliceID, "nightly-sales")
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Engine != "papermill" || len(job.Spec.Command) != 0 {
		t.Fatalf("stored %s %q", job.Spec.Engine, job.Spec.Command)
	}

	renamed := strings.Replace(fmt.Sprintf(apitest.NewJob, `,"run":{"retries":2}`), "Nightly sales", "Nightly sales EU", 1)
	if w := f.Do("PUT", "/api/jobs/nightly-sales?owner="+apitest.AliceID, renamed, admin); w.Code != http.StatusOK {
		t.Fatalf("admin rename and retries of a job with secrets: %d %s", w.Code, w.Body)
	}
	plain := strings.Replace(rewrite, "Nightly sales", "Plain", 1)
	if w := f.Do("PUT", "/api/jobs/plain?owner="+apitest.AliceID, plain, admin); w.Code != http.StatusOK {
		t.Fatalf("admin rewrite of a job without secrets: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/jobs/nightly-sales", rewrite, alice); w.Code != http.StatusOK {
		t.Fatalf("owner rewrite: %d %s", w.Code, w.Body)
	}
}

// An app's own token cannot give a job secrets, so it cannot change what
// runs next to them either.
func TestAppTokensCannotRewriteAJobThatHoldsSecrets(t *testing.T) {
	t.Parallel()
	ws := gatewaytest.Workspace("agent", apitest.LocalAlice(), "Agent")
	ws.Template = "agent"
	f := apitest.JobFixture(t, apitest.Options{}, apitest.AgentTemplate(), ws)
	f.WithSecretKeys()
	c := f.LoginAlice()
	f.CreateSecret(c, "API_KEY", apitest.SecretValue)
	if w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, `,"env":[{"name":"K","secret":"API_KEY"}]`), c); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	text, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{Name: "agent", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: ws.Namespace, Name: ws.ObjectName(), Short: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	w := f.Bearer("PUT", "/api/jobs/nightly-sales", rewrite, text, nil)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsHeldOwnerOnly) {
		t.Fatalf("app token rewrite: %d %s", w.Code, w.Body)
	}
	job, err := f.Db.Jobs().GetByOwnerName(context.Background(), apitest.LocalAlice(), "nightly-sales")
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Engine != "papermill" {
		t.Fatalf("stored engine %s", job.Spec.Engine)
	}
}
