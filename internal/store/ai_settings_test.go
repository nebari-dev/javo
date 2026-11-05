package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The admin's AI settings are one row: each write replaces it with a new
// revision and records who changed which fields; a reset removes it.
func TestAISettingsPutGetReset(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	admin := model.Actor{Person: "admin-1"}
	if _, err := s.AISettings().Get(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get before Put = %v, want ErrNotFound", err)
	}
	in := store.AISetting{Provider: "openai", BaseURL: "https://llm.example.com/v1", Model: "m1", KeySealed: []byte("sealed"), KeyLast4: "wxyz"}
	if err := s.AISettings().Put(ctx, admin, in, []string{"provider", "model", "apiKey"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.AISettings().Get(ctx)
	if err != nil || first.Model != "m1" || string(first.KeySealed) != "sealed" || first.KeyLast4 != "wxyz" || first.Rev == "" || first.UpdatedAt.IsZero() {
		t.Fatalf("Get = %+v, %v", first, err)
	}
	in.Model, in.KeySealed, in.KeyLast4 = "m2", nil, ""
	if err := s.AISettings().Put(ctx, admin, in, []string{"model", "apiKey"}); err != nil {
		t.Fatal(err)
	}
	second, err := s.AISettings().Get(ctx)
	if err != nil || second.Model != "m2" || second.KeySealed != nil || second.Rev == first.Rev {
		t.Fatalf("second Get = %+v, %v (first rev %s)", second, err, first.Rev)
	}
	if err := s.AISettings().Reset(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AISettings().Get(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Reset = %v", err)
	}
	events, err := s.Audit().List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if e.ObjectKind != "settings" || e.ObjectName != "ai" || e.Actor != "admin-1" {
			t.Fatalf("event %+v", e)
		}
		var d struct{ Fields []string }
		_ = json.Unmarshal(e.Detail, &d)
		got = append(got, e.Action+":"+strings.Join(d.Fields, ","))
	}
	want := []string{"ai.settings.reset:", "ai.settings.update:model,apiKey", "ai.settings.update:provider,model,apiKey"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("audit = %v, want %v", got, want)
	}
}
