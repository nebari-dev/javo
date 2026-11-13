//go:build linux || darwin

package local

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"

	authlocal "github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/store"
)

// Username is the one account of `javo local`, and adminGroup the group
// that makes it the platform's admin: the person owns the machine.
const (
	Username   = "local"
	adminGroup = "admins"
)

// Settings is the platform config of `javo local`, read as a config file
// is, so it gets the same defaults and the same checks: apps run as
// processes, one local account that is the admin, plain HTTP on loopback
// and everything in d. pixi is the program that installs environments.
func (d Dirs) Settings(pixi string) (config.Platform, error) {
	type m = map[string]any
	data, err := json.Marshal(m{
		"auth":     m{"local": m{"enabled": true}},
		"roles":    m{"platformAdmin": m{"groups": []string{adminGroup}}},
		"urls":     m{"scheme": "http"},
		"executor": config.ExecutorProcess,
		"keysDir":  d.Keys,
		"process":  m{"stateDir": d.State, "homesDir": d.Homes, "filesDir": d.Files, "pixi": pixi},
	})
	if err != nil {
		return config.Platform{}, err
	}
	return config.Parse("the settings of javo local", data)
}

// EnsureAccount makes the one account on the first start. Its password is
// 256 random bits that are hashed and then forgotten: nobody signs in
// with it, and the person signs in through the link `javo local` prints
// (internal/features/authlocal.Link). An account that is there is left
// as it is.
func EnsureAccount(ctx context.Context, db *store.Store) error {
	users := &authlocal.DBUsers{Accounts: db.LocalAccounts()}
	if _, err := users.Lookup(ctx, Username); !errors.Is(err, authlocal.ErrUserNotFound) {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	hash, err := authlocal.HashPassword(base64.RawURLEncoding.EncodeToString(raw))
	if err != nil {
		return err
	}
	return users.Add(ctx, Username, hash, []string{adminGroup}, false)
}
