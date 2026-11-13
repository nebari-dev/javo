package onhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/nebari-dev/javo/internal/controller/storage/rows"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/hosthome"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const hostOwner = "alice-aaaaaaaa"

var ctx = context.Background()

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// hostHomes is a host home reconciler over a new database and a new root.
func hostHomes(t *testing.T) (*HostHomeReconciler, *store.Store) {
	t.Helper()
	db := storetest.New(t)
	root, err := hosthome.Open(filepath.Join(t.TempDir(), "homes"))
	must(t, err)
	return &HostHomeReconciler{Rows: rows.Rows{Store: db}, Root: root}, db
}

func requestFor(h model.Home) string { return h.ID }

// A home asked for (by an app's start, or the first look at the files)
// becomes a folder of this machine, recorded where a claim's name is on
// Kubernetes, and ready for everything that waits on a home.
func TestHostHomeBecomesAFolderRecordedOnItsRow(t *testing.T) {
	r, db := hostHomes(t)
	asked, err := db.Homes().Ensure(ctx, "javo-apps", hostOwner, nil)
	must(t, err)
	res, err := r.Reconcile(ctx, requestFor(asked))
	if err != nil || res != 0 {
		t.Fatalf("Reconcile = %+v, %v", res, err)
	}
	home, err := db.Homes().Get(ctx, asked.ID)
	must(t, err)
	dir := filepath.Join(r.Root.Dir(), "home-"+hostOwner)
	st := home.Status
	if st.ClaimName != dir || !st.Prepared || st.SizeEnforced || !meta.IsStatusConditionTrue(st.Conditions, model.ConditionReady) {
		t.Fatalf("status = %+v, want the folder %s, prepared and ready, its size not enforced", st, dir)
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder %s: %v, %v", dir, fi, err)
	}

	// An app planned for this machine finds its home there: the home
	// volume's source is the folder.
	layout := podstorage.Build(hostOwner, podstorage.Input{Home: &home})
	if got := layout.Volumes[0]; got.Name != podstorage.HomeVolume || got.Source != dir {
		t.Errorf("home volume = %+v, want source %s", got, dir)
	}
	// And the gateway serves its files with no file pod.
	homes := files.Folders{Homes: podstorage.Provisioner{Resolver: podstorage.Resolver{Homes: db.Homes()}, Maker: db.Homes()}, Namespace: "javo-apps", Root: r.Root.Dir()}
	lister := files.NewLister(homes, files.Direct{})
	if _, err := lister.Save(ctx, hostOwner, "notes.txt", []byte("hello\n"), ""); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || string(got) != "hello\n" {
		t.Fatalf("notes.txt = %q, %v", got, err)
	}

	// A pass over a ready home writes nothing.
	if _, err := r.Reconcile(ctx, requestFor(home)); err != nil {
		t.Fatal(err)
	}
	if again, err := db.Homes().Get(ctx, home.ID); err != nil || again.Version != home.Version {
		t.Errorf("version %d after a second pass, was %d (%v)", again.Version, home.Version, err)
	}
	// Nor does a home that is gone trouble it.
	must(t, db.Homes().Delete(ctx, model.Actor{System: "test"}, home.ID))
	if res, err := r.Reconcile(ctx, requestFor(home)); err != nil || res != 0 {
		t.Errorf("Reconcile of a deleted home = %+v, %v", res, err)
	}
}

// A folder that cannot be made, or that is not what a home must be, leaves
// the home not ready with the reason, and is looked at again.
func TestHostHomeThatCannotBeMadeSaysWhy(t *testing.T) {
	r, db := hostHomes(t)
	elsewhere := t.TempDir()
	dir := filepath.Join(r.Root.Dir(), "home-"+hostOwner)
	must(t, os.Symlink(elsewhere, dir))
	asked, err := db.Homes().Ensure(ctx, "javo-apps", hostOwner, nil)
	must(t, err)
	res, err := r.Reconcile(ctx, requestFor(asked))
	if err != nil || res <= 0 {
		t.Fatalf("Reconcile = %+v, %v; want another look later", res, err)
	}
	home, err := db.Homes().Get(ctx, asked.ID)
	must(t, err)
	ready := meta.FindStatusCondition(home.Status.Conditions, model.ConditionReady)
	if home.Status.Prepared || home.Status.ClaimName != "" || ready == nil || ready.Reason != string(reasons.HomePrepareFailed) {
		t.Fatalf("status = %+v, want not prepared, no folder and HomePrepareFailed", home.Status)
	}
	if _, err := (files.Folders{Homes: podstorage.Provisioner{Resolver: podstorage.Resolver{Homes: db.Homes()}, Maker: db.Homes()}, Namespace: "javo-apps", Root: r.Root.Dir()}).Ensure(ctx, hostOwner); err != files.ErrHomeStarting {
		t.Errorf("the gateway's answer for the home: %v, want ErrHomeStarting", err)
	}

	// Once the link is gone the next look makes the home.
	must(t, os.Remove(dir))
	if res, err := r.Reconcile(ctx, requestFor(home)); err != nil || res != 0 {
		t.Fatalf("Reconcile = %+v, %v", res, err)
	}
	home, err = db.Homes().Get(ctx, asked.ID)
	must(t, err)
	if home.Status.ClaimName != dir || !home.Status.Prepared || !meta.IsStatusConditionTrue(home.Status.Conditions, model.ConditionReady) {
		t.Fatalf("status = %+v, want ready at %s", home.Status, dir)
	}
}
