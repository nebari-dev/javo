// Package aitest is a provider for tests: it answers from a queue and
// records every request, so no test calls a real model.
package aitest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/nebari-dev/javo/internal/ai/provider"
)

// Reply is one queued answer: JSON, or an error.
type Reply struct {
	JSON  string
	Err   error
	Usage provider.Usage
}

// Scripted answers each Generate with the next Reply.
type Scripted struct {
	mu       sync.Mutex
	replies  []Reply
	Requests []provider.Request
}

// New returns a provider that answers with replies in order.
func New(replies ...Reply) *Scripted { return &Scripted{replies: replies} }

func (s *Scripted) ID() string { return "scripted" }

// Calls is how many requests reached the provider.
func (s *Scripted) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.Requests)
}

func (s *Scripted) Generate(_ context.Context, req provider.Request) (provider.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, req)
	if len(s.replies) == 0 {
		return provider.Response{}, errors.New("aitest: no reply queued")
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	if r.Err != nil {
		return provider.Response{}, r.Err
	}
	return provider.Response{JSON: json.RawMessage(r.JSON), Usage: r.Usage, Model: "scripted-model"}, nil
}
