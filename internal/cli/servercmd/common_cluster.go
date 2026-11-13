//go:build !local

package servercmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	"github.com/nebari-dev/javo/internal/secrets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// backupPasswordKey is the data key of the backup password Secret.
const backupPasswordKey = "password"

// backupPassword reads the --backup-password-secret Secret through k (making
// it first with --backup-password-create) into platform's bucket settings;
// nothing without a bucket or a Secret named.
func (c *commonFlags) backupPassword(ctx context.Context, k client.Client, platform *config.Platform) error {
	if c.backupPasswordSecret == "" || platform.Backup.S3 == nil {
		return nil
	}
	b, err := secretskey.Secret{
		Client: k, Namespace: c.namespace, Name: c.backupPasswordSecret, Key: backupPasswordKey,
		Component: "backup-password", Create: c.backupPasswordCreate, New: newBackupPassword,
	}.Bytes(ctx)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return fmt.Errorf("Secret %s/%s has no %q", c.namespace, c.backupPasswordSecret, backupPasswordKey)
	}
	platform.Backup.S3.Password = strings.TrimSpace(string(b))
	return nil
}

// newBackupPassword is 288 random bits as 48 URL-safe characters.
func newBackupPassword() ([]byte, error) {
	b := make([]byte, 36)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return []byte(base64.RawURLEncoding.EncodeToString(b)), nil
}

// keySecretKey is the data key of the sessions and fingerprint key Secrets.
const keySecretKey = "key"

// fingerprinter reads the --secret-fingerprint-key-secret Secret through c,
// making it first with --secret-fingerprint-key-create; nil without one.
func (c *commonFlags) fingerprinter(ctx context.Context, k client.Client) (*secrets.Fingerprinter, error) {
	if c.fingerprintKeySecret == "" {
		return nil, nil
	}
	b, err := secretskey.Secret{
		Client: k, Namespace: c.namespace, Name: c.fingerprintKeySecret, Key: keySecretKey,
		Component: "secret-fingerprint-key", Create: c.fingerprintKeyCreate,
	}.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	f, err := secrets.FingerprinterFromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("Secret %s/%s: %w", c.namespace, c.fingerprintKeySecret, err)
	}
	return f, nil
}

// readBackupPassword reads the --backup-password-secret Secret of the
// cluster the kubeconfig names into platform's bucket settings.
func (c *commonFlags) readBackupPassword(ctx context.Context, platform *config.Platform) error {
	restCfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return fmt.Errorf("load kubeconfig: %w", err)
	}
	k, err := client.New(restCfg, client.Options{})
	if err != nil {
		return err
	}
	return c.backupPassword(ctx, k, platform)
}
