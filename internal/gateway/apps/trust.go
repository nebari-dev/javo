package apps

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Trust lets a coding agent act without asking: until the time its owner
// chose (model.App.TrustedUntil), the agent's own API token acts as their
// read-write token would (authn.Trusted) instead of under the agent's
// limits. It never acts as an admin, and like every token it cannot decide
// plans, make tokens or change trust. Whether it holds is decided on every
// request, so turning it off, its end and the platform's switch all take
// effect at once; TrustJanitor only clears and records ends that passed.

// Trust lasts DefaultTrustHours unless the owner picks 1 to MaxTrustHours.
const (
	DefaultTrustHours = 8
	MaxTrustHours     = 24
)

// TrustPolicy says whether owners may trust their agents now: Config is
// agents.allowTrusted in the platform config, DB holds the platform admin's
// switch.
type TrustPolicy struct {
	Config bool
	DB     *store.Store
}

// Allowed reports whether trust is on for the platform.
func (p TrustPolicy) Allowed(ctx context.Context) (bool, error) {
	if !p.Config || p.DB == nil {
		return false, nil
	}
	off, err := p.DB.AgentPolicy().TrustedOff(ctx)
	return !off, err
}

// Trustable reports whether app, from template t, can be trusted at all: a
// coding agent with read-write API access.
func Trustable(app *model.App, t *model.Template) bool {
	return t != nil && t.Spec.Category == model.CategoryAgents && model.APIAccessOf(app, t) == model.APIAccessReadWrite
}

// trustedNow reports whether app's agent acts without asking at now.
func trustedNow(app *model.App, t *model.Template, now time.Time) bool {
	return Trustable(app, t) && now.Before(app.TrustedUntil)
}

// TrustView is a coding agent's trust as the app view shows it.
type TrustView struct {
	Allowed bool       `json:"allowed" doc:"The platform lets owners turn it on now"`
	Until   *time.Time `json:"until,omitempty" doc:"The agent acts without asking until then; absent: it asks"`
}

// trustView is the view of app's trust, nil for an app that cannot be trusted.
func (s *Workspaces) trustView(ctx context.Context, app *model.App, t *model.Template) *TrustView {
	if !Trustable(app, t) {
		return nil
	}
	allowed, err := s.cfg.Trust.Allowed(ctx)
	if err != nil {
		slog.Warn("read the agent policy", "err", err)
	}
	v := &TrustView{Allowed: allowed}
	if allowed && trustedNow(app, t, s.now()) {
		until := app.TrustedUntil.UTC()
		v.Until = &until
	}
	return v
}

type trustInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Trusted bool `json:"trusted" doc:"true: the agent acts with your read-write access without asking until hours from now; false: it asks again"`
		Hours   *int `json:"hours,omitempty" doc:"How long, 1 to 24 hours; default 8. Ignored when trusted is false."`
	}
}

func (s *Workspaces) registerTrust(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "setWorkspaceTrust", Method: http.MethodPut, Path: "/api/workspaces/{name}/trust",
		Summary: "Let a coding agent act without asking, or make it ask again (browser session only)",
		Description: "While on, the agent's own API token may do everything you may do through the API with a read-write " +
			"token, without proposing plans: start, stop and delete apps, attach your secrets, share apps. It never acts " +
			"as an admin and still cannot approve or reject plans, make tokens or change this. Only the owner, in the " +
			"browser, turns it on; it ends by itself after the hours chosen. The app does not restart.",
		Tags: []string{"workspaces"}, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *trustInput) (*workspaceOutput, error) {
		if authn.NoTokens(ctx) != nil {
			return nil, problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgTrustSessionOnly))
		}
		app, err := s.authorizedEdit(ctx, &nameInput{Name: in.Name, Owner: in.Owner}, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		until := time.Time{}
		if in.Body.Trusted {
			if until, err = s.trustUntil(ctx, app, in.Body.Hours); err != nil {
				return nil, err
			}
		}
		if app, err = s.setTrust(ctx, app, until); err != nil {
			return nil, err
		}
		return s.output(ctx, app)
	})
}

// trustUntil checks that the caller may trust app now and returns when it
// ends, hours (default DefaultTrustHours) from now.
func (s *Workspaces) trustUntil(ctx context.Context, app *model.App, hours *int) (time.Time, error) {
	if authn.User(ctx).ID != app.Owner {
		return time.Time{}, problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgTrustOwnerOnly))
	}
	t, err := s.db.Templates().Get(ctx, app.Template)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return time.Time{}, problem.FieldError("template", reasons.MsgTemplateUnavailable)
	case err != nil:
		return time.Time{}, problem.Internal("read template", err)
	}
	if !Trustable(app, &t) {
		return time.Time{}, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgTrustNotAgent))
	}
	h := DefaultTrustHours
	if hours != nil {
		h = *hours
	}
	if h < 1 || h > MaxTrustHours {
		return time.Time{}, problem.FieldError("hours", reasons.MsgTrustHours)
	}
	switch ok, err := s.cfg.Trust.Allowed(ctx); {
	case err != nil:
		return time.Time{}, problem.Internal("read the agent policy", err)
	case !ok:
		return time.Time{}, problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgTrustTurnedOff))
	}
	return s.now().UTC().Truncate(time.Second).Add(time.Duration(h) * time.Hour), nil
}

func (s *Workspaces) setTrust(ctx context.Context, app *model.App, until time.Time) (*model.App, error) {
	if until.IsZero() && app.TrustedUntil.IsZero() {
		return app, nil
	}
	out, err := s.db.Apps().SetTrust(ctx, authn.Actor(ctx), app.ID, until)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgAppNotFound))
	case err != nil:
		return nil, problem.Internal("set trust", err)
	}
	return &out, nil
}

// TrustJanitor clears the trust of agents whose trust ended, or of every
// agent while the platform has trust off, and records each on the audit log
// as model.SystemGateway. Requests stop trusting them at once without it.
type TrustJanitor struct {
	DB     *store.Store
	Policy TrustPolicy
	Now    func() time.Time
}

// Sweep ends what is due and returns how many it ended.
func (j TrustJanitor) Sweep(ctx context.Context) (int, error) {
	allowed, err := j.Policy.Allowed(ctx)
	if err != nil {
		return 0, err
	}
	return EndTrust(ctx, j.DB, model.Actor{System: model.SystemGateway}, !allowed, j.Now())
}

// Run sweeps every interval until ctx ends.
func (j TrustJanitor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := j.Sweep(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("end trusted agents", "err", err)
			}
		}
	}
}

// EndTrust clears the trust of every app whose trust ended by now, recorded
// as app.trust.expire, and with all of every other too, recorded as
// app.trust.off: the platform switched it off. It returns how many it ended.
func EndTrust(ctx context.Context, db *store.Store, actor model.Actor, all bool, now time.Time) (int, error) {
	list, err := db.Apps().ListTrusted(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, app := range list {
		action := "app.trust.expire"
		if now.Before(app.TrustedUntil) {
			if !all {
				continue
			}
			action = "app.trust.off"
		}
		switch ended, err := db.Apps().EndTrust(ctx, actor, app, action); {
		case err != nil:
			errs = append(errs, err)
		case ended:
			n++
		}
	}
	return n, errors.Join(errs...)
}
