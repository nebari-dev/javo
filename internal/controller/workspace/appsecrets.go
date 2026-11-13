package workspace

import (
	"context"
	"fmt"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// AppSecretsName is the Secret holding the secret values of the app whose
// Pod and Service are called objectName, one key per environment variable
// (see SecretName).
func AppSecretsName(objectName string) string {
	return SecretName(objectName, "secrets")
}

// secretsOf reads the secrets app gets that pass secretenv.For, with the
// owner's own agent key when agent has one.
func (r *Reconciler) secretsOf(ctx context.Context, app *model.App, agent agentModel) (secretenv.Set, error) {
	all, err := r.Store.Secrets().ForApp(ctx, app.ID)
	if err != nil {
		return secretenv.Set{}, err
	}
	return secretenv.ForAgent(ctx, all, agent.key, authz.Object{App: app}), nil
}

// writeAppSecrets makes the app's Secret hold want's values, opened with the
// KEK, before its pod is built; an app with none loses the Secret. unreadable
// is why the values could not be opened (never a value), for the app's
// status. No error or log line holds a value.
func (r *Reconciler) writeAppSecrets(ctx context.Context, app *model.App, want secretenv.Set) (unreadable string, err error) {
	ref, name := refOf(app), AppSecretsName(app.ObjectName())
	if want.Fingerprint == "" {
		return "", r.vault().Delete(ctx, ref, name)
	}
	if r.Secrets == nil {
		return secretenv.ErrNoKey.Error(), nil
	}
	have, err := r.vault().Get(ctx, ref, name)
	switch {
	case err != nil:
		return "", err
	case have != nil && have.Meta[model.AnnotationSecrets] == want.Fingerprint:
		return "", nil
	}
	data, unreadable := want.Open(ctx, r.SecretKeys)
	if unreadable != "" {
		return unreadable, nil
	}
	err = r.vault().Put(ctx, ref, name, executor.Bundle{
		Data: data, Meta: map[string]string{model.AnnotationSecrets: want.Fingerprint}, Rebuildable: true,
	})
	if err != nil {
		// The API server's error names the object, never its data.
		return "", fmt.Errorf("store the app's secrets: %w", err)
	}
	return "", nil
}

// AddEnv gives u the variables an app or a job sets, each with a plain value. The platform's own
// variables (reserved names) are never replaced: the gateway refuses those
// names, and one written straight into the database is left out here. The
// defaults a variable may replace (podspec.ColorEnv) are.
func AddEnv(u *workload.Unit, env []model.EnvVar) {
	for _, e := range env {
		if model.EnvNameProblem(e.Name) == model.EnvNameOK {
			u.SetEnv(workload.Env{Name: e.Name, Value: e.Value})
		}
	}
}

// addAppSecrets gives the main unit each of want's variables from the
// app's bundle, by reference: the values are never in the Spec.
func addAppSecrets(s *workload.Spec, want secretenv.Set) {
	for _, b := range want.Bindings {
		s.Main.SetEnv(workload.Env{Name: b.Env, From: &workload.SecretRef{Bundle: AppSecretsName(s.Ref.Name), Key: b.Env}})
	}
}
