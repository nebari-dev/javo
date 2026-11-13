package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Bundles is the workload_bundles table: the values workloads read, for
// executors that have no Kubernetes Secret to keep them in. The store never
// sees a value: callers hand it the values sealed for the row (package
// secrets). No audit row, change or error carries a value or a ciphertext.
type Bundles struct{ s *Store }

// Bundles returns the workload_bundles table.
func (s *Store) Bundles() *Bundles { return &Bundles{s} }

// SealedBundle is a stored bundle: the workload it is kept for, its notes
// and its sealed values.
type SealedBundle struct {
	// WorkloadID is the id of the app or job the bundle belongs to, and
	// Owner the person it runs for.
	WorkloadID, Owner string
	Meta              map[string]string
	Sealed            []byte
}

// Get returns the bundle name of the namespace, whichever workload it is
// kept for; found is false when there is none.
func (b *Bundles) Get(ctx context.Context, namespace, name string) (sb SealedBundle, found bool, err error) {
	row, err := b.s.q.GetBundle(ctx, sqlcgen.GetBundleParams{Namespace: namespace, Name: name})
	if errors.Is(err, sql.ErrNoRows) {
		return SealedBundle{}, false, nil
	}
	if err != nil {
		return SealedBundle{}, false, err
	}
	sb = SealedBundle{WorkloadID: row.WorkloadID, Owner: row.Owner, Sealed: row.Sealed}
	return sb, true, unmarshal(row.Meta, &sb.Meta)
}

// Put makes the bundle name of the namespace hold sb, replacing what the
// same workload kept there. stored is false, and nothing changes, when the
// name holds a bundle of another workload. It is the executor's own
// bookkeeping: not audited, not announced.
func (b *Bundles) Put(ctx context.Context, namespace, name string, sb SealedBundle) (stored bool, err error) {
	meta, err := marshal(sb.Meta, "{}")
	if err != nil {
		return false, err
	}
	n, err := b.s.q.UpsertBundle(ctx, sqlcgen.UpsertBundleParams{
		Namespace: namespace, Name: name, WorkloadID: sb.WorkloadID, Owner: sb.Owner,
		Meta: meta, Sealed: sb.Sealed, UpdatedAt: dbtime.From(now()),
	})
	return n == 1, err
}

// Delete removes the bundle name of the namespace if it is kept for the
// workload; one kept for another workload, or none, is left as it is.
func (b *Bundles) Delete(ctx context.Context, namespace, name, workloadID string) error {
	return b.s.q.DeleteBundle(ctx, sqlcgen.DeleteBundleParams{Namespace: namespace, Name: name, WorkloadID: workloadID})
}
