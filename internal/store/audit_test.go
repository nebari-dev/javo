package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// object is what an audit row says about the thing acted on.
type object struct{ name, id, namespace, owner string }

func appObject(a model.App) object { return object{a.Name, a.ID, a.Namespace, a.Owner} }
func jobObject(j model.Job) object { return object{j.Name, j.ID, j.Namespace, j.Owner} }

// Every method that changes data on behalf of an actor leaves exactly one
// audit row naming the actor, the action and the object: for apps and jobs,
// short names repeat across owners, so the row also carries the row id,
// namespace and owner.
func TestWriteAudited(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	var app model.App
	var job model.Job
	var run model.Run
	tpl := model.Template{Name: "lab", Source: "admin", Spec: model.TemplateSpec{
		DisplayName: "Lab", Kind: "jupyterlab", Image: "registry.example.com/lab:1", Port: 8888,
	}}
	steps := []struct {
		action, kind string
		do           func() (object, error)
	}{
		{"app.create", "app", func() (object, error) {
			var err error
			app, err = s.Apps().Create(ctx, alice, newApp("alice", "lab"))
			return appObject(app), err
		}},
		{"app.update", "app", func() (object, error) {
			_, err := s.Apps().UpdateSpec(ctx, alice, app.ID, 1, func(a *model.App) error { a.Size = "large"; return nil })
			return appObject(app), err
		}},
		{"app.delete", "app", func() (object, error) { return appObject(app), s.Apps().MarkDeleted(ctx, alice, app.ID) }},
		{"app.purge", "app", func() (object, error) { return appObject(app), s.Apps().Purge(ctx, alice, app.ID) }},
		{"job.create", "job", func() (object, error) {
			var err error
			job, err = s.Jobs().Create(ctx, alice, newJob("alice", "nightly"))
			return jobObject(job), err
		}},
		{"job.update", "job", func() (object, error) {
			_, err := s.Jobs().UpdateSpec(ctx, alice, job.ID, 1, func(j *model.Job) error { j.Spec.Kernel = "k"; return nil })
			return jobObject(job), err
		}},
		{"job.run", "job", func() (object, error) {
			var err error
			run, err = s.Jobs().RequestRun(ctx, alice, job.ID, nil)
			return jobObject(job), err
		}},
		{"job.cancel", "job", func() (object, error) { return jobObject(job), s.Jobs().RequestCancel(ctx, alice, run.ID) }},
		{"job.delete", "job", func() (object, error) { return jobObject(job), s.Jobs().MarkDeleted(ctx, alice, job.ID) }},
		{"job.purge", "job", func() (object, error) { return jobObject(job), s.Jobs().Purge(ctx, alice, job.ID) }},
		{"template.put", "template", func() (object, error) {
			_, err := s.Templates().Put(ctx, alice, tpl, 0)
			return object{name: "lab"}, err
		}},
		{"template.delete", "template", func() (object, error) { return object{name: "lab"}, s.Templates().Delete(ctx, alice, "lab") }},
		{"account.put", "account", func() (object, error) {
			return object{name: "bob"}, s.LocalAccounts().Put(ctx, alice, store.LocalAccount{Username: "bob", Hash: "h"})
		}},
		{"account.delete", "account", func() (object, error) { return object{name: "bob"}, s.LocalAccounts().Delete(ctx, alice, "bob") }},
		{"session.create", "person", func() (object, error) {
			return object{name: "alice"}, s.Sessions().Create(ctx, alice, uiSession("ui1", "alice", t0.Add(time.Hour)))
		}},
		{"session.delete", "person", func() (object, error) { return object{name: "alice"}, s.Sessions().Delete(ctx, alice, "ui1") }},
		{"group.create", "group", func() (object, error) {
			_, err := s.Groups(store.IDRange{First: 200000, Last: 200100}).Ensure(ctx, alice, "data")
			return object{name: "data"}, err
		}},
	}
	for _, step := range steps {
		before := auditCount(t, s)
		want, err := step.do()
		if err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		events, _ := s.Audit().List(ctx, 100)
		if len(events) != before+1 {
			t.Fatalf("%s left %d audit rows, want 1", step.action, len(events)-before)
		}
		e := events[0]
		got := object{e.ObjectName, e.ObjectID, e.ObjectNamespace, e.ObjectOwner}
		if e.Action != step.action || e.ObjectKind != step.kind || got != want || e.Actor != "alice" || !e.Allowed || e.At.IsZero() {
			t.Errorf("%s audit row = %+v, want object %+v", step.action, e, want)
		}
	}
}

// In per-user namespace mode alice and bob may both have an app named lab.
// When someone else (an admin, the controller) removes bob's, the log must
// say it was bob's.
func TestAuditTellsApartSameNamedAppsOfDifferentOwners(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	var bobs model.App
	for _, owner := range []string{"alice", "bob"} {
		a := newApp(owner, "lab")
		a.Namespace = "javo-" + owner
		created, err := s.Apps().Create(ctx, model.Actor{Person: owner}, a)
		if err != nil {
			t.Fatal(err)
		}
		bobs = created
	}
	if err := s.Apps().MarkDeleted(ctx, model.Actor{Person: "admin"}, bobs.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, model.Actor{System: model.SystemController}, bobs.ID); err != nil {
		t.Fatal(err)
	}
	events, _ := s.Audit().List(ctx, 2)
	for _, e := range events {
		if got := (object{e.ObjectName, e.ObjectID, e.ObjectNamespace, e.ObjectOwner}); got != appObject(bobs) {
			t.Errorf("%s by %s names %+v, want bob's app %+v", e.Action, e.Actor, got, appObject(bobs))
		}
	}
}

func TestAuditNamesTheActorAndHowTheyActed(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	for i, a := range []model.Actor{
		{Person: "alice", Token: "tok1"}, {Person: "alice"}, {System: model.SystemController},
	} {
		if _, err := s.Templates().Put(ctx, a, model.Template{Name: "t", Source: "admin", Spec: model.TemplateSpec{
			DisplayName: "T", Kind: "jupyterlab", Image: "registry.example.com/t:1", Port: 1,
		}}, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := s.Audit().List(ctx, 10)
	got := [][2]string{{events[2].Actor, events[2].Via}, {events[1].Actor, events[1].Via}, {events[0].Actor, events[0].Via}}
	want := [][2]string{{"alice", "token:tok1"}, {"alice", "session"}, {model.SystemController, "system"}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d actor/via = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestAuditRecordsDeniedActions(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	err := s.Audit().Append(ctx, store.AuditEvent{Actor: "bob", Via: "session", Action: "app.open", ObjectKind: "app", ObjectName: "lab", Allowed: false})
	if err != nil {
		t.Fatal(err)
	}
	events, _ := s.Audit().List(ctx, 10)
	if len(events) != 1 || events[0].Allowed || events[0].Actor != "bob" {
		t.Fatalf("events = %+v", events)
	}
}

// A write and its audit row commit together or not at all.
func TestFailedAuditRollsBackTheWrite(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.Templates().Put(ctx, alice, model.Template{Name: "first", Source: "admin", Spec: model.TemplateSpec{
		DisplayName: "T", Kind: "jupyterlab", Image: "registry.example.com/t:1", Port: 1,
	}}, 0); err != nil {
		t.Fatal(err)
	}
	events, _ := s.Audit().List(ctx, 1)
	store.ForceNextIDs(t, events[0].ID) // the next audit row reuses an id: its insert fails
	_, err := s.Templates().Put(ctx, alice, model.Template{Name: "second", Source: "admin", Spec: model.TemplateSpec{
		DisplayName: "T", Kind: "jupyterlab", Image: "registry.example.com/t:1", Port: 1,
	}}, 0)
	if err == nil {
		t.Fatal("Put succeeded although its audit row could not be written")
	}
	if _, err := s.Templates().Get(ctx, "second"); err != store.ErrNotFound {
		t.Fatalf("the write survived its failed audit row: %v", err)
	}
}

func TestFailedWriteLeavesNoAuditRowAndNoChange(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "lab")
	drain(s)
	before := auditCount(t, s)
	_, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(*model.App) error { return context.Canceled })
	if err == nil {
		t.Fatal("expected the callback error")
	}
	if auditCount(t, s) != before {
		t.Error("a failed update wrote an audit row")
	}
	requireNoChange(t, s, "a failed update")
}
