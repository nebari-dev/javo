package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// AuditEvent is one row of the audit log. Writes made through the store
// record theirs in the same transaction; Append is for the rest, such as a
// refused request.
type AuditEvent struct {
	ID         string
	At         time.Time
	Actor      string
	Via        string
	Action     string
	ObjectKind string
	ObjectName string
	// ObjectID, ObjectNamespace and ObjectOwner tell apart objects whose
	// short names repeat (apps, jobs); empty for kinds keyed by name alone.
	ObjectID        string
	ObjectNamespace string
	ObjectOwner     string
	Allowed         bool
	Detail          json.RawMessage
}

// Audit reads and appends the audit log.
type Audit struct{ s *Store }

// Audit returns the audit log.
func (s *Store) Audit() *Audit { return &Audit{s} }

// Append records an event. A zero ID and At are filled in.
func (a *Audit) Append(ctx context.Context, e AuditEvent) error {
	if e.ID == "" {
		e.ID = newID()
	}
	if e.At.IsZero() {
		e.At = now()
	}
	detail := string(e.Detail)
	if detail == "" {
		detail = "{}"
	}
	return a.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.InsertAudit(ctx, sqlcgen.InsertAuditParams{
			ID: e.ID, At: dbtime.From(e.At), Actor: e.Actor, Via: e.Via, Action: e.Action,
			ObjectKind: e.ObjectKind, ObjectName: e.ObjectName, ObjectID: e.ObjectID, ObjectNamespace: e.ObjectNamespace,
			ObjectOwner: e.ObjectOwner, Allowed: e.Allowed, Detail: detail,
		})
	})
}

// List returns the newest events first, at most limit.
func (a *Audit) List(ctx context.Context, limit int) ([]AuditEvent, error) {
	rows, err := a.s.q.ListAudit(ctx, int32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]AuditEvent, len(rows))
	for i, r := range rows {
		out[i] = AuditEvent{
			ID: r.ID, At: r.At.Time(), Actor: r.Actor, Via: r.Via, Action: r.Action,
			ObjectKind: r.ObjectKind, ObjectName: r.ObjectName, ObjectID: r.ObjectID, ObjectNamespace: r.ObjectNamespace,
			ObjectOwner: r.ObjectOwner, Allowed: r.Allowed, Detail: json.RawMessage(r.Detail),
		}
	}
	return out, nil
}
