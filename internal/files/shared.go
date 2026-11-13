package files

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
)

// What file pods (Pods, on Kubernetes) and file helpers (Helpers, on
// Docker) have in common: when a home can be served, and when what
// serves it has been idle for long enough to go.

// homeInput is what serves userID's files mounts, once their home has
// storage recorded on its row. Before that it asks for the home if they
// have none (as starting an app does) and returns ErrHomeStarting. A home
// whose storage belongs to another account is ErrHomeConflict. The caller
// still checks that the storage named is really this user's.
func homeInput(ctx context.Context, st Storage, ns, userID string) (podstorage.Input, error) {
	home, err := st.Home(ctx, ns, userID)
	if err != nil {
		return podstorage.Input{}, err
	}
	if cd := meta.FindStatusCondition(home.Status.Conditions, model.ConditionReady); cd != nil && cd.Reason == string(reasons.HomeClaimForeign) {
		return podstorage.Input{}, ErrHomeConflict
	}
	if home.Status.ClaimName == "" {
		// The storage controller is making it.
		return podstorage.Input{}, ErrHomeStarting
	}
	in, err := st.Input(ctx, ns, userID)
	if err != nil {
		return podstorage.Input{}, err
	}
	if in.Home == nil || in.Home.Status.ClaimName == "" {
		return podstorage.Input{}, ErrHomeStarting
	}
	return in, nil
}

// runAs gives in the uid and gid userID's apps run as, so a file written
// for them belongs to them. Someone with no uid yet is ErrHomeStarting.
func runAs(ctx context.Context, uids UserIDs, userID string, in *podstorage.Input) error {
	uid, gid, err := uids.UID(ctx, userID)
	if errors.Is(err, ids.ErrNoUID) {
		return ErrHomeStarting
	}
	if err != nil {
		return err
	}
	in.UID, in.GID = uid, gid
	return nil
}

// activity is when each user last used their files through this gateway.
type activity struct {
	mu      sync.Mutex
	lastUse map[string]time.Time
}

// touch records that userID is active now.
func (a *activity) touch(userID string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastUse == nil {
		a.lastUse = map[string]time.Time{}
	}
	a.lastUse[userID] = now
}

// forget drops what is known of userID.
func (a *activity) forget(userID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.lastUse, userID)
}

// idle picks, of the n things that serve files, the ones unused for
// after; at gives the owner of each and when it was made. One this
// gateway has not served (for example after a restart) counts as used
// when it was made. Users idle that long are forgotten.
func (a *activity) idle(now time.Time, after time.Duration, n int, at func(i int) (owner string, made time.Time)) []int {
	a.mu.Lock()
	defer a.mu.Unlock()
	var idle []int
	for i := range n {
		owner, last := at(i)
		if used, ok := a.lastUse[owner]; ok {
			last = used
		}
		if now.Sub(last) >= after {
			idle = append(idle, i)
		}
	}
	for s, last := range a.lastUse {
		if now.Sub(last) >= after {
			delete(a.lastUse, s)
		}
	}
	return idle
}

// reapLoop calls reap every minute until ctx is done; what names it in
// the log when it fails.
func reapLoop(ctx context.Context, what string, reap func(context.Context) error) {
	t := time.NewTicker(reapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := reap(ctx); err != nil {
				slog.Error(what, "err", err)
			}
		}
	}
}
