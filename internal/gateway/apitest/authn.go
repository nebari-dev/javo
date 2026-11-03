package apitest

import (
	"context"
	"net/http"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
)

func (f *Fixture) LoginAlice() *http.Cookie {
	f.T.Helper()
	w := f.Do("POST", "/api/auth/local/login", `{"username":"alice","password":"alice-password"}`, nil)
	if w.Code != http.StatusOK {
		f.T.Fatalf("login status %d: %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == model.SessionCookieName {
			return c
		}
	}
	f.T.Fatal("no session cookie")
	return nil
}

// ExpireRevalidation rewinds the session so the next request revalidates.
func (f *Fixture) ExpireRevalidation(c *http.Cookie) {
	f.T.Helper()
	ctx := context.Background()
	s, err := f.Store.Get(ctx, c.Value)
	if err != nil {
		f.T.Fatal(err)
	}
	s.RevalidateAt = time.Now().Add(-time.Second)
	if err := f.Store.Update(ctx, c.Value, s); err != nil {
		f.T.Fatal(err)
	}
}

// StubProvider is a provider whose Revalidate answers with err.
type StubProvider struct{ Err error }

func (StubProvider) ID() string { return "stub" }

func (StubProvider) Option() authprovider.LoginOption {
	return authprovider.LoginOption{ID: "stub", Label: "Stub", Kind: authprovider.KindRedirect, StartURL: "/auth/stub/start"}
}

func (StubProvider) Mount(authprovider.Mounter) {}

func (p StubProvider) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	return identity.Claims{}, s, p.Err
}

func PrincipalFor(id string) authz.Principal {
	return authz.Principal{ID: id, Username: "bob", Issuer: "stub"}
}
