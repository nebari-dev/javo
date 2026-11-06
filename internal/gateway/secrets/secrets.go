// Package secrets serves people's secrets: write-only values sealed with
// the public half of the keyset (package seal is internal/secrets).
package secrets

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	seal "github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
)

// secretsRoute is where a person's secrets are listed, created, changed and
// deleted. A value is write-only: it goes in, sealed with the
// public half of the data keyset, and no answer, log line or audit row ever
// carries it. The gateway holds no key that opens a value.
const secretsRoute = "/api/secrets"

// secretNamesRoute lists the names and notes alone, for a coding agent's
// token as well.
const secretNamesRoute = "/api/secret-names"

// SecretName is a secret as a coding agent sees it: what to ask for.
type SecretName struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// secretBodyBytes bounds a secrets request: the largest value, escaped.
const secretBodyBytes = 8*model.MaxSecretValue + 4096

// SecretAppView is an app that uses a secret.
type SecretAppView struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

// SecretView is a secret as the API shows it: never its value.
type SecretView struct {
	Name      string          `json:"name"`
	Note      string          `json:"note"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt" doc:"When the value or the note last changed"`
	Apps      []SecretAppView `json:"apps" doc:"Your apps that get this secret"`
	Jobs      []SecretJobView `json:"jobs" doc:"Your jobs whose runs get this secret"`
}

// SecretJobView is a job whose runs get a secret.
type SecretJobView struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

type secretNameInput struct {
	Name string `path:"name" maxLength:"100"`
}

type secretCreateInput struct {
	Body struct {
		Name  string `json:"name" doc:"Letters, digits and underscores, not starting with a digit; also the variable name apps get it as by default"`
		Value string `json:"value" doc:"Write-only: stored encrypted and never shown again"`
		Note  string `json:"note,omitempty" doc:"What the secret is for; at most 200 characters"`
	}
}

type secretUpdateInput struct {
	Name string `path:"name" maxLength:"100"`
	Body struct {
		Value *string `json:"value,omitempty" doc:"A new value, write-only. Apps that use the secret restart."`
		Note  *string `json:"note,omitempty" doc:"A new note"`
	}
}

type secretOutput struct{ Body SecretView }

func secretViewOf(info store.SecretInfo) SecretView {
	v := SecretView{Name: info.Name, Note: info.Note, CreatedAt: info.CreatedAt, UpdatedAt: info.UpdatedAt, Apps: []SecretAppView{}, Jobs: []SecretJobView{}}
	for _, a := range info.Apps {
		v.Apps = append(v.Apps, SecretAppView{Name: a.Name, DisplayName: a.DisplayName})
	}
	for _, j := range info.Jobs {
		v.Jobs = append(v.Jobs, SecretJobView{Name: j.Name, DisplayName: j.DisplayName})
	}
	return v
}

// sealerFor is what seals new values: the public half of the stored keyset,
// read each time so a rotation applies at once. 503 before the controller
// made a keyset.
func sealerFor(ctx context.Context, db *store.Store) (*seal.Sealer, error) {
	ks, found, err := db.SecretKeys().Get(ctx)
	if err != nil {
		return nil, problem.Internal("read the secrets keyset", err)
	}
	if !found {
		return nil, problem.New(http.StatusServiceUnavailable, reasons.Msg(reasons.MsgSecretsUnavailable))
	}
	s, err := seal.NewSealer(ks.Public)
	if err != nil {
		return nil, problem.Internal("read the secrets keyset", err)
	}
	return s, nil
}

// sealValue is the store's seal callback for owner's secret name.
func sealValue(sealer *seal.Sealer, owner, name, value string) func(id string) ([]byte, error) {
	return func(id string) ([]byte, error) {
		return sealer.Seal([]byte(value), seal.AD{ID: id, Owner: owner, Name: name})
	}
}

// Put stores value as owner's secret called name, created or replaced, for
// a platform feature that keeps a value of the person's (such as their own
// agent key); name may be one the person cannot use themselves.
func Put(ctx context.Context, db *store.Store, actor model.Actor, owner, name, note, value string) error {
	if err := checkSecretValue(value); err != nil {
		return err
	}
	sealer, err := sealerFor(ctx, db)
	if err != nil {
		return err
	}
	switch _, err := db.Secrets().Get(ctx, owner, name); {
	case errors.Is(err, store.ErrNotFound):
		_, err = db.Secrets().Create(ctx, actor, owner, name, note, sealValue(sealer, owner, name, value))
		if errors.Is(err, store.ErrTooManySecrets) {
			return problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgSecretsTooMany))
		}
		if err != nil {
			return problem.Internal("create secret", err)
		}
		return nil
	case err != nil:
		return problem.Internal("read secret", err)
	}
	if _, err := db.Secrets().SetValue(ctx, actor, owner, name, sealValue(sealer, owner, name, value)); err != nil {
		return problem.Internal("replace secret value", err)
	}
	return nil
}

// secretNameError checks a new secret's name: a variable name the platform
// does not set.
func secretNameError(name string) error {
	switch model.EnvNameProblem(name) {
	case model.EnvNameInvalid:
		return problem.FieldError("name", reasons.MsgSecretNameInvalid)
	case model.EnvNameReserved:
		return problem.FieldError("name", reasons.MsgEnvNameReserved, name)
	}
	return nil
}

func checkSecretValue(v string) error {
	if !model.ValidSecretValue(v) {
		return problem.FieldError("value", reasons.MsgSecretValueInvalid)
	}
	return nil
}

func checkSecretNote(n string) error {
	if utf8.RuneCountInString(n) > model.MaxSecretNote || strings.ContainsFunc(n, unicode.IsControl) {
		return problem.FieldError("note", reasons.MsgSecretNoteTooLong)
	}
	return nil
}

// API serves a person's secrets under /api/secrets.
type API struct {
	DB    *store.Store
	Roles config.Roles
}

func (a *API) rolesOf(u authz.Principal) []authz.Role { return authz.RolesOf(u, a.Roles) }

// mine returns the caller's secret called name if authz lets them manage it;
// anything else is the one 404.
func (a *API) mine(ctx context.Context, name string) (model.Secret, error) {
	u := authn.User(ctx)
	sec, err := a.DB.Secrets().Get(ctx, u.ID, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return model.Secret{}, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgSecretNotFound))
	case err != nil:
		return model.Secret{}, problem.Internal("read secret", err)
	}
	if d := authz.Decide(u, a.rolesOf(u), authz.ActManageSecret, authz.Object{Secret: &sec}); !d.Allowed {
		return model.Secret{}, problem.New(http.StatusNotFound, reasons.Msg(d.Code))
	}
	return sec, nil
}

// view is the caller's secret with the apps that use it.
func (a *API) view(ctx context.Context, sec model.Secret) (*secretOutput, error) {
	list, err := a.DB.Secrets().List(ctx, sec.Owner)
	if err != nil {
		return nil, problem.Internal("list secrets", err)
	}
	for _, info := range list {
		if info.ID == sec.ID {
			return &secretOutput{Body: secretViewOf(info)}, nil
		}
	}
	return &secretOutput{Body: secretViewOf(store.SecretInfo{Secret: sec})}, nil
}

// Register adds the secret routes.
func (a *API) Register(api huma.API) {
	tagged := func(op huma.Operation) huma.Operation {
		op.Tags = []string{"secrets"}
		op.Errors = append(op.Errors, http.StatusUnauthorized, http.StatusForbidden)
		op.MaxBodyBytes = secretBodyBytes
		return op
	}

	huma.Register(api, tagged(huma.Operation{
		OperationID: "listSecrets", Method: http.MethodGet, Path: secretsRoute,
		Summary: "Your secrets: names, notes, dates and the apps and jobs that use them, never the values",
	}), func(ctx context.Context, _ *struct{}) (*struct{ Body []SecretView }, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		list, err := a.DB.Secrets().List(ctx, u.ID)
		if err != nil {
			return nil, problem.Internal("list secrets", err)
		}
		out := &struct{ Body []SecretView }{Body: make([]SecretView, 0, len(list))}
		for _, info := range list {
			if authz.Decide(u, a.rolesOf(u), authz.ActManageSecret, authz.Object{Secret: &info.Secret}).Allowed {
				out.Body = append(out.Body, secretViewOf(info))
			}
		}
		return out, nil
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "listSecretNames", Method: http.MethodGet, Path: secretNamesRoute,
		Summary: "Your secrets' names and notes only, so a coding agent can propose giving one to an app (attach-secret); never the values",
	}), func(ctx context.Context, _ *struct{}) (*struct{ Body []SecretName }, error) {
		if err := authn.NoAppTokensExceptAgents(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		list, err := a.DB.Secrets().List(ctx, u.ID)
		if err != nil {
			return nil, problem.Internal("list secrets", err)
		}
		out := &struct{ Body []SecretName }{Body: make([]SecretName, 0, len(list))}
		for _, info := range list {
			// The agent key is never a variable anyone binds (envvars.UsableSecret).
			if info.Name != model.AgentKeySecret && authz.Decide(u, a.rolesOf(u), authz.ActManageSecret, authz.Object{Secret: &info.Secret}).Allowed {
				out.Body = append(out.Body, SecretName{Name: info.Name, Note: info.Note})
			}
		}
		return out, nil
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "createSecret", Method: http.MethodPost, Path: secretsRoute,
		Summary: "Store a secret", DefaultStatus: http.StatusCreated,
		Description: "The value is encrypted before it is stored and is never shown again, by any page or API.",
		Errors:      []int{http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}), func(ctx context.Context, in *secretCreateInput) (*secretOutput, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		b := in.Body
		if err := secretNameError(b.Name); err != nil {
			return nil, err
		}
		if err := checkSecretValue(b.Value); err != nil {
			return nil, err
		}
		if err := checkSecretNote(b.Note); err != nil {
			return nil, err
		}
		if d := authz.Decide(u, a.rolesOf(u), authz.ActManageSecret, authz.Object{Secret: &model.Secret{Owner: u.ID, Name: b.Name}}); !d.Allowed {
			return nil, problem.New(http.StatusForbidden, reasons.Msg(d.Code))
		}
		sealer, err := sealerFor(ctx, a.DB)
		if err != nil {
			return nil, err
		}
		sec, err := a.DB.Secrets().Create(ctx, authn.Actor(ctx), u.ID, b.Name, b.Note, sealValue(sealer, u.ID, b.Name, b.Value))
		switch {
		case errors.Is(err, store.ErrExists):
			return nil, problem.FieldError("name", reasons.MsgSecretNameTaken)
		case errors.Is(err, store.ErrTooManySecrets):
			return nil, problem.New(http.StatusUnprocessableEntity, reasons.Msg(reasons.MsgSecretsTooMany))
		case err != nil:
			return nil, problem.Internal("create secret", err)
		}
		return a.view(ctx, sec)
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "updateSecret", Method: http.MethodPut, Path: secretsRoute + "/{name}",
		Summary:     "Replace a secret's value or change its note",
		Description: "A new value is encrypted and never shown again; the apps that use the secret restart to get it, and the next run of each job that uses it gets it.",
		Errors:      []int{http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}), func(ctx context.Context, in *secretUpdateInput) (*secretOutput, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		b := in.Body
		if b.Value == nil && b.Note == nil {
			return nil, problem.FieldError("value", reasons.MsgSecretValueInvalid)
		}
		if b.Value != nil {
			if err := checkSecretValue(*b.Value); err != nil {
				return nil, err
			}
		}
		if b.Note != nil {
			if err := checkSecretNote(*b.Note); err != nil {
				return nil, err
			}
		}
		sec, err := a.mine(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		if b.Value != nil {
			sealer, err := sealerFor(ctx, a.DB)
			if err != nil {
				return nil, err
			}
			if sec, err = a.DB.Secrets().SetValue(ctx, authn.Actor(ctx), sec.Owner, sec.Name, sealValue(sealer, sec.Owner, sec.Name, *b.Value)); err != nil {
				return nil, a.writeError("replace secret value", err)
			}
		}
		if b.Note != nil {
			if sec, err = a.DB.Secrets().SetNote(ctx, authn.Actor(ctx), sec.Owner, sec.Name, *b.Note); err != nil {
				return nil, a.writeError("change secret note", err)
			}
		}
		return a.view(ctx, sec)
	})

	huma.Register(api, tagged(huma.Operation{
		OperationID: "deleteSecret", Method: http.MethodDelete, Path: secretsRoute + "/{name}",
		Summary: "Delete a secret no app or job uses", DefaultStatus: http.StatusNoContent,
		Description: "A secret an app or a job uses is refused (409): take it off them first.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}), func(ctx context.Context, in *secretNameInput) (*struct{}, error) {
		if err := authn.NoAppTokens(ctx); err != nil {
			return nil, err
		}
		sec, err := a.mine(ctx, in.Name)
		if err != nil {
			return nil, err
		}
		var inUse *store.SecretInUseError
		err = a.DB.Secrets().Delete(ctx, authn.Actor(ctx), sec.Owner, sec.Name)
		switch {
		case errors.As(err, &inUse):
			var names []string
			for _, app := range inUse.Apps {
				names = append(names, app.DisplayName)
			}
			for _, job := range inUse.Jobs {
				names = append(names, job.DisplayName)
			}
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgSecretInUse, strings.Join(names, ", ")))
		case err != nil:
			return nil, a.writeError("delete secret", err)
		}
		return nil, nil
	})
}

// writeError maps a store error of a write on one of the caller's secrets.
func (a *API) writeError(what string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgSecretNotFound))
	}
	return problem.Internal(what, err)
}
