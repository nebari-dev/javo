package store_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAppRoundTrip(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := newApp("alice", "lab")
	in.Command, in.Image, in.Port, in.File, in.Sample = "run --x", "registry.example.com/app:1", 8080, "a.py", true
	in.APIAccess = model.APIAccessRead
	in.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}, Groups: []string{"data"}}
	created, err := s.Apps().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Version != 1 || created.CreatedAt.IsZero() {
		t.Fatalf("created = %+v", created)
	}
	got, err := s.Apps().Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Command != in.Command || got.Image != in.Image || got.Port != 8080 || got.File != "a.py" || !got.Sample ||
		got.Size != "small" || got.DisplayName != "lab" || got.APIAccess != model.APIAccessRead {
		t.Errorf("spec lost in the round trip: %+v", got)
	}
	if got.Sharing.Access != model.AccessRestricted || len(got.Sharing.Users) != 1 || got.Sharing.Users[0] != "bob" ||
		len(got.Sharing.Groups) != 1 || got.Sharing.Groups[0] != "data" {
		t.Errorf("sharing = %+v", got.Sharing)
	}
	byName, err := s.Apps().GetByOwnerName(ctx, "alice", "lab")
	if err != nil || byName.ID != created.ID {
		t.Fatalf("GetByOwnerName = %+v, %v", byName, err)
	}
	if _, err := s.Apps().GetByOwnerName(ctx, "bob", "lab"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another owner's app by this name = %v, want ErrNotFound", err)
	}
	if _, err := s.Apps().Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(nope) = %v", err)
	}
}

// A custom CPU and memory is kept exactly, and an app moves between it and
// a named size.
func TestAppCustomResourcesRoundTrip(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := newApp("alice", "custom")
	in.Size = ""
	in.Resources = &model.AppResources{CPU: resource.MustParse("2500m"), Memory: resource.MustParse("6Gi")}
	created, err := s.Apps().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Apps().Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Resources; got.Size != "" || r == nil || r.CPU.Cmp(resource.MustParse("2.5")) != 0 || r.Memory.Cmp(resource.MustParse("6Gi")) != 0 {
		t.Fatalf("custom amount lost in the round trip: size %q, resources %+v", got.Size, got.Resources)
	}
	moved, err := s.Apps().UpdateSpec(ctx, alice, got.ID, got.Version, func(a *model.App) error {
		a.Size, a.Resources = "medium", nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Apps().Get(ctx, moved.ID); got.Size != "medium" || got.Resources != nil {
		t.Fatalf("to medium: size %q, resources %+v", got.Size, got.Resources)
	}
	if listed, _ := s.Apps().List(ctx, "alice", false); len(listed) != 1 || listed[0].Resources != nil {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestCreateAppRejectsInvalidAndDuplicate(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	bad := newApp("alice", "lab")
	bad.Template = ""
	var verr *model.ValidationError
	if _, err := s.Apps().Create(ctx, alice, bad); !errors.As(err, &verr) {
		t.Fatalf("invalid app: err = %v, want a ValidationError", err)
	}
	createApp(t, s, "alice", "lab")
	if _, err := s.Apps().Create(ctx, alice, newApp("alice", "lab")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("duplicate name: err = %v, want ErrExists", err)
	}
}

// Two people may each have an app of the same short name, also in one namespace.
func TestTwoOwnersMayUseTheSameAppName(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := createApp(t, s, "alice", "lab")
	b := createApp(t, s, "bob", "lab")
	if a.ID == b.ID {
		t.Fatal("one row for two owners")
	}
	got, err := s.Apps().GetByOwnerName(ctx, "bob", "lab")
	if err != nil || got.ID != b.ID {
		t.Fatalf("bob's lab = %+v, %v", got, err)
	}
}

func TestUpdateSpecConflict(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, size := range []string{"medium", "large"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
				a.Size = size
				return nil
			})
		}()
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("two updates from version %d: %d succeeded, %d conflicted; want 1 and 1", app.Version, ok, conflicts)
	}
	got, _ := s.Apps().Get(ctx, app.ID)
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
}

func TestUpdateSpecStaleVersionAndCallbackError(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version+5, func(*model.App) error { return nil }); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong version: err = %v", err)
	}
	boom := errors.New("boom")
	if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
		a.Size = "large"
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("callback error = %v", err)
	}
	got, _ := s.Apps().Get(ctx, app.ID)
	if got.Size != "small" || got.Version != 1 {
		t.Fatalf("a failed update changed the app: %+v", got)
	}
	var verr *model.ValidationError
	if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
		a.Owner = "bob"
		return nil
	}); !errors.As(err, &verr) {
		t.Fatalf("changing the owner: err = %v, want a ValidationError", err)
	}
}

func TestSetStatusKeepsSpec(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	edited, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
		a.Size = "large"
		a.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The controller reconciled version 1 and reports it after the edit.
	st := model.AppStatus{Phase: "Running", URL: "/u/alice/lab/", Conditions: []metav1.Condition{{Type: "Ready", Status: "True", Reason: "Up"}}}
	if err := s.Apps().SetStatus(ctx, app.ID, 1, st); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Apps().Get(ctx, app.ID)
	if got.Size != "large" || got.Version != edited.Version || got.Sharing.Users[0] != "bob" {
		t.Errorf("status write undid the spec edit: %+v", got)
	}
	if got.Status.Phase != "Running" || got.Status.ObservedVersion != 1 || len(got.Status.Conditions) != 1 {
		t.Errorf("status = %+v", got.Status)
	}
	if err := s.Apps().SetStatus(ctx, "nope", 1, st); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetStatus of a missing app = %v", err)
	}
}

func TestListOwnerNewestFirstWithoutDeleted(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := createApp(t, s, "alice", "first")
	createApp(t, s, "alice", "second")
	createApp(t, s, "bob", "other")
	c := createApp(t, s, "alice", "third")
	if err := s.Apps().MarkDeleted(ctx, alice, c.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Apps().List(ctx, "alice", false)
	if err != nil || len(got) != 2 || got[0].Name != "second" || got[1].ID != a.ID {
		t.Fatalf("List(alice) = %v, %v; want second then first", names(got), err)
	}
	all, _ := s.Apps().ListAll(ctx)
	if len(all) != 4 {
		t.Fatalf("ListAll = %d apps, want 4 including the deleted one", len(all))
	}
	if all[1].DeletedAt.IsZero() && all[0].DeletedAt.IsZero() {
		t.Errorf("ListAll lost DeletedAt: %v", names(all))
	}
}

func TestAppSourceRoundTrip(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := newApp("alice", "site")
	in.File = "app.py"
	in.Source = &model.Source{
		URL: "https://github.com/example-org/site", Ref: "main", Path: "web",
		Build:    &model.SourceBuild{Command: "npm ci && npm run build", Output: "dist"},
		Previews: &model.Previews{Enabled: true, Max: 5, Forks: true},
	}
	created, err := s.Apps().Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Apps().Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Source, in.Source) || got.ParentID != "" || got.PullNumber != 0 {
		t.Fatalf("source = %+v, parent %q, pull %d", got.Source, got.ParentID, got.PullNumber)
	}
	if err := s.Apps().SetStatus(ctx, got.ID, 1, model.AppStatus{Phase: "Running", Commit: "0123456789abcdef0123456789abcdef01234567"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Apps().Get(ctx, got.ID); got.Status.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("commit lost: %+v", got.Status)
	}
	// The one validation the API and the controller share also guards the store.
	bad := newApp("alice", "bad")
	bad.Source = &model.Source{URL: "http://github.com/example-org/site"}
	var ve *model.ValidationError
	if _, err := s.Apps().Create(ctx, alice, bad); !errors.As(err, &ve) {
		t.Fatalf("Create with an http source = %v, want a validation error", err)
	}
}

// A preview is an app row that points at its parent; the owner's lists do not
// show it unless asked, the controller's resync always does.
func TestListsHidePreviewRowsUnlessAsked(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	parent := createApp(t, s, "alice", "site")
	child := putPreview(t, s, parent, 7, "aaaa")
	if _, err := s.Apps().UpdateSpec(ctx, model.Actor{System: model.SystemController}, child.ID, child.Version, func(a *model.App) error {
		a.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Apps().List(ctx, "alice", false); len(got) != 1 || got[0].ID != parent.ID {
		t.Fatalf("List(false) = %v, want only the parent", names(got))
	}
	if got, _ := s.Apps().List(ctx, "alice", true); len(got) != 2 {
		t.Fatalf("List(true) = %v, want parent and preview", names(got))
	}
	if got, _ := s.Apps().ListShared(ctx, "bob", nil, false); len(got) != 0 {
		t.Fatalf("ListShared(false) = %v, want none", names(got))
	}
	if got, _ := s.Apps().ListShared(ctx, "bob", nil, true); len(got) != 1 || got[0].ID != child.ID {
		t.Fatalf("ListShared(true) = %v, want the preview", names(got))
	}
	if got, _ := s.Apps().ListAll(ctx); len(got) != 2 {
		t.Fatalf("ListAll = %v, want both", names(got))
	}
	if child.ParentID != parent.ID || child.PullNumber != 7 {
		t.Fatalf("child = %+v", child)
	}
}

func TestMarkDeletedOnAParentMarksItsPreviewsInTheSameTransaction(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	parent := createApp(t, s, "alice", "site")
	c1 := putPreview(t, s, parent, 1, "aaaa")
	c2 := putPreview(t, s, parent, 2, "bbbb")
	other := createApp(t, s, "alice", "unrelated")
	drain(s)

	if err := s.Apps().MarkDeleted(ctx, alice, parent.ID); err != nil {
		t.Fatal(err)
	}
	announced := map[string]bool{}
	for range 3 {
		c := next(t, s)
		if c.Table != "apps" {
			t.Fatalf("change = %+v", c)
		}
		announced[c.ID] = true
	}
	if len(announced) != 3 || !announced[parent.ID] || !announced[c1.ID] || !announced[c2.ID] {
		t.Fatalf("announced %v, want the parent and both previews", announced)
	}
	for _, id := range []string{parent.ID, c1.ID, c2.ID} {
		if got, _ := s.Apps().Get(ctx, id); got.DeletedAt.IsZero() {
			t.Errorf("%s not marked deleted", id)
		}
	}
	if got, _ := s.Apps().Get(ctx, other.ID); !got.DeletedAt.IsZero() {
		t.Error("an unrelated app was marked deleted")
	}

	// The parent goes last.
	if err := s.Apps().Purge(ctx, alice, parent.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("purge of the parent with previews = %v, want ErrConflict", err)
	}
	if err := s.Apps().Purge(ctx, alice, c1.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, parent.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("purge with one preview left = %v, want ErrConflict", err)
	}
	if err := s.Apps().Purge(ctx, alice, c2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, parent.ID); err != nil {
		t.Fatalf("purge after the previews: %v", err)
	}
}

func names(apps []model.App) []string {
	out := make([]string, len(apps))
	for i, a := range apps {
		out[i] = a.Name
	}
	return out
}

func TestListSharedByGroup(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	share := func(name string, sh model.Sharing) {
		app := createApp(t, s, "alice", name)
		if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, app.Version, func(a *model.App) error {
			a.Sharing = sh
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	share("to-data", model.Sharing{Access: model.AccessRestricted, Groups: []string{"data"}})
	share("to-ops", model.Sharing{Access: model.AccessRestricted, Groups: []string{"ops"}})
	share("to-bob", model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}})
	share("to-both", model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}, Groups: []string{"data"}})
	createApp(t, s, "alice", "private")
	own := createApp(t, s, "bob", "bobs-own")
	if _, err := s.Apps().UpdateSpec(ctx, model.Actor{Person: "bob"}, own.ID, own.Version, func(a *model.App) error {
		a.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Apps().ListShared(ctx, "bob", []string{"data"}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"to-data": true, "to-bob": true, "to-both": true}
	if len(got) != len(want) {
		t.Fatalf("shared = %v, want %v", names(got), want)
	}
	for _, a := range got {
		if !want[a.Name] {
			t.Errorf("%s listed as shared with bob in group data", a.Name)
		}
	}
	if got[0].Name != "to-both" {
		t.Errorf("shared apps not newest first: %v", names(got))
	}
}

func TestPurgeNeedsMarkedDeleted(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	if err := s.Apps().Purge(ctx, alice, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purge of a live app = %v, want ErrNotFound", err)
	}
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	marked, _ := s.Apps().Get(ctx, app.ID)
	if marked.DeletedAt.IsZero() || marked.Version != 2 {
		t.Fatalf("marked = %+v", marked)
	}
	if _, err := s.Apps().UpdateSpec(ctx, alice, app.ID, 2, func(*model.App) error { return nil }); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("editing a deleted app = %v, want ErrNotFound", err)
	}
	// A deleted app still holds its name until purged.
	if _, err := s.Apps().Create(ctx, alice, newApp("alice", "lab")); !errors.Is(err, store.ErrExists) {
		t.Errorf("reusing the name of a deleted app = %v, want ErrExists", err)
	}
	if err := s.Apps().Purge(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apps().Get(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged app still there: %v", err)
	}
	createApp(t, s, "alice", "lab")
}

// An app is found by the name its Pod and Service carry, which is what an
// app's own host spells.
func TestAppIsFoundByItsObjectName(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a := createApp(t, s, "alice", "lab")
	b := createApp(t, s, "bob", "lab")
	got, err := s.Apps().GetByObjectName(ctx, b.ObjectName())
	if err != nil || got.ID != b.ID {
		t.Fatalf("GetByObjectName(%q) = %+v, %v", b.ObjectName(), got, err)
	}
	if a.ObjectName() == b.ObjectName() {
		t.Fatalf("two owners share the object name %q", a.ObjectName())
	}
	if _, err := s.Apps().GetByObjectName(ctx, "nobody-lab"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown object name = %v, want ErrNotFound", err)
	}
}

// Revoking an app's API token leaves the id on the app row and wakes the
// controller; it changes neither the spec nor the version.
func TestRevokedAppTokenIsRecordedAndAnnounced(t *testing.T) {
	t.Parallel()
	s, _ := startedStore(t)
	app := createApp(t, s, "alice", "lab")
	drain(s)
	before := auditCount(t, s)
	if err := s.Apps().NoteTokenRevoked(ctx, model.Actor{Person: "alice"}, app.ID, "tok1"); err != nil {
		t.Fatal(err)
	}
	events, _ := s.Audit().List(ctx, 100)
	if len(events) != before+1 {
		t.Fatalf("revoke left %d audit rows, want 1", len(events)-before)
	}
	if e := events[0]; e.Action != "app.token_revoked" || e.ObjectKind != "app" || e.ObjectID != app.ID ||
		e.ObjectName != app.Name || e.ObjectNamespace != app.Namespace || e.ObjectOwner != app.Owner || e.Actor != "alice" {
		t.Fatalf("audit row = %+v", e)
	}
	if c := next(t, s); c.Table != "apps" || c.ID != app.ID {
		t.Fatalf("change = %+v, want apps/%s", c, app.ID)
	}
	got, err := s.Apps().Get(ctx, app.ID)
	if err != nil || got.TokenRevoked != "tok1" || got.Version != app.Version {
		t.Fatalf("app = %+v, %v", got, err)
	}
	if err := s.Apps().NoteTokenRevoked(ctx, model.Actor{Person: "alice"}, "no-such-app", "tok2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown app = %v, want ErrNotFound", err)
	}
}

func TestSetStatusOpensAndClosesUsageIntervals(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	app := createApp(t, s, "alice", "lab")
	set := func(phase string) {
		t.Helper()
		if err := s.Apps().SetStatus(ctx, app.ID, 1, model.AppStatus{Phase: phase}); err != nil {
			t.Fatal(err)
		}
	}
	intervals := func() []store.UsageInterval {
		t.Helper()
		got, err := s.Usage().ForApp(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	set("Starting")
	if n := len(intervals()); n != 0 {
		t.Fatalf("%d intervals before the app ran", n)
	}
	set("Running")
	set("Running")
	got := intervals()
	if len(got) != 1 || !got[0].StoppedAt.IsZero() || got[0].Owner != "alice" || got[0].Template != "jupyterlab" || got[0].Size != "small" {
		t.Fatalf("after Running twice: %+v", got)
	}
	set("Failed")
	set("Failed")
	if got = intervals(); len(got) != 1 || got[0].StoppedAt.IsZero() {
		t.Fatalf("after Failed: %+v", got)
	}
	set("Running")
	if got = intervals(); len(got) != 2 || !got[1].StoppedAt.IsZero() {
		t.Fatalf("after Running again: %+v", got)
	}
}
