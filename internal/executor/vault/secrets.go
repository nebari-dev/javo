// Package vault keeps the bundles workloads read in the database, for
// executors that have no Kubernetes Secret to keep them in. The values are
// sealed with the secrets keyset (internal/secrets), so the database never
// holds one in clear.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// Secrets keeps each bundle in the workload_bundles row of that name in the
// workload's namespace, marked as the workload's. Its values are sealed for
// that row (the namespace, the name and the workload's id), so a row copied
// to another workload does not open.
type Secrets struct {
	// Keys is the database the rows and the keyset live in, with the
	// key-encryption key that opens the keyset. Put seals with the public
	// half of the keyset; only Get uses the key.
	Keys *secrets.Keys
}

var _ executor.Secrets = Secrets{}

// errDecode is opened values that are not a bundle's; it carries no detail,
// so no error text can hold any part of a value.
var errDecode = errors.New("its opened values are not a bundle's")

func (s Secrets) Get(ctx context.Context, of workload.Ref, name string) (*executor.Bundle, error) {
	row, found, err := s.Keys.Store.Bundles().Get(ctx, of.Namespace, name)
	if err != nil || !found {
		return nil, err
	}
	if row.WorkloadID != of.ID {
		return nil, foreign(of, name)
	}
	o, err := s.Keys.Opener(ctx)
	if err != nil {
		return nil, err
	}
	plain, err := o.Open(row.Sealed, secrets.BundleAD(of.Namespace, name, of.ID))
	if err != nil {
		return nil, fmt.Errorf("bundle %s/%s: %w", of.Namespace, name, err)
	}
	defer clear(plain)
	b := &executor.Bundle{Meta: row.Meta}
	if json.Unmarshal(plain, &b.Data) != nil {
		return nil, fmt.Errorf("bundle %s/%s: %w", of.Namespace, name, errDecode)
	}
	return b, nil
}

func (s Secrets) Put(ctx context.Context, of workload.Ref, name string, b executor.Bundle) error {
	ks, found, err := s.Keys.Store.SecretKeys().Get(ctx)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("no secrets keyset is stored yet")
	}
	sealer, err := secrets.NewSealer(ks.Public)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(b.Data)
	if err != nil {
		return fmt.Errorf("bundle %s/%s: encode its values", of.Namespace, name)
	}
	defer clear(plain)
	sealed, err := sealer.Seal(plain, secrets.BundleAD(of.Namespace, name, of.ID))
	if err != nil {
		return fmt.Errorf("bundle %s/%s: seal its values", of.Namespace, name)
	}
	stored, err := s.Keys.Store.Bundles().Put(ctx, of.Namespace, name, store.SealedBundle{
		WorkloadID: of.ID, Owner: of.Owner, Meta: b.Meta, Sealed: sealed,
	})
	if err == nil && !stored {
		err = foreign(of, name)
	}
	return err
}

func (s Secrets) Delete(ctx context.Context, of workload.Ref, name string) error {
	return s.Keys.Store.Bundles().Delete(ctx, of.Namespace, name, of.ID)
}

// foreign is the error for a row at the workload's name that is kept for
// another workload.
func foreign(of workload.Ref, name string) error {
	return fmt.Errorf("bundle %s/%s %w %s", of.Namespace, name, executor.ErrForeign, of.ID)
}
