// Package idle stops apps nobody has used for their "Stop when idle"
// timeout. Use is what the gateways write with Apps.NoteActivity; a freshly
// started app counts as used from the moment it became ready.
package idle

import (
	"context"
	"errors"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Every is how often the controller looks for idle apps.
const Every = 30 * time.Second

// Checker finds idle apps and stops them through Apps.StopIdle, the same
// versioned stop a person's Stop makes. Add it to the manager: it runs on
// the leader only.
type Checker struct {
	Apps *store.Apps
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Start checks every Every until ctx ends (manager.Runnable).
func (c *Checker) Start(ctx context.Context) error {
	tick := time.NewTicker(Every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := c.Check(ctx); err != nil {
				log.FromContext(ctx).Error(err, "idle check")
			}
		}
	}
}

// Check stops every running app whose last use, or its start when it has
// not been used since, is older than its timeout plus the gateways' flush
// lag. An app changed meanwhile (started, given a new timeout) is left for
// the next check.
func (c *Checker) Check(ctx context.Context) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	apps, err := c.Apps.ListIdleStopCandidates(ctx)
	if err != nil {
		return err
	}
	at := now()
	for i := range apps {
		app := &apps[i]
		since, ok := lastUse(app)
		if !ok || at.Sub(since) < time.Duration(app.IdleStopMinutes)*time.Minute+activity.FlushEvery {
			continue
		}
		_, err := c.Apps.StopIdle(ctx, app.ID, app.Version)
		switch {
		case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			log.FromContext(ctx).Info("stopped idle app", "app", app.ID, "minutes", app.IdleStopMinutes)
		}
	}
	return nil
}

// lastUse is when a running app was last used: its latest traffic, or when
// it became ready if that is later. false for an app that is not running.
func lastUse(app *model.App) (time.Time, bool) {
	if app.Status.Phase != "Running" {
		return time.Time{}, false
	}
	var ready time.Time
	for _, c := range app.Status.Conditions {
		if c.Type == model.ConditionReady && c.Status == metav1.ConditionTrue {
			ready = c.LastTransitionTime.Time
		}
	}
	if ready.IsZero() {
		return time.Time{}, false
	}
	if app.LastActiveAt.After(ready) {
		return app.LastActiveAt, true
	}
	return ready, true
}
