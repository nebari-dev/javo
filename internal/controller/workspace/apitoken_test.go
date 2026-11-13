package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

func (fx *env) records() *token.DBStore { return &token.DBStore{Tokens: fx.db.Tokens()} }

// signedIn makes a person, as signing in does, and returns their name: an
// API token belongs to a person who exists.
func (fx *env) signedIn(t *testing.T, username string) string {
	t.Helper()
	p, err := fx.db.People(store.IDRange{First: 100000, Last: 199999}).Ensure(bg, store.Claims{
		Issuer: "https://idp.example.com", Subject: username, Username: username,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p.Name
}

func (fx *env) newAPITokenTemplate(t *testing.T, name string) {
	t.Helper()
	fx.newTemplate(t, name, func(s *model.TemplateSpec) {
		s.Kind, s.Image, s.Port = "claude-code", "example.com/agents:1", 7681
		s.Command, s.Args = []string{"ttyd"}, nil
		s.APIToken, s.APIGuidePaths = true, []string{"/etc/claude-code/CLAUDE.md"}
	})
}

// appTokenSecret returns the API token Secret of the app whose Pod is called
// objectName, once it holds a token.
func (fx *env) appTokenSecret(t *testing.T, objectName string) *corev1.Secret {
	t.Helper()
	sec := &corev1.Secret{}
	eventually(t, "the app's API token Secret", func() bool {
		return fx.exists(sec, APITokenSecretName(objectName)) && len(sec.Data[APITokenKey]) > 0
	})
	return sec
}

func TestAPITokenTemplateGivesTheAppATokenOfItsOwner(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl")
	alice := fx.signedIn(t, "alice-agent")
	app := fx.newApp(t, "ws-agent", alice, "agent-tpl")
	sec := fx.appTokenSecret(t, app.ObjectName())
	if sec.Labels[model.LabelApp] != app.ID {
		t.Fatalf("token Secret labels %v lack the app id, so it would outlive the app", sec.Labels)
	}
	tok, err := fx.records().Authenticate(context.Background(), string(sec.Data[APITokenKey]))
	if err != nil {
		t.Fatalf("the app's token does not authenticate: %v", err)
	}
	want := token.App{Namespace: fx.ns, Name: app.ObjectName(), Short: "ws-agent"}
	if tok.Owner != alice || tok.Scope != token.ScopeReadWrite || tok.App == nil || *tok.App != want || tok.Name != "ws-agent" {
		t.Fatalf("token %+v", tok)
	}
	if sec.Annotations[model.AnnotationTokenID] != tok.ID {
		t.Fatalf("annotations %v, token %s", sec.Annotations, tok.ID)
	}
	if !strings.Contains(string(sec.Data[APIGuideKey]), "JAVO_TOKEN") {
		t.Fatalf("guide %q", sec.Data[APIGuideKey])
	}
	pod := &corev1.Pod{}
	eventually(t, "pod", func() bool { return fx.exists(pod, app.ObjectName()) })
	var ref *corev1.SecretKeySelector
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == "JAVO_TOKEN" && e.ValueFrom != nil {
			ref = e.ValueFrom.SecretKeyRef
		}
	}
	if ref == nil || ref.Name != sec.Name {
		t.Fatalf("pod env %+v", pod.Spec.Containers[0].Env)
	}
	// Reconciling again keeps the one token.
	reconcileNow(t, fx.rec, app.ID)
	if n := fx.appTokenRecords(t, alice, "ws-agent"); n != 1 {
		t.Fatalf("%d token records for one app", n)
	}
}

func TestRevokedAppTokenIsReplaced(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-revoke")
	alice := fx.signedIn(t, "alice-revoke")
	app := fx.newApp(t, "ws-agent-revoke", alice, "agent-tpl-revoke")
	ctx := context.Background()
	old := string(fx.appTokenSecret(t, app.ObjectName()).Data[APITokenKey])
	tok, err := fx.records().Authenticate(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	// What the gateway does when the owner revokes it in Settings.
	if _, err := fx.records().Revoke(ctx, model.Actor{Person: alice}, alice, tok.ID); err != nil {
		t.Fatal(err)
	}
	if err := fx.db.Apps().NoteTokenRevoked(ctx, model.Actor{Person: alice}, app.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	var fresh string
	eventually(t, "a new token", func() bool {
		sec := &corev1.Secret{}
		if !fx.exists(sec, APITokenSecretName(app.ObjectName())) {
			return false
		}
		fresh = string(sec.Data[APITokenKey])
		return fresh != old
	})
	if _, err := fx.records().Authenticate(ctx, fresh); err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := fx.records().Authenticate(ctx, old); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("old token: %v", err)
	}
}

func TestNoTokenForOtherTemplates(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-no-token")
	app := fx.newApp(t, "ws-no-token", "alice", "lab-no-token")
	eventually(t, "pod", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
	if fx.exists(&corev1.Secret{}, APITokenSecretName(app.ObjectName())) {
		t.Fatal("an app whose template has no apiToken got a token Secret")
	}
}

func (fx *env) appTokenRecords(t *testing.T, owner, shortName string) int {
	t.Helper()
	list, err := fx.records().List(bg, owner)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, tok := range list {
		if tok.App != nil && tok.App.Short == shortName {
			n++
		}
	}
	return n
}

// After an upgrade, or when the owner's tokens are deleted with `javo user
// remove`, the app's Secret names a token id that has no row: the pod's token
// gets 401 on every call and must be replaced.
func TestAppTokenWithoutARowIsReplaced(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-norow")
	alice := fx.signedIn(t, "alice-norow")
	app := fx.newApp(t, "ws-agent-norow", alice, "agent-tpl-norow")
	ctx := context.Background()
	old := string(fx.appTokenSecret(t, app.ObjectName()).Data[APITokenKey])
	tok, err := fx.records().Authenticate(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	// No controller runs from here on, so only the reconcile below acts.
	fx.stop()
	// The row goes without the gateway marking the app (DeleteFor, a restore).
	if n, err := fx.db.Tokens().DeleteFor(ctx, model.Actor{System: model.SystemCLI}, alice); err != nil || n != 1 {
		t.Fatalf("DeleteFor = %d, %v", n, err)
	}
	reconcileNow(t, newRec(fx.db, func(r *Reconciler) { r.Tokens = fx.records() }), app.ID)
	var fresh string
	eventually(t, "a new token", func() bool {
		sec := &corev1.Secret{}
		if !fx.exists(sec, APITokenSecretName(app.ObjectName())) {
			return false
		}
		fresh = string(sec.Data[APITokenKey])
		return fresh != old
	})
	got, err := fx.records().Authenticate(ctx, fresh)
	if err != nil || got.ID == tok.ID {
		t.Fatalf("new token: %+v, %v", got, err)
	}
}

// appPod returns the pod called objectName once it satisfies cond.
func (fx *env) appPod(t *testing.T, objectName, what string, cond func(*corev1.Pod) bool) *corev1.Pod {
	t.Helper()
	pod := &corev1.Pod{}
	eventually(t, what, func() bool { return fx.exists(pod, objectName) && cond(pod) })
	return pod
}

func hasTokenEnv(p *corev1.Pod) bool {
	for _, e := range p.Spec.Containers[0].Env {
		if e.Name == "JAVO_TOKEN" {
			return true
		}
	}
	return false
}

func (fx *env) authenticateSecret(t *testing.T, objectName string) token.Token {
	t.Helper()
	tok, err := fx.records().Authenticate(context.Background(), string(fx.appTokenSecret(t, objectName).Data[APITokenKey]))
	if err != nil {
		t.Fatalf("the app's token does not authenticate: %v", err)
	}
	return tok
}

func TestAnyAppCanAskForReadAccess(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-read-access")
	alice := fx.signedIn(t, "alice-read-access")
	app := fx.newApp(t, "ws-read-access", alice, "lab-read-access", func(a *model.App) { a.APIAccess = model.APIAccessRead })
	if tok := fx.authenticateSecret(t, app.ObjectName()); tok.Scope != token.ScopeRead {
		t.Fatalf("scope %q, want read", tok.Scope)
	}
	pod := fx.appPod(t, app.ObjectName(), "pod with JAVO_TOKEN", hasTokenEnv)
	var guide bool
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		guide = guide || m.MountPath == APIGuidePath
	}
	if !guide {
		t.Fatalf("no API guide mounted: %+v", pod.Spec.Containers[0].VolumeMounts)
	}
}

func TestChangingAPIAccessRotatesTheTokenAndRestarts(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-rotate")
	alice := fx.signedIn(t, "alice-rotate")
	app := fx.newApp(t, "ws-agent-rotate", alice, "agent-tpl-rotate")
	if tok := fx.authenticateSecret(t, app.ObjectName()); tok.Scope != token.ScopeReadWrite {
		t.Fatalf("default scope %q, want read-write", tok.Scope)
	}
	old := fx.appPod(t, app.ObjectName(), "pod", hasTokenEnv)
	editApp(t, fx.db, app.ID, func(a *model.App) { a.APIAccess = model.APIAccessRead })
	eventually(t, "a read token", func() bool {
		sec := &corev1.Secret{}
		if !fx.exists(sec, APITokenSecretName(app.ObjectName())) {
			return false
		}
		tok, err := fx.records().Authenticate(context.Background(), string(sec.Data[APITokenKey]))
		return err == nil && tok.Scope == token.ScopeRead
	})
	fx.appPod(t, app.ObjectName(), "a new pod", func(p *corev1.Pod) bool { return p.UID != old.UID && hasTokenEnv(p) })
}

func TestAPIAccessNoneRemovesTheTokenSecretAndRestarts(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-none")
	alice := fx.signedIn(t, "alice-none")
	app := fx.newApp(t, "ws-agent-none", alice, "agent-tpl-none")
	fx.appTokenSecret(t, app.ObjectName())
	old := fx.appPod(t, app.ObjectName(), "pod", hasTokenEnv)
	editApp(t, fx.db, app.ID, func(a *model.App) { a.APIAccess = model.APIAccessNone })
	eventually(t, "the token Secret removed", func() bool {
		return !fx.exists(&corev1.Secret{}, APITokenSecretName(app.ObjectName()))
	})
	pod := fx.appPod(t, app.ObjectName(), "a new pod", func(p *corev1.Pod) bool { return p.UID != old.UID })
	if hasTokenEnv(pod) {
		t.Fatal("the new pod still gets JAVO_TOKEN")
	}
}

func TestTemplateCapsAPIAccess(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "agent-tpl-capped", func(s *model.TemplateSpec) {
		s.Kind, s.Image, s.Port = "claude-code", "example.com/agents:1", 7681
		s.Command, s.Args = []string{"ttyd"}, nil
		s.APIToken, s.MaxAPIAccess = true, model.APIAccessRead
	})
	alice := fx.signedIn(t, "alice-capped")
	app := fx.newApp(t, "ws-agent-capped", alice, "agent-tpl-capped")
	if tok := fx.authenticateSecret(t, app.ObjectName()); tok.Scope != token.ScopeRead {
		t.Fatalf("scope %q, want read (the template's cap)", tok.Scope)
	}
}

// secretWriteFails fails every write of the Secrets named name, as a write
// that loses a race with another reconcile does.
type secretWriteFails struct {
	client.Client
	name string
}

func (c secretWriteFails) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if _, ok := o.(*corev1.Secret); ok && o.GetName() == c.name {
		return apierrors.NewConflict(corev1.Resource("secrets"), c.name, errors.New("modified"))
	}
	return c.Client.Create(ctx, o, opts...)
}

func (c secretWriteFails) Update(ctx context.Context, o client.Object, opts ...client.UpdateOption) error {
	if _, ok := o.(*corev1.Secret); ok && o.GetName() == c.name {
		return apierrors.NewConflict(corev1.Resource("secrets"), c.name, errors.New("modified"))
	}
	return c.Client.Update(ctx, o, opts...)
}

// TestAppTokenNotStoredIsRevoked: a token made for an app whose Secret could
// not be written is deleted again, so no working token is left that no
// Secret holds.
func TestAppTokenNotStoredIsRevoked(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newAPITokenTemplate(t, "agent-tpl-lost")
	alice := fx.signedIn(t, "alice-lost")
	app := fx.newApp(t, "ws-agent-lost", alice, "agent-tpl-lost")
	name := APITokenSecretName(app.ObjectName())
	fx.appTokenSecret(t, app.ObjectName())
	// No controller runs from here on, so only the reconcile below acts.
	fx.stop()
	failing := newRec(fx.db, func(r *Reconciler) {
		r.Secrets = kube.Secrets{Reader: k8s, Client: secretWriteFails{Client: k8s, name: name}}
		r.Tokens = fx.records()
	})
	// The row goes, so the next reconcile makes a new token.
	if _, err := fx.db.Tokens().DeleteFor(bg, model.Actor{System: model.SystemCLI}, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Reconcile(bg, reconcile.Request{NamespacedName: types.NamespacedName{Name: app.ID}}); err == nil {
		t.Fatal("the reconcile whose Secret write failed reported no error")
	}
	// Every token alice has is the one her app's Secret holds.
	eventually(t, "only the stored token", func() bool {
		sec := &corev1.Secret{}
		if !fx.exists(sec, name) {
			return false
		}
		toks, err := fx.records().List(bg, alice)
		if err != nil {
			t.Fatal(err)
		}
		for _, tk := range toks {
			if tk.ID != sec.Annotations[model.AnnotationTokenID] {
				return false
			}
		}
		return true
	})
}
