// Package preview keeps one preview app per open pull request of an app that
// runs from a GitHub repository. It writes preview rows to the database; the
// workspace reconciler turns each row into a pod. It never runs git and only
// talks to the code host's API.
package preview

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

const (
	// busyRetry is how soon a pull request whose earlier preview row is still
	// marked deleted (its pod is being removed) is looked at again.
	busyRetry = 5 * time.Second
	// conflictRetry is how soon a sync that lost a version race starts over.
	conflictRetry = time.Second
	// maxMessage is the longest host error a stored sync result keeps.
	maxMessage = 512
)

// Reconciler is the preview reconciler. A request names the id of the app
// whose pull requests to look at.
type Reconciler struct {
	Store *store.Store
	// Hosts are the code hosts; the first that owns an app's repository is used.
	Hosts []codehost.Host
	// Secrets reads the app's repository token Secret live (Secrets are never cached).
	Secrets client.Reader
	// PollInterval is how often each app's pull requests are looked at again.
	PollInterval time.Duration
	// MaxPerApp is the platform's cap on previews per app.
	MaxPerApp int32
	Now       func() time.Time
	// Enabled is the platform switch (source.previews.enabled). When off, no
	// app keeps a preview and the code host is never asked.
	Enabled bool

	mu sync.Mutex
	// answers is the code host's last answer per app id, so a resync or a
	// change to the app before the answer runs out costs no request. It is
	// lost on restart, which costs one request per app.
	answers map[string]answer
}

// answer is what the code host said about one app's repository.
type answer struct {
	// asked is a hash of the repository address and token asked with; a new
	// address or token asks again at once.
	asked [sha256.Size]byte
	// poked is the app's last webhook delivery when asked; a newer one asks
	// again at once, unless the host is rate limiting.
	poked   time.Time
	at      time.Time
	until   time.Time
	limited bool
	pulls   []codehost.PullRequest
	err     error
}

// SetupWithManager wires the controller: app rows (and their pokes) come
// from the database through feed. Like every controller of the manager it
// runs on the leader only, so two replicas never both poll the code host.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, feed *dbsource.Feed) error {
	if r.Store == nil || r.Secrets == nil || r.Now == nil || r.PollInterval <= 0 || r.MaxPerApp <= 0 {
		return errors.New("preview reconciler needs Store, Secrets, Now, PollInterval and MaxPerApp")
	}
	b := ctrl.NewControllerManagedBy(mgr).Named("preview")
	for _, s := range r.sources(feed) {
		b = b.WatchesRawSource(s)
	}
	return b.Complete(r)
}

// sources are the rows that wake the reconciler: every app change (a parent's
// settings, or a parent that stopped being one) and a poke of an app.
func (r *Reconciler) sources(feed *dbsource.Feed) []source.TypedSource[reconcile.Request] {
	return []source.TypedSource[reconcile.Request]{
		dbsource.For(feed.Subscribe(), "apps", 0, r.allParents),
		dbsource.Fan(feed.Subscribe(), "app_previews", func(_ context.Context, id string) ([]reconcile.Request, error) {
			return dbsource.Request(id), nil
		}),
	}
}

// allParents is the apps for the periodic resync: those with preview settings,
// and those that still have a live preview whatever their settings now say, so
// a switched-off or removed setting whose change event was lost still gets its
// previews stopped.
func (r *Reconciler) allParents(ctx context.Context) ([]reconcile.Request, error) {
	apps, err := r.Store.Apps().ListAll(ctx)
	if err != nil {
		return nil, err
	}
	withLiveChild := map[string]bool{}
	for i := range apps {
		if apps[i].ParentID != "" && apps[i].DeletedAt.IsZero() {
			withLiveChild[apps[i].ParentID] = true
		}
	}
	return dbsource.Requests(apps, func(a *model.App) string { return a.ID }, func(a *model.App) bool {
		return a.ParentID == "" && (a.Source != nil && a.Source.Previews != nil || withLiveChild[a.ID])
	}), nil
}

// Reconcile looks at the open pull requests of one app and makes its previews
// match.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	parent, err := r.Store.Apps().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		r.forget(req.Name)
		return reconcile.Result{}, nil
	}
	if err != nil {
		return reconcile.Result{}, err
	}
	if parent.ParentID != "" {
		return reconcile.Result{}, nil
	}
	if !r.Enabled || !parent.DeletedAt.IsZero() || parent.Source == nil || parent.Source.Previews == nil || !parent.Source.Previews.Enabled {
		return reconcile.Result{}, r.removeAll(ctx, &parent)
	}
	return r.sync(ctx, &parent)
}

// removeAll marks every live preview of the app deleted. When that removed
// any (previews turned off) and the app still exists, the stored result no
// longer lists them.
func (r *Reconciler) removeAll(ctx context.Context, parent *model.App) error {
	r.forget(parent.ID)
	kids, err := r.Store.Previews().Children(ctx, parent.ID)
	if err != nil {
		return err
	}
	removed := 0
	for _, k := range kids {
		if !k.DeletedAt.IsZero() {
			continue
		}
		if err := r.Store.Previews().Remove(ctx, k.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		removed++
	}
	if removed > 0 && parent.DeletedAt.IsZero() {
		return r.Store.Previews().RecordSync(ctx, parent.ID, model.PreviewSync{}, r.Now())
	}
	return nil
}

func (r *Reconciler) host(repoURL string) codehost.Host {
	i := slices.IndexFunc(r.Hosts, func(h codehost.Host) bool { return h.Owns(repoURL) })
	if i < 0 {
		return nil
	}
	return r.Hosts[i]
}

func (r *Reconciler) sync(ctx context.Context, parent *model.App) (reconcile.Result, error) {
	host := r.host(parent.Source.URL)
	if host == nil {
		return reconcile.Result{RequeueAfter: r.PollInterval}, r.record(ctx, parent, model.PreviewSync{
			Reason: string(reasons.PreviewsUnavailable), Message: "no code host serves " + parent.Source.URL,
		}, r.Now())
	}
	token, err := workspace.ReadGitToken(ctx, r.Secrets, parent)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("read the repository token: %w", err)
	}
	poked, err := r.Store.Previews().PokedAt(ctx, parent.ID)
	if err != nil {
		return reconcile.Result{}, err
	}
	asked := sha256.Sum256([]byte(parent.Source.URL + "\x00" + token))
	now := r.Now()
	if a, ok := r.last(parent.ID); ok && a.asked == asked && now.Before(a.until) && (a.limited || a.poked.Equal(poked)) {
		if a.err != nil {
			return reconcile.Result{RequeueAfter: a.until.Sub(now)}, nil
		}
		return r.apply(ctx, parent, a.pulls, a.at, a.until.Sub(now))
	}

	pulls, err := host.OpenPulls(ctx, parent.Source.URL, token)
	a := answer{asked: asked, poked: poked, at: now, until: now.Add(r.PollInterval), pulls: pulls, err: err}
	var limit *codehost.RateLimitError
	if a.limited = errors.As(err, &limit); a.limited {
		a.until = now.Add(max(limit.Reset.Sub(now), r.PollInterval))
	}
	r.remember(parent.ID, a)
	if err == nil {
		return r.apply(ctx, parent, pulls, now, r.PollInterval)
	}
	failed := model.PreviewSync{Reason: string(failure(err)), Message: cut(err.Error())}
	if a.limited {
		failed.Message = "The limit resets at " + limit.Reset.UTC().Format(time.RFC3339)
	} else {
		slog.Warn("previews: listing pull requests failed", "app", parent.ID, "error", err)
	}
	return reconcile.Result{RequeueAfter: a.until.Sub(now)}, r.record(ctx, parent, failed, now)
}

// failure is the reason a failed look at the pull requests is recorded with:
// the owner's when they can fix it, else the platform's.
func failure(err error) reasons.Code {
	var limit *codehost.RateLimitError
	switch {
	case errors.As(err, &limit):
		return reasons.PreviewRateLimited
	case errors.Is(err, codehost.ErrNotFound):
		return reasons.RepoNotFound
	case errors.Is(err, codehost.ErrAuth):
		return reasons.AuthFailed
	default:
		return reasons.PreviewsUnavailable
	}
}

func (r *Reconciler) last(id string) (answer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.answers[id]
	return a, ok
}

func (r *Reconciler) remember(id string, a answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answers == nil {
		r.answers = map[string]answer{}
	}
	r.answers[id] = a
}

func (r *Reconciler) forget(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.answers, id)
}

// apply makes the previews match pulls, the host's answer at at: the newest
// ones up to the limit get a preview, the rest are listed with the reason they
// have none. The app is looked at again after next.
func (r *Reconciler) apply(ctx context.Context, parent *model.App, pulls []codehost.PullRequest, at time.Time, next time.Duration) (reconcile.Result, error) {
	pulls = slices.Clone(pulls)
	set := parent.Source.Previews
	limit := int(min(set.Max, r.MaxPerApp))
	if set.Max <= 0 {
		limit = int(min(model.DefaultPreviewMax, r.MaxPerApp))
	}
	slices.SortFunc(pulls, func(a, b codehost.PullRequest) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return int(b.Number - a.Number)
	})

	res := reconcile.Result{RequeueAfter: next}
	wanted := map[int32]bool{}
	items := make([]model.PreviewItem, 0, len(pulls))
	for _, pr := range pulls {
		item := model.PreviewItem{Number: pr.Number, Title: pr.Title, HeadSHA: pr.HeadSHA}
		switch {
		case pr.Fork && !set.Forks:
			item.Reason = string(reasons.PreviewForkSkipped)
		case len(wanted) >= limit:
			item.Reason = string(reasons.PreviewLimitReached)
		default:
			wanted[pr.Number] = true
			child := Child(parent, pr)
			item.App = child.Name
			_, err := r.Store.Previews().Put(ctx, &child)
			switch {
			case errors.Is(err, store.ErrExists):
				res.RequeueAfter = min(res.RequeueAfter, busyRetry)
			case errors.Is(err, store.ErrConflict):
				return reconcile.Result{RequeueAfter: conflictRetry}, nil
			case errors.Is(err, store.ErrNotFound):
				return reconcile.Result{}, nil
			case err != nil:
				return reconcile.Result{}, fmt.Errorf("preview of pull request %d: %w", pr.Number, err)
			}
		}
		items = append(items, item)
	}

	kids, err := r.Store.Previews().Children(ctx, parent.ID)
	if err != nil {
		return reconcile.Result{}, err
	}
	for _, k := range kids {
		if k.DeletedAt.IsZero() && !wanted[k.PullNumber] {
			if err := r.Store.Previews().Remove(ctx, k.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return reconcile.Result{}, err
			}
		}
	}
	return res, r.record(ctx, parent, model.PreviewSync{Items: items}, at)
}

func (r *Reconciler) record(ctx context.Context, parent *model.App, sync model.PreviewSync, at time.Time) error {
	return r.Store.Previews().RecordSync(ctx, parent.ID, sync, at)
}

func cut(s string) string {
	if rs := []rune(s); len(rs) > maxMessage {
		return string(rs[:maxMessage])
	}
	return s
}
