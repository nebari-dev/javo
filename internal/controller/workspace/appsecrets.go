package workspace

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

// AppSecretsName is the Secret holding the secret values of the app whose
// Pod and Service are called objectName, one key per environment variable
// (see SecretName).
func AppSecretsName(objectName string) string { return SecretName(objectName, "secrets") }

// secretsOf reads the secrets app gets that pass secretenv.For.
func (r *Reconciler) secretsOf(ctx context.Context, app *model.App) (secretenv.Set, error) {
	all, err := r.Store.Secrets().ForApp(ctx, app.ID)
	if err != nil {
		return secretenv.Set{}, err
	}
	return secretenv.For(ctx, all, authz.Object{App: app}), nil
}

// writeAppSecrets makes the app's Secret hold want's values, opened with the
// KEK, before its pod is built; an app with none loses the Secret. unreadable
// is why the values could not be opened (never a value), for the app's
// status. No error or log line holds a value.
func (r *Reconciler) writeAppSecrets(ctx context.Context, app *model.App, want secretenv.Set) (unreadable string, err error) {
	name := AppSecretsName(app.ObjectName())
	if want.Fingerprint == "" {
		return "", r.deleteAppSecret(ctx, app, name)
	}
	if r.Secrets == nil {
		return secretenv.ErrNoKey.Error(), nil
	}
	sec, err := r.appSecret(ctx, app, name)
	create := sec == nil
	switch {
	case err != nil:
		return "", err
	case !create && sec.Annotations[model.AnnotationSecrets] == want.Fingerprint:
		return "", nil
	case create:
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: app.Namespace}}
	}
	data, unreadable := want.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		return unreadable, nil
	}
	// The app's Service owns the Secret: Kubernetes deletes it with the
	// Service, also when the sweep removes an orphaned one.
	svc := &corev1.Service{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: app.Namespace, Name: app.ObjectName()}, svc); err != nil {
		return "", err
	}
	sec.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: svc.Name, UID: svc.UID}}
	sec.Labels = labels(app)
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, model.AnnotationSecrets, want.Fingerprint)
	sec.Type = corev1.SecretTypeOpaque
	sec.Data = data
	if create {
		err = r.Create(ctx, sec)
	} else {
		err = r.Update(ctx, sec)
	}
	if err != nil {
		// The API server's error names the object, never its data.
		return "", fmt.Errorf("store the app's secrets: %w", err)
	}
	return "", nil
}

// WithEnv gives the app container the app's variables with a plain value.
// The platform's own variables (reserved names) are never replaced: the
// gateway refuses those names, and one written straight into the database is
// left out here. The defaults a variable may replace (podspec.ColorEnv) are.
func WithEnv(pod *corev1.Pod, env []model.EnvVar) {
	app := &pod.Spec.Containers[0]
	for _, e := range env {
		if model.EnvNameProblem(e.Name) == model.EnvNameOK {
			podspec.SetEnv(app, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}
	}
}

// WithAppSecrets gives the app container of pod each of want's variables
// from the Secret secretName, by reference: the values are never in the pod
// spec. It records the fingerprint on the pod.
func WithAppSecrets(pod *corev1.Pod, secretName string, want secretenv.Set) {
	if want.Fingerprint == "" {
		return
	}
	metav1.SetMetaDataAnnotation(&pod.ObjectMeta, model.AnnotationSecrets, want.Fingerprint)
	app := &pod.Spec.Containers[0]
	podspec.SetEnv(app, want.Env(secretName)...)
}
