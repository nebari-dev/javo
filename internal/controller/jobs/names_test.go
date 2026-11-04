package jobs

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/auth/token"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
)

// An app and a job of one owner live in one namespace. No name a person can
// pick may make the two share a Secret: the job reconciler would then revoke
// the app's token and write its own into the app's Secret, or the two would
// overwrite each other's secret values.
func TestAppAndJobSecretNamesNeverMeet(t *testing.T) {
	const owner = "alice-1a2b3c4d"
	picks := []string{"x", "x-javo", "x-javo-api", "x-javo-secrets", "x-javo-git", "x-javo-nebi", "x-api", "report"}
	// A manual run's Job is "<cronjob>-<10 hex>", which an app named
	// "<job>-<10 hex>" also produces.
	run := ManualJobName(&model.Job{Owner: owner, Name: "x"}, "run-1")
	picks = append(picks, run[len(owner)+1:])

	appOf := map[string]string{}
	for _, n := range picks {
		obj := naming.ObjectName(owner, n)
		for _, s := range []string{workspace.APITokenSecretName(obj), workspace.AppSecretsName(obj), workspace.GitTokenSecretName(obj), workspace.NebiTokenSecretName(obj)} {
			appOf[s] = "app " + n
		}
	}
	for _, n := range picks {
		j := &model.Job{Owner: owner, Name: n}
		for _, s := range []string{ScheduledSecretName(j), apiSecretName(j), RunSecretName(ManualJobName(j, "run-1"))} {
			if a, ok := appOf[s]; ok {
				t.Errorf("job %s and %s share the Secret %s", n, a, s)
			}
		}
	}
	// Within jobs: a job's own Secret is never a run's of another job.
	seen := map[string]string{}
	for _, n := range picks {
		j := &model.Job{Owner: owner, Name: n}
		for kind, s := range map[string]string{"scheduled": ScheduledSecretName(j), "api": apiSecretName(j), "run": RunSecretName(ManualJobName(j, "run-1"))} {
			if other, ok := seen[s]; ok {
				t.Errorf("%s Secret of job %s is also the %s", kind, n, other)
			}
			seen[s] = kind + " Secret of job " + n
		}
	}
}

// A Secret at one of the job's names that carries another job's id is not
// the job's: the controller neither reads nor overwrites it.
func TestJobRefusesASecretLabelledForAnotherJob(t *testing.T) {
	t.Parallel()
	h := newSecretsHarness(t)
	h.template(nil)
	h.putSecret("WAREHOUSE_PASSWORD", testValue)
	h.stop()
	d := h.definition("nightly", nil)
	if _, err := h.db.Jobs().SetEnv(bg, actor, d.ID, d.Version, nil, []model.SecretBinding{{Env: "PGPASSWORD", Secret: "WAREHOUSE_PASSWORD"}}); err != nil {
		t.Fatal(err)
	}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: h.ns, Name: ScheduledSecretName(&d),
		Labels: map[string]string{model.LabelJob: "another-job"}}, Data: map[string][]byte{"keep": []byte("x")}}
	if err := k8s.Create(bg, foreign); err != nil {
		t.Fatal(err)
	}
	r := h.direct()
	r.SecretKeys = h.rec.SecretKeys
	if _, err := r.Reconcile(bg, reconcile.Request{NamespacedName: types.NamespacedName{Name: d.ID}}); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("the reconcile used another job's Secret: %v", err)
	}
	got := h.k8sSecret(foreign.Name)
	if got == nil || string(got.Data["keep"]) != "x" || got.Labels[model.LabelJob] != "another-job" {
		t.Fatalf("another job's Secret was changed: %+v", got)
	}
}

// After an upgrade the job's Secrets sit under their old names. Once no run
// of the job is still going, the old Secrets the job owns go and the token
// the old API Secret held is revoked; an old name an app owns is left alone.
func TestUpgradeMovesAJobToItsNewSecretNames(t *testing.T) {
	t.Parallel()
	h, tokens, d, sec := tokenJob(t)
	if sec.Name != apiSecretName(&d) {
		t.Fatalf("the CronJob reads %s, want %s", sec.Name, apiSecretName(&d))
	}
	text, oldTok, err := tokens.Create(bg, model.Actor{System: model.SystemController}, token.Token{Name: "job " + d.Name, Owner: d.Owner, Scope: token.ScopeReadWrite, Job: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]string{model.LabelJob: d.ID, model.LabelOwner: d.Owner}
	oldAPI := legacyAPISecretName(&d)
	oldScheduled := cronJobName(&d) + legacySecretsSuffix
	for _, s := range []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Namespace: h.ns, Name: oldAPI, Labels: mine, Annotations: map[string]string{
			model.AnnotationTokenID: oldTok.ID, model.AnnotationTokenScope: string(token.ScopeReadWrite)}},
			Data: map[string][]byte{APITokenKey: []byte(text)}},
		// An app of the same owner made this one under the old names.
		{ObjectMeta: metav1.ObjectMeta{Namespace: h.ns, Name: oldScheduled, Labels: map[string]string{model.LabelApp: "app-1"}}},
	} {
		if err := k8s.Create(bg, s); err != nil {
			t.Fatal(err)
		}
	}
	// The controller's resync reconciles the job again.
	eventually(t, "the job's old token Secret gone", func() bool { return h.k8sSecret(oldAPI) == nil })
	if _, err := tokens.Authenticate(bg, text); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("the old token still works: %v", err)
	}
	if h.k8sSecret(oldScheduled) == nil {
		t.Fatal("an app's Secret under an old name was deleted")
	}
	if h.k8sSecret(apiSecretName(&d)) == nil {
		t.Fatal("the job lost its token Secret")
	}
}

// A token in the job's Secret is used only when its record is this job's: an
// app's token written there is neither adopted nor revoked, and the job gets
// a token of its own.
func TestJobNeverAdoptsAnAppsToken(t *testing.T) {
	t.Parallel()
	h, tokens, d, sec := tokenJob(t)
	h.stop()
	app, err := h.db.Apps().Create(bg, model.Actor{Person: d.Owner}, &model.App{Namespace: h.ns, Name: "x", Owner: d.Owner, Template: d.Spec.Template})
	if err != nil {
		t.Fatal(err)
	}
	text, appTok, err := tokens.Create(bg, model.Actor{System: model.SystemController}, token.Token{
		Name: "x", Owner: d.Owner, Scope: token.ScopeReadWrite, App: &token.App{Namespace: h.ns, Name: app.ObjectName(), Short: "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	sec.Annotations[model.AnnotationTokenID] = appTok.ID
	sec.Data[APITokenKey] = []byte(text)
	if err := k8s.Update(bg, sec); err != nil {
		t.Fatal(err)
	}
	r := h.direct()
	r.Tokens, r.APIURL = tokens, "https://javo.example.com"
	tpl, err := h.db.Templates().Get(bg, d.Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.syncToken(bg, &d, &tpl); err != nil {
		t.Fatal(err)
	}
	got := h.k8sSecret(apiSecretName(&d))
	tok, err := tokens.Authenticate(bg, string(got.Data[APITokenKey]))
	if err != nil || tok.Job != d.ID || tok.App != nil {
		t.Fatalf("the job's token is %+v, %v", tok, err)
	}
	if _, err := tokens.Authenticate(bg, text); err != nil {
		t.Fatalf("the app's token was revoked: %v", err)
	}
}
