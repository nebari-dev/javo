package apps_test

import (
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Every app runs in its owner's home, where their secret-holding jobs and
// apps keep their code, so a platform admin cannot change what any app of
// someone else runs, even one that holds no credential: the new command
// could rewrite those files, or read the whole home.
func TestOnlyTheOwnerChangesWhatAnAppRuns(t *testing.T) {
	t.Parallel()
	tpl := gatewaytest.Template("streamlit", "streamlit")
	tpl.Spec.UserCommand = model.UserCommandOptional
	f := apitest.New(t, apitest.Options{}, tpl, gatewaytest.Workspace("sales", apitest.AliceID, "Sales"))
	root := f.Session(admin, "javo-admins")

	w := f.Do("PUT", "/api/workspaces/sales/launch?owner="+apitest.AliceID, `{"command":"python3 -c \"print(open('/home/user/notes.txt').read())\""}`, root)
	if w.Code != http.StatusForbidden || apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgRewriteOwnerOnly) {
		t.Fatalf("admin changes the command of an app without credentials: %d %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace(apitest.AliceID, "sales").Command; got != "" {
		t.Fatalf("stored %q", got)
	}
	if w := f.Do("PUT", "/api/workspaces/sales/launch", `{"command":"python b.py"}`, f.Session(apitest.AliceID)); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
}
