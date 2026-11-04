package workspace

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/model"
)

// What an app with API access (model.APIAccessOf not None) gets: a Secret in
// its namespace, labelled with the app's id and deleted with it, holding its
// token (APITokenKey, read into
// JAVO_TOKEN) and the API guide (APIGuideKey, mounted read-only at
// APIGuidePath and at each of the template's apiGuidePaths).
const (
	APITokenKey  = "token"
	APIGuideKey  = "AGENTS.md"
	APIGuidePath = "/opt/javo/AGENTS.md"

	apiGuideVolume = "javo-api-guide"
	envURL         = "JAVO_URL"
	envInternalURL = "JAVO_API_URL"
	envToken       = "JAVO_TOKEN"
)

//go:embed agents_guide.md
var apiGuide []byte

// TokenMinter stores a new API token and returns its text, and reads a
// token's record. The controller never lists tokens.
type TokenMinter interface {
	Get(ctx context.Context, id string) (token.Token, error)
	Create(ctx context.Context, actor model.Actor, t token.Token) (string, token.Token, error)
	Revoke(ctx context.Context, actor model.Actor, owner, id string) (token.Token, error)
}

// podAPIAccess is the API access pod was built with. A pod built before the
// annotation existed had read-write access exactly when it got JAVO_TOKEN.
func podAPIAccess(pod *corev1.Pod) string {
	if a, ok := pod.Annotations[model.AnnotationAPIAccess]; ok {
		return a
	}
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == envToken {
			return model.APIAccessReadWrite
		}
	}
	return model.APIAccessNone
}

// APITokenSecretName is the Secret holding the API token of the app whose Pod
// and Service are called objectName (see SecretName).
func APITokenSecretName(objectName string) string { return SecretName(objectName, "api") }

// WithAPIToken gives the app container of pod, built from template t with
// API access, the owner's API token from the Secret secretName as
// JAVO_TOKEN, the UI's public URL as JAVO_URL and the API guide, and records
// access on the pod. With access None it only records it. The token is
// never in the pod spec in clear and never a file. internalURL, when set, is
// the gateway inside the cluster, as JAVO_API_URL: the public address may
// resolve to one the pod may not reach.
func WithAPIToken(pod *corev1.Pod, t *model.Template, access, secretName, apiURL, internalURL string) {
	metav1.SetMetaDataAnnotation(&pod.ObjectMeta, model.AnnotationAPIAccess, access)
	if token.ScopeFor(access) == "" {
		return
	}
	app := &pod.Spec.Containers[0]
	if internalURL != "" {
		app.Env = append(app.Env, corev1.EnvVar{Name: envInternalURL, Value: internalURL})
	}
	app.Env = append(app.Env,
		corev1.EnvVar{Name: envURL, Value: apiURL},
		corev1.EnvVar{Name: envToken, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: APITokenKey,
		}}},
	)
	for _, p := range append([]string{APIGuidePath}, t.Spec.APIGuidePaths...) {
		app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: apiGuideVolume, MountPath: p, SubPath: APIGuideKey, ReadOnly: true})
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: apiGuideVolume, VolumeSource: corev1.VolumeSource{
		Secret: &corev1.SecretVolumeSource{SecretName: secretName, Items: []corev1.KeyToPath{{Key: APIGuideKey, Path: APIGuideKey}}},
	}})
}

// ensureAPIToken makes sure the app has a live token of its owner with scope
// in its Secret and returns the Secret's name. A new token is made when the
// Secret is missing, holds a token of another scope, holds the token the
// gateway marked revoked (App.TokenRevoked), names a token that has no
// record, or names one whose record is not this app's (another app's or a
// job's is never adopted). A Secret of that name labelled for another app is
// refused. The token it replaces is revoked by the gateway, which refuses and
// sweeps app tokens whose scope no longer matches their app. The token
// record is written first: a failure after it leaves a record no one holds,
// which the owner sees in Settings and the janitor removes with the app.
func (r *Reconciler) ensureAPIToken(ctx context.Context, app *model.App, scope token.Scope) (string, error) {
	if r.Tokens == nil || r.Secrets == nil {
		return "", errors.New("the template gives apps an API token, but the controller has no token store")
	}
	name := APITokenSecretName(app.ObjectName())
	sec, err := r.appSecret(ctx, app, name)
	switch {
	case err != nil:
		return "", err
	case sec == nil:
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: app.Namespace}}
	default:
		id := sec.Annotations[model.AnnotationTokenID]
		live := false
		if id != "" && secretScope(sec) == scope && id != app.TokenRevoked && len(sec.Data[APITokenKey]) > 0 {
			// A Secret can name a token whose record is gone (an upgrade, or
			// `javo user remove`): its token gets 401, so it is replaced.
			switch tok, err := r.Tokens.Get(ctx, id); {
			case errors.Is(err, token.ErrNotFound):
			case err != nil:
				return "", fmt.Errorf("look up the app's API token: %w", err)
			default:
				live = tokenOfApp(tok, app)
			}
		}
		if live {
			if bytes.Equal(sec.Data[APIGuideKey], apiGuide) {
				return name, nil
			}
			// A newer guide ships with this controller; the token stays.
			sec.Data[APIGuideKey] = apiGuide
			return name, r.Update(ctx, sec)
		}
	}
	text, tok, err := r.Tokens.Create(ctx, model.Actor{System: model.SystemController}, token.Token{
		Name: app.Name, Owner: app.Owner, Scope: scope,
		App: &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: app.Name},
	})
	if err != nil {
		return "", fmt.Errorf("make the app's API token: %w", err)
	}
	if sec.Labels == nil {
		sec.Labels = map[string]string{}
	}
	for k, v := range labels(app) {
		sec.Labels[k] = v
	}
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, model.AnnotationTokenID, tok.ID)
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, model.AnnotationTokenScope, string(scope))
	sec.Data = map[string][]byte{APITokenKey: []byte(text), APIGuideKey: apiGuide}
	if sec.ResourceVersion == "" {
		err = r.Create(ctx, sec)
	} else {
		err = r.Update(ctx, sec)
	}
	if err != nil {
		// No Secret holds the new token, so it must not keep working; the
		// next reconcile makes another.
		if _, rerr := r.Tokens.Revoke(ctx, model.Actor{System: model.SystemController}, app.Owner, tok.ID); rerr != nil && !errors.Is(rerr, token.ErrNotFound) {
			return "", fmt.Errorf("store the app's API token: %w (and revoke it: %v)", err, rerr)
		}
		return "", fmt.Errorf("store the app's API token: %w", err)
	}
	return name, nil
}

// secretScope is the scope of the token an app's Secret holds; a Secret made
// before the annotation existed holds a read-write one.
func secretScope(sec *corev1.Secret) token.Scope {
	if s, ok := sec.Annotations[model.AnnotationTokenScope]; ok {
		return token.Scope(s)
	}
	return token.ScopeReadWrite
}

// removeAPIToken deletes the app's token Secret, if any. Its token record
// is the gateway's to revoke (see ensureAPIToken).
func (r *Reconciler) removeAPIToken(ctx context.Context, app *model.App) error {
	return r.deleteAppSecret(ctx, app, APITokenSecretName(app.ObjectName()))
}
