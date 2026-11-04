package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Plan states.
const (
	PlanPending  = "pending"
	PlanApplied  = "applied"
	PlanRejected = "rejected"
	PlanFailed   = "failed"
)

// ErrLimit is a reservation that would go over its limit.
var ErrLimit = errors.New("store: over the limit")

// Plan is one row of the plans table: a proposed change waiting for its
// owner's approval. ID is the hash of the id handed out (internal/plans).
type Plan struct {
	ID, Owner, Source, State string
	Body                     json.RawMessage
	Error                    string
	CreatedAt, ExpiresAt     time.Time
	// DecidedAt is when it left pending; zero before.
	DecidedAt time.Time
}

// PlanAudit names the object a decided plan acts on, for its audit row.
type PlanAudit struct {
	Kind, Name, ID, Namespace, Owner string
	Detail                           map[string]string
}

// Plans is the plans table.
type Plans struct{ s *Store }

// Plans returns the plans table.
func (s *Store) Plans() *Plans { return &Plans{s} }

// Create stores p. Proposing is not audited: nothing changes until a person
// approves, and that decision is.
func (p *Plans) Create(ctx context.Context, in Plan) error {
	return p.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		if err := within(ctx, q, in.Owner, p.s.planCap(ctx, q, in.Owner, in.CreatedAt)); err != nil {
			return err
		}
		return q.InsertPlan(ctx, sqlcgen.InsertPlanParams{
			ID: in.ID, Owner: in.Owner, Source: in.Source, State: in.State, Body: string(in.Body),
			CreatedAt: dbtime.From(in.CreatedAt), ExpiresAt: dbtime.From(in.ExpiresAt),
		})
	})
}

// Get returns the plan with this id, or ErrNotFound.
func (p *Plans) Get(ctx context.Context, id string) (Plan, error) {
	r, err := p.s.q.GetPlan(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		ID: r.ID, Owner: r.Owner, Source: r.Source, State: r.State, Body: json.RawMessage(r.Body), Error: r.Error,
		CreatedAt: r.CreatedAt.Time(), ExpiresAt: r.ExpiresAt.Time(), DecidedAt: timeOf(r.DecidedAt),
	}, nil
}

// Transition moves plan id from state from to state to, once: when another
// request moved it first, the answer is ErrConflict. The decision is audited
// as "ActionApplied", "ActionRejected" or "ActionFailed" on the object a, with
// the plan's source in the detail; with a zero a it is not audited (a
// step on the way, such as claiming the plan).
func (p *Plans) Transition(ctx context.Context, actor model.Actor, id, from, to, errText string, a PlanAudit) (Plan, error) {
	err := p.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		cur, err := q.GetPlan(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return effect{}, ErrNotFound
		}
		if err != nil {
			return effect{}, err
		}
		n, err := q.TransitionPlan(ctx, sqlcgen.TransitionPlanParams{
			State: to, Error: errText, DecidedAt: nullTime(now()), ID: id, State_2: from,
		})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrConflict
		}
		if a.Kind == "" {
			return effect{}, nil
		}
		detail := map[string]string{"plan": id, "source": cur.Source}
		for k, v := range a.Detail {
			detail[k] = v
		}
		if errText != "" {
			detail["error"] = errText
		}
		return effect{audit: &auditRow{action: auditAction(to), kind: a.Kind, name: a.Name, id: a.ID,
			namespace: a.Namespace, owner: a.Owner, detail: detail}}, nil
	})
	if err != nil {
		return Plan{}, err
	}
	return p.Get(ctx, id)
}

func auditAction(state string) string {
	switch state {
	case PlanApplied:
		return "ActionApplied"
	case PlanRejected:
		return "ActionRejected"
	}
	return "ActionFailed"
}

// Sweep deletes plans that expired before now, decided or not, and returns
// how many.
func (p *Plans) Sweep(ctx context.Context, before time.Time) (int64, error) {
	var n int64
	err := p.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		var err error
		n, err = q.DeleteExpiredPlans(ctx, dbtime.From(before))
		return err
	})
	return n, err
}

// AIUsage counts the model tokens each person used per day.
type AIUsage struct{ s *Store }

// AIUsage returns the ai_usage table.
func (s *Store) AIUsage() *AIUsage { return &AIUsage{s} }

// Reserve adds tokens to person's count for day if the count stays within
// limit, in one statement, so concurrent callers on any replica cannot pass
// it together. Otherwise it returns ErrLimit and counts nothing.
func (u *AIUsage) Reserve(ctx context.Context, person, day string, tokens, limit int64) error {
	if tokens > limit {
		return ErrLimit
	}
	return u.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		_, err := q.ReserveAIUsage(ctx, sqlcgen.ReserveAIUsageParams{Person: person, Day: day, Tokens: tokens, MaxTokens: limit})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLimit
		}
		return err
	})
}

// Release takes back tokens reserved but not used.
func (u *AIUsage) Release(ctx context.Context, person, day string, tokens int64) error {
	return u.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.ReleaseAIUsage(ctx, sqlcgen.ReleaseAIUsageParams{Tokens: tokens, Person: person, Day: day})
	})
}

// Charge adds tokens used above a reservation, whatever the limit: they
// were spent, so they count.
func (u *AIUsage) Charge(ctx context.Context, person, day string, tokens int64) error {
	return u.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		return q.ChargeAIUsage(ctx, sqlcgen.ChargeAIUsageParams{Person: person, Day: day, Tokens: tokens})
	})
}

// Used is person's count for day.
func (u *AIUsage) Used(ctx context.Context, person, day string) (int64, error) {
	n, err := u.s.q.GetAIUsage(ctx, sqlcgen.GetAIUsageParams{Person: person, Day: day})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
