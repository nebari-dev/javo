// Package authlocal is the auth provider for local accounts: a username and
// password checked against the users Secret.
package authlocal

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/reasons"
)

// ID is the provider's registry key, Session.Provider value and Claims.Issuer.
const ID = "local"

// DefaultRevalidateEvery is how often a session re-reads its account.
const DefaultRevalidateEvery = time.Minute

// Config wires the provider.
type Config struct {
	Users local.Users
	// RevalidateEvery is how often a session re-reads its account; zero means
	// DefaultRevalidateEvery.
	RevalidateEvery time.Duration
}

// Provider signs people in with local accounts.
type Provider struct {
	users local.Users
	every time.Duration
	host  authprovider.Mounter
}

var _ authprovider.Provider = (*Provider)(nil)

// New returns the local provider.
func New(cfg Config) *Provider {
	every := cfg.RevalidateEvery
	if every <= 0 {
		every = DefaultRevalidateEvery
	}
	return &Provider{users: cfg.Users, every: every}
}

func (p *Provider) ID() string { return ID }

func (p *Provider) Option() authprovider.LoginOption {
	return authprovider.LoginOption{ID: ID, Label: reasons.Msg(reasons.MsgLocalSignInLabel), Kind: authprovider.KindPassword}
}

type loginInput struct {
	Body struct {
		Username string `json:"username" minLength:"1" maxLength:"256"`
		Password string `json:"password" minLength:"1" maxLength:"1024"`
	}
}

type loginOutput struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      authprovider.Me
}

func (p *Provider) Mount(m authprovider.Mounter) {
	p.host = m
	huma.Register(m.API(), huma.Operation{
		OperationID: "localLogin", Method: http.MethodPost, Path: "/api/auth/local/login",
		Summary: "Sign in with a local account", Tags: []string{"auth"}, Security: []map[string][]string{},
		Errors: []int{http.StatusUnauthorized, http.StatusTooManyRequests},
	}, func(ctx context.Context, in *loginInput) (*loginOutput, error) {
		done, err := m.LimitLogin(ctx, in.Body.Username)
		if err != nil {
			return nil, err
		}
		u, err := p.users.Verify(ctx, in.Body.Username, in.Body.Password)
		if errors.Is(err, local.ErrInvalidCredentials) {
			done(false)
			return nil, m.Reject(http.StatusUnauthorized, reasons.MsgWrongCredentials)
		}
		if err != nil {
			slog.Error("verify credentials", "err", err)
			return nil, m.Reject(http.StatusInternalServerError, reasons.MsgServerError)
		}
		done(true)
		signed, err := m.Complete(ctx, claims(u), session.Session{
			Provider: ID, Kind: session.KindUI, RevalidateAt: m.Now().Add(p.every),
		})
		if err != nil {
			slog.Error("complete sign-in", "err", err)
			return nil, m.Reject(http.StatusInternalServerError, reasons.MsgServerError)
		}
		return &loginOutput{SetCookie: signed.Cookie, Body: signed.Me}, nil
	})
}

// claims describes a local account: its subject is the username.
func claims(u local.User) identity.Claims {
	return identity.Claims{Issuer: ID, Subject: u.Name, Username: u.Name, Groups: u.Groups}
}

// Revalidate re-reads the account: a deleted one is revoked, an unreadable
// Secret is an outage, and a changed group list is picked up.
func (p *Provider) Revalidate(ctx context.Context, s session.Session) (identity.Claims, session.Session, error) {
	u, err := p.users.Lookup(ctx, s.Principal.Username)
	switch {
	case errors.Is(err, local.ErrUserNotFound):
		return identity.Claims{}, s, authprovider.ErrRevoked
	case err != nil:
		slog.Warn("revalidate local account", "err", err)
		return identity.Claims{}, s, authprovider.ErrUnavailable
	}
	s.Principal = authz.Principal{ID: s.Principal.ID, Username: u.Name, Issuer: ID, Groups: u.Groups}
	s.RevalidateAt = p.host.Now().Add(p.every)
	return claims(u), s, nil
}
