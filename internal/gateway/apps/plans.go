package apps

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
)

type planCreateInput struct {
	Body struct {
		Proposal actions.Proposal `json:"proposal"`
		Source   string           `json:"source" pattern:"^(rules|ai-diagnosis|palette|mcp:[A-Za-z0-9 ._-]{1,64})$" doc:"What proposed the change: rules, ai-diagnosis, palette, or mcp:<agent name>"`
	}
}

type planIDInput struct {
	ID string `path:"id" maxLength:"64"`
}

type planOutput struct{ Body plans.Plan }

type plansAPI struct {
	ws    *Workspaces
	plans *plans.Service
}

func (a *plansAPI) register(api huma.API) {
	tag := []string{"plans"}
	huma.Register(api, huma.Operation{
		OperationID: "createPlan", Method: http.MethodPost, Path: authn.PlansRoute,
		Summary: "Propose a change for review; nothing changes until it is approved", Tags: tag, DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *planCreateInput) (*planOutput, error) {
		p, err := a.propose(ctx, in.Body.Source, in.Body.Proposal)
		if err != nil {
			return nil, err
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getPlan", Method: http.MethodGet, Path: authn.PlansRoute + "/{id}",
		Summary: "A proposed change and its state", Tags: tag,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, func(ctx context.Context, in *planIDInput) (*planOutput, error) {
		p, err := a.plans.Get(ctx, authn.User(ctx).ID, in.ID)
		if err != nil {
			return nil, planError(p, err)
		}
		if p.State == plans.Expired {
			p.Message = reasons.Msg(reasons.MsgPlanExpired)
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "approvePlan", Method: http.MethodPost, Path: authn.PlansRoute + "/{id}/approve",
		Summary: "Approve and make a proposed change (browser session only)", Tags: tag, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusGone, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *planIDInput) (*planOutput, error) {
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		cur, err := a.plans.Get(ctx, u.ID, in.ID)
		if err != nil {
			return nil, planError(cur, err)
		}
		act, ok := a.ws.actions.Get(cur.Planned.Action)
		if !ok {
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgActionNotAllowed))
		}
		p, err := a.plans.Approve(ctx, authn.Actor(ctx), u.ID, in.ID, a.audit(ctx, cur.Planned.Proposal), act.Apply)
		if err != nil {
			return nil, planError(p, err)
		}
		return &planOutput{Body: p}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "rejectPlan", Method: http.MethodPost, Path: authn.PlansRoute + "/{id}/reject",
		Summary: "Turn down a proposed change (browser session only)", Tags: tag, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, func(ctx context.Context, in *planIDInput) (*planOutput, error) {
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		cur, err := a.plans.Get(ctx, u.ID, in.ID)
		if err != nil {
			return nil, planError(cur, err)
		}
		p, err := a.plans.Reject(ctx, authn.Actor(ctx), u.ID, in.ID, a.audit(ctx, cur.Planned.Proposal))
		if err != nil {
			return nil, planError(p, err)
		}
		return &planOutput{Body: p}, nil
	})
}

// propose checks p as the person in ctx (the same checks as the change's own
// route) and stores it as a plan from source.
func (a *plansAPI) propose(ctx context.Context, source string, p actions.Proposal) (plans.Plan, error) {
	pl, err := a.ws.actions.Plan(ctx, p)
	if errors.Is(err, actions.ErrUnknownAction) {
		return plans.Plan{}, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgActionNotAllowed))
	}
	if err != nil {
		return plans.Plan{}, err
	}
	var by plans.Requester
	if tok, ok := authn.APIToken(ctx); ok {
		switch {
		case tok.App != nil:
			by.App = tok.App.Short
		case tok.Job != "":
			by.Job = true
		default:
			by.Token = tok.Name
		}
	}
	out, err := a.plans.Create(ctx, authn.User(ctx).ID, source, by, pl)
	if err != nil {
		return plans.Plan{}, problem.Internal("store plan", err)
	}
	return out, nil
}

// audit names the app a plan acts on, when it still exists.
func (a *plansAPI) audit(ctx context.Context, p actions.Proposal) plans.Audit {
	out := plans.Audit{Kind: "app", Name: p.Target}
	if app, err := a.ws.allowed(ctx, &nameInput{Name: p.Target, Owner: p.Owner}, authz.ActManageApp); err == nil {
		out.ID, out.Namespace, out.Owner = app.ID, app.Namespace, app.Owner
	}
	return out
}

// planError maps the plans errors to catalog answers; any other error (such
// as the change's own refusal) passes through.
func planError(p plans.Plan, err error) error {
	switch {
	case errors.Is(err, plans.ErrNotFound):
		return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgPlanNotFound))
	case errors.Is(err, plans.ErrExpired):
		return problem.New(http.StatusGone, reasons.Msg(reasons.MsgPlanExpired))
	case errors.Is(err, plans.ErrDone):
		return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgPlanDone, stateWords(p.State)))
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return err
	}
	return problem.Internal("plan", err)
}

func stateWords(state string) string {
	switch state {
	case plans.Applied, plans.Applying:
		return reasons.Msg(reasons.MsgPlanStateApplied)
	case plans.Rejected:
		return reasons.Msg(reasons.MsgPlanStateReject)
	}
	return reasons.Msg(reasons.MsgPlanStateFailed)
}
