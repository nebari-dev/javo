package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

// What an app with Nebi access (model.NebiTokenFor, with Nebi on) gets:
// its owner's Nebi token as the file NebiTokenDir/NebiTokenKey, from a
// Secret in its namespace that the gateway writes and renews while the
// owner uses the app (gateway: nebiTokens); Nebi's address as
// NEBI_REMOTE_URL and the token file's path as JAVO_NEBI_TOKEN_FILE, which
// the nebi wrapper (pod/bin/nebi) and the Nebi kernels (the
// jupyterlab environment) read; and the nebi CLI, copied out of the
// platform's image into NebiCLIDir. The token is never in the pod spec or
// its environment: the kubelet writes the file and replaces it when the
// Secret changes.
const (
	NebiTokenKey = "token"
	NebiTokenDir = "/var/run/javo/nebi"
	// NebiCLIContainer copies the nebi CLI from NebiCLIImageDir of the
	// platform's image into NebiCLIDir.
	NebiCLIContainer = "nebi-cli"
	NebiCLIDir       = "/opt/javo-nebi"
	NebiCLIImageDir  = "/opt/javo-nebi-files"

	nebiVolume    = "javo-nebi-token"
	nebiCLIVolume = "javo-nebi-cli"
)

// NebiTokenSecretName is the Secret holding the Nebi token of the app whose
// Pod and Service are called objectName (see SecretName).
func NebiTokenSecretName(objectName string) string { return SecretName(objectName, "nebi") }

// WithNebi gives the app container of pod the owner's Nebi token file, the
// Nebi address nebiURL and the nebi CLI from runner (the platform's image).
// The Secret is optional: a pod made before the gateway first wrote it
// starts without the file, and the kubelet adds it once the Secret exists.
func WithNebi(pod *corev1.Pod, objectName, nebiURL string, runner podspec.Image) {
	spec := &pod.Spec
	app := &spec.Containers[0]
	spec.Volumes = append(spec.Volumes,
		corev1.Volume{Name: nebiVolume, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
			SecretName: NebiTokenSecretName(objectName), Optional: ptr.To(true),
			Items: []corev1.KeyToPath{{Key: NebiTokenKey, Path: NebiTokenKey}},
			// The kubelet writes it as root and app pods have no fsGroup,
			// so the app's own uid reads it as "others". Only that uid
			// runs in the pod.
			DefaultMode: ptr.To[int32](0o444),
		}}},
		corev1.Volume{Name: nebiCLIVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	)
	cli := corev1.VolumeMount{Name: nebiCLIVolume, MountPath: NebiCLIDir}
	spec.InitContainers = append(spec.InitContainers, corev1.Container{
		Name: NebiCLIContainer, Image: runner.Ref, SecurityContext: podspec.RestrictedContext(),
		Command:                  []string{"/" + model.ProductName},
		Args:                     []string{"pod-files", "--from", NebiCLIImageDir, "--to", NebiCLIDir},
		VolumeMounts:             []corev1.VolumeMount{cli},
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	})
	runner.Pull(spec)
	cli.ReadOnly = true
	app.VolumeMounts = append(app.VolumeMounts, cli,
		corev1.VolumeMount{Name: nebiVolume, MountPath: NebiTokenDir, ReadOnly: true})
	app.Env = append(app.Env,
		corev1.EnvVar{Name: "NEBI_REMOTE_URL", Value: nebiURL},
		corev1.EnvVar{Name: "JAVO_NEBI_TOKEN_FILE", Value: NebiTokenDir + "/" + NebiTokenKey},
	)
}

// annotationNebiExpires, on the token Secret, is when the token it holds
// expires (RFC 3339): a stored token with the same expiry is already there.
const annotationNebiExpires = model.Group + "/nebi-expires"

// syncNebiToken copies the app's stored Nebi token (store.NebiTokens, sealed
// by the gateway) into its Secret when the Secret holds another one. No
// stored token yet leaves things as they are. Errors never carry the token.
func (r *Reconciler) syncNebiToken(ctx context.Context, app *model.App) error {
	sealed, expires, err := r.Store.NebiTokens().Get(ctx, app.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.SecretKeys == nil || r.Secrets == nil {
		return errors.New("the app gets a Nebi token, but the controller has no secrets key")
	}
	name := NebiTokenSecretName(app.ObjectName())
	stamp := expires.UTC().Format(time.RFC3339Nano)
	sec, err := r.appSecret(ctx, app, name)
	create := sec == nil
	switch {
	case err != nil:
		return err
	case !create && sec.Annotations[annotationNebiExpires] == stamp:
		return nil
	case create:
		sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: app.Namespace}}
	}
	opener, err := r.SecretKeys.Opener(ctx)
	if err != nil {
		return fmt.Errorf("open the app's Nebi token: %w", err)
	}
	tok, err := opener.Open(sealed, secrets.NebiTokenAD(app.ID, app.Owner))
	if err != nil {
		return fmt.Errorf("open the app's Nebi token: %w", err)
	}
	sec.Labels = labels(app)
	metav1.SetMetaDataAnnotation(&sec.ObjectMeta, annotationNebiExpires, stamp)
	sec.Type = corev1.SecretTypeOpaque
	sec.Data = map[string][]byte{NebiTokenKey: tok}
	if create {
		err = r.Create(ctx, sec)
	} else {
		err = r.Update(ctx, sec)
	}
	if err != nil {
		// The API server's error names the object, never its data.
		return fmt.Errorf("store the app's Nebi token: %w", err)
	}
	return nil
}
