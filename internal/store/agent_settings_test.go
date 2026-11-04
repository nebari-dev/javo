package store_test

import (
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// A person's own agent model is kept until they change or remove it.
func TestAgentSettingsPutGetDelete(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	if _, err := s.AgentSettings().Get(ctx, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get before Put = %v, want ErrNotFound", err)
	}
	in := store.AgentSetting{Owner: "alice", BaseURL: "https://llm.example.com/v1", Model: "m1", KeyLast4: "wxyz"}
	if err := s.AgentSettings().Put(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Model = "m2"
	if err := s.AgentSettings().Put(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentSettings().Get(ctx, "alice")
	if err != nil || got.BaseURL != in.BaseURL || got.Model != "m2" || got.KeyLast4 != "wxyz" || got.UpdatedAt.IsZero() {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := s.AgentSettings().Get(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob's = %v", err)
	}
	if err := s.AgentSettings().Delete(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentSettings().Get(ctx, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
}
