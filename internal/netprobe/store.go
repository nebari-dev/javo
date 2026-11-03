package netprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nebari-dev/javo/internal/store"
)

// Key is the cluster fact that holds the NetworkPolicy probe result (JSON).
const Key = "networkPolicy"

// Reader reads the stored result; *Store implements it.
type Reader interface {
	Get(ctx context.Context) (Result, error)
}

// Putter stores a result; *Store implements it.
type Putter interface {
	Put(ctx context.Context, r Result) error
}

// Store keeps the latest Result as the cluster fact Key. The controller
// writes it; the gateway reads it.
type Store struct {
	Facts *store.Facts
}

// Get returns the stored result, or Unknown when none was stored yet.
func (s *Store) Get(ctx context.Context) (Result, error) {
	f, err := s.Facts.Get(ctx, Key)
	if errors.Is(err, store.ErrNotFound) {
		return Result{State: Unknown}, nil
	}
	if err != nil {
		return Result{}, err
	}
	var r Result
	if err := json.Unmarshal(f.Value, &r); err != nil {
		return Result{}, fmt.Errorf("cluster fact %s: %w", Key, err)
	}
	return r, nil
}

// Put stores r under Key, replacing the previous result.
func (s *Store) Put(ctx context.Context, r Result) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	at := r.CheckedAt
	if at.IsZero() {
		at = time.Now()
	}
	return s.Facts.Set(ctx, Key, raw, at)
}
