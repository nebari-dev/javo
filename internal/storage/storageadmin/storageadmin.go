// Package storageadmin holds the three requests an admin makes of the
// storage at run time (ask for a probe, schedule the move of homes, retry the
// failed ones). The CLI and the admin API both call it, so the two always
// write the same fields.
package storageadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// ErrNotInspected means the controller has not chosen a mode yet, so there is
// nothing to move homes to.
var ErrNotInspected = errors.New("the cluster's storage has not been inspected yet, so there is no mode to move to")

// RequestProbe asks the controller to probe class and returns the nonce that
// marks this request in the probe status. A timeout of zero or less leaves
// the configured one.
func RequestProbe(ctx context.Context, st *store.Storage, actor model.Actor, class string, timeout time.Duration) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(b)
	err := st.UpdateRequests(ctx, actor, "storage.probe", func(r *model.StorageRequests) error {
		r.Probe = model.ProbeSpec{StorageClassName: class, Nonce: nonce}
		if timeout > 0 {
			r.Probe.Timeout = &metav1.Duration{Duration: timeout}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("ask for a probe: %w", err)
	}
	return nonce, nil
}

// ScheduleMove sets when homes start moving to the mode the cluster has
// chosen. The mode is stored with the time, so a later mode change does not
// start an old schedule.
func ScheduleMove(ctx context.Context, st *store.Storage, actor model.Actor, at time.Time) error {
	status, err := st.Status(ctx)
	if err != nil {
		return err
	}
	if status.Choice == nil || status.Choice.Mode == "" {
		return ErrNotInspected
	}
	err = st.UpdateRequests(ctx, actor, "storage.schedule", func(r *model.StorageRequests) error {
		r.Migration.StartAt = &metav1.Time{Time: at}
		r.Migration.To = status.Choice.Mode
		return nil
	})
	if err != nil {
		return fmt.Errorf("schedule the move: %w", err)
	}
	return nil
}

// RetryMove lets homes whose move failed try again: a failed home restarts
// when the cluster's attempt number is above the one it failed at.
func RetryMove(ctx context.Context, st *store.Storage, actor model.Actor) error {
	err := st.UpdateRequests(ctx, actor, "storage.retry", func(r *model.StorageRequests) error {
		r.Migration.Attempt++
		return nil
	})
	if err != nil {
		return fmt.Errorf("retry the move: %w", err)
	}
	return nil
}
