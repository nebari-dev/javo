// Package jobs serves the jobs API: a person's scheduled and on-demand
// runs of a file, their runs, logs and variables.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/cmdline"
	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/environments"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/gateway/storage"
	"github.com/nebari-dev/javo/internal/gateway/streams"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/schedule"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// previewRuns is how many upcoming times a schedule preview shows.
const previewRuns = 5

// API serves the jobs routes. A job is a row of the jobs table; it lives
// in the namespace chosen when it was made (the owner's own, in the same
// places as the owner's apps), and every route finds it by owner and short
// name and asks authz (ManageJob) like the app routes do. Starting and
// cancelling a run are requests the controller acts on. Pods and their logs
// are read live through Client.
type API struct {
	DB         *store.Store
	Client     client.Reader
	Namespaces *namespaces.Finder
	// Rules holds the roles every route checks, and the sizes runs get.
	sizing.Rules
	Logs podlogs.Source
	// Homes reads a finished run's saved log once its pod is gone; nil means
	// no saved log can be read.
	Homes   storage.Homes
	Engines *engine.Registry
	// Environments copies in the environment a new job picks.
	Environments *environments.API
	// Streams caps each person's followed logs, shared with the app API.
	Streams *streams.Limit
}

// Engine is a way of running a file, from the registry.
type Engine struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Extensions  []string `json:"extensions"`
}

// jobBody is what the create and update forms send.
type jobBody struct {
	DisplayName string               `json:"displayName" minLength:"1" maxLength:"100"`
	Template    string               `json:"template" minLength:"1" maxLength:"253"`
	Engine      string               `json:"engine" minLength:"1" maxLength:"63"`
	Source      string               `json:"source,omitempty" maxLength:"4096" doc:"Path relative to the home folder; none for engine command"`
	Kernel      string               `json:"kernel,omitempty" maxLength:"128"`
	Command     string               `json:"command,omitempty" maxLength:"4096" doc:"Command line, for engine command only: split into words like a shell but run without one, with {{.Home}} filled in; it runs in the home folder and gets each parameter as environment variable JAVO_PARAM_<NAME>"`
	Parameters  []templates.JobParam `json:"parameters,omitempty" maxItems:"32"`
	Run         JobRunBody           `json:"run,omitempty"`
	Schedule    *JobScheduleBody     `json:"schedule,omitempty" doc:"Absent for a job that only runs on demand"`
	Notify      JobNotifyBody        `json:"notify,omitempty"`
	// Team is read only when the job is made; a job stays where it is.
	Team string `json:"team,omitempty" maxLength:"63" doc:"Team the job belongs to, on create; needed only when namespaces are per team and the user is in several"`
	// Env is read only when the job is made, like Team.
	Env []envvars.EnvVar `json:"env,omitempty" doc:"Environment variables each run gets, plain or from your secrets, on create; change them later with PUT /api/jobs/{name}/env"`
	// Size and Resources are read only when the job is made, like Env.
	Size      string         `json:"size,omitempty" maxLength:"63" doc:"The size each run gets, one of the names from GET /api/sizes, on create; empty means the default size. Change it later with PUT /api/jobs/{name}/size"`
	Resources *sizing.Amount `json:"resources,omitempty" doc:"A custom amount each run gets instead of a size, for people the admin allows (GET /api/sizes custom), on create"`
	// Environment is read only when the job is made, like Env.
	Environment *environments.EnvironmentPick `json:"environment,omitempty" doc:"Run in this pixi environment from GET /api/environments, on create; its newest version is copied in now"`
}

type jobInput struct {
	Body jobBody
}

type updateJobInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the job's owner; default the caller"`
	Body  jobBody
}

type jobOutput struct{ Body JobDetail }

type jobListOutput struct {
	Body struct {
		Items []JobView `json:"items"`
	}
}

type engineListOutput struct {
	Body struct {
		Items []Engine `json:"items"`
	}
}

type runInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the job's owner; default the caller"`
	Run   string `path:"run" maxLength:"253"`
}

type runNowInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the job's owner; default the caller"`
	// A pointer makes the body optional.
	Body *struct {
		Parameters runParams `json:"parameters,omitempty" maxProperties:"32" doc:"Values for this run, by parameter name"`
	}
}

// maxParamValueBytes bounds a one-off value like the CRD bounds a saved
// default (JobParameter.Default), so run records stay small.
const maxParamValueBytes = 1024

// runParams are one-off parameter values, each at most maxParamValueBytes.
type runParams map[string]string

func (runParams) TransformSchema(_ huma.Registry, s *huma.Schema) *huma.Schema {
	if v, ok := s.AdditionalProperties.(*huma.Schema); ok {
		v.MaxLength = ptr.To(maxParamValueBytes)
		v.PrecomputeMessages()
	}
	return s
}

type runOutput struct{ Body RunView }

type previewInput struct {
	Body struct {
		Cron     string `json:"cron" maxLength:"256"`
		TimeZone string `json:"timeZone" maxLength:"64"`
	}
}

// PreviewWarning is a daylight saving effect on a schedule: the text, and
// its parts for a client that words it itself.
type PreviewWarning struct {
	Code  string `json:"code" enum:"DSTSkipped,DSTRepeated"`
	Text  string `json:"text"`
	Date  string `json:"date" format:"date" doc:"Day of the clock change in the schedule's zone"`
	Time  string `json:"time" doc:"First affected run, HH:MM wall clock"`
	Until string `json:"until" doc:"Last affected run, HH:MM wall clock"`
	Runs  int    `json:"runs" doc:"How many runs are affected"`
}

type previewOutput struct {
	Body struct {
		Next     []time.Time      `json:"next" doc:"Upcoming run times, RFC 3339 UTC"`
		Warnings []PreviewWarning `json:"warnings"`
	}
}

// Register adds the jobs routes.
func (s *API) Register(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"jobs"}
		op.Errors = append(op.Errors, http.StatusUnauthorized)
		return op
	}
	huma.Register(api, tagged(huma.Operation{
		OperationID: "listEngines", Method: http.MethodGet, Path: "/api/engines",
		Summary: "The ways a file can be run as a job",
	}), s.listEngines)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "listJobs", Method: http.MethodGet, Path: "/api/jobs",
		Summary: "The user's jobs",
	}), s.list)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "getJob", Method: http.MethodGet, Path: "/api/jobs/{name}",
		Summary: "One of the user's jobs with its runs", Errors: []int{http.StatusNotFound},
	}), s.get)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "createJob", Method: http.MethodPost, Path: "/api/jobs",
		Summary: "Create a job", DefaultStatus: http.StatusCreated, Errors: []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}), s.create)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "startJobSample", Method: http.MethodPost, Path: "/api/job-samples/{template}",
		Summary: "Make a template's sample job (run on demand)", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusForbidden, http.StatusUnprocessableEntity, http.StatusConflict, http.StatusServiceUnavailable},
	}), s.startSample)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "updateJob", Method: http.MethodPut, Path: "/api/jobs/{name}",
		Summary: "Change a job", Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), s.update)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "setJobEnv", Method: http.MethodPut, Path: "/api/jobs/{name}/env",
		Summary: "Set a job's environment variables: plain values and values from your secrets (from the next run on)",
		Description: "Only the job's owner, with their own secrets. A run already going keeps what it started with. " +
			"Anything a run prints, a value included, is in its log.",
		Errors: []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), s.setEnv)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "setJobSize", Method: http.MethodPut, Path: "/api/jobs/{name}/size",
		Summary:     "Change the size or custom amount a job's runs get (from the next run on)",
		Description: "A run already going keeps the size it started with.",
		Errors:      []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), s.setSize)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "deleteJob", Method: http.MethodDelete, Path: "/api/jobs/{name}",
		Summary: "Delete a job (its run folders are kept)", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusNotFound, http.StatusConflict},
	}), s.delete)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "previewSchedule", Method: http.MethodPost, Path: "/api/schedule/preview",
		Summary: "Upcoming run times of a schedule, as the cluster will run them", Errors: []int{http.StatusUnprocessableEntity},
	}), s.preview)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "runJob", Method: http.MethodPost, Path: "/api/jobs/{name}/run",
		Summary: "Run a job now", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}), s.runNow)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "pauseJob", Method: http.MethodPost, Path: "/api/jobs/{name}/pause",
		Summary: "Pause a job's schedule", Errors: []int{http.StatusNotFound, http.StatusConflict},
	}), func(ctx context.Context, in *jobNameInput) (*jobOutput, error) {
		return s.setPaused(ctx, in, true)
	})
	huma.Register(api, tagged(huma.Operation{
		OperationID: "resumeJob", Method: http.MethodPost, Path: "/api/jobs/{name}/resume",
		Summary: "Resume a job's schedule", Errors: []int{http.StatusNotFound, http.StatusConflict},
	}), func(ctx context.Context, in *jobNameInput) (*jobOutput, error) {
		return s.setPaused(ctx, in, false)
	})
	huma.Register(api, tagged(huma.Operation{
		OperationID: "cancelJobRun", Method: http.MethodPost, Path: "/api/jobs/{name}/runs/{run}/cancel",
		Summary: "Cancel a run that is still going", Errors: []int{http.StatusNotFound},
	}), s.cancel)
	huma.Register(api, s.Streams.Op(api, tagged(huma.Operation{
		OperationID: "getJobRunLog", Method: http.MethodGet, Path: "/api/jobs/{name}/runs/{run}/log",
		Summary: "A run's log as plain text, followed live while the run is going",
		Errors:  []int{http.StatusNotFound, http.StatusGone},
		Responses: map[string]*huma.Response{"200": {
			Description: "The log, as untrusted plain text",
			Headers: map[string]*huma.Param{logSourceHeader: {
				Description: "Where the log comes from: live while the run's pod is there (followed while the run goes), saved once it is gone (the run.log the runner wrote to the run folder)",
				Schema:      &huma.Schema{Type: "string", Enum: []any{logSourceLive, logSourceSaved}},
			}},
			Content: map[string]*huma.MediaType{"text/plain": {
				Schema: api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[string](), true, "RunLogText"),
			}},
		}},
	})), s.log)
}

func (s *API) listEngines(_ context.Context, _ *struct{}) (*engineListOutput, error) {
	out := &engineListOutput{}
	out.Body.Items = []Engine{}
	for _, e := range s.Engines.All() {
		out.Body.Items = append(out.Body.Items, Engine{ID: e.ID(), DisplayName: e.DisplayName(), Extensions: append([]string{}, e.Extensions()...)})
	}
	return out, nil
}

func errJobNotFound() error {
	return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgJobNotFound))
}

func errRunNotFound() error {
	return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgRunNotFound))
}

func errJobChanged() error {
	return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgJobChanged))
}

// authorized returns the job the request names (the caller's unless it names
// another owner, which only a platform admin may) if authz lets the caller
// manage it. Missing, being deleted and not-allowed answer the same 404.
func (s *API) authorized(ctx context.Context, owner, name string) (*model.Job, error) {
	p := authn.User(ctx)
	if owner == "" {
		owner = p.ID
	}
	job, err := s.DB.Jobs().GetByOwnerName(ctx, owner, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, errJobNotFound()
	case err != nil:
		return nil, problem.Internal("read job", err)
	case !job.DeletedAt.IsZero():
		return nil, errJobNotFound()
	}
	if dec := authz.Decide(p, authz.RolesOf(p, s.Roles), authz.ActManageJob, authz.Object{Job: &job}); !dec.Allowed {
		return nil, errJobNotFound()
	}
	// A coding agent's change only on a job it may manage.
	if err := envvars.AgentTarget(ctx, s.DB, authz.Object{Job: &job}); err != nil {
		return nil, err
	}
	return &job, nil
}

// list returns the caller's own jobs, even for an admin who may manage all
// (as the app list does).
func (s *API) list(ctx context.Context, _ *struct{}) (*jobListOutput, error) {
	u := authn.User(ctx)
	jobs, err := s.DB.Jobs().List(ctx, u.ID) // newest first
	if err != nil {
		return nil, problem.Internal("list jobs", err)
	}
	out := &jobListOutput{}
	out.Body.Items = []JobView{}
	roles := authz.RolesOf(u, s.Roles)
	for i := range jobs {
		job := &jobs[i]
		if dec := authz.Decide(u, roles, authz.ActManageJob, authz.Object{Job: job}); !dec.Allowed || dec.Via != "owner" {
			continue
		}
		runs, err := s.DB.Jobs().Runs(ctx, job.ID)
		if err != nil {
			return nil, problem.Internal("list runs", err)
		}
		out.Body.Items = append(out.Body.Items, jobView(job, runs))
	}
	return out, nil
}

func (s *API) get(ctx context.Context, in *jobNameInput) (*jobOutput, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, job)
}

// detail is the job with its runs.
func (s *API) detail(ctx context.Context, job *model.Job) (*jobOutput, error) {
	runs, err := s.DB.Jobs().Runs(ctx, job.ID)
	if err != nil {
		return nil, problem.Internal("list runs", err)
	}
	bound, err := s.DB.Secrets().JobBindings(ctx, job.ID)
	if err != nil {
		return nil, problem.Internal("list job secrets", err)
	}
	out := &jobOutput{Body: jobDetail(job, runs)}
	out.Body.Env = envvars.View(ctx, job.Owner, job.Spec.Env, bound)
	out.Body.Environment = environments.ViewOf(job.Spec.Environment)
	out.Body.Size, out.Body.Resources = s.Shown(job.Size, job.Resources)
	return out, nil
}

// setSize changes the size the job's runs get, from the next run on, with
// the checks an app's size change has (sizing.Rules.Allowed).
func (s *API) setSize(ctx context.Context, in *sizing.SizeInput) (*jobOutput, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	if in.Body.Size == "" && in.Body.Resources == nil {
		return nil, problem.FieldError("size", reasons.MsgSizeUnavailable)
	}
	sz, err := s.Allowed(ctx, in.Body.Size, in.Body.Resources)
	if err != nil {
		return nil, err
	}
	if !sizing.Same(job.Size, job.Resources, sz) {
		saved, err := s.DB.Jobs().UpdateSpec(ctx, authn.Actor(ctx), job.ID, job.Version, func(j *model.Job) error {
			j.Size, j.Resources = sz.Stored()
			return nil
		})
		if err != nil {
			return nil, s.saveError(err, &jobBody{})
		}
		job = &saved
	}
	return s.detail(ctx, job)
}

// setEnv replaces the job's variables, from the next run on. Only the owner
// changes them, with their own secrets (envvars.MayChange).
func (s *API) setEnv(ctx context.Context, in *jobEnvInput) (*jobOutput, error) {
	// A coding agent may set plain values on a job it manages (authorized);
	// envvars.Check refuses it a secret.
	if err := authn.NoAppTokensExceptAgents(ctx); err != nil {
		return nil, err
	}
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	u := authn.User(ctx)
	roles := authz.RolesOf(u, s.Roles)
	if err := envvars.MayChange(ctx, u, roles, authz.Object{Job: job}, job.Owner); err != nil {
		return nil, err
	}
	plain, bindings, err := envvars.Check(ctx, s.DB, u, roles, authz.Object{Job: job}, in.Body.Env)
	if err != nil {
		return nil, err
	}
	saved, err := s.DB.Jobs().SetEnv(ctx, authn.Actor(ctx), job.ID, job.Version, plain, bindings)
	if err != nil {
		return nil, envvars.SaveError(err, errJobNotFound, errJobChanged)
	}
	return s.detail(ctx, &saved)
}

func (s *API) delete(ctx context.Context, in *jobNameInput) (*struct{}, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	// The controller removes the job's CronJob and Jobs, then the row.
	if err := s.DB.Jobs().MarkDeleted(ctx, authn.Actor(ctx), job.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, problem.Internal("delete job", err)
	}
	return nil, nil
}

func (s *API) create(ctx context.Context, in *jobInput) (*jobOutput, error) {
	return s.createFor(ctx, authn.User(ctx), &in.Body)
}

// jobNameTaken is the problem for a job name already in use, so a caller
// that can pick another name tells it from other failures.
type jobNameTaken struct{ *problem.Details }

// createFor creates u's job from b; every create goes through it. A name
// already in use is reported as jobNameTaken.
func (s *API) createFor(ctx context.Context, u authz.Principal, b *jobBody) (*jobOutput, error) {
	if d := authz.Decide(u, authz.RolesOf(u, s.Roles), authz.ActCreateJob, authz.Object{}); !d.Allowed {
		return nil, problem.New(http.StatusForbidden, reasons.Msg(d.Code))
	}
	name, err := naming.WorkspaceName(b.DisplayName)
	if err != nil {
		return nil, problem.FieldError("displayName", reasons.MsgNameNeedsLetter)
	}
	ns, err := s.Namespaces.For(u, b.Team)
	if err != nil {
		return nil, problem.Team(err)
	}
	switch ok, err := s.Namespaces.Managed(ctx, ns); {
	case err != nil:
		return nil, problem.Internal("check namespace", err)
	case !ok:
		// The controller makes it right after the person's first sign-in.
		return nil, problem.Reason(http.StatusServiceUnavailable, reasons.NamespacePending).WithHeader("Retry-After", "2")
	}
	spec, tpl, err := s.fill(ctx, u, b)
	if err != nil {
		return nil, err
	}
	// An empty size stores the template's default or the default size's
	// name, so a later change of either does not change the job's runs.
	sz, err := s.AllowedFor(ctx, tpl, b.Size, b.Resources)
	if err != nil {
		return nil, err
	}
	job := &model.Job{Namespace: ns, Name: name, Owner: u.ID, Spec: spec}
	job.Size, job.Resources = sz.Stored()
	plain, bindings, err := envvars.Check(ctx, s.DB, u, authz.RolesOf(u, s.Roles), authz.Object{Job: job}, b.Env)
	if err != nil {
		return nil, err
	}
	job.Spec.Env = plain
	if err := envvars.AgentCreate(ctx, nil, job, tpl, len(bindings) > 0); err != nil {
		return nil, err
	}
	env, files, err := s.Environments.Pick(ctx, b.Environment)
	if err != nil {
		return nil, err
	}
	job.Spec.Environment = env
	if _, err := s.DB.Jobs().GetByOwnerName(ctx, u.ID, name); err == nil {
		return nil, jobNameTaken{problem.Fields(problem.Field{Name: "displayName", Msg: reasons.Msg(reasons.MsgNameTaken)})}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, problem.Internal("check job name", err)
	}
	created, err := s.DB.Jobs().CreateWith(ctx, authn.Actor(ctx), job, store.JobExtras{Secrets: bindings, Environment: files})
	var missing *store.SecretMissingError
	switch {
	case errors.Is(err, store.ErrExists):
		return nil, jobNameTaken{problem.Fields(problem.Field{Name: "displayName", Msg: reasons.Msg(reasons.MsgNameTaken)})}
	case errors.As(err, &missing):
		// Deleted since envvars.Check read it.
		return nil, problem.FieldError("env", reasons.MsgSecretUnknown, missing.Name)
	case err != nil:
		return nil, s.saveError(err, b)
	}
	return s.detail(ctx, &created)
}

// startSample makes the template's sample command job, run on demand. Its
// name is the sample's title, then "<title> 2" and so on, unique per user.
func (s *API) startSample(ctx context.Context, in *templates.SampleInput) (*jobOutput, error) {
	u := authn.User(ctx)
	t, err := templates.Usable(ctx, s.DB.Templates(), s.Roles, u, in.Template)
	if err != nil {
		return nil, err
	}
	if t.Spec.Jobs == nil || t.Spec.Jobs.Sample == nil {
		return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgNoSample, t.Spec.DisplayName))
	}
	sm := t.Spec.Jobs.Sample
	// A template that runs only bash scripts runs its sample as one.
	engine := commandEngine
	if !slices.Contains(t.Spec.Jobs.Engines, commandEngine) && slices.Contains(t.Spec.Jobs.Engines, bashEngine) {
		engine = bashEngine
	}
	b := jobBody{Template: t.Name, Engine: engine, Command: sm.Command, Parameters: templates.SampleParams(sm)}
	for n := 1; n <= templates.MaxSampleNames; n++ {
		b.DisplayName = templates.SampleName(sm.Title, n)
		out, err := s.createFor(ctx, u, &b)
		var taken jobNameTaken
		if errors.As(err, &taken) {
			continue
		}
		return out, err
	}
	return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgSampleNameTaken))
}

func (s *API) update(ctx context.Context, in *updateJobInput) (*jobOutput, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	u := authn.User(ctx)
	spec, tpl, err := s.fill(ctx, u, &in.Body)
	if err != nil {
		return nil, err
	}
	// A coding agent may not move a job onto a template that gives its runs
	// an API token.
	if err := envvars.AgentCreate(ctx, nil, job, tpl, false); err != nil {
		return nil, err
	}
	if !runsTheSame(job.Spec, spec) {
		if err := envvars.MayChangeCode(ctx, u, authz.RolesOf(u, s.Roles), authz.Object{Job: job}); err != nil {
			return nil, err
		}
	}
	saved, err := s.DB.Jobs().UpdateSpec(ctx, authn.Actor(ctx), job.ID, job.Version, func(j *model.Job) error {
		// The owner stays: an admin changing someone's job does not take it
		// over. History, env and the environment are not part of the form.
		spec.History, spec.Env, spec.Environment = j.Spec.History, j.Spec.Env, j.Spec.Environment
		j.Spec = spec
		return nil
	})
	if err != nil {
		return nil, s.saveError(err, &in.Body)
	}
	return s.detail(ctx, &saved)
}

// runsTheSame is whether next runs the same code as cur: they differ at
// most in name, schedule, notifications and run policy.
func runsTheSame(cur, next model.JobSpec) bool {
	next.DisplayName, next.Schedule, next.Notify, next.Run = cur.DisplayName, cur.Schedule, cur.Notify, cur.Run
	next.History, next.Env, next.Environment = cur.History, cur.Env, cur.Environment
	return equality.Semantic.DeepEqual(cur, next)
}

// fill turns the form into a job spec, with the template it runs on. The
// form is checked against the template and the engine; every problem is a
// field error.
func (s *API) fill(ctx context.Context, u authz.Principal, b *jobBody) (model.JobSpec, *model.Template, error) {
	tpl, err := templates.Usable(ctx, s.DB.Templates(), s.Roles, u, b.Template)
	if err != nil {
		return model.JobSpec{}, nil, err
	}
	spec, errs := s.formSpec(b, tpl)
	if len(errs) > 0 {
		return model.JobSpec{}, nil, problem.Fields(errs...)
	}
	if err := jobs.ValidateFor(tpl, &spec); err != nil {
		return model.JobSpec{}, nil, buildError(err, &spec)
	}
	return spec, tpl, nil
}

// formSpec converts the form to a job spec, collecting field errors.
func (s *API) formSpec(b *jobBody, tpl *model.Template) (model.JobSpec, []problem.Field) {
	var errs []problem.Field
	bad := func(field string, code reasons.MessageCode, args ...any) {
		errs = append(errs, problem.Field{Name: field, Msg: reasons.Msg(code, args...)})
	}
	spec := model.JobSpec{
		DisplayName: b.DisplayName, Template: tpl.Name, Engine: b.Engine, Kernel: b.Kernel,
		Notify: model.JobNotify{OnFailure: ptr.To(ptr.Deref(b.Notify.OnFailure, true)), OnSuccess: b.Notify.OnSuccess},
		Run:    model.JobRunPolicy{Retries: b.Run.Retries},
	}
	for _, p := range b.Parameters {
		spec.Parameters = append(spec.Parameters, model.JobParameter{Name: p.Name, Type: p.Type, Default: p.Default})
	}

	isCommand := spec.RunsCommand()
	if isCommand {
		read := jobCommand
		if b.Engine == bashEngine {
			read = bashScript
		}
		var f *problem.Field
		spec.Command, f = read(b.Command)
		if f != nil {
			errs = append(errs, *f)
		}
	} else if b.Command != "" {
		bad("command", reasons.MsgJobCommandNotTaken)
	}

	file, ok := templates.CleanFile(b.Source)
	if !ok || file == "" && !isCommand {
		bad("source", reasons.MsgFileInvalid)
	}
	spec.Source.Path = file
	if e, found := s.engine(b.Engine); found && ok && file != "" && !templates.OpensFile(file, e.Extensions()) {
		bad("source", reasons.MsgFileWrongEngine, strings.Join(e.Extensions(), ", "))
	} else if !found {
		bad("engine", reasons.MsgEngineNotInTemplate, tpl.Name, b.Engine)
	}

	var err error
	if spec.Run.Timeout, err = parseDuration(b.Run.Timeout); err != nil {
		bad("run.timeout", reasons.MsgInvalidRequest)
	} else if tj := tpl.Spec.Jobs; tj != nil && tj.MaxTimeout != nil && spec.Run.Timeout != nil && spec.Run.Timeout.Duration > tj.MaxTimeout.Duration {
		bad("run.timeout", reasons.MsgTimeoutTooLong, tj.MaxTimeout.Duration)
	}
	if spec.Run.CellTimeout, err = parseDuration(b.Run.CellTimeout); err != nil {
		bad("run.cellTimeout", reasons.MsgInvalidRequest)
	}

	if sc := b.Schedule; sc != nil {
		js := &model.JobSchedule{Cron: sc.Cron, TimeZone: sc.TimeZone, Concurrency: sc.Concurrency, Paused: sc.Paused}
		if js.Concurrency == "" {
			js.Concurrency = "Forbid"
		}
		if js.StartingDeadline, err = parseDuration(sc.StartingDeadline); err != nil {
			bad("schedule.startingDeadline", reasons.MsgInvalidRequest)
		}
		if sc.TimeZone == "" {
			bad("schedule.timeZone", reasons.MsgTimeZoneRequired)
		}
		if sc.Cron == "" {
			bad("schedule.cron", reasons.MsgCronInvalid, "it is empty")
		}
		spec.Schedule = js
	}
	return spec, errs
}

// commandEngine is the engine whose jobs are a command line, not a file;
// bashEngine's jobs are a script bash reads.
const (
	commandEngine = "command"
	bashEngine    = "bash"
)

// bashScript is a bash job's script, kept whole as the job's one command
// word: bash, not javo, reads it. Only a blank or too long script is refused.
func bashScript(script string) ([]string, *problem.Field) {
	switch {
	case strings.TrimSpace(script) == "":
		return nil, &problem.Field{Name: "command", Msg: reasons.Msg(reasons.MsgJobCommandRequired)}
	case len(script) > model.MaxCommandLength:
		return nil, &problem.Field{Name: "command", Msg: reasons.Msg(reasons.MsgCommandTooLong)}
	}
	return []string{script}, nil
}

// jobCommand splits a command job's line (jobs.CommandArgs, the same rules
// as an app's typed command) or says what is wrong with it.
func jobCommand(line string) ([]string, *problem.Field) {
	line = strings.TrimSpace(line)
	msg := func(code reasons.MessageCode, args ...any) *problem.Field {
		return &problem.Field{Name: "command", Msg: reasons.Msg(code, args...)}
	}
	if line == "" {
		return nil, msg(reasons.MsgJobCommandRequired)
	}
	if len(line) > model.MaxCommandLength {
		return nil, msg(reasons.MsgCommandTooLong)
	}
	argv, err := jobs.CommandArgs(line)
	var syn *cmdline.SyntaxError
	switch {
	case err == nil && len(argv) == 0:
		return nil, msg(reasons.MsgJobCommandRequired)
	case err == nil:
		return argv, nil
	case errors.Is(err, cmdline.ErrVariable):
		return nil, msg(reasons.MsgJobCommandVariable)
	case errors.As(err, &syn):
		return nil, msg(reasons.MsgCommandInvalid, syn.Detail)
	}
	return nil, msg(reasons.MsgJobCommandPlaceholder)
}

func (s *API) engine(id string) (engine.Engine, bool) {
	return s.Engines.Get(id)
}

// parseDuration reads a Go duration; "" is no duration.
func parseDuration(s string) (*metav1.Duration, error) {
	if s == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return nil, errors.New("not a positive duration")
	}
	return &metav1.Duration{Duration: d}, nil
}

// buildError maps a refusal of the job renderer to the field that causes it.
func buildError(err error, spec *model.JobSpec) error {
	switch {
	case errors.Is(err, jobs.ErrJobsNotAllowed):
		return problem.FieldError("template", reasons.MsgJobsNotOnTemplate)
	case errors.Is(err, jobs.ErrEngineNotInTemplate):
		return problem.FieldError("engine", reasons.MsgEngineNotInTemplate, spec.Template, spec.Engine)
	case errors.Is(err, jobs.ErrKernelNotInTemplate):
		return problem.FieldError("kernel", reasons.MsgKernelNotInTemplate, spec.Template, spec.Kernel)
	case errors.Is(err, jobs.ErrParameterNotDeclared):
		return problem.FieldError("parameters", reasons.MsgInvalidRequest)
	}
	return problem.Internal("render job", err)
}

// saveError turns a refused save into the answer. Errors on the schedule go
// to schedule.cron and schedule.timeZone; the validator's own words are cut
// to one short line and shown only inside the catalog text.
func (s *API) saveError(err error, b *jobBody) error {
	switch {
	case errors.Is(err, store.ErrExists):
		return problem.FieldError("displayName", reasons.MsgNameTaken)
	case errors.Is(err, store.ErrConflict):
		return errJobChanged()
	case errors.Is(err, store.ErrNotFound):
		return errJobNotFound()
	}
	causes, ok := problem.Causes(err)
	if !ok {
		return problem.Internal("save job", err)
	}
	var errs []problem.Field
	var rest []*huma.ErrorDetail
	add := func(f problem.Field) {
		if !slices.Contains(errs, f) {
			errs = append(errs, f)
		}
	}
	for _, c := range causes {
		var i int
		switch loc := c.Field; {
		case loc == "spec.schedule.cron":
			add(problem.Field{Name: "schedule.cron", Msg: reasons.Msg(reasons.MsgCronInvalid, oneLine(c.Message))})
		case loc == "spec.schedule.timeZone":
			zone := ""
			if b.Schedule != nil {
				zone = b.Schedule.TimeZone
			}
			add(problem.Field{Name: "schedule.timeZone", Msg: reasons.Msg(reasons.MsgTimeZoneInvalid, zone)})
		case strings.HasPrefix(loc, "spec.parameters["):
			if _, err := fmt.Sscanf(loc, "spec.parameters[%d]", &i); err == nil && i >= 0 && i < len(b.Parameters) {
				add(problem.Field{Name: fmt.Sprintf("parameters[%d]", i), Msg: reasons.Msg(reasons.MsgParamInvalid, b.Parameters[i].Name, b.Parameters[i].Type)})
				continue
			}
			rest = append(rest, &huma.ErrorDetail{Location: loc, Message: c.Message})
		default:
			rest = append(rest, &huma.ErrorDetail{Location: loc, Message: c.Message})
		}
	}
	if len(errs) == 0 || len(rest) > 0 {
		for _, f := range problem.Validation(rest) {
			add(f)
		}
	}
	return problem.Fields(errs...)
}

// oneLine cuts a server message to its first line and 200 characters.
func oneLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 200 {
		return string(r[:200])
	}
	return s
}

func (s *API) runNow(ctx context.Context, in *runNowInput) (*runOutput, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	// A person who left the template's groups no longer runs jobs on it.
	tpl, err := templates.Usable(ctx, s.DB.Templates(), s.Roles, authn.User(ctx), job.Spec.Template)
	if err != nil {
		return nil, err
	}
	// The template may have dropped jobs, the engine or the kernel since.
	if err := jobs.ValidateFor(tpl, &job.Spec); err != nil {
		return nil, buildError(err, &job.Spec)
	}
	var override map[string]string
	if in.Body != nil {
		override = in.Body.Parameters
	}
	if err := jobs.CheckOverrides(job.Spec.Parameters, override); err != nil {
		return nil, buildError(err, &job.Spec)
	}
	// Code may build commands from its parameters, so values other than the
	// saved ones decide what runs next to the job's credentials.
	if changesParameters(job.Spec.Parameters, override) {
		u := authn.User(ctx)
		if err := envvars.MayRewrite(ctx, s.DB, u, authz.RolesOf(u, s.Roles), authz.Object{Job: job}); err != nil {
			return nil, err
		}
	}
	// The controller starts the run; until it does the run shows as running.
	run, err := s.DB.Jobs().RequestRun(ctx, authn.Actor(ctx), job.ID, override)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, errJobNotFound()
	case err != nil:
		return nil, problem.Internal("request run", err)
	}
	return &runOutput{Body: runView(run)}, nil
}

// changesParameters is whether override gives any declared parameter a value
// other than its default.
func changesParameters(declared []model.JobParameter, override map[string]string) bool {
	for _, p := range declared {
		if v, ok := override[p.Name]; ok && v != p.Default {
			return true
		}
	}
	return false
}

// setPaused pauses or resumes the schedule. Resuming a job with no starting
// deadline, whose schedule fired while it was paused, makes Kubernetes start
// one missed run; the job detail warns about it while paused (ResumeNotice).
func (s *API) setPaused(ctx context.Context, in *jobNameInput, paused bool) (*jobOutput, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	if sc := job.Spec.Schedule; sc != nil && sc.Paused != paused {
		saved, err := s.DB.Jobs().UpdateSpec(ctx, authn.Actor(ctx), job.ID, job.Version, func(j *model.Job) error {
			j.Spec.Schedule.Paused = paused
			return nil
		})
		switch {
		case errors.Is(err, store.ErrConflict):
			return nil, errJobChanged()
		case errors.Is(err, store.ErrNotFound):
			return nil, errJobNotFound()
		case err != nil:
			return nil, problem.Internal("pause job", err)
		}
		job = &saved
	}
	return s.detail(ctx, job)
}

// missedWhilePaused reports whether the schedule has a slot after the last
// time it started a run (or after the job was made), which resuming without
// a starting deadline runs once.
func missedWhilePaused(j *model.Job, now time.Time) bool {
	sc := j.Spec.Schedule
	if sc == nil || sc.StartingDeadline != nil {
		return false
	}
	last := j.CreatedAt
	if t := j.Status.LastScheduleTime; !t.IsZero() {
		last = t
	}
	prev, err := schedule.Previous(sc.Cron, sc.TimeZone, now)
	return err == nil && prev.After(last)
}

// runActive is true for a run that was requested or is going.
func runActive(r model.Run) bool {
	return r.Outcome == "" || r.Outcome == model.RunOutcomeRunning
}

// runOf returns the run named in the address if it is one of job's, else a 404.
func (s *API) runOf(ctx context.Context, job *model.Job, id string) (model.Run, error) {
	run, err := s.DB.Jobs().Run(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return model.Run{}, errRunNotFound()
	case err != nil:
		return model.Run{}, problem.Internal("read run", err)
	case run.JobID != job.ID:
		return model.Run{}, errRunNotFound()
	}
	return run, nil
}

func (s *API) cancel(ctx context.Context, in *runInput) (*runOutput, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	run, err := s.runOf(ctx, job, in.Run)
	if err != nil {
		return nil, err
	}
	if !runActive(run) {
		return &runOutput{Body: runView(run)}, nil
	}
	switch err := s.DB.Jobs().RequestCancel(ctx, authn.Actor(ctx), run.ID); {
	case errors.Is(err, store.ErrConflict): // finished meanwhile
		if run, err = s.runOf(ctx, job, in.Run); err != nil {
			return nil, err
		}
		return &runOutput{Body: runView(run)}, nil
	case err != nil:
		return nil, problem.Internal("cancel run", err)
	}
	// The controller records the same outcome once it has stopped the run.
	run.Outcome, run.Reason, run.Message = model.RunOutcomeCancelled, string(reasons.RunCancelled), ""
	return &runOutput{Body: runView(run)}, nil
}

func (s *API) preview(_ context.Context, in *previewInput) (*previewOutput, error) {
	b := in.Body
	if b.TimeZone == "" {
		return nil, problem.FieldError("timeZone", reasons.MsgTimeZoneRequired)
	}
	p, err := schedule.Next(b.Cron, b.TimeZone, time.Now(), previewRuns)
	switch {
	case errors.Is(err, schedule.ErrTimeZoneInvalid):
		return nil, problem.FieldError("timeZone", reasons.MsgTimeZoneInvalid, b.TimeZone)
	case errors.Is(err, schedule.ErrCronInvalid):
		return nil, problem.FieldError("cron", reasons.MsgCronInvalid, oneLine(strings.TrimPrefix(err.Error(), schedule.ErrCronInvalid.Error()+": ")))
	case err != nil:
		return nil, problem.Internal("preview schedule", err)
	}
	out := &previewOutput{}
	out.Body.Next, out.Body.Warnings = p.Next, []PreviewWarning{}
	for _, w := range p.Warnings {
		out.Body.Warnings = append(out.Body.Warnings, PreviewWarning{
			Code: string(w.Code), Text: reasons.Msg(w.Code, dayInWords(w.Date), dstRuns(w)),
			Date: w.Date, Time: w.Time, Until: w.Until, Runs: w.Runs,
		})
	}
	return out, nil
}

// dayInWords is "Sun 28 Mar 2027" for "2027-03-28".
func dayInWords(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return d.Format("Mon 2 Jan 2006")
}

// dstRuns names the runs a clock change affects: one, or the first to the last.
func dstRuns(w schedule.Warning) string {
	if w.Runs == 1 {
		return reasons.Msg(reasons.MsgDSTOneRun, w.Time)
	}
	return reasons.Msg(reasons.MsgDSTRuns, w.Time, w.Until)
}

// log streams a run's container log as plain text, following it while the
// run is going. A finished run whose pod is gone answers with the log saved
// in its run folder (X-Log-Source says which); 410 when neither is left.
func (s *API) log(ctx context.Context, in *runInput) (*huma.StreamResponse, error) {
	job, err := s.authorized(ctx, in.Owner, in.Name)
	if err != nil {
		return nil, err
	}
	run, err := s.runOf(ctx, job, in.Run)
	if err != nil {
		return nil, err
	}
	active := runActive(run)
	var rc io.ReadCloser
	if run.K8sJob != "" {
		pod, err := s.runPod(ctx, job, run.K8sJob)
		if err != nil {
			return nil, err
		}
		if rc, err = podlogs.Open(ctx, s.Logs, pod, &corev1.PodLogOptions{Follow: active}); err != nil {
			return nil, err
		}
	}
	source := logSourceLive
	if rc == nil && !active {
		if rc, err = s.savedLog(ctx, job, run.K8sJob); err != nil {
			return nil, err
		}
		source = logSourceSaved
	}
	if rc == nil {
		// Active but the container has not started (not requested of the
		// cluster yet, pod pending or pulling its image): tell the client to
		// ask again instead of closing an empty stream that looks like a
		// finished log.
		return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgRunNotStarted)).WithHeader("Retry-After", "2")
	}
	return &huma.StreamResponse{Body: func(hc huma.Context) {
		hc.SetHeader("Content-Type", "text/plain; charset=utf-8")
		hc.SetHeader(logSourceHeader, source)
		hc.SetHeader("Cache-Control", "no-store")
		hc.SetHeader("X-Accel-Buffering", "no")
		hc.SetStatus(http.StatusOK)
		w := hc.BodyWriter()
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // send the headers now: a followed log may stay quiet for a while
		}
		defer rc.Close()
		buf := make([]byte, 8<<10)
		for {
			n, err := rc.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}}, nil
}

// The run log response says whether the text is the pod's live log or the saved copy.
const (
	logSourceHeader = "X-Log-Source"
	logSourceLive   = "live"
	logSourceSaved  = "saved"
)

// savedLog opens run.log, the copy of the log the runner writes to the run
// folder in the owner's home (jobs/<job>/<run>/, run being the run's
// Kubernetes Job name, as JAVO_RUN_ID tells the runner), for a run whose pod
// is gone. Neither copy left is 410 in plain words.
func (s *API) savedLog(ctx context.Context, job *model.Job, k8sJob string) (io.ReadCloser, error) {
	gone := problem.New(http.StatusGone, reasons.Msg(reasons.MsgLogsGone))
	if s.Homes == nil || k8sJob == "" {
		return nil, gone
	}
	dir := path.Join("jobs", job.Name)
	rel := path.Join(dir, k8sJob, "run.log")
	if path.Dir(path.Dir(rel)) != dir {
		return nil, gone // a run id is one path element, never a way out of the job's folder
	}
	rc, _, err := s.Homes.Open(files.WithNamespace(ctx, job.Namespace), job.Owner, rel)
	switch {
	case errors.Is(err, files.ErrNotFound):
		return nil, gone
	case errors.Is(err, files.ErrTooLarge):
		return nil, problem.New(http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgOutputTooLarge))
	case err != nil:
		return nil, storage.HomeError(err, job.Owner, "open saved run log", rel, reasons.MsgLogsGone, reasons.MsgServerError)
	}
	return rc, nil
}

// runPod returns the newest pod of a run (a retried run has several), or nil.
func (s *API) runPod(ctx context.Context, job *model.Job, k8sJob string) (*corev1.Pod, error) {
	var l corev1.PodList
	if err := s.Client.List(ctx, &l, client.InNamespace(job.Namespace), client.MatchingLabels{
		jobs.LabelJobName: k8sJob, model.LabelOwner: job.Owner, model.LabelJob: job.ID,
	}); err != nil {
		return nil, problem.Internal("list run pods", err)
	}
	var newest *corev1.Pod
	for i := range l.Items {
		if p := &l.Items[i]; newest == nil || p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	return newest, nil
}

type jobEnvInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the job's owner; default the caller"`
	Body  envvars.EnvBody
}

// jobNameInput names a job: its name and, for a platform admin, its owner.
// The owner doc reads "app" as the app routes' does, so the API document
// stays the same.
type jobNameInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
}
