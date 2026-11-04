package store

import (
	"context"
	"fmt"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Limits are the per-person caps (the platform config's limits section).
// The store checks each one in the transaction of the write that would pass
// it, after locking the person's row, so concurrent requests cannot go over
// a cap together. Zero is no cap.
type Limits struct {
	// Apps counts a person's apps, RunningApps the ones not stopped;
	// previews count only under Previews.
	Apps, RunningApps, Previews int64
	Jobs                        int64
	// RunsPerJob and RunsPerPerson count runs not finished (requested or
	// running).
	RunsPerJob, RunsPerPerson int64
	// APITokens counts personal tokens that have not expired.
	APITokens    int64
	PendingPlans int64
}

// SetLimits sets the caps every later write is checked against.
func (s *Store) SetLimits(l Limits) { s.limits.Store(&l) }

// Limits are the caps SetLimits set; the zero value (no cap) before.
func (s *Store) Limits() Limits {
	if l := s.limits.Load(); l != nil {
		return *l
	}
	return Limits{}
}

// Limit names one of the caps in Limits.
type Limit string

// The caps in Limits.
const (
	LimitApps          Limit = "apps"
	LimitRunningApps   Limit = "runningApps"
	LimitPreviews      Limit = "previews"
	LimitJobs          Limit = "jobs"
	LimitRunsPerJob    Limit = "activeRunsPerJob"
	LimitRunsPerPerson Limit = "activeRunsPerPerson"
	LimitAPITokens     Limit = "apiTokens"
	LimitPendingPlans  Limit = "pendingPlans"
)

// LimitError refuses a write that would take a person past Max of Limit.
type LimitError struct {
	Limit Limit
	Max   int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("store: the %s limit of %d is reached", e.Limit, e.Max)
}

// capCheck is one cap and how to count what it caps.
type capCheck struct {
	limit Limit
	max   int64
	count func() (int64, error)
}

// within takes owner's lock row and refuses with a *LimitError the first check
// whose count is at its cap. Checks without a cap cost no query.
func within(ctx context.Context, q *sqlcgen.Queries, owner string, checks ...capCheck) error {
	locked := false
	for _, c := range checks {
		if c.max <= 0 {
			continue
		}
		if !locked {
			if err := q.LockOwner(ctx, owner); err != nil {
				return err
			}
			locked = true
		}
		n, err := c.count()
		if err != nil {
			return err
		}
		if n >= c.max {
			return &LimitError{Limit: c.limit, Max: c.max}
		}
	}
	return nil
}

// appCaps are the caps a new app of owner is checked against: a preview
// against Previews, any other app against Apps and, unless it is stopped,
// RunningApps.
func (s *Store) appCaps(ctx context.Context, q *sqlcgen.Queries, owner string, preview, running bool) []capCheck {
	if preview {
		return []capCheck{{LimitPreviews, s.Limits().Previews, func() (int64, error) { return q.CountPreviewsByOwner(ctx, owner) }}}
	}
	out := []capCheck{{LimitApps, s.Limits().Apps, func() (int64, error) { return q.CountAppsByOwner(ctx, owner) }}}
	if running {
		out = append(out, s.runningCap(ctx, q, owner))
	}
	return out
}

func (s *Store) runningCap(ctx context.Context, q *sqlcgen.Queries, owner string) capCheck {
	return capCheck{LimitRunningApps, s.Limits().RunningApps, func() (int64, error) { return q.CountRunningAppsByOwner(ctx, owner) }}
}

func (s *Store) jobCap(ctx context.Context, q *sqlcgen.Queries, owner string) capCheck {
	return capCheck{LimitJobs, s.Limits().Jobs, func() (int64, error) { return q.CountJobsByOwner(ctx, owner) }}
}

func (s *Store) runCaps(ctx context.Context, q *sqlcgen.Queries, owner, jobID string) []capCheck {
	return []capCheck{
		{LimitRunsPerJob, s.Limits().RunsPerJob, func() (int64, error) { return q.CountActiveRunsByJob(ctx, jobID) }},
		{LimitRunsPerPerson, s.Limits().RunsPerPerson, func() (int64, error) { return q.CountActiveRunsByOwner(ctx, owner) }},
	}
}

// tokenCap counts owner's personal tokens still valid at at.
func (s *Store) tokenCap(ctx context.Context, q *sqlcgen.Queries, owner string, at time.Time) capCheck {
	return capCheck{LimitAPITokens, s.Limits().APITokens, func() (int64, error) {
		return q.CountPersonalTokens(ctx, sqlcgen.CountPersonalTokensParams{Name: owner, ExpiresAt: nullTime(at)})
	}}
}

// planCap counts owner's plans still pending at at.
func (s *Store) planCap(ctx context.Context, q *sqlcgen.Queries, owner string, at time.Time) capCheck {
	return capCheck{LimitPendingPlans, s.Limits().PendingPlans, func() (int64, error) {
		return q.CountPendingPlans(ctx, sqlcgen.CountPendingPlansParams{Owner: owner, State: PlanPending, ExpiresAt: dbtime.From(at)})
	}}
}

// RunningRuns counts owner's runs that have a Job and have not finished: the
// pods their jobs run now (requested runs not yet started are left out).
func (j *Jobs) RunningRuns(ctx context.Context, owner string) (int64, error) {
	return j.s.q.CountRunningRunsByOwner(ctx, sqlcgen.CountRunningRunsByOwnerParams{Owner: owner, Outcome: model.RunOutcomeRunning})
}
