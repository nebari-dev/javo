package store_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func labTemplate(source string) model.Template {
	return model.Template{Name: "jupyterlab", Source: source, Spec: model.TemplateSpec{
		DisplayName: "JupyterLab", Kind: "jupyterlab", Image: "registry.example.com/lab:1", Port: 8888,
		AllowedGroups: []string{"data"},
	}}
}

func TestTemplatesPutCreatesThenUpdatesByVersion(t *testing.T) {
	s := storetest.New(t)
	sync := model.Actor{System: "system:sync"}
	tpl := labTemplate("builtin")
	first, err := s.Templates().Put(ctx, sync, tpl, 0)
	if err != nil || first.Version != 1 {
		t.Fatalf("Put = %+v, %v", first, err)
	}
	tpl.Spec.Image = "registry.example.com/lab:2"
	second, err := s.Templates().Put(ctx, sync, tpl, first.Version)
	if err != nil || second.Version != 2 {
		t.Fatalf("second Put = %+v, %v", second, err)
	}
	got, err := s.Templates().Get(ctx, "jupyterlab")
	if err != nil || got.Spec.Image != "registry.example.com/lab:2" || got.Spec.AllowedGroups[0] != "data" || got.Source != "builtin" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := s.Templates().Put(ctx, alice, model.Template{Name: "x", Source: "admin"}, 0); err == nil {
		t.Fatal("an invalid template was stored")
	}
	list, _ := s.Templates().List(ctx)
	if len(list) != 1 {
		t.Fatalf("List = %d", len(list))
	}
	if err := s.Templates().Delete(ctx, alice, "jupyterlab"); err != nil {
		t.Fatal(err)
	}
	if err := s.Templates().Delete(ctx, alice, "jupyterlab"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

// Two admins edit the same template: the second save, made against the
// version both read, must not overwrite the first.
func TestTemplatesPutRejectsStaleVersion(t *testing.T) {
	s := storetest.New(t)
	created, err := s.Templates().Put(ctx, alice, labTemplate("admin"), 0)
	if err != nil {
		t.Fatal(err)
	}
	byAlice := labTemplate("admin")
	byAlice.Spec.Image = "registry.example.com/lab:alice"
	if _, err := s.Templates().Put(ctx, alice, byAlice, created.Version); err != nil {
		t.Fatal(err)
	}
	byBob := labTemplate("admin")
	byBob.Spec.Image = "registry.example.com/lab:bob"
	if _, err := s.Templates().Put(ctx, model.Actor{Person: "bob"}, byBob, created.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale Put = %v, want ErrConflict", err)
	}
	got, _ := s.Templates().Get(ctx, "jupyterlab")
	if got.Spec.Image != "registry.example.com/lab:alice" {
		t.Fatalf("image = %s: the stale save won", got.Spec.Image)
	}
}

func TestTemplatesPutCreateOnlyAndMissing(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.Templates().Put(ctx, alice, labTemplate("admin"), 3); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update of a missing template = %v, want ErrNotFound", err)
	}
	if _, err := s.Templates().Put(ctx, alice, labTemplate("admin"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Templates().Put(ctx, alice, labTemplate("admin"), 0); !errors.Is(err, store.ErrExists) {
		t.Fatalf("create of a taken name = %v, want ErrExists", err)
	}
}

// The chart sync must not take over a template an admin made with the same name.
func TestTemplatesBuiltinPutDoesNotOverwriteAdminTemplate(t *testing.T) {
	s := storetest.New(t)
	adminTpl, err := s.Templates().Put(ctx, alice, labTemplate("admin"), 0)
	if err != nil {
		t.Fatal(err)
	}
	synced := labTemplate("builtin")
	synced.Spec.Image = "registry.example.com/lab:chart"
	sync := model.Actor{System: "system:sync"}
	if _, err := s.Templates().Put(ctx, sync, synced, 0); !errors.Is(err, store.ErrExists) {
		t.Fatalf("builtin create over an admin template = %v, want ErrExists", err)
	}
	if _, err := s.Templates().Put(ctx, sync, synced, adminTpl.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("builtin update of an admin template = %v, want ErrConflict", err)
	}
	got, _ := s.Templates().Get(ctx, "jupyterlab")
	if got.Source != "admin" || got.Spec.Image != "registry.example.com/lab:1" {
		t.Fatalf("admin template changed: %+v", got)
	}
}

func TestTemplatesPutRejectsUnknownSource(t *testing.T) {
	s := storetest.New(t)
	for _, src := range []string{"", "chart"} {
		if _, err := s.Templates().Put(ctx, alice, labTemplate(src), 0); err == nil {
			t.Errorf("source %q accepted", src)
		}
	}
}

func TestLocalAccounts(t *testing.T) {
	s := storetest.New(t)
	acc := s.LocalAccounts()
	if err := acc.Put(ctx, alice, store.LocalAccount{Username: "alice", Hash: "h1", Groups: []string{"admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := acc.Put(ctx, alice, store.LocalAccount{Username: "alice", Hash: "h2", Groups: []string{"admin", "data"}}); err != nil {
		t.Fatal(err)
	}
	if err := acc.Put(ctx, alice, store.LocalAccount{Username: "bob", Hash: "h3"}); err != nil {
		t.Fatal(err)
	}
	got, err := acc.Get(ctx, "alice")
	if err != nil || got.Hash != "h2" || len(got.Groups) != 2 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	list, _ := acc.List(ctx)
	if len(list) != 2 || list[0].Username != "alice" {
		t.Fatalf("List = %+v", list)
	}
	if err := acc.Delete(ctx, alice, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Get(ctx, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after delete = %v", err)
	}
	if err := acc.Delete(ctx, alice, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestUsageStopClosesOnlyOpenIntervals(t *testing.T) {
	s := storetest.New(t)
	u := s.Usage()
	if err := u.Start(ctx, store.UsageInterval{AppID: "a1", Owner: "alice", Template: "jupyterlab", Size: "small", StartedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := u.Stop(ctx, "a1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := u.Stop(ctx, "a1", t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := u.Start(ctx, store.UsageInterval{AppID: "a1", Owner: "alice", Template: "jupyterlab", Size: "large", StartedAt: t0.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := u.ForApp(ctx, "a1")
	if err != nil || len(got) != 2 {
		t.Fatalf("ForApp = %+v, %v", got, err)
	}
	if !got[0].StoppedAt.Equal(t0.Add(time.Hour)) || got[0].Size != "small" || !got[1].StoppedAt.IsZero() || got[1].Size != "large" {
		t.Fatalf("intervals = %+v", got)
	}
}

func TestFactsKeepTheLatestValue(t *testing.T) {
	s := storetest.New(t)
	f := s.Facts()
	if _, err := f.Get(ctx, "netpol"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get of an unset fact = %v", err)
	}
	if err := f.Set(ctx, "netpol", json.RawMessage(`{"enforced":false}`), t0); err != nil {
		t.Fatal(err)
	}
	if err := f.Set(ctx, "netpol", json.RawMessage(`{"enforced":true}`), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := f.Get(ctx, "netpol")
	if err != nil || string(got.Value) != `{"enforced":true}` || !got.ObservedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	all, _ := f.List(ctx)
	if len(all) != 1 {
		t.Fatalf("List = %d", len(all))
	}
}
