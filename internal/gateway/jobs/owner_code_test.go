package jobs_test

import (
	"context"
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
