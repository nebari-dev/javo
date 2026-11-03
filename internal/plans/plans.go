// Package plans keeps proposed changes until their owner approves or turns
// them down. Rules, a model, the command palette and agents (MCP) all propose
// through it; nothing they propose changes anything until a signed-in person
// approves it in the browser.
package plans

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Sources of a plan. An agent's plans are "mcp:<client name>".
const (
	SourceRules       = "rules"
	SourceAIDiagnosis = "ai-diagnosis"
	SourcePalette     = "palette"
	SourceMCPPrefix   = "mcp:"
)

// States. Applying is held while the change is made, so two approvals of one
// plan cannot both make it.
const (
	Pending  = store.PlanPending
	Applying = "applying"
	Applied  = store.PlanApplied
	Rejected = store.PlanRejected
	Failed   = store.PlanFailed
	Expired  = "expired"
)

// DefaultTTL is how long a plan waits for approval.
const DefaultTTL = 10 * time.Minute

var (
	ErrNotFound = errors.New("plans: not found")
	ErrExpired  = errors.New("plans: expired")
	// ErrDone is a plan no longer pending; Plan.State says what became of it.
	ErrDone = errors.New("plans: already decided")
)

// Plan is a proposed change. ID is the secret handle: it is shown once to
// the person who may approve it and stored only as a hash.
type Plan struct {
	ID      string          `json:"id"`
	Owner   string          `json:"-"`
	Source  string          `json:"source"`
	State   string          `json:"state" enum:"pending,applying,applied,rejected,failed,expired"`
	Planned actions.Planned `json:"planned"`
	Error   string          `json:"error,omitempty"`
	// Message is the catalog text for a plan that can no longer be decided.
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Service stores plans in the database.
type Service struct {
	DB  *store.Store
	TTL time.Duration
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func key(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// Create stores p for owner and returns it with its new id.
func (s *Service) Create(ctx context.Context, owner, source string, p actions.Planned) (Plan, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Plan{}, err
	}
	ttl := s.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	now := s.now()
	out := Plan{ID: base64.RawURLEncoding.EncodeToString(raw), Owner: owner, Source: source, State: Pending,
		Planned: p, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	body, err := json.Marshal(p)
	if err != nil {
		return Plan{}, err
	}
	err = s.DB.Plans().Create(ctx, store.Plan{ID: key(out.ID), Owner: owner, Source: source, State: Pending,
		Body: body, CreatedAt: out.CreatedAt, ExpiresAt: out.ExpiresAt})
	return out, err
}

// Get returns owner's plan id. Another person's plan is ErrNotFound, the
// same as a missing one. A pending plan past its expiry reads as Expired.
func (s *Service) Get(ctx context.Context, owner, id string) (Plan, error) {
	r, err := s.DB.Plans().Get(ctx, key(id))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return Plan{}, ErrNotFound
	case err != nil:
		return Plan{}, err
	case r.Owner != owner:
		return Plan{}, ErrNotFound
	}
	out := Plan{ID: id, Owner: r.Owner, Source: r.Source, State: r.State, Error: r.Error, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt}
	if err := json.Unmarshal(r.Body, &out.Planned); err != nil {
		return Plan{}, err
	}
	if out.State == Pending && !s.now().Before(out.ExpiresAt) {
		out.State = Expired
	}
	return out, nil
}

// Audit names the object a plan acts on, for the audit row of its decision.
type Audit = store.PlanAudit

// Approve runs apply once for owner's pending plan id: it claims the plan
// (a second approval gets ErrDone), calls apply, and records the outcome,
// audited with the plan's source. An expired plan is ErrExpired and nothing
// runs. When apply fails, the plan is Failed and apply's error is returned.
func (s *Service) Approve(ctx context.Context, actor model.Actor, owner, id string, a Audit, apply func(context.Context, actions.Proposal) error) (Plan, error) {
	p, err := s.Get(ctx, owner, id)
	if err != nil {
		return Plan{}, err
	}
	switch p.State {
	case Pending:
	case Expired:
		return p, ErrExpired
	default:
		return p, ErrDone
	}
	if _, err := s.DB.Plans().Transition(ctx, actor, key(id), Pending, Applying, "", Audit{}); errors.Is(err, store.ErrConflict) {
		p, _ = s.Get(ctx, owner, id)
		return p, ErrDone
	} else if err != nil {
		return Plan{}, err
	}
	a.Detail = withSummary(a.Detail, p.Planned.Summary)
	if applyErr := apply(ctx, p.Planned.Proposal); applyErr != nil {
		if _, err := s.DB.Plans().Transition(ctx, actor, key(id), Applying, Failed, applyErr.Error(), a); err != nil {
			slog.Error("record failed plan", "err", err)
		}
		return s.after(ctx, owner, id), applyErr
	}
	if _, err := s.DB.Plans().Transition(ctx, actor, key(id), Applying, Applied, "", a); err != nil {
		return Plan{}, err
	}
	return s.after(ctx, owner, id), nil
}

// Reject turns down owner's pending plan id.
func (s *Service) Reject(ctx context.Context, actor model.Actor, owner, id string, a Audit) (Plan, error) {
	p, err := s.Get(ctx, owner, id)
	if err != nil {
		return Plan{}, err
	}
	if p.State != Pending {
		return p, ErrDone
	}
	a.Detail = withSummary(a.Detail, p.Planned.Summary)
	if _, err := s.DB.Plans().Transition(ctx, actor, key(id), Pending, Rejected, "", a); errors.Is(err, store.ErrConflict) {
		p, _ = s.Get(ctx, owner, id)
		return p, ErrDone
	} else if err != nil {
		return Plan{}, err
	}
	return s.after(ctx, owner, id), nil
}

func (s *Service) after(ctx context.Context, owner, id string) Plan {
	p, err := s.Get(ctx, owner, id)
	if err != nil {
		slog.Error("read plan", "err", err)
	}
	return p
}

func withSummary(d map[string]string, summary string) map[string]string {
	out := map[string]string{"summary": summary}
	for k, v := range d {
		out[k] = v
	}
	return out
}

// RunJanitor deletes expired plans every interval until ctx ends.
func (s *Service) RunJanitor(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.DB.Plans().Sweep(ctx, s.now()); err != nil {
				slog.Error("sweep plans", "err", err)
			}
		}
	}
}
