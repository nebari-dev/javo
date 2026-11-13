package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
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
func NebiTokenSecretName(objectName string) string {
	return SecretName(objectName, "nebi")
}

// addNebi gives the main unit the owner's Nebi token file, the Nebi
// address nebiURL and the nebi CLI from runner (the platform's image). The
// bundle is optional: a workload made before the gateway first wrote it
// starts without the file, which appears once the bundle exists.
func addNebi(s *workload.Spec, nebiURL string, runner podspec.Image) {
	app := &s.Main
	s.Volumes = append(s.Volumes,
		workload.Volume{Name: nebiVolume, Secret: &workload.SecretFiles{
			Bundle: NebiTokenSecretName(s.Ref.Name), Keys: []string{NebiTokenKey}, Optional: true, WorldReadable: true,
		}},
		workload.Volume{Name: nebiCLIVolume},
	)
	cli := workload.Mount{Volume: nebiCLIVolume, Path: NebiCLIDir}
	s.Steps = append(s.Steps, workload.Unit{
		Name: NebiCLIContainer, Image: runner.Ref, ReadOnlyRoot: true,
		Command: []string{"/" + model.ProductName},
		Args:    []string{"pod-files", "--from", NebiCLIImageDir, "--to", NebiCLIDir},
		Mounts:  []workload.Mount{cli},
	})
	s.Pull(runner.PullSecrets)
	cli.ReadOnly = true
	app.Mounts = append(app.Mounts, cli, workload.Mount{Volume: nebiVolume, Path: NebiTokenDir, ReadOnly: true})
	app.Env = append(app.Env,
		workload.Env{Name: "NEBI_REMOTE_URL", Value: nebiURL},
		workload.Env{Name: "JAVO_NEBI_TOKEN_FILE", Value: NebiTokenDir + "/" + NebiTokenKey},
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
	ref, name := refOf(app), NebiTokenSecretName(app.ObjectName())
	stamp := expires.UTC().Format(time.RFC3339Nano)
	have, err := r.vault().Get(ctx, ref, name)
	switch {
	case err != nil:
		return err
	case have != nil && have.Meta[annotationNebiExpires] == stamp:
		return nil
	}
	opener, err := r.SecretKeys.Opener(ctx)
	if err != nil {
		return fmt.Errorf("open the app's Nebi token: %w", err)
	}
	tok, err := opener.Open(sealed, secrets.NebiTokenAD(app.ID, app.Owner))
	if err != nil {
		return fmt.Errorf("open the app's Nebi token: %w", err)
	}
	err = r.vault().Put(ctx, ref, name, executor.Bundle{
		Data: map[string][]byte{NebiTokenKey: tok}, Meta: map[string]string{annotationNebiExpires: stamp},
	})
	if err != nil {
		// The API server's error names the object, never its data.
		return fmt.Errorf("store the app's Nebi token: %w", err)
	}
	return nil
}
