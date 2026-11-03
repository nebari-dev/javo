package apitest

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/identity"
)

// RedirectProvider is a browser-redirect provider: its callback route
// finishes sign-in through the host, as an OIDC callback does.
type RedirectProvider struct {
	Host     authprovider.Mounter
	LogoutTo string
}

func (*RedirectProvider) ID() string { return "redirect" }

func (*RedirectProvider) Option() authprovider.LoginOption {
	return authprovider.LoginOption{ID: "redirect", Label: "Redirect", Kind: authprovider.KindRedirect, StartURL: "/auth/redirect/start"}
}

func (p *RedirectProvider) Mount(m authprovider.Mounter) {
	p.Host = m
	m.Handle("GET /auth/redirect/callback", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signed, err := m.Complete(r.Context(), identity.Claims{Issuer: "https://idp.example.com", Subject: "u-bob", Username: "bob", Picture: "https://example.com/bob.png"},
			session.Session{Provider: "redirect", Kind: session.KindUI, RevalidateAt: m.Now().Add(time.Hour)})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &signed.Cookie)
		http.Redirect(w, r, "/", http.StatusFound)
	}))
}

func (*RedirectProvider) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	return identity.Claims{}, s, nil
}

func (p *RedirectProvider) LogoutURL(session.Session) string { return p.LogoutTo }

// RotatingProvider behaves like an identity provider that rotates refresh
// tokens and rejects reuse of an old one.
type RotatingProvider struct {
	Mu      sync.Mutex
	Current string
	Calls   int
}

func (*RotatingProvider) ID() string { return "rotating" }

func (*RotatingProvider) Option() authprovider.LoginOption {
	return authprovider.LoginOption{ID: "rotating", Label: "Rotating", Kind: authprovider.KindRedirect, StartURL: "/x"}
}

func (*RotatingProvider) Mount(authprovider.Mounter) {}

func (p *RotatingProvider) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	time.Sleep(50 * time.Millisecond) // let parallel requests overlap
	p.Mu.Lock()
	defer p.Mu.Unlock()
	p.Calls++
	if s.Refresh != p.Current {
		return identity.Claims{}, s, authprovider.ErrRevoked
	}
	p.Current = "refresh-" + strconv.Itoa(p.Calls+1)
	s.Refresh = p.Current
	s.RevalidateAt = time.Now().Add(time.Hour)
	return identity.Claims{}, s, nil
}

// HangingProvider behaves like an identity provider whose token endpoint does
// not answer: each call waits out the client timeout and fails as an outage.
type HangingProvider struct {
	Hang  time.Duration
	Mu    sync.Mutex
	Calls int
}

func (*HangingProvider) ID() string { return "hanging" }

func (*HangingProvider) Option() authprovider.LoginOption {
	return authprovider.LoginOption{ID: "hanging", Label: "Hanging", Kind: authprovider.KindRedirect, StartURL: "/x"}
}

func (*HangingProvider) Mount(authprovider.Mounter) {}

func (p *HangingProvider) Revalidate(_ context.Context, s session.Session) (identity.Claims, session.Session, error) {
	p.Mu.Lock()
	p.Calls++
	p.Mu.Unlock()
	time.Sleep(p.Hang)
	return identity.Claims{}, s, authprovider.ErrUnavailable
}
