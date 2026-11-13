package executortest

import (
	"context"
	"maps"
	"sync"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/workload"
)

// Secrets is an executor.Secrets that keeps its bundles in memory, for
// tests of an executor that has no store of its own.
type Secrets struct {
	mu      sync.Mutex
	bundles map[string]executor.Bundle
}

var _ executor.Secrets = (*Secrets)(nil)

func bundleKey(of workload.Ref, name string) string { return of.Namespace + "/" + name }

func (s *Secrets) Get(_ context.Context, of workload.Ref, name string) (*executor.Bundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bundles[bundleKey(of, name)]
	if !ok {
		return nil, nil
	}
	return &executor.Bundle{Data: maps.Clone(b.Data), Meta: maps.Clone(b.Meta), Rebuildable: b.Rebuildable}, nil
}

func (s *Secrets) Put(_ context.Context, of workload.Ref, name string, b executor.Bundle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bundles == nil {
		s.bundles = map[string]executor.Bundle{}
	}
	s.bundles[bundleKey(of, name)] = b
	return nil
}

func (s *Secrets) Delete(_ context.Context, of workload.Ref, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bundles, bundleKey(of, name))
	return nil
}
