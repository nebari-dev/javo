package netprobe

import (
	"context"
	"log/slog"
	"time"
)

// The probe waits for the gateway-only policy it tests.
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get

// Probe is what Loop runs; *Prober implements it.
type Probe interface {
	Probe(ctx context.Context) Result
}

// DefaultRetryMin is the first wait before re-probing after an Unknown result.
const DefaultRetryMin = 30 * time.Second

// Loop probes when it starts and every Every after that, storing each
// result. An Unknown result (slow image pull, API hiccup, busy node) or a
// result that could not be stored is retried after RetryMin, doubling each time, never beyond Every, so one bad
// probe does not leave login-less apps blocked until the next tick. It is a
// controller-runtime Runnable that runs on the leader only.
type Loop struct {
	Prober   Probe
	Store    Putter
	Every    time.Duration
	RetryMin time.Duration // zero means DefaultRetryMin
}

// NeedLeaderElection makes the manager start Loop on the leader only.
func (l *Loop) NeedLeaderElection() bool { return true }

// retryDelay is the wait before retry number n (0-based) after Unknown or a
// failed store.
func (l *Loop) retryDelay(n int) time.Duration {
	d := l.RetryMin
	if d <= 0 {
		d = DefaultRetryMin
	}
	for ; n > 0 && d < l.Every; n-- {
		d *= 2
	}
	return min(d, l.Every)
}

// Start runs until ctx ends.
func (l *Loop) Start(ctx context.Context) error {
	retries := 0
	for {
		res := l.Prober.Probe(ctx)
		if ctx.Err() != nil {
			return nil
		}
		stored := true
		if err := l.Store.Put(ctx, res); err != nil {
			slog.Error("store NetworkPolicy probe result", "err", err)
			stored = false
		} else {
			slog.Info("NetworkPolicy probe", "state", res.State, "evidence", res.Evidence)
		}
		wait := l.Every
		if res.State == Unknown || !stored {
			wait = l.retryDelay(retries)
			retries++
		} else {
			retries = 0
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}
