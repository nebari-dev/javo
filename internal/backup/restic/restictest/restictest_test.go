package restictest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/backup/restic/restictest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A repository from New is restic's own, opens with its password only, and
// its key costs little to open; two of them do not see each other's data.
func TestNewIsACheapRepositoryOfItsOwn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := restictest.New(t, "right")
	b := restictest.New(t, "right")
	if err := a.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BackupStdin(ctx, "f", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if snaps, err := b.Snapshots(ctx); err != nil || len(snaps) != 0 {
		t.Fatalf("other repository: %+v, %v", snaps, err)
	}
	wrong := a
	wrong.Env = []string{"RESTIC_PASSWORD=wrong"}
	var re *restic.Error
	if _, err := wrong.Snapshots(ctx); !errors.As(err, &re) || re.Code != reasons.BackupWrongPassword {
		t.Fatalf("wrong password: %v", err)
	}
	keys, err := filepath.Glob(filepath.Join(a.URL, "keys", "*"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys: %v, %v", keys, err)
	}
	raw, err := os.ReadFile(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	var k struct{ N, R, P int }
	if err := json.Unmarshal(raw, &k); err != nil || k.N*k.R*k.P > 1024 {
		t.Fatalf("key cost %+v, %v", k, err)
	}
}

// Cheapen makes the key of a repository restic made cheap to open, keeping
// its password.
func TestCheapenKeepsThePassword(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := restic.Repo{Bin: restictest.Bin(t), URL: filepath.Join(t.TempDir(), "repo"), Env: []string{"RESTIC_PASSWORD=pw"}}
	if err := r.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	restictest.Cheapen(t, r.URL, "pw")
	if _, err := r.Snapshots(ctx); err != nil {
		t.Fatal(err)
	}
}
