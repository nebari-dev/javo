package preview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	bg    = context.Background()
	alice = model.Actor{Person: "alice"}
	t0    = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
)

// fakeHost is a code host whose open pull requests the test sets.
type fakeHost struct {
	mu     sync.Mutex
	pulls  []codehost.PullRequest
	err    error
	calls  int
	tokens []string
}

func (*fakeHost) ID() string                                          { return "fake" }
func (*fakeHost) Owns(url string) bool                                { return url == repoURL }
func (*fakeHost) VerifyWebhook(*http.Request, []byte) (string, error) { return "", nil }

func (h *fakeHost) OpenPulls(_ context.Context, _, token string) ([]codehost.PullRequest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	h.tokens = append(h.tokens, token)
	if h.err != nil {
		return nil, h.err
	}
	return slices.Clone(h.pulls), nil
}

func (h *fakeHost) set(pulls ...codehost.PullRequest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pulls, h.err = pulls, nil
}

func (h *fakeHost) fail(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = err
}

func (h *fakeHost) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// pr is open pull request n, updated n minutes after t0 (so a higher number is newer).
func pr(n int32, sha string) codehost.PullRequest {
	return codehost.PullRequest{Number: n, Title: fmt.Sprintf("change %d", n), HeadSHA: sha, UpdatedAt: t0.Add(time.Duration(n) * time.Minute)}
}

type fixture struct {
	t    *testing.T
	now  time.Time
	db   *store.Store
	host *fakeHost
	rec  *Reconciler
	app  model.App
}

func newFixture(t *testing.T, previews model.Previews) *fixture {
	t.Helper()
	db := storetest.New(t)
	host := &fakeHost{}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, host: host, now: t0}
	f.rec = &Reconciler{
		Store: db, Hosts: []codehost.Host{host}, Secrets: fake.NewClientBuilder().WithScheme(scheme).Build(),
		PollInterval: 2 * time.Minute, MaxPerApp: model.MaxPreviewMax, Now: func() time.Time { return f.now },
		Enabled: true,
	}
	f.app = f.create("site", &previews)
	return f
}

func (f *fixture) create(name string, previews *model.Previews) model.App {
	f.t.Helper()
	app, err := f.db.Apps().Create(bg, alice, &model.App{
		Namespace: "javo-apps", Name: name, Owner: "alice", Template: "streamlit", Size: "small", File: "app.py",
		Source: &model.Source{URL: repoURL, Ref: "main", Previews: previews},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return app
}

// later moves the clock one poll interval on, so the next sync asks the host.
func (f *fixture) later() { f.now = f.now.Add(f.rec.PollInterval) }

func (f *fixture) sync() reconcile.Result { return f.syncApp(f.app.ID) }

func (f *fixture) syncApp(id string) reconcile.Result {
	f.t.Helper()
	res, err := f.rec.Reconcile(bg, reconcile.Request{NamespacedName: types.NamespacedName{Name: id}})
	if err != nil {
		f.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (f *fixture) children() map[int32]model.App {
	f.t.Helper()
	kids, err := f.db.Previews().Children(bg, f.app.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[int32]model.App{}
	for _, k := range kids {
		out[k.PullNumber] = k
	}
	return out
}

// live is the pull request numbers that have a preview not marked deleted.
func (f *fixture) live() []int32 {
	f.t.Helper()
	var out []int32
	for n, k := range f.children() {
		if k.DeletedAt.IsZero() {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

func (f *fixture) lastSync() model.PreviewSync {
	f.t.Helper()
	s, _, err := f.db.Previews().Sync(bg, f.app.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func reasonsOf(items []model.PreviewItem) map[int32]string {
	out := map[int32]string{}
	for _, it := range items {
		out[it.Number] = it.Reason
	}
	return out
}

func TestOnlyTheNewestPullRequestsGetAPreview(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	for n := int32(1); n <= 7; n++ {
		f.host.pulls = append(f.host.pulls, pr(n, fmt.Sprintf("sha%d", n)))
	}
	res := f.sync()
	if res.RequeueAfter != 2*time.Minute {
		t.Errorf("requeue = %v, want the poll interval", res.RequeueAfter)
	}
	if got := f.live(); !slices.Equal(got, []int32{5, 6, 7}) {
		t.Fatalf("previews for %v, want the 3 newest (5 6 7)", got)
	}
	sync := f.lastSync()
	if sync.Reason != "" || len(sync.Items) != 7 {
		t.Fatalf("sync = %+v", sync)
	}
	limited := 0
	for _, it := range sync.Items {
		switch {
		case it.Reason == string(reasons.PreviewLimitReached) && it.App == "":
			limited++
		case it.Reason == "" && it.App == fmt.Sprintf("site--pr-%d", it.Number) && it.HeadSHA == fmt.Sprintf("sha%d", it.Number):
		default:
			t.Errorf("item %+v", it)
		}
	}
	if limited != 4 {
		t.Fatalf("%d items with PreviewLimitReached, want 4", limited)
	}
	if kid := f.children()[7]; kid.Source.Ref != "sha7" || kid.ParentID != f.app.ID {
		t.Fatalf("child = %+v", kid)
	}
}

func TestThePlatformCapLowersTheAppsMax(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 5})
	f.rec.MaxPerApp = 2
	f.host.set(pr(1, "a"), pr(2, "b"), pr(3, "c"))
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{2, 3}) {
		t.Fatalf("previews for %v, want 2 and 3", got)
	}
}

func TestAClosedPullRequestsPreviewIsMarkedDeleted(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	f.host.set(pr(2, "b"))
	f.later()
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{2}) {
		t.Fatalf("live previews %v, want only 2", got)
	}
	if f.children()[1].DeletedAt.IsZero() {
		t.Fatal("the closed pull request's row is not marked deleted")
	}
	f.sync()
	if got := f.lastSync(); len(got.Items) != 1 || got.Items[0].Number != 2 {
		t.Fatalf("sync items = %+v", got.Items)
	}
}

func TestANewHeadCommitMovesThePreview(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(4, "old"))
	f.sync()
	before := f.children()[4]
	f.host.set(pr(4, "new"))
	f.later()
	f.sync()
	after := f.children()[4]
	if after.ID != before.ID || after.Source.Ref != "new" || after.Version != before.Version+1 {
		t.Fatalf("before %+v after %+v", before.Source, after.Source)
	}
	if workspace.RunFingerprint(&before) == workspace.RunFingerprint(&after) {
		t.Fatal("the pod would not be replaced")
	}
	f.sync()
	if again := f.children()[4]; again.Version != after.Version {
		t.Fatalf("an unchanged sync wrote the row: version %d to %d", after.Version, again.Version)
	}
}

func TestARateLimitKeepsPreviewsAndSaysWhen(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"))
	f.sync()
	f.later()
	reset := f.now.Add(10 * time.Minute)
	f.host.fail(&codehost.RateLimitError{Reset: reset})
	res := f.sync()
	if got := f.live(); !slices.Equal(got, []int32{1}) {
		t.Fatalf("previews %v after the limit, want 1 kept", got)
	}
	s := f.lastSync()
	if s.Reason != string(reasons.PreviewRateLimited) || s.Message == "" || len(s.Items) != 0 {
		t.Fatalf("sync = %+v", s)
	}
	if res.RequeueAfter != 10*time.Minute {
		t.Errorf("requeue = %v, want the time until the reset", res.RequeueAfter)
	}
}

func TestAHostFailureKeepsPreviewsAndIsRecorded(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"))
	f.sync()
	f.host.fail(errors.New("connection refused"))
	f.later()
	if res := f.sync(); res.RequeueAfter != 2*time.Minute {
		t.Errorf("requeue = %v", res.RequeueAfter)
	}
	if s := f.lastSync(); s.Reason != string(reasons.PreviewsUnavailable) || s.Message != "connection refused" {
		t.Fatalf("sync = %+v", s)
	}
	if got := f.live(); !slices.Equal(got, []int32{1}) {
		t.Fatalf("previews %v", got)
	}
}

func TestNoHostForTheRepositoryIsRecorded(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.rec.Hosts = nil
	f.sync()
	if s := f.lastSync(); s.Reason != string(reasons.PreviewsUnavailable) {
		t.Fatalf("sync = %+v", s)
	}
}

func TestDeletingTheParentLeavesNoLivePreviewAndPurgesLast(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	if err := f.db.Apps().MarkDeleted(bg, alice, f.app.ID); err != nil {
		t.Fatal(err)
	}
	calls := f.host.callCount()
	f.sync()
	if got := f.live(); len(got) != 0 {
		t.Fatalf("live previews %v after the parent was deleted", got)
	}
	if f.host.callCount() != calls {
		t.Error("a deleted app still asked the code host for pull requests")
	}
	if err := f.db.Apps().Purge(bg, model.Actor{System: model.SystemController}, f.app.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("purging the parent first = %v, want ErrConflict", err)
	}
	for _, k := range f.children() {
		if err := f.db.Apps().Purge(bg, model.Actor{System: model.SystemController}, k.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Apps().Purge(bg, model.Actor{System: model.SystemController}, f.app.ID); err != nil {
		t.Fatalf("purging the parent after its previews: %v", err)
	}
	if res := f.syncApp(f.app.ID); res.RequeueAfter != 0 {
		t.Errorf("a purged app is requeued after %v", res.RequeueAfter)
	}
}

func TestTurningPreviewsOffRemovesThem(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	calls := f.host.callCount()
	if _, err := f.db.Apps().UpdateSpec(bg, alice, f.app.ID, 1, func(a *model.App) error {
		a.Source.Previews.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.live(); len(got) != 0 {
		t.Fatalf("live previews %v with previews off", got)
	}
	if f.host.callCount() != calls {
		t.Error("an app with previews off still asked the code host")
	}
	if s := f.lastSync(); len(s.Items) != 0 || s.Reason != "" {
		t.Fatalf("the Previews page would still show %+v", s)
	}
}

func TestRemovingThePreviewSettingsRemovesThem(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"))
	f.sync()
	if _, err := f.db.Apps().UpdateSpec(bg, alice, f.app.ID, 1, func(a *model.App) error {
		a.Source.Previews = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.live(); len(got) != 0 {
		t.Fatalf("live previews %v", got)
	}
}

func TestForkPullRequestsAreSkippedUnlessAllowed(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	fork := pr(2, "fork-sha")
	fork.Fork = true
	f.host.set(pr(1, "a"), fork)
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{1}) {
		t.Fatalf("previews %v, want only the same-repo pull request", got)
	}
	if r := reasonsOf(f.lastSync().Items); r[2] != string(reasons.PreviewForkSkipped) || r[1] != "" {
		t.Fatalf("items %v", r)
	}

	if _, err := f.db.Apps().UpdateSpec(bg, alice, f.app.ID, 1, func(a *model.App) error {
		a.Source.Previews.Forks = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{1, 2}) {
		t.Fatalf("previews %v with forks allowed", got)
	}
	if kids := f.children(); !kids[2].Source.Fork || kids[1].Source.Fork {
		t.Fatalf("fork flags: pr2 %v pr1 %v", kids[2].Source.Fork, kids[1].Source.Fork)
	}
}

func TestForkSkippedDoesNotUseUpASlot(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 1})
	fork := pr(9, "f")
	fork.Fork = true
	f.host.set(pr(1, "a"), fork)
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{1}) {
		t.Fatalf("previews %v", got)
	}
}

func TestForbiddingForksRemovesAnExistingForkPreview(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3, Forks: true})
	fork := pr(2, "fork-sha")
	fork.Fork = true
	f.host.set(fork)
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{2}) {
		t.Fatalf("previews %v", got)
	}
	if _, err := f.db.Apps().UpdateSpec(bg, alice, f.app.ID, 1, func(a *model.App) error {
		a.Source.Previews.Forks = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.live(); len(got) != 0 {
		t.Fatalf("a fork preview survived forks being turned off: %v", got)
	}
}

func TestAReopenedPullRequestWaitsForItsOldRowToBePurged(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(3, "a"))
	f.sync()
	f.host.set()
	f.later()
	f.sync()
	f.host.set(pr(3, "b"))
	f.later()
	res := f.sync()
	if res.RequeueAfter <= 0 || res.RequeueAfter >= f.rec.PollInterval {
		t.Fatalf("requeue = %v, want a short retry while the old row is not purged", res.RequeueAfter)
	}
	old := f.children()[3]
	if err := f.db.Apps().Purge(bg, model.Actor{System: model.SystemController}, old.ID); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.live(); !slices.Equal(got, []int32{3}) || f.children()[3].Source.Ref != "b" {
		t.Fatalf("previews %v, ref %q", got, f.children()[3].Source.Ref)
	}
}

func TestAPreviewRowIsNotSyncedAsAParent(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"))
	f.sync()
	calls := f.host.callCount()
	f.syncApp(f.children()[1].ID)
	if f.host.callCount() != calls {
		t.Fatal("a preview row was synced as a parent")
	}
}

// repoSecret is the parent's repository token Secret, labelled as its own
// as the app reconciler writes it.
func repoSecret(parent model.App, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: workspace.GitTokenSecretName(parent.ObjectName()),
			Labels: map[string]string{model.LabelApp: parent.ID}},
		Data: map[string][]byte{"token": []byte(token)},
	}
}

func TestTheParentsRepoTokenIsReadLiveAndPassedToTheHost(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"))
	f.sync()
	if !slices.Equal(f.host.tokens, []string{""}) {
		t.Fatalf("tokens = %q, want none for a public repository", f.host.tokens)
	}
	secrets := f.rec.Secrets.(client.WithWatch)
	if err := secrets.Create(bg, repoSecret(f.app, "ghp_testtoken123")); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.host.tokens[len(f.host.tokens)-1]; got != "ghp_testtoken123" {
		t.Fatalf("token passed = %q", got)
	}
}

// A Secret at the parent's token name that is not labelled as the parent's
// (a job's, another app's) is never sent to the code host.
func TestARepoTokenSecretOfAnotherAppIsNotUsed(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	foreign := repoSecret(f.app, "ghp_someoneelse")
	foreign.Labels[model.LabelApp] = "another-app"
	if err := f.rec.Secrets.(client.WithWatch).Create(bg, foreign); err != nil {
		t.Fatal(err)
	}
	f.host.set(pr(1, "a"))
	if _, err := f.rec.Reconcile(bg, reconcile.Request{NamespacedName: types.NamespacedName{Name: f.app.ID}}); err == nil {
		t.Fatal("the reconcile went on with another app's Secret")
	}
	if slices.Contains(f.host.tokens, "ghp_someoneelse") {
		t.Fatal("another app's token reached the code host")
	}
}

func TestTheTokenNeverReachesTheStoredSyncOrTheChild(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	if err := f.rec.Secrets.(client.WithWatch).Create(bg, repoSecret(f.app, "ghp_testtoken123")); err != nil {
		t.Fatal(err)
	}
	f.host.fail(errors.New("boom"))
	f.sync()
	f.host.set(pr(1, "a"))
	f.later()
	f.sync()
	for _, s := range []string{fmt.Sprintf("%+v", f.lastSync()), fmt.Sprintf("%+v", f.children())} {
		if strings.Contains(s, "ghp_testtoken123") {
			t.Fatalf("the token leaked into %s", s)
		}
	}
}

// A poke row written for the app reaches the reconciler through the change
// feed and asks for a sync long before the poll interval.
func TestAPokeTriggersASyncBeforeTheInterval(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.rec.PollInterval = time.Hour
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)
	if err := f.db.StartFeed(ctx); err != nil {
		t.Fatal(err)
	}
	feed := dbsource.NewFeed(f.db.Changes())
	srcs := f.rec.sources(feed)
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	t.Cleanup(q.ShutDown)
	for _, s := range srcs {
		if err := s.Start(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	go feed.Run(ctx)

	next := func(what string) reconcile.Request {
		t.Helper()
		got := make(chan reconcile.Request, 1)
		go func() {
			if r, down := q.Get(); !down {
				q.Done(r)
				got <- r
			}
		}()
		select {
		case r := <-got:
			return r
		case <-time.After(5 * time.Second):
			t.Fatalf("no request for %s", what)
			return reconcile.Request{}
		}
	}
	if r := next("the start-up listing"); r.Name != f.app.ID {
		t.Fatalf("start-up request = %v", r)
	}
	f.host.set(pr(1, "a"))
	f.sync()
	if f.host.callCount() != 1 {
		t.Fatalf("calls = %d", f.host.callCount())
	}

	hook, _, err := f.db.Previews().SetHook(bg, alice, f.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	// SetHook changes the apps audit only; drain whatever it announced.
	time.Sleep(100 * time.Millisecond)
	for q.Len() > 0 {
		r, _ := q.Get()
		q.Done(r)
	}
	if err := f.db.Previews().Poke(bg, hook, t0); err != nil {
		t.Fatal(err)
	}
	if r := next("the poke"); r.Name != f.app.ID {
		t.Fatalf("poke request = %v", r)
	}
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	if f.host.callCount() != 2 || !slices.Equal(f.live(), []int32{1, 2}) {
		t.Fatalf("calls %d, live %v", f.host.callCount(), f.live())
	}
}

func TestOnlyAppsWithPreviewsAreListedForResync(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.create("plain-repo", nil)
	if _, err := f.db.Apps().Create(bg, alice, &model.App{Namespace: "javo-apps", Name: "home-app", Owner: "alice", Template: "streamlit"}); err != nil {
		t.Fatal(err)
	}
	reqs, err := f.rec.allParents(bg)
	if err != nil || len(reqs) != 1 || reqs[0].Name != f.app.ID {
		t.Fatalf("resync lists %v, %v; want only the app with previews", reqs, err)
	}
}

func TestResyncStopsPreviewsOfAParentWhoseSettingsWereRemovedWithoutAnEvent(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	if !slices.Equal(f.live(), []int32{1, 2}) {
		t.Fatalf("live %v", f.live())
	}
	cur, err := f.db.Apps().Get(bg, f.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The owner switches previews off; the change event is never delivered.
	if _, err := f.db.Apps().UpdateSpec(bg, alice, cur.ID, cur.Version, func(a *model.App) error { a.Source.Previews = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	reqs, err := f.rec.allParents(bg)
	if err != nil || len(reqs) != 1 || reqs[0].Name != f.app.ID {
		t.Fatalf("resync lists %v, %v; want the parent that still has previews", reqs, err)
	}
	f.syncApp(reqs[0].Name)
	if len(f.live()) != 0 {
		t.Fatalf("previews still live after the resync: %v", f.live())
	}
	if reqs, _ := f.rec.allParents(bg); len(reqs) != 0 {
		t.Fatalf("resync still lists %v once the previews are gone", reqs)
	}
}

func TestAChangeToTheParentReachesItsExistingPreviews(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	update := func(fn func(*model.App)) {
		t.Helper()
		cur, err := f.db.Apps().Get(bg, f.app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Apps().UpdateSpec(bg, alice, cur.ID, cur.Version, func(a *model.App) error { fn(a); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	update(func(a *model.App) { a.Sharing = model.Sharing{Access: model.AccessSignedIn} })
	f.host.set(pr(4, "sha"))
	f.sync()
	if got := f.children()[4].Sharing.Access; got != model.AccessSignedIn {
		t.Fatalf("new preview sharing %q", got)
	}

	update(func(a *model.App) {
		a.Sharing = model.Sharing{Access: model.AccessPrivate}
		a.Size = "large"
		a.File = "other.py"
	})
	before := f.children()[4]
	f.sync()
	after := f.children()[4]
	if after.ID != before.ID || after.Sharing.Access != model.AccessPrivate || after.Size != "large" || after.File != "other.py" {
		t.Fatalf("preview did not follow its parent: %+v", after)
	}
	if after.Source.Ref != "sha" || after.PullNumber != 4 || after.APIAccess != model.APIAccessNone || after.Source.Previews != nil {
		t.Fatalf("preview lost its own settings: %+v", after)
	}
	f.sync()
	if again := f.children()[4]; again.Version != after.Version {
		t.Fatalf("an unchanged sync wrote the row: version %d to %d", after.Version, again.Version)
	}
}

func TestAParentsNewShareListReachesItsPreviews(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"))
	f.sync()
	cur, _ := f.db.Apps().Get(bg, f.app.ID)
	if _, err := f.db.Apps().UpdateSpec(bg, alice, cur.ID, cur.Version, func(a *model.App) error {
		a.Sharing = model.Sharing{Access: model.AccessRestricted, Users: []string{"bob"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if got := f.children()[1].Sharing; got.Access != model.AccessRestricted || !slices.Equal(got.Users, []string{"bob"}) {
		t.Fatalf("sharing %+v", got)
	}
}

func TestPreviewsAreRemovedWhenThePlatformSwitchIsOff(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	if len(f.live()) != 2 {
		t.Fatalf("live %v", f.live())
	}
	calls := f.host.callCount()
	f.rec.Enabled = false
	f.sync()
	if got := f.live(); len(got) != 0 {
		t.Fatalf("live previews %v with the platform switch off", got)
	}
	if f.host.callCount() != calls {
		t.Error("the platform switch is off but the code host was asked")
	}
}

// A failure the owner can fix is recorded with the owner's reason (what to
// change), not as the platform's; anything else stays PreviewsUnavailable.
func TestAFailureTheOwnerCanFixIsTheirs(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want reasons.Code
	}{
		{"wrong address, or private without a token", fmt.Errorf("%w: 404 Not Found", codehost.ErrNotFound), reasons.RepoNotFound},
		{"token refused", fmt.Errorf("%w: 401 Bad credentials", codehost.ErrAuth), reasons.AuthFailed},
		{"host not answering", errors.New("connection refused"), reasons.PreviewsUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, model.Previews{Enabled: true, Max: 3})
			f.host.fail(c.err)
			f.sync()
			if s := f.lastSync(); s.Reason != string(c.want) || s.Message != c.err.Error() {
				t.Fatalf("sync = %+v, want reason %s", s, c.want)
			}
		})
	}
}

// A resync or a change to the app inside the poll interval reuses the last
// answer: a long interval saves the GitHub request budget it is set for.
func TestARequestInsideThePollIntervalDoesNotAskTheHost(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 1})
	f.rec.PollInterval = time.Hour
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	f.now = f.now.Add(59 * time.Minute)
	res := f.sync()
	if f.host.callCount() != 1 {
		t.Fatalf("host asked %d times inside the interval, want 1", f.host.callCount())
	}
	if res.RequeueAfter != time.Minute {
		t.Errorf("requeue = %v, want the rest of the interval", res.RequeueAfter)
	}
	// A setting change applies at once from the last answer.
	cur, _ := f.db.Apps().Get(bg, f.app.ID)
	if _, err := f.db.Apps().UpdateSpec(bg, alice, cur.ID, cur.Version, func(a *model.App) error { a.Source.Previews.Max = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	f.sync()
	if f.host.callCount() != 1 || !slices.Equal(f.live(), []int32{1, 2}) {
		t.Fatalf("calls %d, live %v; want the new max applied without a request", f.host.callCount(), f.live())
	}
	if _, at, _ := f.db.Previews().Sync(bg, f.app.ID); !at.Equal(t0) {
		t.Errorf("synced at %v, want %v (when the host answered)", at, t0)
	}
	f.now = t0.Add(time.Hour)
	f.sync()
	if f.host.callCount() != 2 {
		t.Fatalf("host asked %d times after the interval, want 2", f.host.callCount())
	}
}

// While rate limited, nothing (a resync, a change, a webhook) asks the host
// before the reset.
func TestARateLimitIsNotRetriedBeforeItsReset(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	reset := t0.Add(30 * time.Minute)
	f.host.fail(&codehost.RateLimitError{Reset: reset})
	f.sync()
	hook, _, err := f.db.Previews().SetHook(bg, alice, f.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.now = t0.Add(10 * time.Minute)
	if err := f.db.Previews().Poke(bg, hook, f.now); err != nil {
		t.Fatal(err)
	}
	res := f.sync()
	if f.host.callCount() != 1 {
		t.Fatalf("host asked %d times before the reset, want 1", f.host.callCount())
	}
	if res.RequeueAfter != 20*time.Minute {
		t.Errorf("requeue = %v, want the time left until the reset", res.RequeueAfter)
	}
	f.host.set(pr(1, "a"))
	f.now = reset
	f.sync()
	if f.host.callCount() != 2 || !slices.Equal(f.live(), []int32{1}) {
		t.Fatalf("calls %d, live %v after the reset", f.host.callCount(), f.live())
	}
}

// A webhook delivery asks the host at once, once.
func TestAPokeInsideThePollIntervalAsksTheHostOnce(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.rec.PollInterval = time.Hour
	f.host.set(pr(1, "a"))
	f.sync()
	hook, _, err := f.db.Previews().SetHook(bg, alice, f.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.now = t0.Add(time.Minute)
	if err := f.db.Previews().Poke(bg, hook, f.now); err != nil {
		t.Fatal(err)
	}
	f.host.set(pr(1, "a"), pr(2, "b"))
	f.sync()
	f.sync()
	if f.host.callCount() != 2 || !slices.Equal(f.live(), []int32{1, 2}) {
		t.Fatalf("calls %d, live %v; want one more request for the poke", f.host.callCount(), f.live())
	}
}

// An owner who fixes the address or pastes a token is answered at once.
func TestANewTokenAsksTheHostAtOnce(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 3})
	f.host.fail(fmt.Errorf("%w: private", codehost.ErrNotFound))
	f.sync()
	if err := f.rec.Secrets.(client.WithWatch).Create(bg, repoSecret(f.app, "ghp_testtoken123")); err != nil {
		t.Fatal(err)
	}
	f.host.set(pr(1, "a"))
	f.sync()
	if f.host.callCount() != 2 || !slices.Equal(f.live(), []int32{1}) {
		t.Fatalf("calls %d, live %v after the token was pasted", f.host.callCount(), f.live())
	}
}

// The owner's cap on previews (limits.previews) counts every app of theirs:
// past it, a pull request gets no preview and says why, and the sync does not fail.
func TestTheOwnersPreviewCapStopsNewPreviews(t *testing.T) {
	f := newFixture(t, model.Previews{Enabled: true, Max: 5})
	f.db.SetLimits(store.Limits{Previews: 2})
	for n := int32(1); n <= 3; n++ {
		f.host.pulls = append(f.host.pulls, pr(n, fmt.Sprintf("sha%d", n)))
	}
	f.sync()
	if got := f.live(); len(got) != 2 {
		t.Fatalf("previews for %v, want the owner's cap of 2", got)
	}
	got := reasonsOf(f.lastSync().Items)
	capped := 0
	for _, r := range got {
		if r == string(reasons.PreviewOwnerLimitReached) {
			capped++
		}
	}
	if capped != 1 {
		t.Fatalf("reasons %v, want one PreviewOwnerLimitReached", got)
	}
}
