package workspace

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// testKeys is the KEK and data keyset the test manager's reconciler opens
// values with; newTestKeys makes them in TestMain.
var testKeys *secrets.Keys

// newKEK writes 32 random bytes to a file in dir and loads them.
func newKEK(dir string) (secrets.KEK, error) {
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "kek")
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return secrets.KEKFromFile(f.Name())
}

func newTestKeys(s *store.Store, dir string) (*secrets.Keys, error) {
	kek, err := newKEK(dir)
	if err != nil {
		return nil, err
	}
	k := &secrets.Keys{Store: s, KEK: kek}
	return k, k.Ensure(bg)
}

// putSecret stores a secret in db as the gateway does: sealed with the
// stored public half for its row.
func putSecret(t *testing.T, owner, name, value string) {
	t.Helper()
	putSecretIn(t, db, owner, name, value)
}

func putSecretIn(t *testing.T, db *store.Store, owner, name, value string) {
	t.Helper()
	ks, _, err := db.SecretKeys().Get(bg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := secrets.NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	seal := func(id string) ([]byte, error) {
		return s.Seal([]byte(value), secrets.AD{ID: id, Owner: owner, Name: name})
	}
	if _, err := db.Secrets().Get(bg, owner, name); err == nil {
		_, err = db.Secrets().SetValue(bg, model.Actor{Person: owner}, owner, name, seal)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := db.Secrets().Create(bg, model.Actor{Person: owner}, owner, name, "", seal); err != nil {
		t.Fatal(err)
	}
}

func bind(t *testing.T, app model.App, bindings ...model.SecretBinding) {
	t.Helper()
	bindIn(t, db, app, bindings...)
}

// bindIn gives app in s the secrets, keeping its plain values.
func bindIn(t *testing.T, s *store.Store, app model.App, bindings ...model.SecretBinding) {
	t.Helper()
	cur, err := s.Apps().Get(bg, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apps().SetEnv(bg, model.Actor{Person: app.Owner}, app.ID, cur.Version, cur.Env, bindings); err != nil {
		t.Fatal(err)
	}
}

func envSecret(app model.App) *corev1.Secret {
	sec := &corev1.Secret{}
	if !exists(sec, AppSecretsName(app.ObjectName())) {
		return nil
	}
	return sec
}

// secretEnv is the env entry of the app container called name, or nil.
func secretEnv(pod *corev1.Pod, name string) *corev1.EnvVar {
	for i, e := range pod.Spec.Containers[0].Env {
		if e.Name == name {
			return &pod.Spec.Containers[0].Env[i]
		}
	}
	return nil
}

const testValue = "sk-test-PLACEHOLDER-controller-0451"

func TestAppGetsItsSecretsThroughAPerAppSecret(t *testing.T) {
	newTemplate(t, "lab-sec1")
	putSecret(t, "alice", "SEC1_KEY", testValue)
	app := newApp(t, "ws-sec1", "alice", "lab-sec1")
	bind(t, app, model.SecretBinding{Env: "OPENAI_API_KEY", Secret: "SEC1_KEY"})

	var pod *corev1.Pod
	eventually(t, "a pod with the secret", func() bool {
		pod = podOf(app)
		return pod != nil && secretEnv(pod, "OPENAI_API_KEY") != nil
	})
	e := secretEnv(pod, "OPENAI_API_KEY")
	if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
		e.ValueFrom.SecretKeyRef.Name != AppSecretsName(app.ObjectName()) || e.ValueFrom.SecretKeyRef.Key != "OPENAI_API_KEY" {
		t.Fatalf("env %+v", e)
	}
	sec := envSecret(app)
	if sec == nil || string(sec.Data["OPENAI_API_KEY"]) != testValue || sec.Labels[model.LabelApp] != app.ID {
		t.Fatalf("Secret %+v", sec)
	}
	// The app's Service owns it, so it goes with the Service even when the
	// row is removed while the controller is down (the sweep deletes the Service).
	svc := &corev1.Service{}
	if !exists(svc, app.ObjectName()) || len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != svc.UID || sec.OwnerReferences[0].Kind != "Service" {
		t.Fatalf("owner references %+v", sec.OwnerReferences)
	}
	// The value is never in the pod spec or the app's status.
	raw, _ := json.Marshal(pod)
	if strings.Contains(string(raw), testValue) {
		t.Fatal("the value is in the pod spec")
	}
	st, _ := json.Marshal(getApp(t, app.ID).Status)
	if strings.Contains(string(st), testValue) {
		t.Fatal("the value is in the app status")
	}
}

func TestANewValueOrBindingReplacesThePodOnce(t *testing.T) {
	newTemplate(t, "lab-sec2")
	putSecret(t, "alice", "SEC2_KEY", "first")
	app := newApp(t, "ws-sec2", "alice", "lab-sec2")
	bind(t, app, model.SecretBinding{Env: "KEY", Secret: "SEC2_KEY"})
	var old *corev1.Pod
	eventually(t, "first pod", func() bool {
		old = podOf(app)
		return old != nil && secretEnv(old, "KEY") != nil
	})

	putSecret(t, "alice", "SEC2_KEY", "second")
	var fresh *corev1.Pod
	eventually(t, "a new pod for the new value", func() bool {
		fresh = podOf(app)
		sec := envSecret(app)
		return fresh != nil && fresh.UID != old.UID && sec != nil && string(sec.Data["KEY"]) == "second"
	})
	for range 3 {
		reconcileNow(t, rec, app.ID)
	}
	if cur := podOf(app); cur == nil || cur.UID != fresh.UID {
		t.Fatal("the pod was replaced again without a change")
	}

	// Taking the secret off the app removes the variable and the Secret.
	bind(t, app)
	eventually(t, "a pod without the secret, and no Secret", func() bool {
		p := podOf(app)
		return p != nil && p.UID != fresh.UID && secretEnv(p, "KEY") == nil && envSecret(app) == nil
	})
}

// A preview runs a pull request the owner has not reviewed: even a binding
// written straight into the database gives it nothing.
func TestPreviewGetsNoSecrets(t *testing.T) {
	newTemplate(t, "lab-sec3")
	putSecret(t, "alice", "SEC3_KEY", testValue)
	src := func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}
	}
	parent := newApp(t, "ws-sec3", "alice", "lab-sec3", src)
	child := newApp(t, "ws-sec3-pr-1", "alice", "lab-sec3", src, func(a *model.App) { a.ParentID, a.PullNumber = parent.ID, 1 })
	bind(t, child, model.SecretBinding{Env: "KEY", Secret: "SEC3_KEY"})
	var pod *corev1.Pod
	eventually(t, "the preview's pod", func() bool { pod = podOf(child); return pod != nil })
	reconcileNow(t, rec, child.ID)
	if secretEnv(podOf(child), "KEY") != nil || envSecret(child) != nil {
		t.Fatal("a preview got a secret")
	}
}

func TestDeletingTheAppRemovesItsSecret(t *testing.T) {
	newTemplate(t, "lab-sec4")
	putSecret(t, "alice", "SEC4_KEY", testValue)
	app := newApp(t, "ws-sec4", "alice", "lab-sec4")
	bind(t, app, model.SecretBinding{Env: "KEY", Secret: "SEC4_KEY"})
	eventually(t, "the Secret", func() bool { return envSecret(app) != nil })
	if err := db.Apps().MarkDeleted(bg, actor, app.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Secret is gone", func() bool { return envSecret(app) == nil })
}

// A controller whose KEK does not open the keyset starts no pod for an app
// with secrets and says why; it never builds one without them.
func TestUnreadableSecretsFailTheApp(t *testing.T) {
	// A database the test manager does not watch: only r reconciles it.
	s := storetest.New(t)
	if _, err := newTestKeys(s, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	putTemplateIn(t, s, "lab-sec5")
	putSecretIn(t, s, "alice", "SEC5_KEY", testValue)
	app := newAppIn(t, s, allocNS, "ws-sec5", "alice", "lab-sec5")
	bindIn(t, s, app, model.SecretBinding{Env: "KEY", Secret: "SEC5_KEY"})
	other, err := newKEK(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := newRec(s, func(r *Reconciler) { r.SecretKeys, r.Secrets = &secrets.Keys{Store: s, KEK: other}, k8s })
	for range 2 {
		reconcileNow(t, r, app.ID)
	}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: allocNS, Name: app.ObjectName()}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Fatalf("a pod was built without the secrets: %v", err)
	}
	got := mustApp(t, s, app.ID)
	c := condition(got, model.ConditionScheduled)
	if got.Status.Phase != "Failed" || c == nil || c.Reason != string(reasons.SecretsUnreadable) {
		t.Fatalf("phase %q, scheduled %+v", got.Status.Phase, c)
	}
	if strings.Contains(c.Message, testValue) {
		t.Fatal("the condition holds the value")
	}
}

// Every variable the platform sets in an app's container is one a secret may
// not become, so a secret can never replace one.
func TestPlatformVariablesAreReserved(t *testing.T) {
	ws := &model.App{Namespace: testNS, Name: "all", Owner: "alice", Template: "t", Command: "run {{.Home}}", File: "app.py",
		Source: &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}}
	tpl := &model.Template{Name: "t", Spec: model.TemplateSpec{Kind: "streamlit", Image: "example.com/i:1", Port: 8501, UserCommand: model.UserCommandOptional}}
	pod, err := BuildPod(ws, tpl, "/u/alice/all/", "example.com", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	WithAPIToken(pod, tpl, model.APIAccessReadWrite, "tok", "https://javo.example.com")
	WithGitToken(pod, ws, "git", "1")
	var names []string
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, e := range c.Env {
			names = append(names, e.Name)
		}
	}
	if len(names) < 3 {
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
