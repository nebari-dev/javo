package idle_test

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/controller/idle"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	ctx   = context.Background()
	alice = model.Actor{Person: "alice"}
	now   = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
)

// running creates alice's app with this timeout, Running and Ready since
// readySince.
func running(t *testing.T, s *store.Store, name string, minutes int32, readySince time.Time) model.App {
	t.Helper()
	app, err := s.Apps().Create(ctx, alice, &model.App{
		Namespace: "javo-apps", Name: name, Owner: "alice", Template: "jupyterlab", Size: "small", IdleStopMinutes: minutes,
	})
	if err != nil {
		t.Fatal(err)
	}
	st := model.AppStatus{Phase: "Running", Conditions: []metav1.Condition{{
		Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.NewTime(readySince),
	}}}
	if err := s.Apps().SetStatus(ctx, app.ID, app.Version, st); err != nil {
		t.Fatal(err)
	}
	return app
}

func check(t *testing.T, s *store.Store) {
	t.Helper()
	c := &idle.Checker{Apps: s.Apps(), Now: func() time.Time { return now }}
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

func stopped(t *testing.T, s *store.Store, id string) model.App {
	t.Helper()
	app, err := s.Apps().Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestCheckStopsAppsUnusedForTheirTimeout(t *testing.T) {
	s := storetest.New(t)
	hourAgo := now.Add(-time.Hour)
	// Unused since well before its timeout (plus the gateways' flush lag).
	quiet := running(t, s, "quiet", 30, hourAgo)
	if err := s.Apps().NoteActivity(ctx, map[string]time.Time{quiet.ID: now.Add(-31*time.Minute - activity.FlushEvery)}); err != nil {
		t.Fatal(err)
	}
	// Used a minute ago.
	busy := running(t, s, "busy", 30, hourAgo)
	if err := s.Apps().NoteActivity(ctx, map[string]time.Time{busy.ID: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// Never used, but started five minutes ago: it counts as active from its start.
	fresh := running(t, s, "fresh", 30, now.Add(-5*time.Minute))
	// Never used since its start an hour ago.
	forgotten := running(t, s, "forgotten", 30, hourAgo)
	// Off.
	off := running(t, s, "off", 0, hourAgo)
	// Inside the flush lag: the last use may not be written yet.
	edge := running(t, s, "edge", 30, hourAgo)
	if err := s.Apps().NoteActivity(ctx, map[string]time.Time{edge.ID: now.Add(-30*time.Minute - activity.FlushEvery/2)}); err != nil {
		t.Fatal(err)
	}

	check(t, s)

	for _, c := range []struct {
		app  model.App
		want bool
	}{{quiet, true}, {busy, false}, {fresh, false}, {forgotten, true}, {off, false}, {edge, false}} {
		got := stopped(t, s, c.app.ID)
		if got.Paused != c.want {
			t.Errorf("%s: paused %v, want %v", c.app.Name, got.Paused, c.want)
		}
		if c.want && got.StoppedIdleAfter != 30 {
			t.Errorf("%s: stoppedIdleAfter %d, want 30", c.app.Name, got.StoppedIdleAfter)
		}
	}
}

// Only a running app is stopped: one still starting has no use to judge.
func TestCheckLeavesStartingAppsAlone(t *testing.T) {
	s := storetest.New(t)
	app, err := s.Apps().Create(ctx, alice, &model.App{
		Namespace: "javo-apps", Name: "starting", Owner: "alice", Template: "jupyterlab", Size: "small", IdleStopMinutes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().SetStatus(ctx, app.ID, app.Version, model.AppStatus{Phase: "Starting"}); err != nil {
		t.Fatal(err)
	}
	check(t, s)
	if stopped(t, s, app.ID).Paused {
		t.Fatal("stopped a starting app")
	}
}
