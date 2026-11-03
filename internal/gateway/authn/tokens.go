package authn

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// maxTokenName is the longest token name.
const maxTokenName = 64

// tokensRoute is where tokens are created, listed and revoked.
const tokensRoute = "/api/tokens"

// TokenView is one personal API token as the API shows it: never the
// secret, only its prefix.
type TokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope" enum:"read,read-write"`
	Prefix     string     `json:"prefix" doc:"The start of the token, enough to recognise it"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty" doc:"Absent when the token never expires"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty" doc:"Absent until the token is first used; recorded at most once a minute"`
	App        string     `json:"app,omitempty" doc:"The app this token was made for; it is revoked when the app is deleted"`
}

// CreatedToken is a new token, with its secret shown this once.
type CreatedToken struct {
	TokenView
	Token string `json:"token" doc:"The token. It is not stored and cannot be shown again."`
}

// TokenBody asks for a new token.
type TokenBody struct {
	Name          string `json:"name" maxLength:"1024"`
	Scope         string `json:"scope" doc:"read (GET and HEAD only) or read-write"`
	ExpiresInDays *int   `json:"expiresInDays,omitempty" doc:"Days until the token stops working; absent means never, when the server allows that"`
}

// Info says which optional parts of the server are on.
type Info struct {
	APIDocs bool `json:"apiDocs" doc:"The API docs page is served at /api/docs/"`
}

// TokensAPI serves /api/tokens: a person's personal API tokens. MaxLifetime
// caps a new token's life; Docs says whether the API docs page is served.
type TokensAPI struct {
	Store *token.DBStore
	// Apps is told when an app's token was revoked, so the controller makes a new one.
	Apps        *store.Apps
	MaxLifetime time.Duration
	Now         func() time.Time
	Docs        bool
}

func viewOf(t token.Token) TokenView {
	v := TokenView{ID: t.ID, Name: t.Name, Scope: string(t.Scope), Prefix: t.Prefix(), CreatedAt: t.CreatedAt,
		ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt}
	if t.App != nil {
		v.App = t.App.Short
	}
	return v
}

// sessionRequired refuses a request made with an API token: tokens cannot
// create, list or revoke tokens, so a leaked token cannot make itself
// permanent.
func sessionRequired(ctx context.Context) error {
	if viaToken(ctx) {
		return problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgTokenCannotManageTokens))
	}
	return nil
}

// validTokenName is 1 to 64 characters with no control characters.
func validTokenName(name string) bool {
	n := len([]rune(name))
	return n >= 1 && n <= maxTokenName && !strings.ContainsFunc(name, unicode.IsControl)
}

// expiry checks the requested lifetime against api.tokens.maxLifetime.
func (t *TokensAPI) expiry(days *int) (*time.Time, error) {
	maxDays := int(t.MaxLifetime / (24 * time.Hour))
	switch {
	case days == nil && t.MaxLifetime > 0:
		return nil, problem.FieldError("expiresInDays", reasons.MsgTokenExpiryTooLong, maxDays)
	case days == nil:
		return nil, nil
	case *days < 1:
		return nil, problem.FieldError("expiresInDays", reasons.MsgTokenExpiryInvalid)
	case t.MaxLifetime > 0 && *days > maxDays:
		return nil, problem.FieldError("expiresInDays", reasons.MsgTokenExpiryTooLong, maxDays)
	case *days > 36500:
		return nil, problem.FieldError("expiresInDays", reasons.MsgTokenExpiryInvalid)
	}
	at := t.Now().UTC().Truncate(time.Second).AddDate(0, 0, *days)
	return &at, nil
}

// Register adds the token routes to api.
func (t *TokensAPI) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "listTokens", Method: http.MethodGet, Path: tokensRoute,
		Summary: "Your personal API tokens", Tags: []string{"tokens"}, Security: SessionOnly,
		Description: "Needs a browser session: API tokens cannot list tokens.",
		Errors:      []int{http.StatusUnauthorized, http.StatusForbidden},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body []TokenView }, error) {
		if err := sessionRequired(ctx); err != nil {
			return nil, err
		}
		list, err := t.Store.List(ctx, User(ctx).ID)
		if err != nil {
			return nil, problem.Internal("list tokens", err)
		}
		out := &struct{ Body []TokenView }{Body: make([]TokenView, 0, len(list))}
		for _, tok := range list {
			out.Body = append(out.Body, viewOf(tok))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createToken", Method: http.MethodPost, Path: tokensRoute,
		Summary: "Create a personal API token", Tags: []string{"tokens"}, Security: SessionOnly,
		Description: "The token in the answer is shown once and never again. Send it as `Authorization: Bearer <token>`. " +
			"It acts as you, with the groups from your last sign-in; a read token may only GET. Needs a browser session.",
		DefaultStatus: http.StatusCreated,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *struct{ Body TokenBody }) (*struct{ Body CreatedToken }, error) {
		if err := sessionRequired(ctx); err != nil {
			return nil, err
		}
		name := strings.TrimSpace(in.Body.Name)
		if !validTokenName(name) {
			return nil, problem.FieldError("name", reasons.MsgTokenNameInvalid)
		}
		scope := token.Scope(in.Body.Scope)
		if !scope.Valid() {
			return nil, problem.FieldError("scope", reasons.MsgTokenScopeInvalid)
		}
		expires, err := t.expiry(in.Body.ExpiresInDays)
		if err != nil {
			return nil, err
		}
		text, tok, err := t.Store.Create(ctx, Actor(ctx), token.Token{Name: name, Owner: User(ctx).ID, Scope: scope, ExpiresAt: expires})
		if err != nil {
			return nil, problem.Internal("create token", err)
		}
		return &struct{ Body CreatedToken }{Body: CreatedToken{TokenView: viewOf(tok), Token: text}}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeToken", Method: http.MethodDelete, Path: tokensRoute + "/{id}",
		Summary: "Revoke a personal API token", Tags: []string{"tokens"}, Security: SessionOnly,
		Description: "The token stops working at once. Revoking the token of a coding-agent app makes the app " +
			"get a new one when it next starts. Needs a browser session.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound},
	}, func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"64"`
	}) (*struct{}, error) {
		if err := sessionRequired(ctx); err != nil {
			return nil, err
		}
		tok, err := t.Store.Revoke(ctx, Actor(ctx), User(ctx).ID, in.ID)
		switch {
		case errors.Is(err, token.ErrNotFound):
			return nil, problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgTokenNotFound))
		case err != nil:
			return nil, problem.Internal("revoke token", err)
		}
		if tok.App != nil {
			if err := t.noteRevoked(ctx, *tok.App, tok.ID); err != nil {
				return nil, problem.Internal("mark app token revoked", err)
			}
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "serverInfo", Method: http.MethodGet, Path: "/api/info",
		Summary: "Which optional parts of the server are on", Tags: []string{"auth"},
		Errors: []int{http.StatusUnauthorized},
	}, func(context.Context, *struct{}) (*struct{ Body Info }, error) {
		return &struct{ Body Info }{Body: Info{APIDocs: t.Docs}}, nil
	})
}

// noteRevoked records on the app's row that its token id was revoked, so the
// controller makes the app a new one. An app that is already gone needs nothing.
func (t *TokensAPI) noteRevoked(ctx context.Context, app token.App, id string) error {
	row, err := t.Apps.GetByObjectName(ctx, app.Name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := t.Apps.NoteTokenRevoked(ctx, Actor(ctx), row.ID, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}
