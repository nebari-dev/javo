// Package templates serves the template list and its admin changes, and
// holds the template checks apps and jobs share: who may use a template,
// which file it opens and how a sample start is named.
package templates

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"fmt"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/homepath"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"path"
	"strings"
)

// Template is the API view of a template the user may use.
type Template struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// Category is the ID of the category the template is grouped under, one
	// of the list's categories.
	Category       string   `json:"category"`
	Kind           string   `json:"kind"`
	FileExtensions []string `json:"fileExtensions"`
	// MaxSharing is the most open level apps from this template may be shared at.
	MaxSharing string `json:"maxSharing"`
	// BasePath says how the app meets the URL prefix.
	BasePath string `json:"basePath" enum:"Prefix,Strip"`
	// AppLogin says whether the app has a login of its own.
	AppLogin string `json:"appLogin" enum:"Enabled,Disabled"`
	// Jobs says what jobs may run on the template; absent when none may.
	Jobs *TemplateJobs `json:"jobs,omitempty"`
	// JobOnly marks a template for jobs only: not offered as an app.
	JobOnly bool `json:"jobOnly,omitempty"`
	// Sample is the template's ready-made example; absent when it has none.
	Sample *TemplateSample `json:"sample,omitempty"`
	// FromRepo says whether apps may, must or cannot run from a Git repository.
	FromRepo string `json:"fromRepo" enum:"None,Optional,Required"`
	// Build is the template's default build step; absent when it has none.
	Build *TemplateBuild `json:"build,omitempty"`
	// UserCommand says whether apps take a typed command.
	UserCommand string `json:"userCommand" enum:"None,Optional,Required"`
	// UserImage means each app names its own image and port.
	UserImage bool `json:"userImage"`
	// Port is the port apps serve on; with userImage, the default port.
	Port int32 `json:"port"`
	// APIAccessDefault is the API access a new app gets when its creator
	// picks none; MaxAPIAccess is the most it may have.
	APIAccessDefault string `json:"apiAccessDefault" enum:"None,Read,ReadWrite"`
	MaxAPIAccess     string `json:"maxApiAccess" enum:"None,Read,ReadWrite"`
	// DefaultResources is what an app or a job made without a size runs
	// at: this custom amount where the person may ask for one, else the
	// smallest size that holds it. Absent means the default size.
	DefaultResources *sizing.Amount `json:"defaultResources,omitempty"`
	// Run is what an app of the caller's would run, with what the person
	// picks shown as placeholders such as <your file>; absent when the
	// platform cannot run the template.
	Run *PodRun `json:"run,omitempty"`
}

// TemplateJobs is what jobs may run on a template.
type TemplateJobs struct {
	Engines    []string `json:"engines"`
	Kernels    []string `json:"kernels"`
	MaxTimeout string   `json:"maxTimeout,omitempty" doc:"Longest time limit, a Go duration such as 6h0m0s"`
	// Sample is the template's ready-made command job; absent when it has none.
	Sample *TemplateJobSample `json:"sample,omitempty"`
}

// TemplateBuild is what a repository app builds by default: the command and
// the folder it writes (an app's source may replace both).
type TemplateBuild struct {
	Command string `json:"command"`
	Output  string `json:"output" doc:"Folder the build writes, relative to the repository folder"`
	// IfFile, when set, is the file the build waits for: a folder without it is
	// served as it is.
	IfFile string `json:"ifFile,omitempty"`
}

// TemplateJobSample is a template's sample job: its card text and what New
// job is prefilled with.
type TemplateJobSample struct {
	Title      string     `json:"title"`
	Command    string     `json:"command" doc:"The command line the sample job runs"`
	Parameters []JobParam `json:"parameters,omitempty"`
}

// TemplateSample is the card text of a template's sample.
type TemplateSample struct {
	Title string `json:"title"`
	File  string `json:"file" doc:"Path relative to the home folder that the sample app opens; empty for an image sample"`
	// Command is the command line the sample runs with; empty when the
	// template takes none.
	Command string `json:"command"`
	// Image and Port are what an image sample (userImage templates) runs.
	Image string `json:"image,omitempty" doc:"Image the sample app runs, for templates with userImage"`
	Port  int32  `json:"port,omitempty" doc:"Port the sample image serves on, for templates with userImage"`
}

func templateView(t *model.Template) Template {
	max := model.MaxSharing(t)
	view := Template{
		Name: t.Name, DisplayName: t.Spec.DisplayName, Description: t.Spec.Description,
		Category: cmp.Or(t.Spec.Category, model.CategoryOther),
		Kind:     t.Spec.Kind, FileExtensions: nonNil(t.Spec.FileExtensions), MaxSharing: max,
		BasePath: t.Spec.BasePath, AppLogin: t.Spec.AppLogin, JobOnly: t.Spec.JobOnly,
		UserCommand: t.Spec.UserCommand, UserImage: t.Spec.UserImage, Port: t.Spec.Port,
		APIAccessDefault: model.DefaultAPIAccess(t), MaxAPIAccess: model.MaxAPIAccess(t),
	}
	if view.UserCommand == "" {
		view.UserCommand = model.UserCommandNone
	}
	if view.FromRepo = t.Spec.FromRepo; view.FromRepo == "" {
		view.FromRepo = model.FromRepoNone
	}
	if r := t.Spec.DefaultResources; r != nil {
		a := sizing.AmountOf(r.CPU, r.Memory)
		view.DefaultResources = &a
	}
	if b := t.Spec.Build; b != nil {
		view.Build = &TemplateBuild{Command: b.Command, Output: cmp.Or(b.Output, model.DefaultBuildOutput), IfFile: b.IfFile}
	}
	if j := t.Spec.Jobs; j != nil {
		view.Jobs = &TemplateJobs{Engines: nonNil(j.Engines), Kernels: nonNil(j.Kernels)}
		if j.MaxTimeout != nil {
			view.Jobs.MaxTimeout = j.MaxTimeout.Duration.String()
		}
		if sm := j.Sample; sm != nil {
			view.Jobs.Sample = &TemplateJobSample{Title: sm.Title, Command: sm.Command, Parameters: SampleParams(sm)}
		}
	}
	if sm := t.Spec.Sample; sm != nil {
		view.Sample = &TemplateSample{Title: sm.Title, Command: sm.Command}
		if sm.Image == "" {
			view.Sample.File = workspace.SampleFileOf(t)
		} else {
			view.Sample.Image, view.Sample.Port = sm.Image, cmp.Or(sm.Port, t.Spec.Port)
		}
	}
	return view
}

// API serves the template list and, for template admins, changes to it.
type API struct {
	DB    *store.Store
	Roles config.Roles
	// Run fills in what each template's apps run.
	Run workspace.Platform
}

// TemplateCategory is a group of templates, as New app and the Templates page
// head it.
type TemplateCategory struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type templateListOutput struct {
	Body struct {
		Items []Template `json:"items"`
		// Categories are every category a template may name, in the order
		// they are shown.
		Categories []TemplateCategory `json:"categories"`
	}
}

type templateNameInput struct {
	Name string `path:"name"`
}

type templatePutInput struct {
	Name    string `path:"name"`
	RawBody []byte `contentType:"application/json" doc:"The template's spec, as in a WorkspaceTemplate"`
}

type templateOutput struct{ Body Template }

// Register adds the template routes.
func (s *API) Register(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"templates"}
		op.Errors = append(op.Errors, http.StatusUnauthorized)
		return op
	}
	huma.Register(api, tagged(huma.Operation{
		OperationID: "listTemplates", Method: http.MethodGet, Path: "/api/templates",
		Summary: "Templates the user may create apps from",
	}), s.list)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "putTemplate", Method: http.MethodPut, Path: "/api/templates/{name}",
		Summary: "Create or replace a template (template admins)",
		Errors:  []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
		// The body is decoded strictly in put and checked by model validation,
		// which knows the Kubernetes types (quantities) huma does not.
		SkipValidateBody: true,
	}), s.put)
	huma.Register(api, tagged(huma.Operation{
		OperationID: "deleteTemplate", Method: http.MethodDelete, Path: "/api/templates/{name}",
		Summary: "Delete a template (template admins)", DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusNotFound, http.StatusConflict},
	}), s.delete)
}

func (s *API) list(ctx context.Context, _ *struct{}) (*templateListOutput, error) {
	l, err := s.DB.Templates().List(ctx)
	if err != nil {
		return nil, problem.Internal("list templates", err)
	}
	p := authn.User(ctx)
	roles := authz.RolesOf(p, s.Roles)
	out := &templateListOutput{}
	out.Body.Items = []Template{}
	for _, c := range model.TemplateCategories {
		out.Body.Categories = append(out.Body.Categories, TemplateCategory{ID: c.ID, Title: c.Title})
	}
	for i := range l {
		t := &l[i]
		if authz.Decide(p, roles, authz.ActUseTemplate, authz.Object{Template: t}).Allowed {
			v := templateView(t)
			v.Run = previewRun(t, p.ID, s.Run)
			out.Body.Items = append(out.Body.Items, v)
		}
	}
	return out, nil
}

// mayManage answers a caller who may not change templates with the 404 of an
// address that has nothing behind it, so the route reveals nothing.
func (s *API) mayManage(ctx context.Context) error {
	p := authn.User(ctx)
	if authz.Decide(p, authz.RolesOf(p, s.Roles), authz.ActManageTemplates, authz.Object{}).Allowed {
		return nil
	}
	return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
}

func (s *API) put(ctx context.Context, in *templatePutInput) (*templateOutput, error) {
	if err := s.mayManage(ctx); err != nil {
		return nil, err
	}
	var spec model.TemplateSpec
	d := json.NewDecoder(bytes.NewReader(in.RawBody))
	d.DisallowUnknownFields()
	if err := d.Decode(&spec); err != nil {
		slog.Info("template body rejected", "err", err)
		return nil, problem.Fields(problem.Field{Msg: reasons.Msg(reasons.MsgInvalidRequest)})
	}
	spec.ApplyDefaults()
	tpl := model.Template{Name: in.Name, Source: store.SourceAdmin, Spec: spec}
	// Replace the template as it is now; a template that comes with the
	// installation is changed in the Helm values, not here.
	var version int64
	switch old, err := s.DB.Templates().Get(ctx, in.Name); {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, problem.Internal("read template", err)
	case old.Source == store.SourceBuiltin:
		return nil, errTemplateManagedByHelm()
	default:
		version = old.Version
	}
	saved, err := s.DB.Templates().Put(ctx, authn.Actor(ctx), tpl, version)
	if err != nil {
		return nil, templateWriteError(err)
	}
	v := templateView(&saved)
	v.Run = previewRun(&saved, authn.User(ctx).ID, s.Run)
	return &templateOutput{Body: v}, nil
}

func errTemplateManagedByHelm() error {
	return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgTemplateManagedByHelm))
}

func templateWriteError(err error) error {
	if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrExists) {
		return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgTemplateChanged))
	}
	if causes, ok := problem.Causes(err); ok && slices.ContainsFunc(causes, func(c problem.Cause) bool { return c.Field == "metadata.name" }) {
		e := problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgInvalidRequest))
		e.Errors = []*huma.ErrorDetail{{Location: "path.name", Message: reasons.Msg(reasons.MsgTemplateNameInvalid)}}
		return e
	}
	if fe := problem.Invalid(err); fe != nil {
		return fe
	}
	return problem.Internal("write template", err)
}

func (s *API) delete(ctx context.Context, in *templateNameInput) (*struct{}, error) {
	if err := s.mayManage(ctx); err != nil {
		return nil, err
	}
	switch old, err := s.DB.Templates().Get(ctx, in.Name); {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, problem.Internal("read template", err)
	case old.Source == store.SourceBuiltin:
		return nil, errTemplateManagedByHelm()
	}
	if err := s.DB.Templates().Delete(ctx, authn.Actor(ctx), in.Name); err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, problem.Internal("delete template", err)
	}
	return nil, nil
}

// SampleInput names the template whose sample an app or a job starts.
type SampleInput struct {
	Template string `path:"template" maxLength:"253"`
}

// MaxSampleNames bounds how many "<name> N" names a sample start tries.
const MaxSampleNames = 20

// SampleName is the nth name a sample start tries: base, then "base 2" and so on.
func SampleName(base string, n int) string {
	if n == 1 {
		return base
	}
	return fmt.Sprintf("%s %d", base, n)
}

// Usable returns the template if it exists and authz lets u use it.
// Apps and jobs both ask it.
func Usable(ctx context.Context, API *store.Templates, roles config.Roles, u authz.Principal, name string) (*model.Template, error) {
	t, err := API.Get(ctx, name)
	if errors.Is(err, store.ErrNotFound) ||
		err == nil && !authz.Decide(u, authz.RolesOf(u, roles), authz.ActUseTemplate, authz.Object{Template: &t}).Allowed {
		return nil, problem.FieldError("template", reasons.MsgTemplateUnavailable)
	}
	if err != nil {
		return nil, problem.Internal("read template", err)
	}
	return &t, nil
}

// CleanFile normalises a home-relative file path. It rejects paths
// homepath.Check refuses, absolute paths and paths that leave home; "" and
// "." mean the home folder itself.
func CleanFile(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if homepath.Check(raw) != nil {
		return "", false
	}
	if strings.HasPrefix(raw, "/") {
		return "", false
	}
	c := path.Clean(raw)
	if c == "." {
		return "", true
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// OpensFile reports whether a template with exts opens file. An empty exts
// means the template opens a folder, which the gateway cannot tell apart
// from a file without listing it, so any path is accepted.
func OpensFile(file string, exts []string) bool {
	if len(exts) == 0 {
		return true
	}
	ext := strings.ToLower(path.Ext(file))
	if ext == "" {
		return false
	}
	return slices.ContainsFunc(exts, func(e string) bool {
		return strings.ToLower("."+strings.TrimPrefix(e, ".")) == ext
	})
}

// SampleParams is the parameters a template's sample job declares, as the job
// form takes them.
func SampleParams(sm *model.TemplateJobSample) []JobParam {
	var ps []JobParam
	for _, p := range sm.Parameters {
		ps = append(ps, JobParam{Name: p.Name, Type: p.Type, Default: p.Default})
	}
	return ps
}

// JobParam is one named input of a job.
type JobParam struct {
	Name    string `json:"name" minLength:"1" maxLength:"64"`
	Type    string `json:"type" enum:"string,integer,number,boolean"`
	Default string `json:"default,omitempty" maxLength:"1024"`
}

// nonNil keeps an empty list a JSON [] rather than null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
