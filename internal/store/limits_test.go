package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// limited is a store with the caps l.
func limited(t *testing.T, l store.Limits) *store.Store {
	t.Helper()
	s := storetest.New(t)
	s.SetLimits(l)
	return s
}

// wantLimit fails unless err is the *store.LimitError of limit at max.
func wantLimit(t *testing.T, err error, limit store.Limit, max int64) {
	t.Helper()
	var le *store.LimitError
	if !errors.As(err, &le) || le.Limit != limit || le.Max != max {
		t.Fatalf("err = %v, want the %s limit of %d", err, limit, max)
	}
}

func TestAppsPastTheCapAreRefusedAndOthersAreNotCounted(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{Apps: 2})
	createApp(t, s, "alice", "one")
	two := createApp(t, s, "alice", "two")
	_, err := s.Apps().Create(ctx, alice, newApp("alice", "three"))
	wantLimit(t, err, store.LimitApps, 2)
	// Another person's apps are theirs to count.
	createApp(t, s, "bob", "one")
	// A deleted app frees its place.
	if err := s.Apps().MarkDeleted(ctx, alice, two.ID); err != nil {
		t.Fatal(err)
	}
	createApp(t, s, "alice", "three")
}

func TestRunningAppsPastTheCapAreRefusedOnCreateAndOnStart(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{RunningApps: 1})
	createApp(t, s, "alice", "one")
	_, err := s.Apps().Create(ctx, alice, newApp("alice", "two"))
	wantLimit(t, err, store.LimitRunningApps, 1)

	stopped := newApp("alice", "stopped")
	stopped.Paused = true
	app, err := s.Apps().Create(ctx, alice, stopped)
	if err != nil {
		t.Fatalf("a stopped app does not run: %v", err)
	}
	_, err = s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
		a.Paused = false
		return nil
	})
	wantLimit(t, err, store.LimitRunningApps, 1)
	// A change that does not start it is not refused.
	if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
		a.DisplayName = "renamed"
		return nil
	}); err != nil {
		t.Fatalf("rename of a stopped app at the cap: %v", err)
	}
}

func TestConcurrentCreatesDoNotPassTheCap(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{Apps: 3})
	if _, err := s.People(uidRange).Ensure(ctx, store.Claims{Issuer: "https://idp.example.com", Subject: "alice", Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Apps().Create(ctx, alice, newApp("alice", fmt.Sprintf("app%d", i)))
		}()
	}
	wg.Wait()
	apps, err := s.Apps().List(ctx, "alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 {
		t.Fatalf("%d apps after 12 concurrent creates, want the cap of 3", len(apps))
	}
}

func TestPreviewsPastTheOwnersCapAreRefused(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{Previews: 2})
	site := createApp(t, s, "alice", "site")
	docs := createApp(t, s, "alice", "docs")
	putPreview(t, s, site, 1, "aaaa")
	putPreview(t, s, docs, 2, "bbbb")
	_, err := s.Previews().Put(ctx, previewOf(site, 3, "cccc"))
	wantLimit(t, err, store.LimitPreviews, 2)
	// An existing preview still gets its new commit.
	putPreview(t, s, site, 1, "dddd")
}

func TestJobsPastTheCapAreRefused(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{Jobs: 1})
	createJob(t, s, "alice", "nightly")
	_, err := s.Jobs().Create(ctx, alice, newJob("alice", "hourly"))
	wantLimit(t, err, store.LimitJobs, 1)
}

func TestRunRequestsPastTheJobAndPersonCapsAreRefused(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{RunsPerJob: 2, RunsPerPerson: 3})
	a := createJob(t, s, "alice", "a")
	b := createJob(t, s, "alice", "b")
	for range 2 {
		if _, err := s.Jobs().RequestRun(ctx, alice, a.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Jobs().RequestRun(ctx, alice, a.ID, nil)
	wantLimit(t, err, store.LimitRunsPerJob, 2)
	run, err := s.Jobs().RequestRun(ctx, alice, b.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Jobs().RequestRun(ctx, alice, b.ID, nil)
	wantLimit(t, err, store.LimitRunsPerPerson, 3)
	// A finished run no longer counts.
	run.Outcome, run.FinishTime = model.RunOutcomeSucceeded, t0
	if _, err := s.Jobs().RecordRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Jobs().RequestRun(ctx, alice, b.ID, nil); err != nil {
		t.Fatalf("after a run finished: %v", err)
	}
}

func TestPersonalTokensPastTheCapAreRefused(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{APITokens: 1})
	p := signedIn(t, s, "alice")
	if _, err := s.Tokens().Create(ctx, alice, apiToken("t1", p.Name)); err != nil {
		t.Fatal(err)
	}
	_, err := s.Tokens().Create(ctx, alice, apiToken("t2", p.Name))
	wantLimit(t, err, store.LimitAPITokens, 1)
	// An expired token no longer counts.
	s2 := limited(t, store.Limits{APITokens: 1})
	p2 := signedIn(t, s2, "bob")
	old := apiToken("t1", p2.Name)
	old.ExpiresAt = t0.Add(-time.Hour)
	if _, err := s2.Tokens().Create(ctx, model.Actor{Person: "bob"}, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Tokens().Create(ctx, model.Actor{Person: "bob"}, apiToken("t2", p2.Name)); err != nil {
		t.Fatalf("an expired token was counted: %v", err)
	}
}

func TestPendingPlansPastTheCapAreRefused(t *testing.T) {
	t.Parallel()
	s := limited(t, store.Limits{PendingPlans: 1})
	if err := s.Plans().Create(ctx, plan("p1", "alice", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	err := s.Plans().Create(ctx, plan("p2", "alice", t0.Add(time.Hour)))
	wantLimit(t, err, store.LimitPendingPlans, 1)
	if err := s.Plans().Create(ctx, plan("p3", "bob", t0.Add(time.Hour))); err != nil {
		t.Fatalf("another person's plan: %v", err)
	}
}

func TestNoLimitsSetMeansNoCap(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	for i := range 5 {
		createApp(t, s, "alice", fmt.Sprintf("app%d", i))
	}
}
