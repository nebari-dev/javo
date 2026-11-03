package store

import (
	"context"
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// UsageInterval is one stretch an app ran at one size. StoppedAt is zero
// while it is running.
type UsageInterval struct {
	AppID     string
	Owner     string
	Template  string
	Size      string
	StartedAt time.Time
	StoppedAt time.Time
}

// Usage is the usage_intervals table. It outlives the apps it measures.
type Usage struct{ s *Store }

// Usage returns the usage_intervals table.
func (s *Store) Usage() *Usage { return &Usage{s} }

// Start opens an interval.
func (u *Usage) Start(ctx context.Context, i UsageInterval) error {
	return u.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.InsertUsage(ctx, sqlcgen.InsertUsageParams{
			ID: newID(), AppID: i.AppID, Owner: i.Owner, Template: i.Template, Size: i.Size, StartedAt: dbtime.From(i.StartedAt),
		})
	})
}

// Stop closes the app's open interval; with none open it does nothing.
func (u *Usage) Stop(ctx context.Context, appID string, at time.Time) error {
	return u.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := q.CloseUsage(ctx, sqlcgen.CloseUsageParams{StoppedAt: nullTime(at), AppID: appID})
		return err
	})
}

// ForApp returns the app's intervals, oldest first.
func (u *Usage) ForApp(ctx context.Context, appID string) ([]UsageInterval, error) {
	return intervalsOf(u.s.q.ListUsageByApp(ctx, appID))
}

// Since returns the intervals that overlap the time from since to now (still
// running, or stopped after since), oldest first.
func (u *Usage) Since(ctx context.Context, since time.Time) ([]UsageInterval, error) {
	return intervalsOf(u.s.q.ListUsageSince(ctx, nullTime(since)))
}

func intervalsOf(rows []sqlcgen.UsageInterval, err error) ([]UsageInterval, error) {
	if err != nil {
		return nil, err
	}
	out := make([]UsageInterval, len(rows))
	for i, r := range rows {
		out[i] = UsageInterval{
			AppID: r.AppID, Owner: r.Owner, Template: r.Template, Size: r.Size,
			StartedAt: r.StartedAt.Time(), StoppedAt: timeOf(r.StoppedAt),
		}
	}
	return out, nil
}
