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

// putSecret stores a secret in fx.db as the gateway does: sealed with the
// stored public half for its row.
func (fx *env) putSecret(t *testing.T, owner, name, value string) {
	t.Helper()
	putSecretIn(t, fx.db, owner, name, value)
}

func putSecretIn(t *testing.T, st *store.Store, owner, name, value string) {
	t.Helper()
	ks, _, err := st.SecretKeys().Get(bg)
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
	if _, err := st.Secrets().Get(bg, owner, name); err == nil {
		_, err = st.Secrets().SetValue(bg, model.Actor{Person: owner}, owner, name, seal)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := st.Secrets().Create(bg, model.Actor{Person: owner}, owner, name, "", seal); err != nil {
		t.Fatal(err)
	}
}

func (fx *env) bind(t *testing.T, app model.App, bindings ...model.SecretBinding) {
	t.Helper()
	bindIn(t, fx.db, app, bindings...)
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

func (fx *env) envSecret(app model.App) *corev1.Secret {
	sec := &corev1.Secret{}
	if !fx.exists(sec, AppSecretsName(app.ObjectName())) {
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
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-sec1")
	fx.putSecret(t, "alice", "SEC1_KEY", testValue)
	app := fx.newApp(t, "ws-sec1", "alice", "lab-sec1")
	fx.bind(t, app, model.SecretBinding{Env: "OPENAI_API_KEY", Secret: "SEC1_KEY"})

	var pod *corev1.Pod
	eventually(t, "a pod with the secret", func() bool {
		pod = fx.podOf(app)
		return pod != nil && secretEnv(pod, "OPENAI_API_KEY") != nil
	})
	e := secretEnv(pod, "OPENAI_API_KEY")
	if e.Value != "" || e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil ||
		e.ValueFrom.SecretKeyRef.Name != AppSecretsName(app.ObjectName()) || e.ValueFrom.SecretKeyRef.Key != "OPENAI_API_KEY" {
		t.Fatalf("env %+v", e)
	}
	sec := fx.envSecret(app)
	if sec == nil || string(sec.Data["OPENAI_API_KEY"]) != testValue || sec.Labels[model.LabelApp] != app.ID {
		t.Fatalf("Secret %+v", sec)
	}
	// The app's Service owns it, so it goes with the Service even when the
	// row is removed while the controller is down (the sweep deletes the Service).
	svc := &corev1.Service{}
	if !fx.exists(svc, app.ObjectName()) || len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != svc.UID || sec.OwnerReferences[0].Kind != "Service" {
		t.Fatalf("owner references %+v", sec.OwnerReferences)
	}
	// The value is never in the pod spec or the app's status.
	raw, _ := json.Marshal(pod)
	if strings.Contains(string(raw), testValue) {
		t.Fatal("the value is in the pod spec")
	}
	st, _ := json.Marshal(fx.getApp(t, app.ID).Status)
	if strings.Contains(string(st), testValue) {
		t.Fatal("the value is in the app status")
	}
}

func TestANewValueOrBindingReplacesThePodOnce(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-sec2")
	fx.putSecret(t, "alice", "SEC2_KEY", "first")
	app := fx.newApp(t, "ws-sec2", "alice", "lab-sec2")
	fx.bind(t, app, model.SecretBinding{Env: "KEY", Secret: "SEC2_KEY"})
	var old *corev1.Pod
	eventually(t, "first pod", func() bool {
		old = fx.podOf(app)
		return old != nil && secretEnv(old, "KEY") != nil
	})

	fx.putSecret(t, "alice", "SEC2_KEY", "second")
	var fresh *corev1.Pod
	eventually(t, "a new pod for the new value", func() bool {
		fresh = fx.podOf(app)
		sec := fx.envSecret(app)
		return fresh != nil && fresh.UID != old.UID && sec != nil && string(sec.Data["KEY"]) == "second"
	})
	for range 3 {
		reconcileNow(t, fx.rec, app.ID)
	}
	if cur := fx.podOf(app); cur == nil || cur.UID != fresh.UID {
		t.Fatal("the pod was replaced again without a change")
	}

	// Taking the secret off the app removes the variable and the Secret.
	fx.bind(t, app)
	eventually(t, "a pod without the secret, and no Secret", func() bool {
		p := fx.podOf(app)
		return p != nil && p.UID != fresh.UID && secretEnv(p, "KEY") == nil && fx.envSecret(app) == nil
	})
}

// A preview runs a pull request the owner has not reviewed: even a binding
// written straight into the database gives it nothing.
func TestPreviewGetsNoSecrets(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-sec3")
	fx.putSecret(t, "alice", "SEC3_KEY", testValue)
	src := func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}
	}
	parent := fx.newApp(t, "ws-sec3", "alice", "lab-sec3", src)
	child := fx.newApp(t, "ws-sec3-pr-1", "alice", "lab-sec3", src, func(a *model.App) { a.ParentID, a.PullNumber = parent.ID, 1 })
	fx.bind(t, child, model.SecretBinding{Env: "KEY", Secret: "SEC3_KEY"})
	var pod *corev1.Pod
	eventually(t, "the preview's pod", func() bool { pod = fx.podOf(child); return pod != nil })
	reconcileNow(t, fx.rec, child.ID)
	if secretEnv(fx.podOf(child), "KEY") != nil || fx.envSecret(child) != nil {
		t.Fatal("a preview got a secret")
	}
}

func TestDeletingTheAppRemovesItsSecret(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-sec4")
	fx.putSecret(t, "alice", "SEC4_KEY", testValue)
	app := fx.newApp(t, "ws-sec4", "alice", "lab-sec4")
	fx.bind(t, app, model.SecretBinding{Env: "KEY", Secret: "SEC4_KEY"})
	eventually(t, "the Secret", func() bool { return fx.envSecret(app) != nil })
	if err := fx.db.Apps().MarkDeleted(bg, actor, app.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Secret is gone", func() bool { return fx.envSecret(app) == nil })
}

// A controller whose KEK does not open the keyset starts no pod for an app
// with secrets and says why; it never builds one without them.
func TestUnreadableSecretsFailTheApp(t *testing.T) {
	t.Parallel()
	alloc := newNamespace(t, "javo-alloc", nil)
	// A database the test manager does not watch: only r reconciles it.
	s := storetest.New(t)
	if _, err := newTestKeys(s, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	putTemplateIn(t, s, "lab-sec5")
	putSecretIn(t, s, "alice", "SEC5_KEY", testValue)
	app := newAppIn(t, s, alloc, "ws-sec5", "alice", "lab-sec5")
	bindIn(t, s, app, model.SecretBinding{Env: "KEY", Secret: "SEC5_KEY"})
	other, err := newKEK(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := newRec(s, func(r *Reconciler) { r.SecretKeys, r.Secrets = &secrets.Keys{Store: s, KEK: other}, k8s })
	for range 2 {
		reconcileNow(t, r, app.ID)
	}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: alloc, Name: app.ObjectName()}, &corev1.Pod{}); !apierrors.IsNotFound(err) {
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
	t.Parallel()
	fx := newEnv(t)
	ws := &model.App{Namespace: fx.ns, Name: "all", Owner: "alice", Template: "t", Command: "run {{.Home}}", File: "app.py",
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
