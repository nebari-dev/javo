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

// SetTrust records until when the app's coding agent acts with its owner's
// read-write access without asking; the zero time ends that. It is audited
// as app.trust.on (with the end) or app.trust.off and announced, and leaves
// the spec and the version alone. A missing or deleted app is ErrNotFound.
func (a *Apps) SetTrust(ctx context.Context, actor model.Actor, id string, until time.Time) (model.App, error) {
	var out model.App
	err := a.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.SetAppTrust(ctx, sqlcgen.SetAppTrustParams{TrustedUntil: nullTime(until), ID: id})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrNotFound
		}
		if out, err = appKind.read(ctx, q, id); err != nil {
			return effect{}, err
		}
		if until.IsZero() {
			return appKind.effect("app.trust.off", out), nil
		}
		eff := appKind.effect("app.trust.on", out)
		eff.audit.detail = map[string]string{"until": until.UTC().Format(time.RFC3339)}
		return eff, nil
	})
	return out, err
}

// ListTrusted returns the live apps that have a trust end set, past or not.
func (a *Apps) ListTrusted(ctx context.Context) ([]model.App, error) {
	rows, err := a.s.q.ListTrustedApps(ctx)
	if err != nil {
		return nil, err
	}
	return appsOf(rows, nil)
}

// EndTrust clears the trust of app, read with ListTrusted, if it still ends
// when it did then, and records action (app.trust.expire, or app.trust.off
// when the platform switched it off). It reports whether it ended it: false
// when another gateway did first or the owner changed it.
func (a *Apps) EndTrust(ctx context.Context, actor model.Actor, app model.App, action string) (bool, error) {
	ended := false
	err := a.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.EndAppTrust(ctx, sqlcgen.EndAppTrustParams{ID: app.ID, TrustedUntil: nullTime(app.TrustedUntil)})
		if err != nil || n == 0 {
			return effect{}, err
		}
		ended = true
		return appKind.effect(action, app), nil
	})
	return ended, err
}

// AgentPolicy is the agent_policy table: the platform admin's switch for
// trusted coding agents. One row or none.
type AgentPolicy struct{ s *Store }

// AgentPolicy returns the agent_policy table.
func (s *Store) AgentPolicy() *AgentPolicy { return &AgentPolicy{s} }

// TrustedOff reports whether the platform admin switched trusted agents off;
// no row is on.
func (p *AgentPolicy) TrustedOff(ctx context.Context) (bool, error) {
	r, err := p.s.q.GetAgentPolicy(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return r.TrustedOff, err
}

// SetTrustedOff records the platform admin's switch, audited as
// agents.policy.update.
func (p *AgentPolicy) SetTrustedOff(ctx context.Context, actor model.Actor, off bool) error {
	return p.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		err := q.UpsertAgentPolicy(ctx, sqlcgen.UpsertAgentPolicyParams{TrustedOff: off, UpdatedAt: dbtime.From(now())})
		return effect{audit: &auditRow{action: "agents.policy.update", kind: "settings", name: "agents",
			detail: map[string]bool{"allowTrusted": !off}}}, err
	})
}
