package ai

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/reasons"
)

// switchable is a backend an admin changes while the service runs.
type switchable struct {
	mu  sync.Mutex
	cur Backend
	err error
}

func (s *switchable) get(context.Context) (Backend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.err
}

func (s *switchable) set(b Backend, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cur, s.err = b, err
}

// Each call asks the backend in force at that moment: a change applies to
// the next call, with its provider and its model, and its self-test is its
// own.
func TestEachCallUsesTheBackendInForce(t *testing.T) {
	draft := `{"displayName":"Sales","template":"streamlit","file":""}`
	first := aitest.New(aitest.Reply{JSON: `{"ok":true}`}, aitest.Reply{JSON: draft})
	second := aitest.New(aitest.Reply{JSON: draft})
	b := &switchable{cur: Backend{Provider: first, Model: "model-a", Rev: "a"}}
	svc := &Service{Backend: b.get, Budget: &allowAll{}, MaxInputRunes: 10000}
	ctx := context.Background()

	if r := svc.SelfTest(ctx); !r.StructuredOutput {
		t.Fatalf("self-test %+v", r)
	}
	if r := svc.Draft(ctx, "alice-a", "a sales app", tpls); r.Model != "scripted-model" || first.Requests[1].Model != "model-a" {
		t.Fatalf("first draft %+v, model asked %q", r, first.Requests[1].Model)
	}
	b.set(Backend{Provider: second, Model: "model-b", Rev: "b"}, nil)
	if _, ok := svc.SelfTestResult(ctx); ok {
		t.Fatal("the first backend's self-test must not stand for the second")
	}
	svc.Draft(ctx, "alice-a", "a sales app", tpls)
	if second.Calls() != 1 || second.Requests[0].Model != "model-b" || first.Calls() != 2 {
		t.Fatalf("calls: first %d, second %d", first.Calls(), second.Calls())
	}

	// A backend that cannot be built answers as an unavailable provider.
	b.set(Backend{}, errors.New("anthropic: no API key"))
	if r := svc.Draft(ctx, "alice-a", "a sales app", tpls); r.Message != reasons.Msg(reasons.MsgAIUnavailable) {
		t.Fatalf("no backend: %+v", r)
	}
}
