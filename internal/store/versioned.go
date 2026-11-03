package store

import (
	"context"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// rowInfo is what the shared flows need to know about a row of a versioned
// table, whatever its type.
type rowInfo struct {
	ID, Namespace, Name, Owner string
	Version                    int64
	Deleted                    bool
}

// rowKind describes one versioned table (apps, jobs) to the flows they share:
// guarded spec update, mark-deleted, purge and status. A table supplies only
// its column mapping; the read, check, validate, write, audit order lives here.
type rowKind[T any] struct {
	kind  string // audit object kind: "app"
	table string // change feed table: "apps"

	read     func(context.Context, *sqlcgen.Queries, string) (T, error)
	info     func(T) rowInfo
	prepare  func(*T)                 // fills defaults
	validate func(next, old *T) error // next is already prepared
	// writeSpec stores next's spec columns where id and version match and returns
	// the rows changed; it must raise the version by one.
	writeSpec   func(ctx context.Context, q *sqlcgen.Queries, id string, version int64, next *T) (int64, error)
	deleteRow   func(ctx context.Context, q *sqlcgen.Queries, id string, at time.Time) (int64, error)
	purgeRow    func(ctx context.Context, q *sqlcgen.Queries, id string) (int64, error)
	writeStatus func(ctx context.Context, q *sqlcgen.Queries, id string, observedVersion int64, status string, at time.Time) (int64, error)
	// onStatus, when set, runs in the status write's transaction before the
	// write, with the status about to be stored.
	onStatus func(ctx context.Context, q *sqlcgen.Queries, id string, status any, at time.Time) error
}

// effect is the audit row and change announcement for an action on v; with no
// changes given it announces the row itself.
func (k rowKind[T]) effect(action string, v T, changes ...Change) effect {
	info := k.info(v)
	if len(changes) == 0 {
		changes = []Change{{k.table, info.ID}}
	}
	return effect{audit: &auditRow{
		action: action, kind: k.kind, name: info.Name, id: info.ID, namespace: info.Namespace, owner: info.Owner,
	}, changes: changes}
}

// update reads the row, applies fn to a copy, validates the result and
// writes it only if the stored version is still version (else ErrConflict).
// A row marked deleted is ErrNotFound. Status columns are not touched.
func (k rowKind[T]) update(ctx context.Context, s *Store, actor model.Actor, id string, version int64, fn func(*T) error) (T, error) {
	return k.updateAs(ctx, s, actor, k.kind+".update", id, version, fn)
}

// updateAs is update with the audit action named.
func (k rowKind[T]) updateAs(ctx context.Context, s *Store, actor model.Actor, action, id string, version int64, fn func(*T) error) (T, error) {
	return k.updateWith(ctx, s, actor, action, id, version, fn, nil)
}

// updateWith is updateAs that also runs also in the same transaction, after
// the spec is written; its detail goes into the audit row. An error from it
// undoes the whole update.
func (k rowKind[T]) updateWith(ctx context.Context, s *Store, actor model.Actor, action, id string, version int64, fn func(*T) error,
	also func(q *sqlcgen.Queries, next *T) (detail any, err error)) (T, error) {
	var out T
	err := s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		old, err := k.read(ctx, q, id)
		if err != nil {
			return effect{}, err
		}
		if info := k.info(old); info.Deleted {
			return effect{}, ErrNotFound
		} else if info.Version != version {
			return effect{}, ErrConflict
		}
		// fn gets a copy it may change freely: a second read shares no slice
		// or pointer with old.
		next, err := k.read(ctx, q, id)
		if err != nil {
			return effect{}, err
		}
		if err := fn(&next); err != nil {
			return effect{}, err
		}
		k.prepare(&next)
		if err := k.validate(&next, &old); err != nil {
			return effect{}, err
		}
		n, err := k.writeSpec(ctx, q, id, version, &next)
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrConflict
		}
		var detail any
		if also != nil {
			if detail, err = also(q, &next); err != nil {
				return effect{}, err
			}
		}
		if out, err = k.read(ctx, q, id); err != nil {
			return effect{}, err
		}
		eff := k.effect(action, out)
		eff.audit.detail = detail
		return eff, nil
	})
	return out, err
}

// markDeleted marks the row deleted and raises its version. A row that is gone or
// already marked is ErrNotFound. dependents, when set, lists the live rows that
// go with it: each is marked the same way in the same transaction, with its own
// audit row and announcement.
func (k rowKind[T]) markDeleted(ctx context.Context, s *Store, actor model.Actor, id string, dependents func(context.Context, *sqlcgen.Queries) ([]T, error)) error {
	return k.remove(ctx, s, actor, id, "delete", func(ctx context.Context, q *sqlcgen.Queries) (int64, error) {
		return k.deleteRow(ctx, q, id, now())
	}, dependents)
}

// purge removes a row marked deleted. A live or missing row is ErrNotFound.
func (k rowKind[T]) purge(ctx context.Context, s *Store, actor model.Actor, id string) error {
	return k.remove(ctx, s, actor, id, "purge", func(ctx context.Context, q *sqlcgen.Queries) (int64, error) {
		return k.purgeRow(ctx, q, id)
	}, nil)
}

func (k rowKind[T]) remove(ctx context.Context, s *Store, actor model.Actor, id, verb string, do func(context.Context, *sqlcgen.Queries) (int64, error), dependents func(context.Context, *sqlcgen.Queries) ([]T, error)) error {
	return s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		v, err := k.read(ctx, q, id)
		if err != nil {
			return effect{}, err
		}
		n, err := do(ctx, q)
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrNotFound
		}
		changes := []Change{{k.table, id}}
		if dependents != nil {
			rows, err := dependents(ctx, q)
			if err != nil {
				return effect{}, err
			}
			for _, d := range rows {
				dep := k.info(d)
				if _, err := k.deleteRow(ctx, q, dep.ID, now()); err != nil {
					return effect{}, err
				}
				e := k.effect(k.kind+"."+verb, d)
				if err := recordAudit(ctx, q, actor, *e.audit); err != nil {
					return effect{}, err
				}
				changes = append(changes, e.changes...)
			}
		}
		return k.effect(k.kind+"."+verb, v, changes...), nil
	})
}

// recordStatus stores what the controller observed and which spec version it
// acted on. It writes status columns only, so it never undoes a spec edit,
// and it is neither audited nor announced: it is the controller's own write.
func (k rowKind[T]) recordStatus(ctx context.Context, s *Store, id string, observedVersion int64, status any) error {
	text, err := marshal(status, "{}")
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(q *sqlcgen.Queries) error {
		at := now()
		if k.onStatus != nil {
			if err := k.onStatus(ctx, q, id, status, at); err != nil {
				return err
			}
		}
		n, err := k.writeStatus(ctx, q, id, observedVersion, text, at)
		if err == nil && n == 0 {
			err = ErrNotFound
		}
		return err
	})
}
