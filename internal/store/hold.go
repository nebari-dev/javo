package store

import (
	"context"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// ReleaseMode is how an admin lets a restore's held apps go.
type ReleaseMode string

const (
	// ReleaseStart starts the apps that were running when the backup was
	// taken, at a bounded rate; the others stay stopped.
	ReleaseStart ReleaseMode = "start"
	// ReleaseStopped stops every app of the restore; their owners start them.
	ReleaseStopped ReleaseMode = "stopped"
)

// HoldAll holds every live app under the restore name hold, so that after
// a whole-database restore nothing starts (and no home claim is made) until
// an admin releases them. It returns how many apps it held.
func (a *Apps) HoldAll(ctx context.Context, actor model.Actor, hold string) (int, error) {
	var n int
	err := a.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		ids, err := q.ListLiveAppIDs(ctx)
		if err != nil {
			return effect{}, err
		}
		var changes []Change
		for _, id := range ids {
			k, err := q.HoldApp(ctx, sqlcgen.HoldAppParams{RestoreHold: hold, ID: id})
			if err != nil {
				return effect{}, err
			}
			if k > 0 {
				changes = append(changes, Change{Table: "apps", ID: id})
			}
		}
		n = len(changes)
		return effect{
			audit:   &auditRow{action: "restore.hold", kind: "restore", name: hold, detail: map[string]int{"apps": n}},
			changes: changes,
		}, nil
	})
	return n, err
}

// ListHeld returns the apps still held at now, by restore and name.
func (a *Apps) ListHeld(ctx context.Context, now time.Time) ([]model.App, error) {
	rows, err := a.s.q.ListHeldApps(ctx, dbtime.NullTime{Time: dbtime.From(now), Valid: true})
	if err != nil {
		return nil, err
	}
	return appsOf(rows, nil)
}

// Release lets the not yet released apps of restore hold go. With
// ReleaseStart the apps that are to run get start times perMinute apart
// (model.ReleaseTimes), in name order, and stopped apps go at now; with
// ReleaseStopped every app is stopped and goes at now. Each app's version
// is raised, so the controller hears of it. It returns how many it released.
func (a *Apps) Release(ctx context.Context, actor model.Actor, hold string, mode ReleaseMode, perMinute int, now time.Time) (int, error) {
	var n int
	err := a.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		rows, err := q.ListUnreleasedApps(ctx, hold)
		if err != nil {
			return effect{}, err
		}
		running := 0
		for _, r := range rows {
			if !r.Paused && mode == ReleaseStart {
				running++
			}
		}
		slots := model.ReleaseTimes(running, perMinute, now)
		var changes []Change
		for _, r := range rows {
			at, paused := now, r.Paused || mode == ReleaseStopped
			if !paused {
				at, slots = slots[0], slots[1:]
			}
			k, err := q.ReleaseApp(ctx, sqlcgen.ReleaseAppParams{
				RestoreReleaseAt: dbtime.NullTime{Time: dbtime.From(at), Valid: true}, Paused: paused, ID: r.ID,
			})
			if err != nil {
				return effect{}, err
			}
			if k > 0 {
				changes = append(changes, Change{Table: "apps", ID: r.ID})
			}
		}
		n = len(changes)
		return effect{
			audit: &auditRow{action: "restore.release", kind: "restore", name: hold,
				detail: map[string]any{"apps": n, "mode": mode, "perMinute": perMinute}},
			changes: changes,
		}, nil
	})
	return n, err
}
