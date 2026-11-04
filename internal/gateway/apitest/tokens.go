package apitest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/nebari-dev/javo/internal/activity"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/proxy"
	"github.com/nebari-dev/javo/internal/model"
)

type TokenJSON struct {
	ID, Name, Scope, Prefix, Token, App string
	CreatedAt                           time.Time
	ExpiresAt, LastUsedAt               *time.Time
}

// NewToken creates a token through the API with the session cookie c.
func (f *Fixture) NewToken(c *http.Cookie, body string) TokenJSON {
	f.T.Helper()
	w := f.Do("POST", "/api/tokens", body, c)
	if w.Code != http.StatusCreated {
		f.T.Fatalf("create token: %d %s", w.Code, w.Body)
	}
	return Decode[TokenJSON](f.T, w)
}

// Bearer makes a request with an Authorization header and, when c is set, a
// cookie too; extra headers are added as given.
func (f *Fixture) Bearer(method, path, body, tok string, c *http.Cookie, extra ...string) *httptest.ResponseRecorder {
	f.T.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	if c != nil {
		r.AddCookie(c)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		r.Header.Set(extra[i], extra[i+1])
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

func LocalAlice() string { return local.UserID("alice") }

// MountProxy serves apps under /u/ on the fixture's server, all answered by target.
func (f *Fixture) MountProxy(target string) {
	f.T.Helper()
	u, err := url.Parse(target)
	if err != nil {
		f.T.Fatal(err)
	}
	f.Srv.Apps(proxy.New(proxy.Config{
		Store: f.Db, Sessions: f.Store, Revalidator: f.Revalidator, Exposure: gatewaytest.OneHostExposure, Activity: activity.NewTracker(nil),
		Resolve: func(context.Context, *model.App, *model.Template) (proxy.Backend, error) {
			return proxy.Backend{URL: u}, nil
		},
	}))
}
