package authn_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// When the provider says a person is gone (disabled, deleted, consent
// revoked), their API tokens stop with their browser session, rather than
// working on for the rest of groupsMaxAge.
func TestTokensStopWhenTheProviderRevokesTheirOwner(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Extra: []authprovider.Provider{apitest.StubProvider{Err: authprovider.ErrRevoked}}})
	ctx := context.Background()
	p, err := f.People.Ensure(ctx, store.Claims{Issuer: apitest.TestIssuer, Subject: "sub-bob", Username: "bob", Email: "bob@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := f.Store.Create(ctx, session.Session{Principal: authz.Principal{ID: p.Name, Username: "bob"}, Provider: "stub", Kind: session.KindUI, RevalidateAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := f.Tokens.Create(ctx, model.Actor{Person: p.Name}, token.Token{Name: "laptop", Owner: p.Name, Scope: token.ScopeReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusOK {
		t.Fatalf("token before: %d %s", w.Code, w.Body)
	}
	if w := f.Do("GET", "/api/me", "", f.Cookie(sid)); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session: %d", w.Code)
	}
	if w := f.Bearer("GET", "/api/me", "", text, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("token after the provider revoked its owner: %d %s", w.Code, w.Body)
	}
}
