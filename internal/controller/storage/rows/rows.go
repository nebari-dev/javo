// Package rows is what every storage reconciler reads and writes: the
// rows of homes and group folders, and the Ready condition that says why
// one is not ready. The reconcilers of a cluster (controller/storage) and
// of a machine without one (controller/storage/onhost) share it; it needs
// no client of a cluster.
package rows

import (
	"context"
	"errors"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Rows is what every storage reconciler reads: the database and the
// platform config's storage section (defaults applied).
type Rows struct {
	// Store is the database. Required.
	Store *store.Store
	// Config is the platform config's storage section, defaults applied.
	Config model.ClusterStorageSpec
}

// Cluster is the storage view: config, the admin's requests and findings.
func (r Rows) Cluster(ctx context.Context) (*model.ClusterStorage, error) {
	cs, err := r.Store.Storage().Cluster(ctx, r.Config)
	return &cs, err
}

// HomeIDs is the id of each home that keep accepts (all when nil).
func (r Rows) HomeIDs(ctx context.Context, keep func(*model.Home) bool) ([]string, error) {
	homes, err := r.Store.Homes().List(ctx)
	return dbsource.IDs(homes, func(h *model.Home) string { return h.ID }, keep), err
}

// WriteHome stores h if it differs from base (the home as read) and returns
// whether another writer got there first; the caller then reconciles again.
func (r Rows) WriteHome(ctx context.Context, h, base *model.Home) (conflict bool, err error) {
	if HomeEqual(h, base) {
		return false, nil
	}
	got, err := r.Store.Homes().Update(ctx, *h, base.Version)
	if errors.Is(err, store.ErrConflict) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	*h, *base = got, *got.DeepCopy()
	return false, nil
}

// FolderIDs is the id of each folder that keep accepts (all when nil).
func (r Rows) FolderIDs(ctx context.Context, keep func(*model.SharedFolder) bool) ([]string, error) {
	folders, err := r.Store.SharedFolders().List(ctx)
	return dbsource.IDs(folders, func(f *model.SharedFolder) string { return f.ID }, keep), err
}

// ErrRowChanged is a write in the middle of a pass that lost the race to
// another writer; the pass is run again.
var ErrRowChanged = errors.New("the row changed since it was read")

// WriteFolder stores f if it differs from base (the folder as read); another
// writer getting there first is ErrRowChanged.
func (r Rows) WriteFolder(ctx context.Context, f, base *model.SharedFolder) error {
	if equality.Semantic.DeepEqual(base.Status, f.Status) {
		return nil
	}
	got, err := r.Store.SharedFolders().Update(ctx, *f, base.Version)
	if errors.Is(err, store.ErrConflict) {
		return ErrRowChanged
	} else if err != nil {
		return err
	}
	*f, *base = got, *got.DeepCopy()
	return nil
}

// How soon a pass looks again: one that lost a write race, one that waits
// for something of its own to finish, for the home's volume, and for the
// volume to be handed to its owner.
const (
	RetryConflict    = 200 * time.Millisecond
	HomeWaitRetry    = 5 * time.Second
	HomeClaimRetry   = 30 * time.Second
	HomePrepareRetry = time.Minute
)

// HomeEqual is true when a and b hold the same spec and status.
func HomeEqual(a, b *model.Home) bool {
	return equality.Semantic.DeepEqual(a.Spec, b.Spec) && equality.Semantic.DeepEqual(a.Status, b.Status)
}

// ReasonReady is the reason of a Ready condition that is fine. Failures
// come from internal/reasons.
const ReasonReady = "Ready"

// Blocked is why an object is not Ready: a catalog reason, what was found, and
// when to look again (zero: when something it watches changes).
type Blocked struct {
	Code     reasons.Code
	Evidence []string
	Retry    time.Duration
}

// ReadyCondition is the Ready condition for an object that is ready (b nil,
// text is the message) or blocked; the catalog supplies the blocked text.
func ReadyCondition(text string, b *Blocked) metav1.Condition {
	c := metav1.Condition{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: ReasonReady, Message: text}
	if b != nil {
		reason, _ := reasons.Lookup(b.Code)
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, string(b.Code), reason.What
		if len(b.Evidence) > 0 {
			c.Message += " (" + strings.Join(b.Evidence, "; ") + ")"
		}
	}
	return c
}
