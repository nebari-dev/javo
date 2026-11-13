// Subscription hook choice (read from notifications-engine v0.5.0, pkg/api):
// the notifier calls the engine's API directly: RunTrigger evaluates a
// trigger from the operator's ConfigMap against a run, and Send delivers the
// notice. The engine's own controller (which keeps "already notified" in an
// annotation on a Kubernetes object) is not used: the database records, in
// job_runs.notified_at, which runs the owner has been told about. The
// ConfigMap's global subscriptions give the recipients; spec.notify.onFailure
// can veto them and the owner's verified email (RecipientResolver) can be
// added. No subscription annotation exists, so a user can never name an
// arbitrary recipient. A send error is not retried for the same run and is
// shown on the job as Notified=False.
package notify_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/jobs/notify"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const relNS = "javo-system"

var bg = context.Background()

// webhook records the bodies posted to it and answers with status.
type webhook struct {
	mu     sync.Mutex
	bodies []string
	status int
	srv    *httptest.Server
	posted chan struct{}
}

func newWebhook(t *testing.T, status int) *webhook {
	w := &webhook{status: status, posted: make(chan struct{}, 16)}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.bodies = append(w.bodies, string(b))
		w.mu.Unlock()
		w.posted <- struct{}{}
		rw.WriteHeader(w.status)
	}))
	t.Cleanup(w.srv.Close)
	return w
}

func (w *webhook) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.bodies)
}

func (w *webhook) wait(t *testing.T) string {
	t.Helper()
	select {
	case <-w.posted:
	case <-time.After(10 * time.Second):
		t.Fatal("no webhook call")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bodies[len(w.bodies)-1]
}

// configMap is the example the chart ships: one trigger on a failed newest run,
// one webhook service, one template.
func configMap(url string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "javo-notifications", Namespace: relNS},
		Data: map[string]string{
			// Retries are kept tiny so the 500 test finishes quickly (nanoseconds).
			"service.webhook.hook": "url: " + url + "\nretryWaitMin: 1000000\nretryWaitMax: 2000000\nretryMax: 1\n",
			"template.on-run-failed": `webhook:
  hook:
    method: POST
    body: '{"job":"{{.job.displayName}}","reason":"{{.reason.title}}","run":"{{.run.id}}"}'
`,
			"trigger.on-run-failed": `- when: len(obj.status?.recentRuns ?? []) > 0 && obj.status.recentRuns[0].outcome in ['Failed','TimedOut','Lost']
  oncePer: obj.status.recentRuns[0].id
  send: [on-run-failed]
`,
			"subscriptions": "- recipients: [hook]\n  triggers: [on-run-failed]\n",
		},
	}
}

// jobOf stores a job whose display name is "Nightly report" and returns it.
func jobOf(t *testing.T, db *store.Store, name string, onFailure *bool) model.Job {
	t.Helper()
	j, err := db.Jobs().Create(bg, model.Actor{Person: "alice"}, &model.Job{
		Namespace: "javo-apps", Name: name, Owner: "u-alice",
		Spec: model.JobSpec{
			DisplayName: "Nightly report", Template: "lab", Engine: "papermill", Kernel: "python3",
			Source: model.JobSource{Path: "nightly.ipynb"}, Notify: model.JobNotify{OnFailure: onFailure},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

var recorded = time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)

// finish records a finished run of the job and returns it.
func finish(t *testing.T, db *store.Store, j model.Job, k8sJob, outcome, reason string) model.Run {
	t.Helper()
	r, err := db.Jobs().RecordRun(bg, model.Run{
		JobID: j.ID, Trigger: "schedule", Handle: k8sJob, Outcome: outcome, Reason: reason, FinishTime: recorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// owners maps an owner name to a verified email.
type owners map[string]string

func (o owners) EmailFor(_ context.Context, name string) (string, bool) {
	a, ok := o[name]
	return a, ok
}

// interval is how often a started notifier looks for runs to tell about.
const interval = 20 * time.Millisecond

func start(t *testing.T, db *store.Store, cm *corev1.ConfigMap) {
	t.Helper()
	startWith(t, db, cm, notify.NoRecipients{})
}

func startWith(t *testing.T, db *store.Store, cm *corev1.ConfigMap, rec notify.RecipientResolver) {
	t.Helper()
	startCfg(t, db, cm, rec, 0)
}

// startCfg starts a notifier that sends at most one notice per job per gap
// (zero: the default).
func startCfg(t *testing.T, db *store.Store, cm *corev1.ConfigMap, rec notify.RecipientResolver, gap time.Duration) {
	t.Helper()
	n, err := notify.New(notify.Config{
		Store: db, Kube: kubefake.NewClientset(cm), Namespace: relNS,
		ConfigMapName: "javo-notifications", SecretName: "javo-notifications",
		Recipients: rec, Interval: interval, MinGap: gap,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _ = n.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func notified(t *testing.T, db *store.Store, runID string) bool {
	t.Helper()
	r, err := db.Jobs().Run(bg, runID)
	if err != nil {
		t.Fatal(err)
	}
	return !r.NotifiedAt.IsZero()
}

func waitNotified(t *testing.T, db *store.Store, runID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !notified(t, db, runID) {
		if time.Now().After(deadline) {
			t.Fatalf("run %s was never marked notified", runID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func notifiedCondition(t *testing.T, db *store.Store, jobID string) *metav1.Condition {
	t.Helper()
	j, err := db.Jobs().Get(bg, jobID)
	if err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(j.Status.Conditions, model.ConditionNotified)
}

func TestFailedRunSendsOneWebhookWithNameAndCatalogTitle(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	j := jobOf(t, db, "nightly", nil)
	run := finish(t, db, j, "n-1", model.RunOutcomeTimedOut, string(reasons.RunTimedOut))
	start(t, db, configMap(hook.srv.URL))

	body := hook.wait(t)
	title, _ := reasons.Lookup(reasons.RunTimedOut)
	for _, want := range []string{"Nightly report", title.Title, run.ID} {
		if !contains(body, want) {
			t.Fatalf("body %q lacks %q", body, want)
		}
	}
	waitNotified(t, db, run.ID)
	// Several looks later the same run has not been sent again.
	time.Sleep(10 * interval)
	if n := hook.count(); n != 1 {
		t.Fatalf("webhook called %d times for one failed run", n)
	}
}

func TestNextFailedRunSendsAgain(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	j := jobOf(t, db, "nightly", nil)
	finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	startCfg(t, db, configMap(hook.srv.URL), notify.NoRecipients{}, time.Millisecond)
	hook.wait(t)

	second := finish(t, db, j, "n-2", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	if body := hook.wait(t); !contains(body, second.ID) {
		t.Fatalf("body %q lacks the second run %s", body, second.ID)
	}
}

// A controller that restarts after sending does not send the notice again:
// the database remembers who was told.
func TestNotifyOncePerRunAcrossRestarts(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	j := jobOf(t, db, "nightly", nil)
	run := finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	cm := configMap(hook.srv.URL)
	n := func() *notify.Notifier {
		n, err := notify.New(notify.Config{
			Store: db, Kube: kubefake.NewClientset(cm), Namespace: relNS,
			ConfigMapName: "javo-notifications", SecretName: "javo-notifications",
			Recipients: notify.NoRecipients{}, Interval: interval,
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for range 2 {
		ctx, cancel := context.WithCancel(bg)
		done := make(chan struct{})
		first := n()
		go func() { defer close(done); _ = first.Start(ctx) }()
		waitNotified(t, db, run.ID)
		time.Sleep(5 * interval)
		cancel()
		<-done
	}
	if got := hook.count(); got != 1 {
		t.Fatalf("webhook called %d times across two controller runs", got)
	}
}

func TestSuccessAndRunningSendNothing(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	j := jobOf(t, db, "nightly", nil)
	finish(t, db, j, "n-1", model.RunOutcomeSucceeded, "")
	if _, err := db.Jobs().RecordRun(bg, model.Run{JobID: j.ID, Trigger: "schedule", Handle: "n-2", Outcome: model.RunOutcomeRunning}); err != nil {
		t.Fatal(err)
	}
	start(t, db, configMap(hook.srv.URL))

	time.Sleep(10 * interval)
	if n := hook.count(); n != 0 {
		t.Fatalf("webhook called %d times without a failure", n)
	}
}

// notify.onFailure=false sends nothing, and the run is still marked, so the
// job is not looked at again on every pass.
func TestOnFailureFalseSendsNothing(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	j := jobOf(t, db, "quiet", ptr.To(false))
	run := finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	start(t, db, configMap(hook.srv.URL))

	waitNotified(t, db, run.ID)
	time.Sleep(5 * interval)
	if n := hook.count(); n != 0 {
		t.Fatalf("webhook called %d times with notify.onFailure=false", n)
	}
}

func TestSendErrorSetsNotifiedFalseAndIsNotRetried(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusInternalServerError)
	j := jobOf(t, db, "nightly", nil)
	run := finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	start(t, db, configMap(hook.srv.URL))
	hook.wait(t)
	waitNotified(t, db, run.ID)

	deadline := time.Now().Add(10 * time.Second)
	for {
		c := notifiedCondition(t, db, j.ID)
		if c != nil {
			if c.Status != metav1.ConditionFalse || c.Reason != string(reasons.NotificationFailed) {
				t.Fatalf("condition %+v", c)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Notified condition never set")
		}
		time.Sleep(10 * time.Millisecond)
	}
	attempts := hook.count()
	time.Sleep(10 * interval)
	if hook.count() != attempts {
		t.Fatalf("a failed send was retried: %d -> %d calls", attempts, hook.count())
	}
}

func TestSendSuccessSetsNotifiedTrue(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	j := jobOf(t, db, "nightly", nil)
	run := finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	start(t, db, configMap(hook.srv.URL))
	waitNotified(t, db, run.ID)

	deadline := time.Now().Add(10 * time.Second)
	for {
		if c := notifiedCondition(t, db, j.ID); c != nil && c.Status == metav1.ConditionTrue {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Notified=True never set")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// The owner's verified email is added as a recipient next to the ConfigMap's
// own subscriptions; here the "email" service is a webhook so it can be seen.
func TestOwnerEmailIsAddedAsRecipient(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	cm := configMap(hook.srv.URL)
	cm.Data["service.webhook.email"] = cm.Data["service.webhook.hook"]
	cm.Data["template.on-run-failed"] += `  email:
    method: POST
    body: 'to={{.recipient}}'
`
	cm.Data["subscriptions"] = "" // only the owner is a recipient
	j := jobOf(t, db, "nightly", nil)
	finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	startWith(t, db, cm, owners{"u-alice": "alice@example.com"})

	if body := hook.wait(t); body != "to=alice@example.com" {
		t.Fatalf("body %q", body)
	}
}

func TestOwnerEmailNotAddedWhenOnFailureFalse(t *testing.T) {
	db := storetest.New(t)
	hook := newWebhook(t, http.StatusOK)
	cm := configMap(hook.srv.URL)
	cm.Data["service.webhook.email"] = cm.Data["service.webhook.hook"]
	cm.Data["subscriptions"] = ""
	j := jobOf(t, db, "quiet", ptr.To(false))
	run := finish(t, db, j, "n-1", model.RunOutcomeFailed, string(reasons.RunTimedOut))
	startWith(t, db, cm, owners{"u-alice": "alice@example.com"})

	waitNotified(t, db, run.ID)
	time.Sleep(5 * interval)
	if n := hook.count(); n != 0 {
		t.Fatalf("webhook called %d times", n)
	}
}
