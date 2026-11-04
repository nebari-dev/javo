package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/model"
)

// SecretName is the name of an app's Secret of the given use (api, secrets,
// git, nebi): "<objectName>.app-<use>". Object names are DNS labels and never
// hold a ".", so the part after it says whose Secret it is: no name a person
// picks for an app or a job makes another app's or a job's Secret (jobs use
// ".job-" and ".run-").
func SecretName(objectName, use string) string { return objectName + ".app-" + use }

// secretUses are the uses of an app's Secrets.
var secretUses = []string{"api", "secrets", "git", "nebi"}

// legacySecretNames are the names an app's Secrets had before SecretName,
// "<objectName>-javo-<use>", in the order of secretUses. A job of the same
// owner could produce them, so they are only cleaned up, and only when
// labelled as the app's.
func legacySecretNames(objectName string) []string {
	out := make([]string, 0, len(secretUses))
	for _, use := range secretUses {
		out = append(out, objectName+"-"+model.ProductName+"-"+use)
	}
	return out
}

// errForeignSecret means a Secret at one of an app's names carries another
// app's id, or none: it is never read, written or deleted for the app.
var errForeignSecret = errors.New("exists and does not belong to the app")

// secretReader reads Secrets live: the Secrets reader, or the client when a
// test leaves it out.
func (r *Reconciler) secretReader() client.Reader {
	if r.Secrets != nil {
		return r.Secrets
	}
	return r.Client
}

// appSecret reads the app's Secret name: nil when there is none, an error
// wrapping errForeignSecret when it is not labelled with the app's id.
func (r *Reconciler) appSecret(ctx context.Context, app *model.App, name string) (*corev1.Secret, error) {
	return ownedSecret(ctx, r.secretReader(), app, name)
}

func ownedSecret(ctx context.Context, rd client.Reader, app *model.App, name string) (*corev1.Secret, error) {
	sec := &corev1.Secret{}
	err := rd.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: name}, sec)
	switch {
	case apierrors.IsNotFound(err):
		return nil, nil
	case err != nil:
		return nil, err
	case sec.Labels[model.LabelApp] != app.ID:
		return nil, fmt.Errorf("secret %s/%s %w %s", app.Namespace, name, errForeignSecret, app.ID)
	}
	return sec, nil
}

// deleteAppSecret deletes the app's Secret name, if there is one. One that is
// not the app's is left alone.
func (r *Reconciler) deleteAppSecret(ctx context.Context, app *model.App, name string) error {
	sec, err := r.appSecret(ctx, app, name)
	if errors.Is(err, errForeignSecret) {
		log.FromContext(ctx).Info("not deleting a Secret of another owner", "secret", name)
		return nil
	}
	if err != nil || sec == nil {
		return err
	}
	return client.IgnoreNotFound(r.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}))
}

// dropLegacySecrets deletes the app's Secrets under their names before
// SecretName; the caller makes sure no pod of the app reads them any more.
// The token the old API Secret holds is revoked when its record is the app's.
func (r *Reconciler) dropLegacySecrets(ctx context.Context, app *model.App) error {
	for i, name := range legacySecretNames(app.ObjectName()) {
		sec, err := r.appSecret(ctx, app, name)
		if errors.Is(err, errForeignSecret) || (err == nil && sec == nil) {
			continue
		}
		if err != nil {
			return err
		}
		if id := sec.Annotations[model.AnnotationTokenID]; secretUses[i] == "api" && id != "" && r.Tokens != nil {
			switch tok, err := r.Tokens.Get(ctx, id); {
			case errors.Is(err, token.ErrNotFound):
			case err != nil:
				return err
			case tokenOfApp(tok, app):
				if _, err := r.Tokens.Revoke(ctx, model.Actor{System: model.SystemController}, app.Owner, id); err != nil && !errors.Is(err, token.ErrNotFound) {
					return fmt.Errorf("revoke the app's old API token: %w", err)
				}
			}
		}
		if err := r.Delete(ctx, sec, client.Preconditions{UID: &sec.UID}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}

// tokenOfApp is true when tok was made for app: same owner, same namespace
// and object name, and not a job's.
func tokenOfApp(tok token.Token, app *model.App) bool {
	return tok.Owner == app.Owner && tok.Job == "" && tok.App != nil &&
		tok.App.Namespace == app.Namespace && tok.App.Name == app.ObjectName()
}

// readsOtherSecrets is true when pod reads a Secret that is not one of want,
// the names the app's pod is built with now: a pod built before SecretName
// reads the old names and must be built again.
func readsOtherSecrets(pod *corev1.Pod, want ...string) bool {
	other := func(name string) bool { return !slices.Contains(want, name) }
	for _, v := range pod.Spec.Volumes {
		if v.Secret != nil && other(v.Secret.SecretName) {
			return true
		}
	}
	for _, cs := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, c := range cs {
			for _, e := range c.Env {
				if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && other(e.ValueFrom.SecretKeyRef.Name) {
					return true
				}
			}
			for _, f := range c.EnvFrom {
				if f.SecretRef != nil && other(f.SecretRef.Name) {
					return true
				}
			}
		}
	}
	return false
}
