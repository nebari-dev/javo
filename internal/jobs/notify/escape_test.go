package notify_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// hostile is failure text a job's own code wrote: it tries to close the
// JSON string, ping everyone in a chat and post a link.
var hostile = `boom", "admin": true, "x": "<!channel> @channel @here @all see https://evil.example/login ` + strings.Repeat("A", 20000)

func TestFailureTextIsEscapedAndCappedForAWebhook(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	cm := configMap(hook.srv.URL)
	cm.Data["template.on-run-failed"] = `webhook:
  hook:
    method: POST
    body: '{"job":"{{.job.displayName}}","message":"{{.run.message}}","raw":"{{(index .obj.status.recentRuns 0).message}}","json":{{ toJson .run.message }}}'
`
	j := jobOf(t, db, "nightly", nil)
	if _, err := db.Jobs().RecordRun(bg, model.Run{JobID: j.ID, Trigger: "schedule", Handle: "n-1", Outcome: model.RunOutcomeFailed,
		Reason: string(reasons.RunTimedOut), Message: hostile, FinishTime: recorded}); err != nil {
		t.Fatal(err)
	}
	start(t, db, cm)

	body := hook.wait(t)
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("the message broke the JSON body: %v\n%.300s", err, body)
	}
	if len(got) != 4 {
		t.Fatalf("the message added fields: %v", keys(got))
	}
	for _, k := range []string{"message", "raw", "json"} {
		s, _ := got[k].(string)
		checkClean(t, k, s)
		// The chart's own template quotes with toJson: the text must read
		// the same there, not escaped twice.
		if !strings.HasPrefix(s, "boom', 'admin': true") {
			t.Errorf("%s reads %.60q", k, s)
		}
	}
}

func TestFailureTextIsEscapedForAChat(t *testing.T) {
	db := storetest.New(t)
	chat := newWebhook(t, http.StatusCreated)
	cm := configMap("http://unused.example")
	cm.Data["service.mattermost"] = "apiURL: " + chat.srv.URL + "\ntoken: t\n"
	cm.Data["template.on-run-failed"] = `message: '{{.job.displayName}} failed: {{.run.message}}'
`
	cm.Data["subscriptions"] = "- recipients: [mattermost:ops]\n  triggers: [on-run-failed]\n"
	j := jobOf(t, db, "nightly", nil)
	if _, err := db.Jobs().RecordRun(bg, model.Run{JobID: j.ID, Trigger: "schedule", Handle: "n-1", Outcome: model.RunOutcomeFailed,
		Reason: string(reasons.RunTimedOut), Message: hostile, FinishTime: recorded}); err != nil {
		t.Fatal(err)
	}
	start(t, db, cm)

	var post struct{ Message string }
	if err := json.Unmarshal([]byte(chat.wait(t)), &post); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(post.Message, "Nightly report failed: boom") {
		t.Fatalf("message %.200q", post.Message)
	}
	checkClean(t, "chat message", post.Message)
}

// One job failing over and over sends one notice per gap, not one per run;
// another job is not held back by it.
func TestNoticesAreLimitedPerJob(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	loud := jobOf(t, db, "loud", nil)
	for _, k := range []string{"n-1", "n-2", "n-3"} {
		finish(t, db, loud, k, model.RunOutcomeFailed, string(reasons.RunTimedOut))
	}
	other := jobOf(t, db, "other", nil)
	last := finish(t, db, other, "o-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	start(t, db, configMap(hook.srv.URL))
	waitNotified(t, db, last.ID)
	time.Sleep(10 * interval)
	if n := hook.count(); n != 2 {
		t.Fatalf("%d notices for three failures of one job and one of another, want 2", n)
	}
}

func checkClean(t *testing.T, what, s string) {
	t.Helper()
	for _, bad := range []string{"<!channel>", "@channel", "@here", "@all", "https://", "evil.example/login"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s holds %q", what, bad)
		}
	}
	if n := len([]rune(s)); n > 2000 {
		t.Errorf("%s is %d characters long", what, n)
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
