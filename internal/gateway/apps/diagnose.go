package apps

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/reasons"
)

// What the diagnosis reads of the app's log: the newest lines of the current
// run and, after a restart, of the run before it.
const (
	diagnoseTailLines = 200
	diagnoseMaxBytes  = 64 << 10
)

// ProposalView is a proposed change with what it does in words.
type ProposalView struct {
	actions.Proposal
	Summary string `json:"summary"`
}

// Diagnosis is the cause of an app's state found by the rules, with the
// catalog text for it and the changes that may fix it.
type Diagnosis struct {
	Rule  string `json:"rule"`
	Code  string `json:"code"`
	Known bool   `json:"known" doc:"False when no rule pinned the cause down"`
	Title string `json:"title"`
	What  string `json:"what"`
	Todo  string `json:"todo"`
	Blame string `json:"blame"`
	Who   string `json:"who"`
	// Evidence backs the finding; Bundle is everything collected.
	Evidence  []diagnose.Evidence `json:"evidence"`
	Bundle    []diagnose.Evidence `json:"bundle"`
	Proposals []ProposalView      `json:"proposals"`
}

type diagnosisOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         Diagnosis
}

func (s *Workspaces) registerDiagnosis(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "getDiagnosis", Method: http.MethodGet, Path: "/api/workspaces/{name}/diagnosis",
		Summary: "Why an app failed: the cause the rules found, its evidence and the changes that may fix it", Tags: []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized, http.StatusNotFound},
	}, func(ctx context.Context, in *nameInput) (*diagnosisOutput, error) {
		app, err := s.authorized(ctx, in, authz.ActManageApp)
		if err != nil {
			return nil, err
		}
		d, err := s.diagnose(ctx, app)
		if err != nil {
			return nil, err
		}
		return &diagnosisOutput{CacheControl: "no-store", Body: s.diagnosisView(ctx, app, d)}, nil
	})
}

// diagnose collects what the rules read about app and runs them. It reads the
// app's own Pod, the events about it and the app container's log, nothing
// else: no other pod, no Secret, no environment value.
func (s *Workspaces) diagnose(ctx context.Context, app *model.App) (diagnose.Diagnosis, error) {
	in := &diagnose.Input{App: app}
	u := authn.User(ctx)
	if cur, ok := s.Sizes.Resolve(app.Size); ok {
		in.Current = cur
	}
	in.Sizes = s.Usable(u).List()
	pod, err := s.pod(ctx, app)
	if err != nil {
		return diagnose.Diagnosis{}, err
	}
	if pod != nil {
		if in.Events, err = s.eventsAbout(ctx, app.Namespace, "Pod", pod.Name, pod.UID); err != nil {
			return diagnose.Diagnosis{}, err
		}
		in.Logs = append(in.Logs, s.logLines(ctx, pod, false)...)
		if cs := podlogs.ContainerStatus(pod, podspec.ContainerName); cs != nil && cs.RestartCount > 0 {
			in.Logs = append(in.Logs, s.logLines(ctx, pod, true)...)
		}
	}
	return s.engine.Diagnose(in), nil
}

// logLines reads one run of the app container's log. A log that cannot be
// read (the pod went away meanwhile) gives no lines, not an error.
func (s *Workspaces) logLines(ctx context.Context, pod *corev1.Pod, previous bool) []diagnose.LogLine {
	tail := int64(diagnoseTailLines)
	rc, err := podlogs.Open(ctx, s.logs, pod, &corev1.PodLogOptions{
		Timestamps: true, TailLines: &tail, Previous: previous, LimitBytes: new(int64(diagnoseMaxBytes)),
	})
	if err != nil || rc == nil {
		return nil
	}
	defer rc.Close()
	var out []diagnose.LogLine
	sc := podlogs.Scanner(rc)
	for sc.Scan() {
		out = append(out, diagnose.LogLine{Container: "app", Previous: previous, N: len(out) + 1, Text: podlogs.ParseLine(sc.Text()).Text})
	}
	return out
}

// diagnosisView adds the catalog text and each proposal's summary. A proposal
// the person could not make (the planner refuses it) is left out.
func (s *Workspaces) diagnosisView(ctx context.Context, app *model.App, d diagnose.Diagnosis) Diagnosis {
	r, _ := reasons.Lookup(d.Code)
	out := Diagnosis{
		Rule: d.Rule, Code: string(d.Code), Known: d.Known,
		Title: r.Title, What: r.What, Todo: r.Todo, Blame: r.Blame, Who: r.Who,
		Evidence: d.Evidence, Bundle: d.Bundle, Proposals: s.proposalViews(ctx, d.Proposals),
	}
	return out
}

// proposalViews adds each proposal's summary; one the person in ctx could
// not make (the action refuses it) is left out.
func (s *Workspaces) proposalViews(ctx context.Context, ps []actions.Proposal) []ProposalView {
	out := []ProposalView{}
	for _, p := range ps {
		pl, err := s.actions.Plan(ctx, p)
		if err != nil {
			continue
		}
		out = append(out, ProposalView{Proposal: p, Summary: pl.Summary})
	}
	return out
}
