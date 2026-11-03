// Package clitest helps test javo's commands: it runs them under a root like
// javo's, on a test database and a fake cluster.
package clitest

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/nebari-dev/javo/internal/cli/dbflag"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// Root is a root command like javo's, holding cmds.
func Root(cmds ...*cobra.Command) *cobra.Command {
	root := &cobra.Command{Use: "javo", SilenceUsage: true}
	root.AddCommand(cmds...)
	return root
}

// Run runs `javo args...` with cmd as javo's only command and returns what it
// wrote to stdout and stderr together.
func Run(cmd *cobra.Command, args ...string) (string, error) {
	root := Root(cmd)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

// Opener opens db for a command: each call hands out a second handle on the
// same database, since the command closes what it opens.
func Opener(t *testing.T, db *store.Store) dbflag.Opener {
	return func(context.Context) (*store.Store, error) { return storetest.Reopen(t, db), nil }
}

// Cluster is a fake cluster that holds core and batch objects.
func Cluster(t *testing.T) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).Build()
}

// HomeClaim makes the home claim of owner in ns.
func HomeClaim(t *testing.T, c client.Client, ns, owner string) *corev1.PersistentVolumeClaim {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: naming.HomeClaimName(owner), Namespace: ns, Labels: map[string]string{model.LabelOwner: owner},
	}}
	if err := c.Create(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
	return pvc
}

// KEKFile writes a key-encryption key as the chart's Secret holds it.
func KEKFile(t *testing.T) string {
	t.Helper()
	b := make([]byte, secrets.KEKSize)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// StoreSecret seals a secret the way the gateway does.
func StoreSecret(t *testing.T, db *store.Store, owner, name, value string) model.Secret {
	t.Helper()
	ks, _, err := db.SecretKeys().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s, err := secrets.NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := db.Secrets().Create(context.Background(), model.Actor{Person: owner}, owner, name, "", func(id string) ([]byte, error) {
		return s.Seal([]byte(value), secrets.AD{ID: id, Owner: owner, Name: name})
	})
	if err != nil {
		t.Fatal(err)
	}
	return sec
}
