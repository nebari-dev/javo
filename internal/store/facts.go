package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Fact is something observed about the cluster (for example whether
// NetworkPolicy is enforced), with when it was observed.
type Fact struct {
	Key        string
	Value      json.RawMessage
	ObservedAt time.Time
}

// Facts is the cluster_facts table.
type Facts struct{ s *Store }

// Facts returns the cluster_facts table.
func (s *Store) Facts() *Facts { return &Facts{s} }

// Set records the latest value of a fact.
func (f *Facts) Set(ctx context.Context, key string, value json.RawMessage, observedAt time.Time) error {
	return f.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.UpsertFact(ctx, sqlcgen.UpsertFactParams{Key: key, Value: string(value), ObservedAt: dbtime.From(observedAt)})
	})
}

// Get returns the fact or ErrNotFound.
func (f *Facts) Get(ctx context.Context, key string) (Fact, error) {
	row, err := f.s.q.GetFact(ctx, key)
	if errors.Is(err, sql.ErrNoRows) {
		return Fact{}, ErrNotFound
	}
	return factOf(row), err
}

// List returns every fact ordered by key.
func (f *Facts) List(ctx context.Context) ([]Fact, error) {
	rows, err := f.s.q.ListFacts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Fact, len(rows))
	for i, r := range rows {
		out[i] = factOf(r)
	}
	return out, nil
}

func factOf(r sqlcgen.ClusterFact) Fact {
	return Fact{Key: r.Key, Value: json.RawMessage(r.Value), ObservedAt: r.ObservedAt.Time()}
}
