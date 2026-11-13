package workspace

import (
	"context"
	"errors"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/model"
)

// A token in the app's Secret is used only when its record is this app's:
// another app's token (or a job's) written there is never adopted, and the
// app gets a token of its own.
func TestAppNeverAdoptsATokenMadeForSomethingElse(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-adopt")
	alice := fx.signedIn(t, "alice-adopt")
	app := fx.newApp(t, "ws-agent-adopt", alice, "agent-tpl-adopt")
	other := fx.newApp(t, "ws-agent-adopt-other", alice, "agent-tpl-adopt")
	sec := fx.appTokenSecret(t, app.ObjectName())
	fx.stop()
	ctx := context.Background()
	text, jobTok, err := fx.records().Create(ctx, model.Actor{System: model.SystemController}, token.Token{
		Name: other.Name, Owner: alice, Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: fx.ns, Name: other.ObjectName(), Short: other.Name},
	})
	if err != nil {
		t.Fatal(err)
	}
	sec.Annotations[model.AnnotationTokenID] = jobTok.ID
	sec.Data[APITokenKey] = []byte(text)
	if err := k8s.Update(ctx, sec); err != nil {
		t.Fatal(err)
	}
	reconcileNow(t, newRec(fx.db, func(r *Reconciler) { r.Tokens = fx.records() }), app.ID)
	got := &corev1.Secret{}
	if !fx.exists(got, APITokenSecretName(app.ObjectName())) {
		t.Fatal("no token Secret")
	}
	if got.Annotations[model.AnnotationTokenID] == jobTok.ID {
		t.Fatal("the app adopted another app's token")
	}
	tok, err := fx.records().Authenticate(ctx, string(got.Data[APITokenKey]))
	if err != nil || tok.App == nil || tok.App.Name != app.ObjectName() || tok.Job != "" {
		t.Fatalf("token %+v, %v", tok, err)
	}
}

// A Secret at one of the app's names that carries another app's id is not
// the app's: the reconcile refuses it rather than read or overwrite it.
func TestAppRefusesASecretLabelledForAnotherApp(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-foreign")
	alice := fx.signedIn(t, "alice-foreign")
	app := fx.newApp(t, "ws-agent-foreign", alice, "agent-tpl-foreign")
	mine := fx.appTokenSecret(t, app.ObjectName())
	fx.stop()
	ctx := context.Background()
	if err := k8s.Delete(ctx, mine); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the Secret gone", func() bool { return !fx.exists(&corev1.Secret{}, mine.Name) })
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fx.ns, Name: APITokenSecretName(app.ObjectName()),
		Labels: map[string]string{model.LabelApp: "someone-else"}}, Data: map[string][]byte{"keep": []byte("x")}}
	if err := k8s.Create(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	r := newRec(fx.db, func(r *Reconciler) { r.Tokens = fx.records() })
	if _, err := r.Reconcile(ctx, app.ID); err == nil {
		t.Fatal("the reconcile used another app's Secret")
	}
	got := &corev1.Secret{}
	if !fx.exists(got, foreign.Name) || string(got.Data["keep"]) != "x" || got.Labels[model.LabelApp] != "someone-else" {
		t.Fatalf("another app's Secret was changed: %+v", got)
	}
}

// oldSecretNames are the names an app's Secrets had before SecretName,
// "<objectName>-javo-<use>", in the order of secretUses.
func oldSecretNames(objectName string) []string {
	var out []string
	for _, use := range secretUses {
		out = append(out, objectName+"-javo-"+use)
	}
	return out
}

// legacyPod is the app's pod as an earlier controller built it: the same, but
// reading its Secrets under their old names.
func legacyPod(pod *corev1.Pod, objectName string) *corev1.Pod {
	old := pod.DeepCopy()
	old.ObjectMeta = metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, Labels: pod.Labels, Annotations: pod.Annotations}
	old.Status = corev1.PodStatus{}
	rename := func(n string) string {
		for i, use := range secretUses {
			if n == SecretName(objectName, use) {
				return oldSecretNames(objectName)[i]
			}
		}
		return n
	}
	for i := range old.Spec.Volumes {
		if s := old.Spec.Volumes[i].Secret; s != nil {
			s.SecretName = rename(s.SecretName)
		}
	}
	for _, cs := range [][]corev1.Container{old.Spec.InitContainers, old.Spec.Containers} {
		for i := range cs {
			for j := range cs[i].Env {
				if v := cs[i].Env[j].ValueFrom; v != nil && v.SecretKeyRef != nil {
					v.SecretKeyRef.Name = rename(v.SecretKeyRef.Name)
				}
			}
		}
	}
	return old
}

func podSecretNames(pod *corev1.Pod) []string {
	var out []string
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil {
			out = append(out, v.Secret.SecretName)
		}
	}
	for _, c := range pod.Spec.Containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
				out = append(out, e.ValueFrom.SecretKeyRef.Name)
			}
		}
	}
	return out
}

// After an upgrade an app's pod still reads the Secrets under their old
// names. The pod is built again once with the new names, the old Secrets the
// app owns go, and the token the old one held is revoked; an old name that
// another app or a job owns is left alone.
func TestUpgradeMovesAnAppToItsNewSecretNames(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-upgrade")
	alice := fx.signedIn(t, "alice-upgrade")
	app := fx.newApp(t, "ws-agent-upgrade", alice, "agent-tpl-upgrade")
	obj := app.ObjectName()
	fx.appTokenSecret(t, obj)
	pod := fx.appPod(t, obj, "pod", hasTokenEnv)
	fx.stop()
	ctx := context.Background()

	legacy := oldSecretNames(obj)
	text, oldTok, err := fx.records().Create(ctx, model.Actor{System: model.SystemController}, token.Token{
		Name: app.Name, Owner: alice, Scope: token.ScopeReadWrite, App: &token.App{Namespace: fx.ns, Name: obj, Short: app.Name},
	})
	if err != nil {
		t.Fatal(err)
	}
	mine := map[string]string{model.LabelApp: app.ID, model.LabelOwner: alice}
	for _, s := range []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Namespace: fx.ns, Name: legacy[0], Labels: mine, Annotations: map[string]string{
			model.AnnotationTokenID: oldTok.ID, model.AnnotationTokenScope: string(token.ScopeReadWrite)}},
			Data: map[string][]byte{APITokenKey: []byte(text), APIGuideKey: apiGuide}},
		// A job of the same owner made this one under the old names.
		{ObjectMeta: metav1.ObjectMeta{Namespace: fx.ns, Name: legacy[1], Labels: map[string]string{model.LabelJob: "job-1"}}},
	} {
		if err := k8s.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := k8s.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the pod gone", func() bool { return !fx.exists(&corev1.Pod{}, obj) })
	old := legacyPod(pod, obj)
	if err := k8s.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(podSecretNames(old), legacy[0]) {
		t.Fatalf("the old pod reads %v", podSecretNames(old))
	}

	r := newRec(fx.db, func(r *Reconciler) { r.Tokens, r.SecretKeys = fx.records(), fx.keys })
	var fresh *corev1.Pod
	eventually(t, "a pod with the new names", func() bool {
		reconcileNow(t, r, app.ID)
		p := &corev1.Pod{}
		if !fx.exists(p, obj) || p.UID == old.UID {
			return false
		}
		fresh = p
		return true
	})
	for _, n := range podSecretNames(fresh) {
		if slices.Contains(legacy, n) {
			t.Fatalf("the new pod still reads %s", n)
		}
	}
	if !slices.Contains(podSecretNames(fresh), APITokenSecretName(obj)) {
		t.Fatalf("the new pod reads %v", podSecretNames(fresh))
	}
	if fx.exists(&corev1.Secret{}, legacy[0]) {
		t.Fatal("the app's old token Secret is left")
	}
	if _, err := fx.records().Authenticate(ctx, text); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("the old token still works: %v", err)
	}
	if !fx.exists(&corev1.Secret{}, legacy[1]) {
		t.Fatal("a job's Secret under an old name was deleted")
	}
}
