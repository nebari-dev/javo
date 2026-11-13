//go:build linux || darwin

package local

import (
	"context"
	"path/filepath"
	"testing"

	authlocal "github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/store"
)

func TestTheSettingsRunAppsAsProcessesForOneAdminInTheFolders(t *testing.T) {
	d, err := Prepare(filepath.Join(t.TempDir(), "javo"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Settings("/opt/bin/pixi")
	if err != nil {
		t.Fatal(err)
	}
	if p.Executor != config.ExecutorProcess || p.Auth.OIDC != nil || !p.Auth.Local.Enabled {
		t.Errorf("executor %q, OIDC %v, local accounts %v", p.Executor, p.Auth.OIDC, p.Auth.Local.Enabled)
	}
	if p.KeysDir != d.Keys || p.Process != (config.ProcessCfg{StateDir: d.State, HomesDir: d.Homes, FilesDir: d.Files, Pixi: "/opt/bin/pixi"}) {
		t.Errorf("keys in %q, processes with %+v", p.KeysDir, p.Process)
	}
	if got := p.Roles.PlatformAdmin.Groups; len(got) != 1 || got[0] != adminGroup {
		t.Errorf("admins are the groups %v", got)
	}
	// The API docs page is off, so its files are not in the binary.
	if p.API.Docs.Enabled {
		t.Error("the API docs page is on")
	}
}

func TestTheAccountIsMadeOnceAndIsTheAdmin(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, store.Config{Engine: store.EngineSQLite, Path: filepath.Join(t.TempDir(), "javo.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAccount(ctx, db); err != nil {
		t.Fatal(err)
	}
	users := &authlocal.DBUsers{Accounts: db.LocalAccounts()}
	first, err := users.Lookup(ctx, Username)
	if err != nil || len(first.Groups) != 1 || first.Groups[0] != adminGroup {
		t.Fatalf("the account: %+v, %v", first, err)
	}
	// No password a person could type opens it.
	for _, guess := range []string{"local", "password", "admin"} {
		if _, err := users.Verify(ctx, Username, guess); err == nil {
			t.Errorf("the password %q signs in", guess)
		}
	}
	if err := EnsureAccount(ctx, db); err != nil {
		t.Fatalf("a second start: %v", err)
	}
}
