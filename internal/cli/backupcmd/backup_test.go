package backupcmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// `javo backup restore`: one person at a date, or every home at its newest
// backup (disaster recovery). A home with no backup is named and skipped.
func TestBackupRestoreAsksForRestoresOfTheChosenHomes(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	for _, o := range []string{"alice-1234abcd", "bob-5678abcd", "carol-9999abcd"} {
		if _, err := db.Homes().Ensure(ctx, "javo-apps", o, nil); err != nil {
			t.Fatal(err)
		}
	}
	old, newer := time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	list := func(_ context.Context, _, owner string) ([]restic.Snapshot, error) {
		switch owner {
		case "carol-9999abcd":
			return nil, &restic.Error{Code: reasons.BackupRepoMissing}
		default:
			return []restic.Snapshot{{ID: owner + "-new", Time: newer}, {ID: owner + "-old", Time: old}}, nil
		}
	}
	var out bytes.Buffer
	if err := restoreHomes(ctx, db, list, restoreChoice{owners: []string{"alice-1234abcd"}, snapshot: "alice-1234abcd-old", mode: model.RestoreBeside}, &out); err != nil {
		t.Fatal(err)
	}
	a, err := db.Homes().ByOwner(ctx, "javo-apps", "alice-1234abcd")
	if err != nil || a.Spec.Restore == nil || a.Spec.Restore.Snapshot != "alice-1234abcd-old" || !a.Spec.Restore.SnapshotTime.Time.Equal(old) || a.Spec.Restore.Mode != model.RestoreBeside {
		t.Fatalf("alice = %+v, %v", a.Spec.Restore, err)
	}
	if err := restoreHomes(ctx, db, list, restoreChoice{owners: []string{"alice-1234abcd"}, snapshot: "nope", mode: model.RestoreReplace}, &out); err == nil {
		t.Fatal("a second restore of alice, or an unknown snapshot, was accepted")
	}

	out.Reset()
	if err := restoreHomes(ctx, db, list, restoreChoice{all: true, snapshot: "latest", mode: model.RestoreReplace}, &out); err != nil {
		t.Fatal(err)
	}
	b, _ := db.Homes().ByOwner(ctx, "javo-apps", "bob-5678abcd")
	if b.Spec.Restore == nil || b.Spec.Restore.Snapshot != "bob-5678abcd-new" || b.Spec.Restore.Mode != model.RestoreReplace {
		t.Fatalf("bob = %+v", b.Spec.Restore)
	}
	c, _ := db.Homes().ByOwner(ctx, "javo-apps", "carol-9999abcd")
	if c.Spec.Restore != nil {
		t.Fatalf("carol has no backup but got %+v", c.Spec.Restore)
	}
	for _, want := range []string{"bob-5678abcd", "carol-9999abcd: no backup", "alice-1234abcd: a restore is under way"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}
