package homecmd

import (
	"context"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/cli/clitest"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// `javo home prune --orphans` lists the homes whose owner has no account
// and no claim, and removes them only with --yes. A home with files left,
// or whose owner signs in elsewhere, is never touched.
func TestHomePruneRemovesOnlyOrphans(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	c := clitest.Cluster(t)
	hash, err := local.HashPassword("a-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&local.DBUsers{Accounts: db.LocalAccounts()}).Add(ctx, "alice", hash, nil, false); err != nil {
		t.Fatal(err)
	}
	oidc, err := db.People(store.IDRange{}).Ensure(ctx, store.Claims{Issuer: "https://idp.example.com", Subject: "b", Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	owners := map[string]bool{ // owner: an orphan
		local.UserID("alice"):  false,
		local.UserID("e2e-x1"): true,
		local.UserID("e2e-x2"): false, // its claim is still there
		oidc.Name:              false,
	}
	for o := range owners {
		if _, err := db.Homes().Ensure(ctx, "javo-apps", o, nil); err != nil {
			t.Fatal(err)
		}
	}
	clitest.HomeClaim(t, c, "javo-apps", local.UserID("e2e-x2"))

	out, err := run(t, db, c, "home", "prune", "--orphans")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, local.UserID("e2e-x1")) || strings.Contains(out, local.UserID("e2e-x2")) || !strings.Contains(out, "--yes") {
		t.Fatalf("dry run said:\n%s", out)
	}
	if all, _ := db.Homes().List(ctx); len(all) != 4 {
		t.Fatalf("a dry run removed homes: %d left", len(all))
	}
	if out, err := run(t, db, c, "home", "prune", "--orphans", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for o, orphan := range owners {
		left, _ := db.Homes().ListByOwner(ctx, o)
		if (len(left) == 0) != orphan {
			t.Errorf("%s: %d homes left, orphan %v", o, len(left), orphan)
		}
	}
}

// run runs `javo args...` on db and the cluster c.
func run(t *testing.T, db *store.Store, c client.Client, args ...string) (string, error) {
	t.Helper()
	return clitest.Run(New(func() (client.Client, error) { return c, nil }, clitest.Opener(t, db)), args...)
}
