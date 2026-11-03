package store_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func plan(id, owner string, expires time.Time) store.Plan {
	return store.Plan{ID: id, Owner: owner, Source: "rules", State: store.PlanPending,
		Body: json.RawMessage(`{"action":"restart","target":"sales"}`), CreatedAt: t0, ExpiresAt: expires}
}

func TestPlanIsDecidedOnceUnderRace(t *testing.T) {
	s := storetest.New(t)
	if err := s.Plans().Create(ctx, plan("p1", "alice-a", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = s.Plans().Transition(ctx, alice, "p1", store.PlanPending, store.PlanApplied, "", store.PlanAudit{Kind: "app", Name: "sales"})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, store.ErrConflict):
			t.Errorf("loser got %v, want ErrConflict", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d requests applied the plan, want 1", won)
	}
	got, err := s.Plans().Get(ctx, "p1")
	if err != nil || got.State != store.PlanApplied || got.DecidedAt.IsZero() {
		t.Fatalf("plan %+v %v", got, err)
	}
}

func TestPlanDecisionIsAuditedWithItsSource(t *testing.T) {
	s := storetest.New(t)
	p := plan("p1", "alice-a", t0.Add(time.Hour))
	p.Source = "mcp:claude-code"
	if err := s.Plans().Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plans().Transition(ctx, alice, "p1", store.PlanPending, store.PlanApplied, "",
		store.PlanAudit{Kind: "app", Name: "sales", Owner: "alice-a", Detail: map[string]string{"summary": "Restart Sales"}}); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Audit().List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("%d audit rows, want 1: %+v", len(evs), evs)
	}
	e := evs[0]
	var detail map[string]string
	_ = json.Unmarshal(e.Detail, &detail)
	if e.Action != "ActionApplied" || e.ObjectName != "sales" || e.Actor != alice.Person ||
		detail["source"] != "mcp:claude-code" || detail["summary"] != "Restart Sales" {
		t.Fatalf("audit row %+v detail %v", e, detail)
	}
}

func TestSweepDeletesOnlyExpiredPlans(t *testing.T) {
	s := storetest.New(t)
	for _, p := range []store.Plan{plan("old", "a", t0.Add(-time.Minute)), plan("new", "a", t0.Add(time.Minute))} {
		if err := s.Plans().Create(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Plans().Sweep(ctx, t0)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if _, err := s.Plans().Get(ctx, "old"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expired plan still there: %v", err)
	}
	if _, err := s.Plans().Get(ctx, "new"); err != nil {
		t.Errorf("live plan gone: %v", err)
	}
}

func TestAIUsageHoldsTheLimitAcrossCallers(t *testing.T) {
	s := storetest.New(t)
	u := s.AIUsage()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = u.Reserve(ctx, "alice-a", "2026-11-01", 600, 1000) }()
	}
	wg.Wait()
	ok, used := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrLimit):
			used++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || used != 1 {
		t.Fatalf("reserved %d, refused %d; want one each", ok, used)
	}
	// The call used 250 of its 600: the rest is free again.
	if err := u.Release(ctx, "alice-a", "2026-11-01", 350); err != nil {
		t.Fatal(err)
	}
	if err := u.Reserve(ctx, "alice-a", "2026-11-01", 750, 1000); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if err := u.Reserve(ctx, "alice-a", "2026-11-02", 1000, 1000); err != nil {
		t.Fatalf("a new day starts at zero: %v", err)
	}
	if err := u.Reserve(ctx, "bob-b", "2026-11-01", 1001, 1000); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("one call over the limit: %v", err)
	}
}
