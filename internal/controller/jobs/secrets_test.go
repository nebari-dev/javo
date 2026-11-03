package jobs

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

const testValue = "sk-test-PLACEHOLDER-jobs-0451"

// newKEK writes 32 random bytes to a file and loads them.
func newKEK(t *testing.T) secrets.KEK {
	t.Helper()
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	kek, err := secrets.KEKFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return kek
}

// newSecretsHarness is a harness whose controller opens values with a KEK
// that made the database's keyset, as at a real start.
func newSecretsHarness(t *testing.T) *harness {
	t.Helper()
	kek := newKEK(t)
	return newHarnessWith(t, func(r *Reconciler) {
		keys := &secrets.Keys{Store: r.Store, KEK: kek}
		if err := keys.Ensure(bg); err != nil {
			t.Fatal(err)
		}
		r.SecretKeys = keys
	})
}

// putSecret stores (or replaces) owner's secret as the gateway does: sealed
// with the stored public half for its row.
func (h *harness) putSecret(name, value string) {
	h.t.Helper()
	ks, found, err := h.db.SecretKeys().Get(bg)
	if err != nil || !found {
		h.t.Fatalf("keyset: %v, %v", found, err)
	}
	s, err := secrets.NewSealer(ks.Public)
	if err != nil {
		h.t.Fatal(err)
	}
	seal := func(id string) ([]byte, error) {
		return s.Seal([]byte(value), secrets.AD{ID: id, Owner: owner, Name: name})
	}
	if _, err := h.db.Secrets().Get(bg, owner, name); err == nil {
		if _, err := h.db.Secrets().SetValue(bg, actor, owner, name, seal); err != nil {
			h.t.Fatal(err)
		}
		return
	}
	if _, err := h.db.Secrets().Create(bg, actor, owner, name, "", seal); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) bind(j model.Job, bindings ...model.SecretBinding) {
	h.t.Helper()
	cur := h.job(j.ID)
	if _, err := h.db.Jobs().SetEnv(bg, actor, j.ID, cur.Version, cur.Spec.Env, bindings); err != nil {
		h.t.Fatal(err)
	}
}

// k8sSecret is the Secret called name in the harness namespace, or nil.
func (h *harness) k8sSecret(name string) *corev1.Secret {
	sec := &corev1.Secret{}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: h.ns, Name: name}, sec); err != nil {
		if !apierrors.IsNotFound(err) {
			h.t.Fatal(err)
		}
		return nil
	}
	return sec
}

func (h *harness) getCronJob(j model.Job) *batchv1.CronJob {
	cj := h.cronJob(j)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		return nil
	}
	return cj
}

// runEnv is the env entry called name of a run's app container, or nil.
func runEnv(spec *corev1.PodSpec, name string) *corev1.EnvVar {
	for i, e := range spec.Containers[0].Env {
		if e.Name == name {
			return &spec.Containers[0].Env[i]
		}
	}
	return nil
}

// refersTo reports whether e takes its value from key env of the Secret secretName.
func refersTo(e *corev1.EnvVar, secretName, key string) bool {
	return e != nil && e.Value == "" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil &&
		e.ValueFrom.SecretKeyRef.Name == secretName && e.ValueFrom.SecretKeyRef.Key == key
}

func ownedBy(sec *corev1.Secret, kind, name string, uid string) bool {
	return len(sec.OwnerReferences) == 1 && sec.OwnerReferences[0].Kind == kind &&
		sec.OwnerReferences[0].Name == name && string(sec.OwnerReferences[0].UID) == uid
}

// A scheduled job's runs read the values from one Secret the CronJob owns,
// so Kubernetes deletes it with the CronJob. The values are never in the
// CronJob, its Jobs or the job's status.
func TestScheduledRunsGetSecretsFromASecretTheCronJobOwns(t *testing.T) {
	t.Parallel()
	h := newSecretsHarness(t)
	h.template(nil)
	h.putSecret("WAREHOUSE_PASSWORD", testValue)
	d := h.definition("nightly", nil)
	h.bind(d, model.SecretBinding{Env: "PGPASSWORD", Secret: "WAREHOUSE_PASSWORD"})

	name := ScheduledSecretName(&d)
	var cj *batchv1.CronJob
	eventually(t, "the CronJob takes PGPASSWORD from the job's Secret", func() bool {
		cj = h.getCronJob(d)
		return cj != nil && refersTo(runEnv(&cj.Spec.JobTemplate.Spec.Template.Spec, "PGPASSWORD"), name, "PGPASSWORD")
	})
	var sec *corev1.Secret
	eventually(t, "the job's Secret", func() bool { sec = h.k8sSecret(name); return sec != nil })
	if string(sec.Data["PGPASSWORD"]) != testValue || len(sec.Data) != 1 || sec.Labels[model.LabelJob] != d.ID {
		t.Fatalf("Secret labels %v, keys %d", sec.Labels, len(sec.Data))
	}
	if !ownedBy(sec, "CronJob", cj.Name, string(cj.UID)) {
		t.Fatalf("owner references %+v", sec.OwnerReferences)
	}
	raw, _ := json.Marshal(cj)
	st, _ := json.Marshal(h.job(d.ID).Status)
	if strings.Contains(string(raw), testValue) || strings.Contains(string(st), testValue) {
		t.Fatal("the value is in the CronJob or the job status")
	}

	// A new value reaches the Secret, so the next run gets it; the CronJob
	// does not change.
	h.putSecret("WAREHOUSE_PASSWORD", "second")
	eventually(t, "the new value in the Secret", func() bool {
		s := h.k8sSecret(name)
		return s != nil && string(s.Data["PGPASSWORD"]) == "second"
	})
	if now := h.getCronJob(d); now.Annotations[annotationSpecHash] != cj.Annotations[annotationSpecHash] {
		t.Fatal("a new value changed the CronJob")
	}

	// Taking the secret off removes the variable and the Secret.
	h.bind(d)
	eventually(t, "no variable and no Secret", func() bool {
		cj := h.getCronJob(d)
		return cj != nil && runEnv(&cj.Spec.JobTemplate.Spec.Template.Spec, "PGPASSWORD") == nil && h.k8sSecret(name) == nil
	})
}

// A run started by hand gets a Secret of its own, holding the values at its
// start and owned by its Job: Kubernetes deletes it with the run.
func TestManualRunGetsASecretOfItsOwn(t *testing.T) {
	t.Parallel()
	h := newSecretsHarness(t)
	h.template(nil)
	h.putSecret("API_KEY", testValue)
	d := h.definition("adhoc", func(j *model.Job) { j.Spec.Schedule = nil })
	h.bind(d, model.SecretBinding{Env: "OPENAI_API_KEY", Secret: "API_KEY"})
	h.waitPhase(d, model.JobPhaseReady)

	_, k := h.requestRun(d, nil)
	name := RunSecretName(k.Name)
	if !refersTo(runEnv(&k.Spec.Template.Spec, "OPENAI_API_KEY"), name, "OPENAI_API_KEY") {
		t.Fatalf("env %+v", k.Spec.Template.Spec.Containers[0].Env)
	}
	var sec *corev1.Secret
	eventually(t, "the run's Secret, owned by its Job", func() bool {
		sec = h.k8sSecret(name)
		return sec != nil && ownedBy(sec, "Job", k.Name, string(k.UID))
	})
	if string(sec.Data["OPENAI_API_KEY"]) != testValue || sec.Labels[model.LabelJob] != d.ID {
		t.Fatalf("Secret labels %v", sec.Labels)
	}
	if raw, _ := json.Marshal(k); strings.Contains(string(raw), testValue) {
		t.Fatal("the value is in the Job")
	}
	if h.k8sSecret(ScheduledSecretName(&d)) != nil {
		t.Fatal("a job without a schedule has a scheduled Secret")
	}

	// The next run gets the new value; the first keeps what it started with.
	h.putSecret("API_KEY", "second")
	_, k2 := h.requestRun(d, nil)
	eventually(t, "the second run's Secret", func() bool {
		s := h.k8sSecret(RunSecretName(k2.Name))
		return s != nil && string(s.Data["OPENAI_API_KEY"]) == "second"
	})
	if s := h.k8sSecret(name); s == nil || string(s.Data["OPENAI_API_KEY"]) != testValue {
		t.Fatal("a new value changed a run already started")
	}
}

// A controller that cannot open the values runs nothing without them: the
// job is blocked, its schedule suspended and a run asked for fails, each with
// SecretsUnreadable; no Secret is made.
func TestUnreadableSecretsBlockTheJob(t *testing.T) {
	t.Parallel()
	kek := newKEK(t)
	h := newHarnessWith(t, func(r *Reconciler) {
		keys := &secrets.Keys{Store: r.Store, KEK: kek}
		if err := keys.Ensure(bg); err != nil {
			t.Fatal(err)
		}
		// The keyset is made, then the controller is given another key.
		r.SecretKeys = &secrets.Keys{Store: r.Store, KEK: newKEK(t)}
	})
	h.template(nil)
	h.putSecret("API_KEY", testValue)
	d := h.definition("nightly", nil)
	h.bind(d, model.SecretBinding{Env: "KEY", Secret: "API_KEY"})

	got := h.waitPhase(d, model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.SecretsUnreadable) {
		t.Fatalf("conditions %+v", got.Status.Conditions)
	}
	if h.cronJobExists(d) && !h.suspended(d) {
		t.Fatal("the schedule runs without the secrets")
	}
	run, err := h.db.Jobs().RequestRun(bg, actor, d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	run = h.waitRunWhere(d, run.ID, " to fail", func(r model.Run) bool { return r.Outcome != "" })
	if run.Outcome != model.RunOutcomeFailed || run.Reason != string(reasons.SecretsUnreadable) || run.K8sJob != "" {
		t.Fatalf("run %+v", run)
	}
	if h.k8sSecret(ScheduledSecretName(&d)) != nil {
		t.Fatal("a Secret was made")
	}
}

// Deleting the job removes its Secrets at once, without waiting for
// Kubernetes to collect them after their owners.
func TestDeletingAJobRemovesItsSecrets(t *testing.T) {
	t.Parallel()
	h := newSecretsHarness(t)
	h.template(nil)
	h.putSecret("API_KEY", testValue)
	d := h.definition("nightly", nil)
	h.bind(d, model.SecretBinding{Env: "KEY", Secret: "API_KEY"})
	_, k := h.requestRun(d, nil)
	eventually(t, "both Secrets", func() bool {
		return h.k8sSecret(ScheduledSecretName(&d)) != nil && h.k8sSecret(RunSecretName(k.Name)) != nil
	})
	if err := h.db.Jobs().MarkDeleted(bg, actor, d.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "no Secret left", func() bool {
		return h.k8sSecret(ScheduledSecretName(&d)) == nil && h.k8sSecret(RunSecretName(k.Name)) == nil
	})
}

// Every variable the platform sets in a run's container is one a secret may
// not become, so a secret never replaces one.
func TestRunVariablesAreReserved(t *testing.T) {
	t.Parallel()
	j := &model.Job{Namespace: "ns", Name: "nightly", Owner: owner, Spec: model.JobSpec{
		Template: "lab", Engine: "command", Command: []string{"python", "etl.py"},
		Parameters: []model.JobParameter{{Name: "region", Type: "string", Default: "emea"}},
	}}
	tpl := &model.Template{Name: "lab", Spec: model.TemplateSpec{Image: "example.com/lab:1",
		Jobs: &model.TemplateJobs{Engines: []string{"command"}}}}
	layout := podstorage.Build(owner, podstorage.Input{UID: 1000, GID: podspec.FixedGID})
	k, err := BuildManualJob(Inputs{Job: j, Template: tpl, RunnerImage: podspec.Image{Ref: "example.com/javo:9"}, Layout: layout}, "run-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range append(k.Spec.Template.Spec.InitContainers, k.Spec.Template.Spec.Containers...) {
		for _, e := range c.Env {
			names = append(names, e.Name)
		}
	}
	if len(names) < 2 {
		t.Fatalf("only %v", names)
	}
	for _, n := range names {
		if slices.ContainsFunc(podspec.ColorEnv(), func(e corev1.EnvVar) bool { return e.Name == n }) {
			continue // a default the app's or job's own variable replaces
		}
		if model.EnvNameProblem(n) != model.EnvNameReserved {
			t.Errorf("the platform sets %s, which a secret may become", n)
		}
	}
}
