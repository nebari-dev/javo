package secrets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/store"
)

// Keys is the data keyset in the database together with the KEK that opens
// it. Only the controller (and `javo secrets rotate`) holds one.
type Keys struct {
	Store *store.Store
	KEK   KEK

	mu      sync.Mutex
	opener  *Opener
	version int64
}

// Ensure makes the data keyset when the database has none, and checks that
// the KEK opens the one it has. It never replaces a keyset: a KEK that does
// not open it (a lost or regenerated key) is an error, because a new keyset
// would leave every stored value unreadable.
func (k *Keys) Ensure(ctx context.Context) error {
	if _, found, err := k.Store.SecretKeys().Get(ctx); err != nil {
		return err
	} else if !found {
		ks, err := NewKeyset(k.KEK)
		if err != nil {
			return err
		}
		// A controller that lost the race keeps the winner's.
		if _, err := k.Store.SecretKeys().Insert(ctx, store.SecretKeyset{Wrapped: ks.Wrapped, Public: ks.Public}); err != nil {
			return err
		}
	}
	_, err := k.Opener(ctx)
	return err
}

// Opener returns what opens values sealed under the stored keyset, unwrapped
// once per keyset version.
func (k *Keys) Opener(ctx context.Context) (*Opener, error) {
	ks, found, err := k.Store.SecretKeys().Get(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("no secrets keyset is stored yet")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.opener != nil && k.version == ks.Version {
		return k.opener, nil
	}
	o, err := NewOpener(Keyset{Wrapped: ks.Wrapped, Public: ks.Public}, k.KEK)
	if err != nil {
		return nil, err
	}
	k.opener, k.version = o, ks.Version
	return o, nil
}

// Rotate adds a new primary key to the data keyset and seals every stored
// value again under it, without changing any value (no app restarts). It
// returns how many values it sealed again. Values sealed before the rotation
// keep opening until then; running it again is safe.
func (k *Keys) Rotate(ctx context.Context) (int, error) {
	ks, found, err := k.Store.SecretKeys().Get(ctx)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, errors.New("no secrets keyset is stored yet")
	}
	rotated, err := Rotate(Keyset{Wrapped: ks.Wrapped, Public: ks.Public}, k.KEK)
	if err != nil {
		return 0, err
	}
	if err := k.Store.SecretKeys().Replace(ctx, store.SecretKeyset{Wrapped: rotated.Wrapped, Public: rotated.Public}, ks.Version); err != nil {
		return 0, fmt.Errorf("store the rotated keyset: %w", err)
	}
	o, err := k.Opener(ctx)
	if err != nil {
		return 0, err
	}
	all, err := k.Store.Secrets().AllSealed(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range all {
		resealed, err := o.Reseal(s.Ciphertext, AD{ID: s.ID, Owner: s.Owner, Name: s.Name})
		if err != nil {
			return n, fmt.Errorf("secret %s of %s: %w", s.Name, s.Owner, err)
		}
		ok, err := k.Store.Secrets().Reseal(ctx, s.ID, s.Ciphertext, resealed)
		if err != nil {
			return n, err
		}
		// A value replaced meanwhile was sealed under the new key already.
		if ok {
			n++
		}
	}
	return n, nil
}

// FingerprintStored fingerprints with f every stored value that has no
// fingerprint under f's key for its current value, and returns how many it
// made. A value that does not open, or a secret deleted meanwhile, is
// skipped and tried again on the next pass.
func (k *Keys) FingerprintStored(ctx context.Context, f *Fingerprinter) (int, error) {
	todo, err := k.Store.Secrets().Unfingerprinted(ctx, f.KeyID())
	if err != nil || len(todo) == 0 {
		return 0, err
	}
	o, err := k.Opener(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range todo {
		v, err := o.Open(s.Ciphertext, AD{ID: s.ID, Owner: s.Owner, Name: s.Name})
		if err != nil {
			slog.Warn("fingerprint a secret: its value does not open", "owner", s.Owner, "secret", s.Name)
			continue
		}
		fp := f.Fingerprint(s.Name, v)
		clear(v)
		if err := k.Store.Secrets().SetFingerprint(ctx, store.SecretFingerprint{
			SecretID: s.ID, Version: s.Version, KeyID: fp.KeyID, Length: fp.Length, MAC: fp.MAC,
		}); err != nil {
			slog.Warn("fingerprint a secret", "owner", s.Owner, "secret", s.Name, "err", err)
			continue
		}
		n++
	}
	return n, nil
}

// RunFingerprints calls FingerprintStored now and every interval until ctx
// is done.
func (k *Keys) RunFingerprints(ctx context.Context, f *Fingerprinter, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := k.FingerprintStored(ctx, f); err != nil {
			slog.Error("fingerprint secrets", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
