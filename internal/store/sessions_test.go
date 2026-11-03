package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func uiSession(id, person string, expires time.Time) store.Session {
	return store.Session{
		ID: id, Kind: "ui", Person: person, Principal: `{"id":"` + person + `"}`,
		Provider: "oidc", Refresh: "r", IDToken: "t", ExpiresAt: expires, RevalidateAt: expires,
	}
}

func child(id, parent, kind string, expires time.Time) store.Session {
	s := uiSession(id, "alice", expires)
	s.Kind, s.ParentID, s.Workspace = kind, parent, "javo-apps/lab"
	return s
}

func TestSessionRoundTripAndVersionedUpdate(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	sessions := s.Sessions()
	if err := sessions.Create(ctx, alice, uiSession("a", "alice", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, err := sessions.Get(ctx, "a")
	if err != nil || got.Version != 1 || got.Refresh != "r" || !got.ExpiresAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	stale := got
	got.Refresh = "r2"
	if err := sessions.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	stale.Refresh = "r3"
	if err := sessions.Update(ctx, stale); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update from a stale version = %v, want ErrConflict", err)
	}
	after, _ := sessions.Get(ctx, "a")
	if after.Refresh != "r2" || after.Version != 2 {
		t.Fatalf("after = %+v", after)
	}
	if _, err := sessions.Get(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing) = %v", err)
	}
}

func TestSessionCascade(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	sessions := s.Sessions()
	exp := t0.Add(time.Hour)
	for _, sess := range []store.Session{
		uiSession("ui1", "alice", exp), uiSession("ui2", "alice", exp),
		child("app1", "ui1", "app", exp), child("tk1", "ui1", "ticket", exp), child("app2", "ui2", "app", exp),
	} {
		if err := sessions.Create(ctx, alice, sess); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.Delete(ctx, alice, "ui1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ui1", "app1", "tk1"} {
		if _, err := sessions.Get(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s survived deleting its ui session: %v", id, err)
		}
	}
	for _, id := range []string{"ui2", "app2"} {
		if _, err := sessions.Get(ctx, id); err != nil {
			t.Errorf("%s was deleted with someone else's parent: %v", id, err)
		}
	}
}

func TestSessionCreateWithUnknownParentFails(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	if err := s.Sessions().Create(ctx, alice, child("orphan", "nope", "app", t0.Add(time.Hour))); err == nil {
		t.Fatal("an app session without a parent was stored")
	}
}

func TestSweepDeletesExpired(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	sessions := s.Sessions()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(sessions.Create(ctx, alice, uiSession("old", "alice", t0.Add(-time.Minute))))
	must(sessions.Create(ctx, alice, child("old-child", "old", "app", t0.Add(time.Hour))))
	must(sessions.Create(ctx, alice, uiSession("live", "alice", t0.Add(time.Hour))))
	n, err := sessions.Sweep(ctx, t0)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1 (the child goes by cascade)", n, err)
	}
	if _, err := sessions.Get(ctx, "old-child"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("child of an expired session survived: %v", err)
	}
	if _, err := sessions.Get(ctx, "live"); err != nil {
		t.Errorf("live session swept: %v", err)
	}
}

func TestRedeemUsesATicketOnce(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	sessions := s.Sessions()
	exp := t0.Add(time.Hour)
	_ = sessions.Create(ctx, alice, uiSession("ui", "alice", exp))
	_ = sessions.Create(ctx, alice, child("tk", "ui", "ticket", exp))
	got, err := sessions.Redeem(ctx, "tk")
	if err != nil || got.Kind != "ticket" || got.ParentID != "ui" {
		t.Fatalf("Redeem = %+v, %v", got, err)
	}
	if _, err := sessions.Redeem(ctx, "tk"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Redeem = %v, want ErrNotFound", err)
	}
}

func TestDeleteForPersonEndsEverySession(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	sessions := s.Sessions()
	exp := t0.Add(time.Hour)
	_ = sessions.Create(ctx, alice, uiSession("a1", "alice", exp))
	_ = sessions.Create(ctx, alice, uiSession("a2", "alice", exp))
	_ = sessions.Create(ctx, alice, uiSession("b1", "bob", exp))
	n, err := sessions.DeleteFor(ctx, alice, "alice")
	if err != nil || n != 2 {
		t.Fatalf("DeleteFor(alice) = %d, %v", n, err)
	}
	if _, err := sessions.Get(ctx, "b1"); err != nil {
		t.Fatalf("bob's session deleted: %v", err)
	}
}

func TestDeleteForIsAudited(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	_ = s.Sessions().Create(ctx, model.Actor{Person: "bob"}, uiSession("a1", "bob", t0.Add(time.Hour)))
	before := auditCount(t, s)
	admin := model.Actor{Person: "alice"}
	if _, err := s.Sessions().DeleteFor(ctx, admin, "bob"); err != nil {
		t.Fatal(err)
	}
	events, _ := s.Audit().List(ctx, 10)
	if len(events) != before+1 || events[0].Action != "session.delete_for" || events[0].Actor != "alice" ||
		events[0].ObjectKind != "person" || events[0].ObjectName != "bob" {
		t.Fatalf("audit = %+v", events)
	}
	// Signing out someone with no sessions changes nothing and leaves no row.
	if _, err := s.Sessions().DeleteFor(ctx, admin, "bob"); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, s) - before; n != 1 {
		t.Fatalf("a no-op sign-out left %d audit rows", n-1)
	}
}

// Signing in and out are audited; app sessions and tickets, made per app
// request, are not, and nor is deleting a session that is already gone.
func TestOnlyUISessionsAreAudited(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	sessions := s.Sessions()
	exp := t0.Add(time.Hour)
	if err := sessions.Create(ctx, alice, uiSession("ui", "alice", exp)); err != nil {
		t.Fatal(err)
	}
	before := auditCount(t, s)
	for _, sess := range []store.Session{child("app", "ui", "app", exp), child("tk", "ui", "ticket", exp)} {
		if err := sessions.Create(ctx, alice, sess); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"app", "tk", "missing"} {
		if err := sessions.Delete(ctx, alice, id); err != nil {
			t.Fatal(err)
		}
	}
	if n := auditCount(t, s) - before; n != 0 {
		t.Errorf("app sessions, tickets and a missing session left %d audit rows", n)
	}
}
