package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
)

// revoked is a provider that says every person is gone (an account deleted,
// or disabled in the identity provider).
type revoked struct{ calls *int }

func (revoked) ID() string                       { return "local" }
func (revoked) Option() authprovider.LoginOption { return authprovider.LoginOption{} }
func (revoked) Mount(authprovider.Mounter)       {}
func (r revoked) Revalidate(context.Context, session.Session) (identity.Claims, session.Session, error) {
	*r.calls++
	return identity.Claims{}, session.Session{}, authprovider.ErrRevoked
}

type noReconfirm struct{}

func (noReconfirm) Reconfirm(context.Context, string, []string, []string) error { return nil }
func (noReconfirm) Revoked(context.Context, string) error                       { return nil }

// On one host the app request rides the UI cookie: when that session is due,
// the proxy asks the provider as the API does, so a person the provider has
// revoked stops opening apps at once, not at the session's 12 h expiry.
func TestOneHostRevalidatesADueUISession(t *testing.T) {
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "app page") }))
	u, _ := url.Parse(f.backend.URL)
	calls := 0
	reg := registry.New[authprovider.Provider]()
	reg.Register(revoked{&calls})
	rv := authn.NewRevalidator(reg, f.store, noReconfirm{}, 0, nil)
	p := New(Config{Store: f.db, Sessions: f.store, Exposure: gatewaytest.OneHostExposure, Activity: activity.NewTracker(nil),
		Revalidator: rv, DialWait: 200 * time.Millisecond,
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) { return Backend{URL: u}, nil }})
	tok, err := f.store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: "alice-aaaaaaaa", Username: "alice"},
		Provider: "local", Kind: session.KindUI, RevalidateAt: time.Now().Add(-6 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/u/"+f.aliceSl+"/nb1/", nil)
	r.AddCookie(&http.Cookie{Name: model.CookieName(model.SessionCookieName, true), Value: tok})
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if calls != 1 || w.Body.String() == "app page" {
		t.Fatalf("proxy answer: %d %q; provider asked %d times", w.Code, w.Body.String(), calls)
	}
}

// stillThere is a provider that confirms every person as they are.
type stillThere struct{ id string }

func (s stillThere) ID() string                     { return s.id }
func (stillThere) Option() authprovider.LoginOption { return authprovider.LoginOption{} }
func (stillThere) Mount(authprovider.Mounter)       {}
func (stillThere) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	s.RevalidateAt = time.Now().Add(time.Hour)
	return identity.Claims{}, s, nil
}

// confirming is a Revalidator whose providers confirm everyone, for tests
// about something else than revalidation.
func confirming(sessions session.Store) *authn.Revalidator {
	reg := registry.New[authprovider.Provider]()
	reg.Register(stillThere{"local"})
	reg.Register(stillThere{"oidc"})
	return authn.NewRevalidator(reg, sessions, noReconfirm{}, 0, nil)
}
