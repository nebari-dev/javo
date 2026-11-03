// Package registry is the one plug-in registry every extension point uses
// (auth providers, exposure modes, placement modes, and later points).
// Implementations are registered explicitly from internal/cli/wiring.
package registry

import (
	"fmt"
	"slices"
	"strings"
)

// Named is anything that can be registered under a stable id.
type Named interface{ ID() string }

// Registry holds implementations of one extension point by id.
type Registry[T Named] struct{ items map[string]T }

// New returns an empty registry.
func New[T Named]() *Registry[T] { return &Registry[T]{items: map[string]T{}} }

// Register adds v. It panics on a duplicate id: that is a wiring mistake in
// internal/cli/wiring, found at startup.
func (r *Registry[T]) Register(v T) {
	id := v.ID()
	if _, dup := r.items[id]; dup {
		panic(fmt.Sprintf("registry: id %q registered twice", id))
	}
	r.items[id] = v
}

// Get returns the implementation registered under id.
func (r *Registry[T]) Get(id string) (T, bool) {
	v, ok := r.items[id]
	return v, ok
}

// All returns every implementation, sorted by id.
func (r *Registry[T]) All() []T {
	out := make([]T, 0, len(r.items))
	for _, v := range r.items {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b T) int { return strings.Compare(a.ID(), b.ID()) })
	return out
}
