package workspace

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/kube"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/source"
	"github.com/nebari-dev/javo/internal/store"
)

// syncGitToken keeps the app's repository token Secret in step with the
// database and returns the name of the workload that holds it (the app's
// own, or its parent's for a preview) and the generation of the token the
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
func (r *Reconciler) syncGitToken(ctx context.Context, app *model.App, pod *corev1.Pod) (holderName, generation, unreadable string, err error) {
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
	holderName = holder.ObjectName()
	bound, err := r.gitTokenOf(ctx, holder)
	if err != nil {
		return "", "", "", err
	}
	if bound.Fingerprint != "" {
		gen := "secret-" + bound.Fingerprint
		if app.ParentID == "" {
			if unreadable, err = r.writeBoundGitSecret(ctx, app, gen, bound); err != nil || unreadable != "" {
				return "", "", unreadable, err
			}
		}
		return holderName, gen, "", nil
	}
	pasted, gen, has, err := r.Store.GitTokens().Pending(ctx, holder.ID)
	if err != nil {
		return "", "", "", err
	}
	switch {
	case !has:
		if app.ParentID == "" && (pod == nil || pod.Annotations[model.AnnotationGitToken] != "") {
			if err := r.vault().Delete(ctx, refOf(app), GitTokenSecretName(app.ObjectName())); err != nil {
				return "", "", "", err
			}
		}
		return "", "", "", nil
	case pasted != "" && app.ParentID == "":
		if err := r.writeGitSecret(ctx, app, pasted, strconv.FormatInt(gen, 10)); err != nil {
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
	return holderName, strconv.FormatInt(gen, 10), "", nil
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
func (r *Reconciler) writeBoundGitSecret(ctx context.Context, app *model.App, gen string, bound secretenv.Set) (unreadable string, err error) {
	if r.Secrets == nil {
		return secretenv.ErrNoKey.Error(), nil
	}
	have, err := r.vault().Get(ctx, refOf(app), GitTokenSecretName(app.ObjectName()))
	if err != nil {
		return "", err
	}
	if have != nil && have.Meta[model.AnnotationGitToken] == gen {
		return "", nil
	}
	data, unreadable := bound.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		return unreadable, nil
	}
	return "", r.writeGitSecret(ctx, app, string(data[model.GitTokenEnv]), gen)
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
func (r *Reconciler) writeGitSecret(ctx context.Context, app *model.App, token, gen string) error {
	if r.Secrets == nil {
		return errors.New("the app has a repository token, but the controller has no Secret reader")
	}
	err := r.vault().Put(ctx, refOf(app), GitTokenSecretName(app.ObjectName()), executor.Bundle{
		Data: map[string][]byte{gitTokenKey: []byte(token)}, Meta: map[string]string{model.AnnotationGitToken: gen},
	})
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
// from its Secret, or "" when the app has none. A Secret of that name that is
// not labelled as the app's is an error. Callers must never log the token.
func ReadGitToken(ctx context.Context, secrets client.Reader, app *model.App) (string, error) {
	have, err := kube.Secrets{Reader: secrets}.Get(ctx, refOf(app), GitTokenSecretName(app.ObjectName()))
	if err != nil || have == nil {
		return "", err
	}
	return string(have.Data[gitTokenKey]), nil
}
