package files_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// --- file helper lifecycle ---

const homeVolume = "javo-javo-apps.home-" + owner

// fakeExec is an executor that keeps what it was asked to run, and says
// of each workload what a test tells it to.
type fakeExec struct {
	mu      sync.Mutex
	specs   map[string]workload.Spec
	states  map[string]*workload.State
	stopped []string
	now     func() time.Time
	// logins is where it reads values from, as an executor does.
	logins executor.Secrets
}

var _ executor.Executor = (*fakeExec)(nil)

func (x *fakeExec) Start(_ context.Context, s workload.Spec) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.specs[s.Ref.Name]; ok {
		return nil
	}
	if x.specs == nil {
		x.specs, x.states = map[string]workload.Spec{}, map[string]*workload.State{}
	}
	x.specs[s.Ref.Name] = s
	x.states[s.Ref.Name] = &workload.State{Phase: "Starting", Fingerprint: s.Fingerprint, Created: x.now()}
	return nil
}

func (x *fakeExec) Stop(_ context.Context, ref workload.Ref) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, ok := x.specs[ref.Name]; ok {
		x.stopped = append(x.stopped, ref.Name)
	}
	delete(x.specs, ref.Name)
	delete(x.states, ref.Name)
	return nil
}

func (x *fakeExec) Observe(_ context.Context, ref workload.Ref) (*workload.State, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if st := x.states[ref.Name]; st != nil {
		return new(*st), nil
	}
	return nil, nil
}

func (x *fakeExec) List(context.Context) ([]workload.Ref, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []workload.Ref
	for _, s := range x.specs {
		out = append(out, s.Ref)
	}
	return out, nil
}

func (x *fakeExec) Watch(context.Context, func(workload.Ref)) error { return nil }

func (x *fakeExec) Addr(_ context.Context, ref workload.Ref, port int32) (string, error) {
	return "javo-" + ref.Namespace + "." + ref.Name + ":5572", nil
}

func (x *fakeExec) Logs(context.Context, workload.Ref, executor.LogOptions) (io.ReadCloser, error) {
	return nil, nil
}

// only is the one workload it runs.
func (x *fakeExec) only(t *testing.T) workload.Spec {
	t.Helper()
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.specs) != 1 {
		t.Fatalf("the executor runs %d workloads, want one", len(x.specs))
	}
	for _, s := range x.specs {
		return s
	}
	return workload.Spec{}
}

// phase makes the workload called name report phase.
func (x *fakeExec) phase(name, phase string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.states[name].Phase = phase
}

// password is what the executor would give the helper s as rclone's
// password: the value its variable names, read where an executor reads.
func (x *fakeExec) password(t *testing.T, s workload.Spec) string {
	t.Helper()
	for _, e := range s.Main.Env {
		if e.Name != "RCLONE_RC_PASS" {
			continue
		}
		if e.Value != "" || e.From == nil {
			t.Fatalf("the password is part of the plan: %+v", e)
		}
		b, err := x.logins.Get(context.Background(), s.Ref, e.From.Bundle)
		if err != nil || b == nil {
			t.Fatalf("the helper's bundle: %v, %v", b, err)
		}
		return string(b.Data[e.From.Key])
	}
	t.Fatal("the helper has no password variable")
	return ""
}

// volumesOf says whose home each volume is.
type volumesOf map[string]string

func (v volumesOf) Owned(_ context.Context, _, owner, name string) (bool, bool, error) {
	who, ok := v[name]
	return ok, who == owner, nil
}

// dockerHome is a prepared home on a named volume.
func dockerHome(who string) *model.Home {
	return &model.Home{Spec: model.HomeSpec{Owner: who}, Status: model.HomeStatus{ClaimName: "javo-javo-apps." + naming.HomeClaimName(who), Prepared: true}}
}

type helperOpts struct {
	storage files.Storage
	uids    files.UserIDs
	volumes volumesOf
	clk     *clock
}

// newHelpers is a file helper manager over a fake executor; alice's home
// is a prepared volume of hers unless o says otherwise.
func newHelpers(t *testing.T, o helperOpts) (*files.Helpers, *fakeExec) {
	t.Helper()
	if o.storage == nil {
		o.storage = stubStorage{podstorage.Input{Home: dockerHome(owner)}}
	}
	if o.uids == nil {
		o.uids = fixedUID(ownerUID)
	}
	if o.volumes == nil {
		o.volumes = volumesOf{homeVolume: owner}
	}
	if o.clk == nil {
		o.clk = &clock{time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
	}
	logins := files.NewLogins()
	x := &fakeExec{now: o.clk.Now, logins: logins}
	h, err := files.NewHelpers(files.HelpersConfig{
		Exec: x, Logins: logins, Volumes: o.volumes, Namespace: ns, Image: image,
		UIDs: o.uids, Storage: o.storage, Now: o.clk.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, x
}

// started is alice's helper, started by a first Ensure.
func started(t *testing.T, h *files.Helpers, x *fakeExec) workload.Spec {
	t.Helper()
	if _, err := h.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("first Ensure = %v, want ErrHomeStarting", err)
	}
	return x.only(t)
}

func TestNewHelpersRequiresItsParts(t *testing.T) {
	if _, err := files.NewHelpers(files.HelpersConfig{Namespace: ns, Image: image}); err == nil {
		t.Fatal("helpers were made without an executor, logins, volumes, uids and storage")
	}
}

// The first look at a home starts a helper for it: rclone as the owner,
// with the home volume writable and nothing else of anyone's, a root it
// cannot write and a memory limit.
func TestEnsureStartsAConfinedHelperForTheHome(t *testing.T) {
	h, x := newHelpers(t, helperOpts{})
	s := started(t, h, x)

	if s.Ref.Namespace != ns || s.Ref.Owner != owner || s.Ref.ID != "" {
		t.Errorf("ref = %+v", s.Ref)
	}
	// No app can have the helper's name: an app's is "<person>-<app>".
	if !strings.HasPrefix(s.Ref.Name, "files--") || !strings.HasSuffix(s.Ref.Name, owner) {
		t.Errorf("name = %q", s.Ref.Name)
	}
	if s.User.UID != ownerUID || s.User.GID != 100 {
		t.Errorf("user = %+v, want the owner's ids", s.User)
	}
	if s.Main.Image != image || !s.Main.ReadOnlyRoot || s.Port != 5572 || s.Batch != nil || len(s.Steps) != 0 {
		t.Errorf("main = %+v, port %d", s.Main, s.Port)
	}
	if got := s.Resources.Limits.Memory().String(); got != "256Mi" {
		t.Errorf("memory limit = %s", got)
	}
	var sources []string
	for _, v := range s.Volumes {
		if v.Source != "" {
			sources = append(sources, v.Source)
			if v.ReadOnly {
				t.Errorf("the home volume is read-only: %+v", v)
			}
		}
	}
	if !slices.Equal(sources, []string{homeVolume}) {
		t.Errorf("storage mounted = %v, want only %s", sources, homeVolume)
	}
	if m, ok := s.Main.MountAt(podstorage.HomeDir); !ok || m.Volume != podstorage.HomeVolume || m.ReadOnly || m.SubPath != "" {
		t.Errorf("home mount = %+v, %v", m, ok)
	}
}

// Once the helper answers, Ensure gives its address with the login the
// helper asks for. The password is the helper's own, random, and no part
// of its plan (which is kept on the container as a label).
func TestEnsureGivesTheAddressAndLoginOfARunningHelper(t *testing.T) {
	h, x := newHelpers(t, helperOpts{})
	s := started(t, h, x)
	pass := x.password(t, s)
	if len(pass) < 43 {
		t.Fatalf("password of %d characters, want 256 random bits", len(pass))
	}
	plan, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plan), pass) {
		t.Fatal("the plan holds the password")
	}

	if _, err := h.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("Ensure of a helper that does not answer yet = %v", err)
	}
	x.phase(s.Ref.Name, "Running")
	addr, err := h.Ensure(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := u.User.Password(); u.User.Username() != "javo" || got != pass || u.Host != "javo-javo-apps."+s.Ref.Name+":5572" {
		t.Errorf("address = %s@%s with a password that is the helper's: %v", u.User.Username(), u.Host, got == pass)
	}
	if len(x.stopped) != 0 {
		t.Errorf("stopped %v", x.stopped)
	}
}

func TestEachHelperHasItsOwnPassword(t *testing.T) {
	bob := "bob-bbbbbbbb"
	h, x := newHelpers(t, helperOpts{
		volumes: volumesOf{homeVolume: owner, "javo-javo-apps.home-" + bob: bob},
		storage: perOwner{},
	})
	ctx := context.Background()
	for _, who := range []string{owner, bob} {
		if _, err := h.Ensure(ctx, who); !errors.Is(err, files.ErrHomeStarting) {
			t.Fatal(err)
		}
	}
	x.mu.Lock()
	specs := x.specs
	x.mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("%d helpers", len(specs))
	}
	seen := map[string]bool{}
	for _, s := range specs {
		seen[x.password(t, s)] = true
		for _, v := range s.Volumes {
			if v.Source != "" && v.Source != "javo-javo-apps.home-"+s.Ref.Owner {
				t.Errorf("%s mounts %s", s.Ref.Owner, v.Source)
			}
		}
	}
	if len(seen) != 2 {
		t.Error("two helpers share a password")
	}
}

// perOwner gives everyone a prepared home on a volume of their own.
type perOwner struct{}

func (perOwner) Input(_ context.Context, _, who string) (podstorage.Input, error) {
	return podstorage.Input{Home: dockerHome(who)}, nil
}

func (perOwner) Home(_ context.Context, _, who string) (model.Home, error) {
	return *dockerHome(who), nil
}

func TestHelpersAskForTheHomeOfAPersonWithoutOne(t *testing.T) {
	st := &homelessStorage{}
	h, x := newHelpers(t, helperOpts{storage: st})
	if _, err := h.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
		t.Fatalf("err = %v", err)
	}
	if st.asked != owner || len(x.specs) != 0 {
		t.Errorf("asked for %q, started %d", st.asked, len(x.specs))
	}
}

// Nothing mounts a home before it was handed to its owner, a volume that
// is not there, or one whose owner has no uid yet.
func TestHelpersWaitForAHomeThatIsNotReady(t *testing.T) {
	unprepared := dockerHome(owner)
	unprepared.Status.Prepared = false
	for what, o := range map[string]helperOpts{
		"not prepared":     {storage: stubStorage{podstorage.Input{Home: unprepared}}},
		"no volume":        {volumes: volumesOf{"other": owner}},
		"no uid":           {uids: noUID{}},
		"no volume on row": {storage: stubStorage{podstorage.Input{Home: &model.Home{Spec: model.HomeSpec{Owner: owner}}}}},
	} {
		h, x := newHelpers(t, o)
		if _, err := h.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeStarting) {
			t.Errorf("%s: err = %v, want ErrHomeStarting", what, err)
		}
		if len(x.specs) != 0 {
			t.Errorf("%s: a helper was started", what)
		}
	}
}

// A volume that is not labelled as this person's home is never mounted,
// whatever the home's row says.
func TestHelpersNeverMountAnotherPersonsVolume(t *testing.T) {
	foreign := dockerHome(owner)
	foreign.Status.Conditions = []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionFalse, Reason: string(reasons.HomeClaimForeign)}}
	bobs := dockerHome(owner)
	bobs.Status.ClaimName = "javo-javo-apps.home-bob-bbbbbbbb"
	for what, o := range map[string]helperOpts{
		"the volume is someone else's":   {volumes: volumesOf{homeVolume: "bob-bbbbbbbb"}},
		"the row names someone's volume": {storage: stubStorage{podstorage.Input{Home: bobs}}, volumes: volumesOf{homeVolume: owner, bobs.Status.ClaimName: "bob-bbbbbbbb"}},
		"the home says so":               {storage: stubStorage{podstorage.Input{Home: foreign}}},
	} {
		h, x := newHelpers(t, o)
		if _, err := h.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeConflict) {
			t.Errorf("%s: err = %v, want ErrHomeConflict", what, err)
		}
		if len(x.specs) != 0 {
			t.Errorf("%s: a helper was started", what)
		}
	}
}

// A host has no group folders on Docker, so one asked for is not found.
func TestHelpersHaveNoGroupFolders(t *testing.T) {
	h, x := newHelpers(t, helperOpts{})
	if _, err := h.EnsureGroup(context.Background(), owner, "team"); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(x.specs) != 0 {
		t.Error("a helper was started")
	}
}

// A helper this gateway holds no password of (one an earlier gateway
// left), one made for other ids, and one that failed are replaced.
func TestHelpersReplaceAHelperTheyCannotUse(t *testing.T) {
	ctx := context.Background()
	t.Run("no password", func(t *testing.T) {
		h, x := newHelpers(t, helperOpts{})
		s := started(t, h, x)
		x.phase(s.Ref.Name, "Running")
		// The next gateway knows the executor's workloads and no password.
		logins := files.NewLogins()
		x.logins = logins
		next, err := files.NewHelpers(files.HelpersConfig{Exec: x, Logins: logins, Volumes: volumesOf{homeVolume: owner}, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{podstorage.Input{Home: dockerHome(owner)}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := next.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) || !slices.Equal(x.stopped, []string{s.Ref.Name}) {
			t.Fatalf("err = %v, stopped %v", err, x.stopped)
		}
		if _, err := next.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) || len(x.specs) != 1 {
			t.Fatalf("err = %v with %d helpers; want a new one starting", err, len(x.specs))
		}
	})
	t.Run("other ids", func(t *testing.T) {
		logins := files.NewLogins()
		x := &fakeExec{now: time.Now, logins: logins}
		cfg := files.HelpersConfig{Exec: x, Logins: logins, Volumes: volumesOf{homeVolume: owner}, Namespace: ns, Image: image, UIDs: fixedUID(ownerUID), Storage: stubStorage{podstorage.Input{Home: dockerHome(owner)}}}
		h, _ := files.NewHelpers(cfg)
		s := started(t, h, x)
		x.phase(s.Ref.Name, "Running")
		cfg.UIDs = fixedUID(ownerUID + 1)
		h, _ = files.NewHelpers(cfg)
		if _, err := h.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) || len(x.stopped) != 1 {
			t.Fatalf("err = %v, stopped %v", err, x.stopped)
		}
	})
	t.Run("failed", func(t *testing.T) {
		h, x := newHelpers(t, helperOpts{})
		s := started(t, h, x)
		x.phase(s.Ref.Name, "Failed")
		if _, err := h.Ensure(ctx, owner); !errors.Is(err, files.ErrHomeStarting) || len(x.stopped) != 1 {
			t.Fatalf("err = %v, stopped %v", err, x.stopped)
		}
	})
}

// A home being restored or moved is held: its helper goes, and none
// starts.
func TestHelpersLetGoOfAHeldHome(t *testing.T) {
	held := dockerHome(owner)
	st := &swapStorage{home: dockerHome(owner)}
	h, x := newHelpers(t, helperOpts{storage: st})
	started(t, h, x)
	held.Status.Conditions = []metav1.Condition{{Type: model.ConditionRestoring, Status: metav1.ConditionTrue, Reason: "Restoring"}}
	st.home = held
	for range 2 {
		if _, err := h.Ensure(context.Background(), owner); !errors.Is(err, files.ErrHomeBusy) {
			t.Fatalf("err = %v, want ErrHomeBusy", err)
		}
	}
	if len(x.specs) != 0 || len(x.stopped) != 1 {
		t.Errorf("%d helpers, stopped %v", len(x.specs), x.stopped)
	}
}

// swapStorage answers with the home it holds now.
type swapStorage struct{ home *model.Home }

func (s *swapStorage) Input(context.Context, string, string) (podstorage.Input, error) {
	return podstorage.Input{Home: s.home}, nil
}
func (s *swapStorage) Home(context.Context, string, string) (model.Home, error) { return *s.home, nil }

// An idle helper is stopped; one in use, and a workload that is no file
// helper, stay.
func TestHelpersReapIdleHelpersOnly(t *testing.T) {
	start := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	clk := &clock{start}
	bob := "bob-bbbbbbbb"
	h, x := newHelpers(t, helperOpts{clk: clk, storage: perOwner{}, volumes: volumesOf{homeVolume: owner, "javo-javo-apps.home-" + bob: bob}})
	ctx := context.Background()
	for _, who := range []string{owner, bob} {
		if _, err := h.Ensure(ctx, who); !errors.Is(err, files.ErrHomeStarting) {
			t.Fatal(err)
		}
	}
	// Something that is not a file helper, as old as can be.
	app := workload.Spec{Ref: workload.Ref{Namespace: ns, Name: bob + "-sales", Owner: bob, ID: "app-1"}}
	clk.t = start.Add(-time.Hour)
	if err := x.Start(ctx, app); err != nil {
		t.Fatal(err)
	}

	// Alice looks at her files 9 minutes in; bob does not.
	clk.t = start.Add(9 * time.Minute)
	h.Touch(owner)
	clk.t = start.Add(11 * time.Minute)
	if err := h.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.stopped, []string{"files--" + bob}) {
		t.Fatalf("stopped %v, want only bob's helper", x.stopped)
	}
	clk.t = start.Add(20 * time.Minute)
	if err := h.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if len(x.stopped) != 2 || x.only(t).Ref.Name != app.Ref.Name {
		t.Fatalf("stopped %v, want both helpers and never the app", x.stopped)
	}
}

// Release stops the person's helper at once, and its password is known
// to no one any more.
func TestHelpersReleaseStopsTheHelperAndForgetsItsPassword(t *testing.T) {
	h, x := newHelpers(t, helperOpts{})
	s := started(t, h, x)
	env := s.Main.Env[slices.IndexFunc(s.Main.Env, func(e workload.Env) bool { return e.From != nil })]
	if err := h.Release(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.stopped, []string{s.Ref.Name}) {
		t.Fatalf("stopped %v", x.stopped)
	}
	if b, err := x.logins.Get(context.Background(), s.Ref, env.From.Bundle); err != nil || b != nil {
		t.Errorf("the password is still kept: %v, %v", b != nil, err)
	}
	// Releasing what does not run is not an error.
	if err := h.Release(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
}

// Logins hold file helpers' passwords and nothing an executor is given
// to keep.
func TestLoginsKeepNothingTheyAreHanded(t *testing.T) {
	l := files.NewLogins()
	ref := workload.Ref{Namespace: ns, Name: "alice-sales"}
	if err := l.Put(context.Background(), ref, "token", executor.Bundle{Data: map[string][]byte{"k": []byte("v")}}); err == nil {
		t.Error("a bundle was accepted")
	}
	if b, err := l.Get(context.Background(), ref, "token"); err != nil || b != nil {
		t.Errorf("Get = %v, %v", b, err)
	}
}

// A gateway that ends stops every helper it could have served: their
// passwords were only in its memory, so the next gateway would replace
// each one anyway. Nothing that is not a file helper is stopped.
func TestHelpersEndStopsEveryHelperAndNothingElse(t *testing.T) {
	bob := "bob-bbbbbbbb"
	h, x := newHelpers(t, helperOpts{storage: perOwner{}, volumes: volumesOf{homeVolume: owner, "javo-javo-apps.home-" + bob: bob}})
	ctx := context.Background()
	for _, who := range []string{owner, bob} {
		if _, err := h.Ensure(ctx, who); !errors.Is(err, files.ErrHomeStarting) {
			t.Fatal(err)
		}
	}
	if err := x.Start(ctx, workload.Spec{Ref: workload.Ref{Namespace: ns, Name: bob + "-sales", Owner: bob, ID: "app-1"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.End(ctx); err != nil {
		t.Fatal(err)
	}
	slices.Sort(x.stopped)
	if want := []string{"files--" + owner, "files--" + bob}; !slices.Equal(x.stopped, want) {
		t.Fatalf("stopped %v, want %v", x.stopped, want)
	}
}
