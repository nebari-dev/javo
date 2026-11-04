package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// sealedHookPrefix starts a hook secret stored sealed. A secret without it
// was stored in clear by an earlier version; it is sealed the first time it
// is read.
const sealedHookPrefix = "sealed:v1:"

type hookKeyBox struct{ k tink.AEAD }

// SetHookKey sets the key webhook secrets are sealed with in the database
// (the gateway's sessions key, which every replica shares), so a copy of the
// database cannot forge deliveries. Without one, a hook secret can be
// neither stored nor read.
func (s *Store) SetHookKey(k tink.AEAD) { s.hookKey.Store(&hookKeyBox{k}) }

var errNoHookKey = errors.New("store: no key for webhook secrets (SetHookKey)")

func (s *Store) hookAEAD() (tink.AEAD, error) {
	if b := s.hookKey.Load(); b != nil && b.k != nil {
		return b.k, nil
	}
	return nil, errNoHookKey
}

// hookContext binds a sealed secret to its hook, so one copied onto another
// row does not open.
func hookContext(hookID string) []byte { return []byte("javo webhook secret\x00" + hookID) }

// sealHook is secret as stored for hookID.
func (s *Store) sealHook(hookID, secret string) (string, error) {
	k, err := s.hookAEAD()
	if err != nil {
		return "", err
	}
	ct, err := k.Encrypt([]byte(secret), hookContext(hookID))
	if err != nil {
		return "", err
	}
	return sealedHookPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// openHook is the secret stored as stored for hookID, and whether it was in
// clear.
func (s *Store) openHook(hookID, stored string) (secret []byte, clear bool, err error) {
	rest, sealed := strings.CutPrefix(stored, sealedHookPrefix)
	if !sealed {
		return []byte(stored), true, nil
	}
	k, err := s.hookAEAD()
	if err != nil {
		return nil, false, err
	}
	ct, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return nil, false, err
	}
	pt, err := k.Decrypt(ct, hookContext(hookID))
	if err != nil {
		return nil, false, errors.New("store: the webhook secret does not open with this key")
	}
	return pt, false, nil
}

// resealHook stores a secret read in clear sealed, unless the row changed in
// between (a new hook was set).
func (s *Store) resealHook(ctx context.Context, appID, hookID, clearSecret string) error {
	sealed, err := s.sealHook(hookID, clearSecret)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		row, err := q.GetPreviewByHook(ctx, sql.NullString{String: hookID, Valid: true})
		if errors.Is(err, sql.ErrNoRows) || err == nil && (row.AppID != appID || row.HookSecret != clearSecret) {
			return nil
		}
		if err != nil {
			return err
		}
		return q.UpsertPreviewHook(ctx, sqlcgen.UpsertPreviewHookParams{
			AppID: appID, HookID: sql.NullString{String: hookID, Valid: true}, HookSecret: sealed,
		})
	})
}
