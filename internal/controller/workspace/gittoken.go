package workspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/store"
)

// syncGitToken keeps the app's repository token Secret in step with the
// database and returns the Secret's name and the generation of the token the
// pod must have been built for ("" when the app's clone gets no token).
// unreadable is why a token from a secret could not be opened (never a
// value): the app then does not start.
//
// The token comes from one of the owner's secrets: the
// controller opens its value into the Secret, whose generation names the
// secret and its value version, so a new value replaces the pod once.
//
// A token pasted before tokens came from secrets (app_git_tokens) still works: the
// controller copies a pasted text into the Secret and clears it from the row
// (so no logical backup holds it), and, given the secrets key, adopts it
// into a secret of the owner (AdoptGitToken), after which it comes from that
// secret. An app whose token was removed loses the Secret (pod is the app's
// Pod or nil: only an app that had one, or whose pod is not built yet, can
// have one left, so others make no call).
//
// A preview uses its parent's Secret in the same namespace and never writes
// it; a fork preview uses none.
func (r *Reconciler) syncGitToken(ctx context.Context, app *model.App, pod *corev1.Pod) (secretName, generation, unreadable string, err error) {
	if app.Source == nil || app.Source.Fork {
		return "", "", "", nil
	}
	holder := app
	if app.ParentID != "" {
		parent, err := r.Store.Apps().Get(ctx, app.ParentID)
		if errors.Is(err, store.ErrNotFound) {
			return "", "", "", nil
		}
		if err != nil {
			return "", "", "", err
		}
		holder = &parent
	}
	secretName = GitTokenSecretName(holder.ObjectName())
	bound, err := r.gitTokenOf(ctx, holder)
	if err != nil {
		return "", "", "", err
	}
	if bound.Fingerprint != "" {
		gen := "secret-" + bound.Fingerprint
		if app.ParentID == "" {
			if unreadable, err = r.writeBoundGitSecret(ctx, app, secretName, gen, bound); err != nil || unreadable != "" {
				return "", "", unreadable, err
			}
		}
		return secretName, gen, "", nil
	}
	pasted, gen, has, err := r.Store.GitTokens().Pending(ctx, holder.ID)
	if err != nil {
		return "", "", "", err
	}
	switch {
	case !has:
		if app.ParentID == "" && (pod == nil || pod.Annotations[model.AnnotationGitToken] != "") {
			sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: app.Namespace, Name: secretName}}
			if err := r.Delete(ctx, sec); client.IgnoreNotFound(err) != nil {
				return "", "", "", err
			}
		}
		return "", "", "", nil
	case pasted != "" && app.ParentID == "":
		if err := r.writeGitSecret(ctx, app, secretName, pasted, strconv.FormatInt(gen, 10)); err != nil {
			return "", "", "", err
		}
		if err := r.Store.GitTokens().HandedOff(ctx, app.ID, gen); err != nil {
			return "", "", "", fmt.Errorf("record the token hand-off: %w", err)
		}
	}
	if app.ParentID == "" && r.SecretKeys != nil {
		if err := r.adoptGitToken(ctx, app); err != nil {
			// The pasted token keeps working; adopting is tried again.
			log.FromContext(ctx).Error(err, "adopt the pasted repository token into a secret")
		}
	}
	return secretName, strconv.FormatInt(gen, 10), "", nil
}

// gitTokenOf is the app's token from a secret, if authz lets the controller
// hand it to the app's clone (the app and the secret share an owner).
func (r *Reconciler) gitTokenOf(ctx context.Context, app *model.App) (secretenv.Set, error) {
	all, err := r.Store.Secrets().ForApp(ctx, app.ID)
	if err != nil {
		return secretenv.Set{}, err
	}
	return secretenv.GitToken(ctx, all, authz.Object{App: app}), nil
}

// writeBoundGitSecret makes the app's token Secret hold the value of the
// secret in bound, opened with the KEK, unless it already holds generation
// gen. unreadable is why the value could not be opened.
func (r *Reconciler) writeBoundGitSecret(ctx context.Context, app *model.App, name, gen string, bound secretenv.Set) (unreadable string, err error) {
	if r.Secrets == nil {
		return secretenv.ErrNoKey.Error(), nil
	}
	sec := &corev1.Secret{}
	err = r.Secrets.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: name}, sec)
	if client.IgnoreNotFound(err) != nil {
		return "", err
	}
	if err == nil && sec.Annotations[model.AnnotationGitToken] == gen {
		return "", nil
	}
	data, unreadable := bound.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		return unreadable, nil
	}
	return "", r.writeGitSecret(ctx, app, name, string(data[model.GitTokenEnv]), gen)
}

// adoptGitToken moves the app's pasted token, read from its Secret, into a
// secret of the owner sealed with the public half of the keyset. Nothing is
// done while the Secret holds no token yet.
func (r *Reconciler) adoptGitToken(ctx context.Context, app *model.App) error {
	token, err := ReadGitToken(ctx, r.Secrets, app)
	if err != nil || token == "" {
		return err
	}
	ks, found, err := r.Store.SecretKeys().Get(ctx)
	if err != nil || !found {
		return err
	}
	sealer, err := secrets.NewSealer(ks.Public)
	if err != nil {
		return err
	}
	_, err = r.Store.Secrets().AdoptGitToken(ctx, model.Actor{System: model.SystemController}, *app, func(id, name string) ([]byte, error) {
		return sealer.Seal([]byte(token), secrets.AD{ID: id, Owner: app.Owner, Name: name})
	})
	return err
}

// writeGitSecret creates or replaces the Secret holding the token of
// generation gen. Errors never include the token.
func (r *Reconciler) writeGitSecret(ctx context.Context, app *model.App, name, token, gen string) error {
	if r.Secrets == nil {
		return errors.New("the app has a repository token, but the controller has no Secret reader")
	}
	sec := &corev1.Secret{}
	err := r.Secrets.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: name}, sec)
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return err
	}
	if create {
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: app.Namespace}}
	}
	sec.Labels = labels(app)
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, model.AnnotationGitToken, gen)
	sec.Data = map[string][]byte{gitTokenKey: []byte(token)}
	if create {
		err = r.Create(ctx, sec)
	} else {
		err = r.Update(ctx, sec)
	}
	if err != nil {
		return fmt.Errorf("store the app's repository token: %w", err)
	}
	return nil
}

// commitOf is the commit the pod's clone checked out, once source-check has
// finished, or "".
func commitOf(pod *corev1.Pod) string {
	if pod == nil {
		return ""
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		if t := cs.State.Terminated; cs.Name == source.CheckContainer && t != nil && t.ExitCode == 0 {
			c, _ := source.CommitOf(t.Message)
			return c
		}
	}
	return ""
}

// ReadGitToken returns the repository token the app's clone uses, read live
// from its Secret, or "" when the app has none. Callers must never log it.
func ReadGitToken(ctx context.Context, secrets client.Reader, app *model.App) (string, error) {
	sec := &corev1.Secret{}
	key := client.ObjectKey{Namespace: app.Namespace, Name: GitTokenSecretName(app.ObjectName())}
	if err := secrets.Get(ctx, key, sec); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	return string(sec.Data[gitTokenKey]), nil
}
