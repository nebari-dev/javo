package secretscmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func runSecrets(t *testing.T, db *store.Store, args ...string) (string, error) {
	t.Helper()
	return clitest.Run(New(clitest.Opener(t, db)), append([]string{"secrets"}, args...)...)
}

func TestSecretsRotateResealsEveryValue(t *testing.T) {
	db := storetest.New(t)
	path := clitest.KEKFile(t)
	kek, err := secrets.KEKFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := &secrets.Keys{Store: db, KEK: kek}
	if err := keys.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	sec := clitest.StoreSecret(t, db, "alice", "API_KEY", "v")
	before, _ := db.Secrets().Sealed(context.Background(), sec.ID)
	// Two bundles of a workload, the way the vault keeps them.
	ks, _, _ := db.SecretKeys().Get(context.Background())
	sealer, err := secrets.NewSealer(ks.Public)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice-sales.app-api", "alice-sales.app-git"} {
		sealed, err := sealer.Seal([]byte("v"), secrets.BundleAD("javo", name, "app-1"))
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := db.Bundles().Put(context.Background(), "javo", name, store.SealedBundle{WorkloadID: "app-1", Owner: "alice", Sealed: sealed}); !ok || err != nil {
			t.Fatalf("Put = %v, %v", ok, err)
		}
	}

	out, err := runSecrets(t, db, "rotate", "--key-file", path)
	if err != nil {
		t.Fatalf("rotate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 secret and 2 workload bundles encrypted again") {
		t.Fatalf("output %q", out)
	}
	after, _ := db.Secrets().Sealed(context.Background(), sec.ID)
	if bytes.Equal(before, after) {
		t.Fatal("the value was not sealed again")
	}
	// Another key file is refused before anything changes.
	if out, err := runSecrets(t, db, "rotate", "--key-file", clitest.KEKFile(t)); err == nil {
		t.Fatalf("rotate with another key: %s", out)
	}
	if now, _ := db.Secrets().Sealed(context.Background(), sec.ID); !bytes.Equal(now, after) {
		t.Fatal("a refused rotation changed a value")
	}
}
