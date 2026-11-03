package apps

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/envvars"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/transfer"
)

// Outcomes of an import or restore row.
const (
	OutcomeCreate          = "Create"
	OutcomeExists          = "Exists"
	OutcomeInvalid         = "Invalid"
	OutcomeTemplateMissing = "TemplateMissing"
	OutcomeOwnerUnknown    = "OwnerUnknown"
	OutcomeCreated         = "Created"
	OutcomeSkipped         = "Skipped"
)

// TransferRow is what an import (or a restore) does, or would do, with one
// app.
type TransferRow struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Template    string `json:"template"`
	// Owner is the identity name of the app's owner; absent on an import,
	// whose apps are always the caller's.
	Owner   string `json:"owner,omitempty"`
	Outcome string `json:"outcome" enum:"Create,Exists,Invalid,TemplateMissing,OwnerUnknown,Created,Skipped"`
	Message string `json:"message"`
	// WasRunning is whether the app was running when it was saved.
	WasRunning bool `json:"wasRunning"`
}

type exportOutput struct {
	ContentType        string `header:"Content-Type"`
	ContentDisposition string `header:"Content-Disposition"`
	Body               []byte
}

type importInput struct {
	Body struct {
		YAML     string `json:"yaml" doc:"The exported file"`
		OnExists string `json:"onExists,omitempty" enum:"skip,rename" default:"skip" doc:"What to do with an app whose name you already use"`
	}
}

// TransferOutput is the rows of an import or a restore, one per app.
type TransferOutput struct {
	Body struct {
		Rows []TransferRow `json:"rows"`
	}
}

// exportFile is the name a browser saves the export under.
const exportFile = model.ProductName + "-apps.yaml"

func (s *Workspaces) registerTransfer(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"workspaces"}
		op.Errors = append(op.Errors, http.StatusUnauthorized)
		return op
	}
	huma.Register(api, tagged(huma.Operation{
		OperationID: "exportWorkspaces", Method: http.MethodGet, Path: "/api/workspaces/export",
		Summary: "The user's apps as a YAML file to keep or import elsewhere",
		Responses: map[string]*huma.Response{"200": {
			Description: "One YAML document per app",
			Content:     map[string]*huma.MediaType{"application/yaml": {Schema: &huma.Schema{Type: "string"}}},
		}},
	}), func(ctx context.Context, _ *struct{}) (*exportOutput, error) {
		u := authn.User(ctx)
		apps, err := s.db.Apps().List(ctx, u.ID, false)
		if err != nil {
			return nil, problem.Internal("list workspaces", err)
		}
		bindings := map[string][]model.SecretBinding{}
		for _, a := range apps {
			if bindings[a.ID], err = s.db.Secrets().Bindings(ctx, a.ID); err != nil {
				return nil, problem.Internal("list app secrets", err)
			}
		}
		body, err := transfer.Export(apps, bindings)
		if err != nil {
			return nil, problem.Internal("export workspaces", err)
		}
		return &exportOutput{ContentType: "application/yaml", ContentDisposition: `attachment; filename="` + exportFile + `"`, Body: body}, nil
	})
	for _, apply := range []bool{false, true} {
		op := huma.Operation{
			OperationID: "previewImportWorkspaces", Method: http.MethodPost, Path: "/api/workspaces/import/preview",
			Summary: "What importing a YAML file of apps would do; nothing is changed",
			Errors:  []int{http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
			// The YAML is at most transfer.MaxBytes; JSON escaping can double it.
			MaxBodyBytes: 2*transfer.MaxBytes + 1024,
		}
		if apply {
			op.OperationID, op.Path, op.Summary = "importWorkspaces", "/api/workspaces/import",
				"Import a YAML file of apps; they arrive stopped and private"
		}
		huma.Register(api, tagged(op), func(ctx context.Context, in *importInput) (*TransferOutput, error) {
			return s.importApps(ctx, in.Body.YAML, in.Body.OnExists == "rename", apply)
		})
	}
}

// importApps plans each document of the file as a create by the caller, with
// every check a create makes; with apply it stores the ones that pass,
// stopped. A document that fails a check is a row that says why.
func (s *Workspaces) importApps(ctx context.Context, yaml string, rename, apply bool) (*TransferOutput, error) {
	if len(yaml) > transfer.MaxBytes {
		return nil, problem.New(http.StatusRequestEntityTooLarge, reasons.Msg(reasons.MsgImportTooLarge))
	}
	items, err := transfer.Parse([]byte(yaml))
	switch {
	case errors.Is(err, transfer.ErrTooMany):
		return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgImportTooMany, transfer.MaxDocuments))
	case err != nil:
		return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgImportUnreadable))
	}
	u := authn.User(ctx)
	out := &TransferOutput{}
	out.Body.Rows = []TransferRow{}
	planned := map[string]bool{} // names this file already takes
	for _, it := range items {
		row, app, bindings := s.planImport(ctx, u, it, rename, planned)
		if app != nil {
			planned[app.Name] = true
			if apply {
				if _, err := s.insert(ctx, u, app, store.AppExtras{Secrets: bindings}); err != nil {
					row.Outcome, row.Message = OutcomeInvalid, errorText(err)
				} else {
					row.Outcome, row.Message = OutcomeCreated, reasons.Msg(reasons.MsgImportCreated)
				}
			}
		} else if apply && row.Outcome == OutcomeExists {
			row.Outcome, row.Message = OutcomeSkipped, reasons.Msg(reasons.MsgImportSkipped)
		}
		out.Body.Rows = append(out.Body.Rows, row)
	}
	return out, nil
}

// maxRenames bounds the search for a free name.
const maxRenames = 20

// planImport is one document's row, and the app to store when it is to be
// created with the secrets it gets: the caller's own of the names the file
// gives (one they do not have makes the row Invalid).
func (s *Workspaces) planImport(ctx context.Context, u authz.Principal, it transfer.Item, rename bool, planned map[string]bool) (TransferRow, *model.App, []model.SecretBinding) {
	if it.Err != nil {
		return TransferRow{Outcome: OutcomeInvalid, Message: reasons.Msg(reasons.MsgImportNotAnApp, it.Err)}, nil, nil
	}
	d := it.Doc
	row := TransferRow{Name: d.Metadata.Name, DisplayName: d.Spec.DisplayName, Template: d.Spec.Template, WasRunning: !d.Spec.Paused}
	want, err := d.ToApp()
	if err != nil {
		row.Outcome, row.Message = OutcomeInvalid, reasons.Msg(reasons.MsgImportNotAnApp, err)
		return row, nil, nil
	}
	base, first := want.DisplayName, ""
	for n := 1; n <= maxRenames; n++ {
		req := requestOf(want)
		req.displayName = templates.SampleName(base, n)
		app, err := s.prepare(ctx, u, req)
		if err != nil {
			row.Outcome, row.Message = OutcomeInvalid, errorText(err)
			return row, nil, nil
		}
		row.Name, row.DisplayName = app.Name, app.DisplayName
		if n == 1 {
			first = app.Name
		}
		taken, err := s.nameTaken(ctx, u.ID, app.Name)
		if err != nil {
			row.Outcome, row.Message = OutcomeInvalid, reasons.Msg(reasons.MsgServerError)
			return row, nil, nil
		}
		if !taken && !planned[app.Name] {
			var refs []envvars.EnvVar
			for _, e := range d.EnvVars() {
				refs = append(refs, envvars.EnvVar{Name: e.Name, Value: e.Value, Secret: e.Secret})
			}
			plain, bindings, err := envvars.Check(ctx, s.db, u, s.rolesOf(u), authz.Object{App: app}, refs)
			if err != nil {
				row.Outcome, row.Message = OutcomeInvalid, errorText(err)
				return row, nil, nil
			}
			app.Env = plain
			app.Paused = true
			row.Outcome, row.Message = OutcomeCreate, reasons.Msg(reasons.MsgImportCreate)
			return row, app, bindings
		}
		if !rename {
			break
		}
	}
	row.Name, row.DisplayName = first, base
	row.Outcome, row.Message = OutcomeExists, reasons.Msg(reasons.MsgImportExists, base)
	return row, nil, nil
}

// requestOf is the create request for an app read from a file.
func requestOf(a model.App) appRequest {
	req := appRequest{
		displayName: a.DisplayName, template: a.Template, file: a.File, size: a.Size, apiAccess: a.APIAccess,
		sample: a.Sample, launch: launch{command: a.Command, image: a.Image, port: a.Port}, source: a.Source,
	}
	if r := a.Resources; r != nil {
		amt := sizing.AmountOf(r.CPU, r.Memory)
		req.resources = &amt
	}
	idle := a.IdleStopMinutes
	req.idleStop = &idle
	return req
}

// nameTaken is whether owner already has a live app (or one being deleted)
// with this short name.
func (s *Workspaces) nameTaken(ctx context.Context, owner, name string) (bool, error) {
	_, err := s.db.Apps().GetByOwnerName(ctx, owner, name)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// errorText is the catalog text of an API error, with its field messages.
func errorText(err error) string {
	var e *problem.Details
	if !errors.As(err, &e) {
		return reasons.Msg(reasons.MsgServerError)
	}
	parts := []string{}
	for _, d := range e.Errors {
		if d != nil && d.Message != "" && d.Message != e.Title {
			parts = append(parts, d.Message)
		}
	}
	if len(parts) == 0 {
		return e.Title
	}
	return strings.Join(parts, " ")
}
