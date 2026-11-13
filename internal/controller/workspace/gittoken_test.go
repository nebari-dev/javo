package workspace

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const pastedToken = "ghp_testtoken123"

// newPrivateApp stores a source app with a pasted token, as the gateway does.
func (fx *env) newPrivateApp(t *testing.T, name, owner, tpl string, tune ...func(*model.App)) model.App {
	t.Helper()
	a := &model.App{
		Namespace: fx.ns, Name: name, Owner: owner, Template: tpl, File: "app.py",
		Source: &model.Source{URL: "https://github.com/example-org/private-site", Ref: "main"},
	}
	for _, f := range tune {
		f(a)
	}
	app, err := fx.db.Apps().CreateFromRepo(bg, model.Actor{Person: owner}, a, store.RepoSecrets{Token: pastedToken})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (fx *env) gitSecret(app model.App) *corev1.Secret {
	sec := &corev1.Secret{}
	if !fx.exists(sec, GitTokenSecretName(app.ObjectName())) {
		return nil
	}
	return sec
}

func (fx *env) podOf(app model.App) *corev1.Pod {
	pod := &corev1.Pod{}
	if !fx.exists(pod, app.ObjectName()) {
		return nil
	}
	return pod
}

func (fx *env) tokenRow(t *testing.T, id string) (token string, gen int64, has bool) {
	t.Helper()
	token, gen, has, err := fx.db.GitTokens().Pending(bg, id)
	if err != nil {
		t.Fatal(err)
	}
	return token, gen, has
}

// newSourceApp stores an app from a repository whose token is the owner's
// secret called secret.
func (fx *env) newSourceApp(t *testing.T, name, owner, tpl, secret string, tune ...func(*model.App)) model.App {
	t.Helper()
	app := fx.newApp(t, name, owner, tpl, append([]func(*model.App){func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/private-site", Ref: "main"}
	}}, tune...)...)
	if err := fx.db.Secrets().SetGitToken(bg, model.Actor{Person: owner}, app, secret); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestTokenFromASecretReachesTheCloneAsAFile(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-gt1")
	fx.putSecret(t, "alice", "GT1_TOKEN", pastedToken)
	app := fx.newSourceApp(t, "ws-gt1", "alice", "lab-gt1", "GT1_TOKEN")
	var pod *corev1.Pod
	eventually(t, "a pod that clones with the token", func() bool {
		pod = fx.podOf(app)
		return pod != nil && strings.HasPrefix(pod.Annotations[model.AnnotationGitToken], "secret-")
	})
	sec := fx.gitSecret(app)
	if sec == nil || string(sec.Data["token"]) != pastedToken || sec.Labels[model.LabelApp] != app.ID ||
		sec.Annotations[model.AnnotationGitToken] != pod.Annotations[model.AnnotationGitToken] {
		t.Fatalf("secret = %+v", sec)
	}
	if !slices.ContainsFunc(pod.Spec.InitContainers, func(c corev1.Container) bool {
		return slices.ContainsFunc(c.Args, func(a string) bool { return strings.HasPrefix(a, "--password-file=") })
	}) {
		t.Fatalf("the clone reads no token file: %v", initNames(pod))
	}
	raw, _ := json.Marshal(pod)
	st, _ := json.Marshal(fx.getApp(t, app.ID).Status)
	if strings.Contains(string(raw), pastedToken) || strings.Contains(string(st), pastedToken) {
		t.Fatal("the token is in the pod spec or the app status")
	}
}

func TestNewTokenValueReplacesThePodOnce(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-gt2")
	fx.putSecret(t, "alice", "GT2_TOKEN", pastedToken)
	app := fx.newSourceApp(t, "ws-gt2", "alice", "lab-gt2", "GT2_TOKEN")
	var old *corev1.Pod
	eventually(t, "first pod with token", func() bool { old = fx.podOf(app); return old != nil && fx.gitSecret(app) != nil })

	fx.putSecret(t, "alice", "GT2_TOKEN", "ghp_second")
	var fresh *corev1.Pod
	eventually(t, "a new pod for the new value", func() bool {
		fresh = fx.podOf(app)
		sec := fx.gitSecret(app)
		return fresh != nil && fresh.UID != old.UID && sec != nil && string(sec.Data["token"]) == "ghp_second"
	})
	for range 3 {
		reconcileNow(t, fx.rec, app.ID)
	}
	if cur := fx.podOf(app); cur == nil || cur.UID != fresh.UID {
		t.Fatal("the pod was replaced again without a new value")
	}
}

func TestRemovingTheTokenDeletesTheSecretAndReplacesThePod(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-gt3")
	fx.putSecret(t, "alice", "GT3_TOKEN", pastedToken)
	app := fx.newSourceApp(t, "ws-gt3", "alice", "lab-gt3", "GT3_TOKEN")
	var old *corev1.Pod
	eventually(t, "pod with token", func() bool {
		old = fx.podOf(app)
		return old != nil && old.Annotations[model.AnnotationGitToken] != "" && fx.gitSecret(app) != nil
	})
	if err := fx.db.Secrets().SetGitToken(bg, model.Actor{Person: "alice"}, app, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "secret gone and pod rebuilt without the token", func() bool {
		p := fx.podOf(app)
		if p == nil || p.UID == old.UID {
			return false
		}
		_, annotated := p.Annotations[model.AnnotationGitToken]
		return fx.gitSecret(app) == nil && !annotated
	})
}

// A token pasted before tokens came from secrets keeps working: it moves into a secret of
// the owner, and the app's clone then reads it from there.
func TestPastedTokenMovesIntoASecret(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-gt6")
	app := fx.newPrivateApp(t, "ws-gt6", "alice", "lab-gt6", func(a *model.App) { a.Paused = true })
	eventually(t, "the token adopted", func() bool {
		name, err := fx.db.Secrets().GitTokenSecret(bg, app.ID)
		return err == nil && name == "GIT_TOKEN_WS_GT6"
	})
	if _, _, has := fx.tokenRow(t, app.ID); has {
		t.Fatal("the pasted copy is still the app's token")
	}
	if tok, _, _ := fx.tokenRow(t, app.ID); tok != "" {
		t.Fatal("the row still holds the token")
	}
	sec, err := fx.db.Secrets().Get(bg, "alice", "GIT_TOKEN_WS_GT6")
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := fx.db.Secrets().Sealed(bg, sec.ID)
	opener, err := fx.keys.Opener(bg)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := opener.Open(sealed, secrets.AD{ID: sec.ID, Owner: sec.Owner, Name: sec.Name}); err != nil || string(v) != pastedToken {
		t.Fatalf("the adopted secret opens to %q (%v)", v, err)
	}
	eventually(t, "the Secret from the adopted secret", func() bool {
		s := fx.gitSecret(app)
		return s != nil && string(s.Data["token"]) == pastedToken && strings.HasPrefix(s.Annotations[model.AnnotationGitToken], "secret-")
	})
}

// A controller without the secrets key still hands a pasted token to the
// clone, as before tokens came from secrets.
func TestPastedTokenWorksWithoutTheSecretsKey(t *testing.T) {
	t.Parallel()
	alloc := newNamespace(t, "javo-alloc", nil)
	s := storetest.New(t)
	putTemplateIn(t, s, "lab-gt7")
	a := &model.App{Namespace: alloc, Name: "ws-gt7", Owner: "alice", Template: "lab-gt7", File: "app.py",
		Source: &model.Source{URL: "https://github.com/example-org/private-site", Ref: "main"}}
	app, err := s.Apps().CreateFromRepo(bg, model.Actor{Person: "alice"}, a, store.RepoSecrets{Token: pastedToken})
	if err != nil {
		t.Fatal(err)
	}
	r := newRec(s)
	reconcileNow(t, r, app.ID)
	sec := &corev1.Secret{}
	if err := k8s.Get(bg, client.ObjectKey{Namespace: alloc, Name: GitTokenSecretName(app.ObjectName())}, sec); err != nil {
		t.Fatal(err)
	}
	if string(sec.Data["token"]) != pastedToken || sec.Annotations[model.AnnotationGitToken] != "1" {
		t.Fatalf("secret %+v", sec.ObjectMeta)
	}
	if tok, _, has, _ := s.GitTokens().Pending(bg, app.ID); !has || tok != "" {
		t.Fatalf("the row: has %v, text left %v", has, tok != "")
	}
}

func TestDeletingTheAppDeletesTheTokenSecret(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-gt4")
	app := fx.newPrivateApp(t, "ws-gt4", "alice", "lab-gt4")
	eventually(t, "secret", func() bool { return fx.gitSecret(app) != nil })

	if err := fx.db.Apps().MarkDeleted(bg, model.Actor{Person: "alice"}, app.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "row purged", func() bool {
		_, err := fx.db.Apps().Get(bg, app.ID)
		return errors.Is(err, store.ErrNotFound)
	})
	eventually(t, "secret gone", func() bool { return fx.gitSecret(app) == nil })
}

func TestAppWithoutATokenMakesNoSecretAndAPodWithoutOne(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-gt5")
	app := fx.newApp(t, "ws-gt5", "alice", "lab-gt5", func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/site"}
	})
	var pod *corev1.Pod
	eventually(t, "pod", func() bool { pod = fx.podOf(app); return pod != nil })
	if _, ok := pod.Annotations[model.AnnotationGitToken]; ok || fx.gitSecret(app) != nil {
		t.Fatalf("annotations %v, secret %v", pod.Annotations, fx.gitSecret(app))
	}
	for _, c := range pod.Spec.InitContainers {
		for _, a := range c.Args {
			if strings.HasPrefix(a, "--password-file") {
				t.Fatalf("clone reads a token file: %v", c.Args)
			}
		}
	}
}

func TestSourceCommitReachesTheStatus(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-commit")
	app := fx.newApp(t, "ws-commit", "alice", "lab-commit", func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/site"}
	})
	var pod *corev1.Pod
	eventually(t, "pod", func() bool { pod = fx.podOf(app); return pod != nil })
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		{Name: "source-check", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: "commit=" + exampleSHA}}},
	}
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
	if err := k8s.Status().Update(bg, pod); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the commit in status", func() bool {
		reconcileNow(t, newRec(fx.db), app.ID)
		return fx.getApp(t, app.ID).Status.Commit == exampleSHA
	})
}

// A replaced pod has not cloned yet, so the old pod's commit must not stay in
// the status next to the new run (or its failure).
func TestRestartClearsTheDeployedCommit(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-commit-clear")
	app := fx.newApp(t, "ws-commit-clear", "alice", "lab-commit-clear", func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/site"}
	})
	var pod *corev1.Pod
	eventually(t, "pod", func() bool { pod = fx.podOf(app); return pod != nil })
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{
		{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
		{Name: "source-check", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Message: "commit=" + exampleSHA}}},
	}
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.Now()}}
	if err := k8s.Status().Update(bg, pod); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the commit in status", func() bool {
		reconcileNow(t, newRec(fx.db), app.ID)
		return fx.getApp(t, app.ID).Status.Commit == exampleSHA
	})

	editApp(t, fx.db, app.ID, func(a *model.App) { a.RestartRequestedAt = time.Now() })
	eventually(t, "the commit cleared", func() bool {
		reconcileNow(t, newRec(fx.db), app.ID)
		return fx.getApp(t, app.ID).Status.Commit == ""
	})
}

// Redeploy is the restart request every app carries: a new pod clones again.
func TestRestartRequestReplacesASourceAppsPodOnce(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-redeploy")
	app := fx.newApp(t, "ws-redeploy", "alice", "lab-redeploy", func(a *model.App) {
		a.File = "app.py"
		a.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}
	})
	var old *corev1.Pod
	eventually(t, "pod", func() bool { old = fx.podOf(app); return old != nil })

	editApp(t, fx.db, app.ID, func(a *model.App) { a.RestartRequestedAt = time.Now() })
	var fresh *corev1.Pod
	eventually(t, "a new pod", func() bool { fresh = fx.podOf(app); return fresh != nil && fresh.UID != old.UID })
	if len(fresh.Spec.InitContainers) < 2 || fresh.Annotations[model.AnnotationRestart] == "" {
		t.Fatalf("the new pod does not clone again: %v %v", initNames(fresh), fresh.Annotations)
	}
	eventually(t, "the cache sees the new pod", func() bool {
		p := &corev1.Pod{}
		return fx.cache.Get(bg, fx.key(app.ObjectName()), p) == nil && p.UID == fresh.UID
	})
	for range 3 {
		reconcileNow(t, fx.rec, app.ID)
	}
	time.Sleep(300 * time.Millisecond)
	if cur := fx.podOf(app); cur == nil || cur.UID != fresh.UID {
		t.Fatal("the pod was replaced again without a new request")
	}
}
