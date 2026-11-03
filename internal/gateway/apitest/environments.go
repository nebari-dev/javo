package apitest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/ext/envsource"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
)

// FakeSource is an environment manager that serves alice's environments to
// whoever presents the ID token it was told is hers.
type FakeSource struct {
	Mu   sync.Mutex
	Want string   // the ID token it accepts
	Seen []string // the ID tokens it was given
}

func (f *FakeSource) ID() string { return "nebi" }

func (f *FakeSource) Check(idToken string) error {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.Seen = append(f.Seen, idToken)
	if idToken != f.Want {
		return envsource.ErrRefused
	}
	return nil
}

func (f *FakeSource) List(_ context.Context, idToken string) ([]envsource.Environment, error) {
	if err := f.Check(idToken); err != nil {
		return nil, err
	}
	return []envsource.Environment{{ID: "ws-1", Name: "sales", Owner: "bob"}}, nil
}

func (f *FakeSource) Fetch(_ context.Context, idToken, id string) (envsource.Pinned, error) {
	if err := f.Check(idToken); err != nil {
		return envsource.Pinned{}, err
	}
	if id != "ws-1" {
		return envsource.Pinned{}, envsource.ErrNotFound
	}
	return envsource.Pinned{Environment: envsource.Environment{ID: "ws-1", Name: "sales", Owner: "bob"}, Version: "3",
		Manifest: SalesEnv.Manifest, Lock: SalesEnv.Lock}, nil
}

var SalesEnv = model.EnvironmentFiles{Manifest: "[workspace]\nname = \"sales\"\n", Lock: "version: 6\n"}

// IdTokenAt is an unsigned JWT whose only claim is its expiry: the gateway
// reads exp to decide whether to refresh, and never verifies it (the
// environment manager does).
func IdTokenAt(name string, exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{"exp": exp.Unix(), "sub": name})
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + "." + enc([]byte(name))
}

// RefreshingProvider hands out a new ID token on every refresh.
type RefreshingProvider struct{ Token string }

func (RefreshingProvider) ID() string { return "oidc" }

func (RefreshingProvider) Option() authprovider.LoginOption {
	return authprovider.LoginOption{ID: "oidc", Label: "OIDC", Kind: authprovider.KindRedirect, StartURL: "/auth/oidc/start"}
}

func (RefreshingProvider) Mount(authprovider.Mounter) {}

func (p RefreshingProvider) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	s.IDToken = p.Token
	s.RevalidateAt = time.Now().Add(time.Hour)
	return identity.Claims{}, s, nil
}

// OidcSession signs alice in through the identity provider with idToken.
func (f *Fixture) OidcSession(idToken string) *http.Cookie {
	f.T.Helper()
	p := PrincipalFor("alice-aaaaaaaa")
	p.Username = "alice"
	tok, err := f.Store.Create(context.Background(), session.Session{
		Principal: p, Provider: "oidc", Kind: session.KindUI, IDToken: idToken,
		Expires: time.Now().Add(24 * time.Hour), RevalidateAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		f.T.Fatal(err)
	}
	return &http.Cookie{Name: model.SessionCookieName, Value: tok}
}

type EnvListJSON struct {
	Available bool                    `json:"available"`
	Message   string                  `json:"message"`
	Items     []envsource.Environment `json:"items"`
}

type EnvViewJSON struct {
	Environment *struct {
		Source, ID, Name, Version string
	} `json:"environment"`
}
