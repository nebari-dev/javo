//go:build !local

package servercmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/javo/internal/config"
)

// The gateway makes its sessions key in the named Secret on its first start
// and every later gateway reads the same key, so a restart or another
// replica opens the sessions the first one sealed.
func TestTheSessionsKeyIsMadeOnceAndShared(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	o := gatewayOptions{sessionsKeySecret: "javo-sessions-key", sessionsKeyCreate: true}
	first, err := o.sessionsKeyOf(ctx, c, "javo-system")
	if err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "javo-system", Name: "javo-sessions-key"}, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Data["key"]) != 32 {
		t.Fatalf("key is %d bytes", len(s.Data["key"]))
	}
	second, err := (&gatewayOptions{sessionsKeySecret: "javo-sessions-key"}).sessionsKeyOf(ctx, c, "javo-system")
	if err != nil {
		t.Fatal(err)
	}
	ct, err := first.Encrypt([]byte("refresh"), []byte("ad"))
	if err != nil {
		t.Fatal(err)
	}
	if pt, err := second.Decrypt(ct, []byte("ad")); err != nil || string(pt) != "refresh" {
		t.Fatalf("another gateway does not open it: %v", err)
	}
}

// The controller and the gateway read one fingerprint key, whichever of
// them makes it, so the gateway finds what the controller fingerprinted.
func TestBothComponentsReadOneFingerprintKey(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	flags := commonFlags{namespace: "javo-system", fingerprintKeySecret: "javo-secret-fingerprint-key", fingerprintKeyCreate: true}
	ctl, err := flags.fingerprinter(ctx, c)
	if err != nil || ctl == nil {
		t.Fatalf("controller: %v, %v", ctl, err)
	}
	gw, err := (&commonFlags{namespace: "javo-system", fingerprintKeySecret: "javo-secret-fingerprint-key"}).fingerprinter(ctx, c)
	if err != nil || gw == nil || gw.KeyID() != ctl.KeyID() {
		t.Fatalf("gateway: %v, %v", gw, err)
	}
	if none, err := (&commonFlags{}).fingerprinter(ctx, c); none != nil || err != nil {
		t.Fatalf("without a Secret: %v, %v", none, err)
	}
}

// The backup password is made once at run time, like the keys above, not by
// the chart (a render without the cluster would make a new one each time,
// and a new password opens none of the backups already in the bucket). The
// component that makes it and every later one use the same password, and
// that password, not the mounted file, is what restic gets.
func TestTheBackupPasswordIsMadeOnceAndShared(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().Build()
	platform := func() *config.Platform {
		return &config.Platform{Backup: config.BackupCfg{S3: &config.S3Cfg{PasswordFile: "/nonexistent/password"}}}
	}
	first := platform()
	flags := commonFlags{namespace: "javo-system", backupPasswordSecret: "javo-backup-password", backupPasswordCreate: true}
	if err := flags.backupPassword(ctx, c, first); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: "javo-system", Name: "javo-backup-password"}, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Data["password"]) < 48 || s.Immutable == nil || !*s.Immutable {
		t.Fatalf("password Secret: %d bytes, immutable %v", len(s.Data["password"]), s.Immutable)
	}
	second := platform()
	if err := (&commonFlags{namespace: "javo-system", backupPasswordSecret: "javo-backup-password"}).backupPassword(ctx, c, second); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*config.Platform{first, second} {
		p.Backup.S3.CredentialsDir = t.TempDir()
		for _, f := range []string{"access-key-id", "secret-access-key"} {
			if err := os.WriteFile(filepath.Join(p.Backup.S3.CredentialsDir, f), []byte("k"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		tg, err := p.Backup.S3.Target()
		if err != nil || tg.Password != string(s.Data["password"]) {
			t.Fatalf("restic gets %q, %v; want the Secret's password", tg.Password, err)
		}
	}
	// Without a Secret named, the mounted file is read as before.
	if err := (&commonFlags{namespace: "javo-system"}).backupPassword(ctx, c, platform()); err != nil {
		t.Fatal(err)
	}
}
