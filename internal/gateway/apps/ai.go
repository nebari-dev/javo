package apps

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
)

// AIStatus says which AI features this installation offers. With AI off
// the UI shows no AI controls at all.
type AIStatus struct {
	Enabled  bool                     `json:"enabled"`
	Features config.AIFeatures        `json:"features"`
	Model    string                   `json:"model,omitempty"`
	SelfTest *provider.SelfTestResult `json:"selfTest,omitempty" doc:"Absent until the provider self-test has run"`
	Message  string                   `json:"message,omitempty"`
}

type aiStatusOutput struct{ Body AIStatus }

// ExplainView is an explanation with each proposal's summary, as the
// diagnosis shows its own.
type ExplainView struct {
	ai.ExplainResult
	Proposals []ProposalView `json:"proposals"`
}

type explainInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Preview bool `json:"preview,omitempty" doc:"Return what would be sent, without sending it"`
	}
}

type explainOutput struct {
	RetryAfter string `header:"Retry-After"`
	Body       ExplainView
}

type feedbackInput struct {
	Body struct {
		Feature string `json:"feature" enum:"explain,draft,palette,admin"`
		Ref     string `json:"ref" maxLength:"253" doc:"What the answer was about, such as the app's name"`
		Right   bool   `json:"right"`
	}
}

// aiAPI serves the AI routes. svc is nil when AI is off.
type aiAPI struct {
	ws       *Workspaces
	svc      *ai.Service
	features config.AIFeatures
	plans    *plansAPI
	// includeOwners names app owners in the admin report (ai.admin.includeOwners).
	includeOwners bool
	// limit holds back a person asking to explain too often (explainLimit).
	limit *ratelimit.Buckets
}

// explainLimit is how often one person may ask to explain a failure, preview
// or not: each ask reads the cluster and searches the evidence for every
// secret value the app's owner stored, and a preview uses no AI allowance.
var explainLimit = ratelimit.Spec{Every: 6 * time.Second, Burst: 10}

func (a *aiAPI) on(feature bool) bool { return a.svc != nil && feature }

func (a *aiAPI) register(api huma.API) {
	tag := []string{"ai"}
	huma.Register(api, huma.Operation{
		OperationID: "getAiStatus", Method: http.MethodGet, Path: "/api/ai/status",
		Summary: "Which AI features are on", Tags: tag, Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, _ *struct{}) (*aiStatusOutput, error) {
		if a.svc == nil {
			return &aiStatusOutput{Body: AIStatus{Message: reasons.Msg(reasons.MsgAIOff)}}, nil
		}
		st := AIStatus{Enabled: true, Features: a.features, Model: a.svc.Model}
		if r, ok := a.svc.SelfTestResult(); ok {
			// The provider's own words can name internal addresses or part of
			// a key: platform admins see them (the log has them too),
			// members only that the check failed.
			p := authn.User(ctx)
			if r.Err != "" && !slices.Contains(authz.RolesOf(p, a.ws.Roles), authz.RolePlatformAdmin) {
				r.Err = reasons.Msg(reasons.MsgAISelfTestFailed)
			}
			st.SelfTest = &r
		}
		return &aiStatusOutput{Body: st}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "explainDiagnosis", Method: http.MethodPost, Path: "/api/workspaces/{name}/diagnosis/explain",
		Summary: "Ask the model to explain a failure; its evidence is checked against what was sent", Tags: tag,
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests},
	}, a.explain)

	huma.Register(api, huma.Operation{
		OperationID: "draftWorkspace", Method: http.MethodPost, Path: "/api/ai/draft",
		Summary: "Fill the new app form from a description; nothing is created", Tags: tag,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden},
	}, a.draft)

	huma.Register(api, huma.Operation{
		OperationID: "aiCommand", Method: http.MethodPost, Path: "/api/ai/command",
		Summary: "Ask in plain words for changes to your apps; each comes back as a plan to approve", Tags: tag,
		Errors: []int{http.StatusUnauthorized},
	}, a.command)
	a.registerAdmin(api)

	huma.Register(api, huma.Operation{
		OperationID: "aiFeedback", Method: http.MethodPost, Path: "/api/ai/feedback",
		Summary: "Say whether an AI answer was right (logged, nothing else)", Tags: tag, DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, in *feedbackInput) (*struct{}, error) {
		slog.Info("ai.feedback", "user", authn.User(ctx).ID, "feature", in.Body.Feature, "ref", in.Body.Ref, "right", in.Body.Right)
		return nil, nil
	})
}

func (a *aiAPI) explain(ctx context.Context, in *explainInput) (*explainOutput, error) {
	if wait, ok := a.limit.Take(ratelimit.Bucket{Key: authn.User(ctx).ID, Spec: explainLimit}); !ok {
		return nil, problem.New(http.StatusTooManyRequests, reasons.Msg(reasons.MsgAITooManyAsks)).
			WithHeader("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	}
	app, err := a.ws.authorized(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
	if err != nil {
		return nil, err
	}
	d, err := a.ws.diagnose(ctx, app)
	if err != nil {
		return nil, err
	}
	if d.Rule == "" && d.Known {
		return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgAINothingToTell))
	}
	out := &explainOutput{Body: ExplainView{Proposals: []ProposalView{}}}
	if !a.on(a.features.Explain) {
		out.Body.Message = reasons.Msg(reasons.MsgAIOff)
		return out, nil
	}
	scope := ai.Scope{Targets: []string{app.Name}, Actions: explainActions(a.ws.actions)}
	// The owner's secret values and credential-like variables are hidden by
	// name before the patterns run, whoever asks.
	known, err := a.svc.KnownFor(ctx, app.Owner, app.Env)
	if err != nil {
		return nil, problem.Internal("read the app's secrets", err)
	}
	res, err := a.svc.Explain(ctx, authn.User(ctx).ID, d, scope, known, in.Body.Preview)
	if err != nil {
		return nil, problem.Internal("explain", err)
	}
	out.Body.ExplainResult = res
	out.RetryAfter = retryAfter(res.RetryAfter)
	if res.Explanation != nil {
		out.Body.Proposals = a.ws.proposalViews(ctx, app, res.Explanation.Actions)
	}
	return out, nil
}

// FieldProblem is one field error, as a create would answer it.
type FieldProblem struct {
	Location string `json:"location"`
	Message  string `json:"message"`
}

// DraftView is the AI-filled form with the field errors a create with it
// would get. Nothing is created.
type DraftView struct {
	ai.DraftResult
	Errors []FieldProblem `json:"errors"`
}

type draftInput struct {
	Body struct {
		Text string `json:"text" minLength:"1" maxLength:"2000" doc:"What the app should be, in plain words"`
		Kind string `json:"kind" enum:"app" doc:"What to fill in; only app forms today"`
	}
}

type draftOutput struct {
	RetryAfter string `header:"Retry-After"`
	Body       DraftView
}

func (a *aiAPI) draft(ctx context.Context, in *draftInput) (*draftOutput, error) {
	out := &draftOutput{Body: DraftView{Errors: []FieldProblem{}}}
	if !a.on(a.features.Draft) {
		out.Body.Message, out.Body.SetBy = reasons.Msg(reasons.MsgAIOff), []string{}
		return out, nil
	}
	v, err := a.draftFor(ctx, authn.User(ctx), in.Body.Text)
	if err != nil {
		return nil, err
	}
	out.Body = v
	out.RetryAfter = retryAfter(v.RetryAfter)
	return out, nil
}

// draftFor fills the new app form from text for u, with the field errors a
// create with it would get. Nothing is created.
func (a *aiAPI) draftFor(ctx context.Context, u authz.Principal, text string) (DraftView, error) {
	out := DraftView{Errors: []FieldProblem{}}
	if err := a.ws.mayCreate(u); err != nil {
		return out, err
	}
	all, err := a.ws.db.Templates().List(ctx)
	if err != nil {
		return out, problem.Internal("list templates", err)
	}
	roles := authz.RolesOf(u, a.ws.Roles)
	var tpls []ai.DraftTemplate
	for i := range all {
		t := &all[i]
		if t.Spec.JobOnly || !authz.Decide(u, roles, authz.ActUseTemplate, authz.Object{Template: t}).Allowed {
			continue
		}
		tpls = append(tpls, ai.DraftTemplate{Name: t.Name, DisplayName: t.Spec.DisplayName, Kind: t.Spec.Kind,
			Description: t.Spec.Description, Extensions: t.Spec.FileExtensions})
	}
	res := a.svc.Draft(ctx, u.ID, text, tpls)
	out.DraftResult = res
	if res.Message != "" {
		return out, nil
	}
	// A dry run of the create: the same checks, nothing stored.
	d := res.Draft
	_, err = a.ws.prepare(ctx, u, appRequest{displayName: d.DisplayName, template: d.Template, file: d.File})
	if err == nil {
		err = a.ws.nameFree(ctx, u, d.DisplayName)
	}
	if err != nil {
		var ae *problem.Details
		if nt := (nameTaken{}); errors.As(err, &nt) {
			ae = nt.Details
		} else if !errors.As(err, &ae) || len(ae.Errors) == 0 {
			return out, err
		}
		for _, e := range ae.Errors {
			out.Errors = append(out.Errors, FieldProblem{Location: e.Location, Message: e.Message})
		}
	}
	return out, nil
}

// CommandView is the palette's answer: the model's reply and one plan per
// proposed change, each waiting for approval.
type CommandView struct {
	Reply string       `json:"reply"`
	Plans []plans.Plan `json:"plans"`
	// Draft is the new app form filled from the request, when it asks for a
	// new app. Nothing is created until the person presses Create on it.
	Draft      *DraftView `json:"draft,omitempty"`
	Dropped    ai.Dropped `json:"dropped"`
	Model      string     `json:"model,omitempty"`
	Message    string     `json:"message,omitempty"`
	RetryAfter int        `json:"retryAfter,omitempty"`
}

type commandInput struct {
	Body struct {
		Text string `json:"text" minLength:"1" maxLength:"2000"`
	}
}

type commandOutput struct {
	RetryAfter string `header:"Retry-After"`
	Body       CommandView
}

func (a *aiAPI) command(ctx context.Context, in *commandInput) (*commandOutput, error) {
	out := &commandOutput{Body: CommandView{Plans: []plans.Plan{}}}
	if !a.on(a.features.Palette) {
		out.Body.Message = reasons.Msg(reasons.MsgAIOff)
		return out, nil
	}
	u := authn.User(ctx)
	mine, err := a.ws.ownApps(ctx, u, false)
	if err != nil {
		return nil, err
	}
	apps := make([]ai.AppSummary, len(mine))
	for i, app := range mine {
		apps[i] = ai.AppSummary{Name: app.Name, DisplayName: app.DisplayName, Phase: phaseWord(&app), Template: app.Template}
	}
	res := a.svc.Command(ctx, u.ID, in.Body.Text, apps, a.ws.actions.Describe())
	out.Body.Reply, out.Body.Dropped, out.Body.Model, out.Body.Message, out.Body.RetryAfter = res.Reply, res.Dropped, res.Model, res.Message, res.RetryAfter
	out.RetryAfter = retryAfter(res.RetryAfter)
	for _, p := range res.Proposals {
		pl, err := a.plans.propose(ctx, plans.SourcePalette, p)
		if err != nil {
			// The change does not apply (already done, not allowed): leave it out.
			out.Body.Dropped.Actions++
			continue
		}
		out.Body.Plans = append(out.Body.Plans, pl)
	}
	if res.Create && a.on(a.features.Draft) {
		v, err := a.draftFor(ctx, u, in.Body.Text)
		var ae *problem.Details
		switch {
		case err == nil:
			out.Body.Draft = &v
		case errors.As(err, &ae):
			out.Body.Message = ae.Title
		default:
			return nil, err
		}
	}
	return out, nil
}

// retryAfter is the Retry-After header for a wait of n seconds; empty for none.
func retryAfter(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// explainActions are the catalog actions an explanation may propose: every
// one but deleting the app.
func explainActions(c *actions.Catalog) []string {
	var out []string
	for _, i := range c.Describe() {
		if i.ID != actions.Delete {
			out = append(out, i.ID)
		}
	}
	return slices.Clip(out)
}
