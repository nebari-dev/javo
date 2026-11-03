package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Session is one row of the sessions table. ID is the hex SHA-256 of the
// bearer token, so the token itself is never stored. Principal is JSON the
// session layer owns. Person is the person's name. A session of kind app or
// ticket has a ParentID (a ui session) and ends with it.
type Session struct {
	ID           string
	Kind         string
	ParentID     string
	Person       string
	Principal    string
	Provider     string
	Workspace    string
	Refresh      string
	IDToken      string
	ExpiresAt    time.Time
	RevalidateAt time.Time
	// Version is read with the session and checked by Update.
	Version int64
}

// Sessions is the sessions table. Signing in and out (creating and deleting
// a ui session) is audited; app sessions and tickets are not: they are made
// per app request and would bury the log.
type Sessions struct{ s *Store }

// sessionKindUI is the kind of a browser sign-in session; the session layer
// owns the full list of kinds.
const sessionKindUI = "ui"

// signInEffect is the audit row for a ui session, or nothing for any other
// kind.
func signInEffect(action, kind, person string) effect {
	if kind != sessionKindUI {
		return effect{}
	}
	return effect{audit: &auditRow{action: action, kind: "person", name: person}}
}

// Sessions returns the sessions table.
func (s *Store) Sessions() *Sessions { return &Sessions{s} }

// Create stores a new session at version 1; a ui session (sign-in) is
// audited.
func (ss *Sessions) Create(ctx context.Context, actor model.Actor, sess Session) error {
	return ss.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		return signInEffect("session.create", sess.Kind, sess.Person), q.InsertSession(ctx, sqlcgen.InsertSessionParams{
			ID: sess.ID, Kind: sess.Kind, ParentID: sql.NullString{String: sess.ParentID, Valid: sess.ParentID != ""},
			Person: sess.Person, Principal: sess.Principal, Provider: sess.Provider, Workspace: sess.Workspace,
			Refresh: sess.Refresh, IDToken: sess.IDToken,
			ExpiresAt: dbtime.From(sess.ExpiresAt), RevalidateAt: dbtime.From(sess.RevalidateAt),
		})
	})
}

// Get returns the session, expired or not, or ErrNotFound.
func (ss *Sessions) Get(ctx context.Context, id string) (Session, error) {
	row, err := ss.s.q.GetSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sessionOf(row), err
}

// Update writes the changeable fields of sess if the stored version is still
// sess.Version; otherwise ErrConflict (or ErrNotFound if it is gone).
func (ss *Sessions) Update(ctx context.Context, sess Session) error {
	return ss.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.UpdateSession(ctx, sqlcgen.UpdateSessionParams{
			Principal: sess.Principal, Refresh: sess.Refresh, IDToken: sess.IDToken,
			ExpiresAt: dbtime.From(sess.ExpiresAt), RevalidateAt: dbtime.From(sess.RevalidateAt),
			ID: sess.ID, Version: sess.Version,
		})
		if err != nil || n > 0 {
			return err
		}
		if _, err := q.GetSession(ctx, sess.ID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	})
}

// Delete ends a session and, by cascade, every app session and ticket made
// from it. Deleting a ui session (sign-out) is audited. Deleting a session
// that is gone is not an error and is not audited.
func (ss *Sessions) Delete(ctx context.Context, actor model.Actor, id string) error {
	return ss.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		row, err := q.DeleteSession(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, nil
		}
		return signInEffect("session.delete", row.Kind, row.Person), err
	})
}

// DeleteFor ends every sign-in of the person (sign out everywhere), with the
// app sessions and tickets made from them, and returns how many sign-ins went. It is audited when it ended any. The other
// session writes are not: they are per-request plumbing that would bury the
// log.
func (ss *Sessions) DeleteFor(ctx context.Context, actor model.Actor, person string) (int64, error) {
	var n int64
	err := ss.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		var err error
		if n, err = q.DeleteSessionsForPerson(ctx, person); err != nil || n == 0 {
			return effect{}, err
		}
		return effect{audit: &auditRow{action: "session.delete_for", kind: "person", name: person, detail: map[string]int64{"sessions": n}}}, nil
	})
	return n, err
}

// Redeem deletes the session and returns it in one step, so a ticket works
// once; a second Redeem is ErrNotFound.
func (ss *Sessions) Redeem(ctx context.Context, id string) (Session, error) {
	var row sqlcgen.Session
	err := ss.s.inTx(ctx, func(q *sqlcgen.Queries) (err error) {
		row, err = q.DeleteSession(ctx, id)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sessionOf(row), err
}

// Sweep deletes sessions that expired before the given time and returns how
// many were swept directly (their children go by cascade).
func (ss *Sessions) Sweep(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := ss.s.inTx(ctx, func(q *sqlcgen.Queries) (err error) {
		n, err = q.SweepSessions(ctx, dbtime.From(before))
		return err
	})
	return n, err
}

func sessionOf(r sqlcgen.Session) Session {
	return Session{
		ID: r.ID, Kind: r.Kind, ParentID: r.ParentID.String, Person: r.Person, Principal: r.Principal,
		Provider: r.Provider, Workspace: r.Workspace, Refresh: r.Refresh, IDToken: r.IDToken,
		ExpiresAt: r.ExpiresAt.Time(), RevalidateAt: r.RevalidateAt.Time(), Version: r.Version,
	}
}
