package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// newID returns a UUID v7: ids sort by creation time.
var newID = func() string { return uuid.Must(uuid.NewV7()).String() }

// effect is what a write did: the audit row to record with it, if any, and
// the changes to announce once it commits.
type effect struct {
	audit   *auditRow
	changes []Change
}

// auditRow names the object acted on. Apps and jobs repeat short names
// across owners, so their rows also carry id, namespace and owner.
type auditRow struct {
	action, kind, name   string
	id, namespace, owner string
	detail               any
}

// Writes that take no actor and write no audit row, by design: app session
// and ticket create and delete, session update, redeem and sweep
// (per-request plumbing and expiry), app and job status, preview webhook pokes
// and sync results, repository token hand-offs, Nebi token renewals, RecordRun,
// MarkRunNotified, API token last-use times and the expiry sweep, device login starts, polls,
// spends and sweep, usage intervals and cluster facts (the controller's own
// bookkeeping at high volume). Sign-in and sign-out (ui session create and
// delete), sign-out everywhere (Sessions.DeleteFor), gid allocation
// (Groups.Ensure), token creation and revocation and run history deletion (Jobs.PruneRuns) are audited.

// mutate runs fn in one transaction. The audit row fn asks for is written in
// the same transaction, so the write and its audit row commit together or
// not at all; the changes are announced after the commit.
func (s *Store) mutate(ctx context.Context, actor model.Actor, fn func(*sqlcgen.Queries) (effect, error)) error {
	return s.inTxAnnounce(ctx, func(q *sqlcgen.Queries) ([]Change, error) {
		eff, err := fn(q)
		if err != nil {
			return nil, err
		}
		if eff.audit != nil {
			if err := recordAudit(ctx, q, actor, *eff.audit); err != nil {
				return nil, err
			}
		}
		return eff.changes, nil
	})
}

// ActorStrings is how an Actor reads on an audit row: the name, and how they
// acted (a token, a browser session, or a system component).
func ActorStrings(a model.Actor) (actor, via string) {
	switch {
	case a.System != "":
		return a.System, "system"
	case a.App != "":
		return "app:" + a.App, "trusted"
	case a.Token != "":
		return a.Person, "token:" + a.Token
	}
	return a.Person, "session"
}

func recordAudit(ctx context.Context, q *sqlcgen.Queries, actor model.Actor, r auditRow) error {
	who, via := ActorStrings(actor)
	detail, err := marshal(r.detail, "{}")
	if err != nil {
		return err
	}
	return q.InsertAudit(ctx, sqlcgen.InsertAuditParams{
		ID: newID(), At: dbtime.From(now()), Actor: who, Via: via, Action: r.action,
		ObjectKind: r.kind, ObjectName: r.name, ObjectID: r.id, ObjectNamespace: r.namespace,
		ObjectOwner: r.owner, Allowed: true, Detail: detail,
	})
}

// marshal encodes v as JSON text; nil is empty.
func marshal(v any, empty string) (string, error) {
	if v == nil {
		return empty, nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// unmarshal decodes JSON text written by marshal; empty text leaves v alone.
func unmarshal(text string, v any) error {
	if text == "" {
		return nil
	}
	return json.Unmarshal([]byte(text), v)
}
