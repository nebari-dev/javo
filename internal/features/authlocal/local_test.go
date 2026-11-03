package authlocal_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/features/authlocal"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/reasons"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// fakeUsers is an in-memory local.Users.
type fakeUsers struct {
	users map[string]local.User
	pw    map[string]string
	err   error // returned by Lookup when set
}

func (f *fakeUsers) Verify(_ context.Context, name, pw string) (local.User, error) {
	if u, ok := f.users[name]; ok && f.pw[name] == pw {
		return u, nil
	}
	return local.User{}, local.ErrInvalidCredentials
}

func (f *fakeUsers) Lookup(_ context.Context, name string) (local.User, error) {
	if f.err != nil {
		return local.User{}, f.err
	}
	if u, ok := f.users[name]; ok {
		return u, nil
	}
	return local.User{}, local.ErrUserNotFound
}

// fakeHost records what the provider asks of the host.
type fakeHost struct {
	api       humatest.TestAPI
	limit     error
	completed []identity.Claims
	sessions  []session.Session
}

func (h *fakeHost) API() huma.API                            { return h.api }
func (h *fakeHost) Handle(string, http.Handler)              { panic("local provider has no browser routes") }
func (h *fakeHost) LimitLogin(context.Context, string) error { return h.limit }
func (h *fakeHost) Now() time.Time                           { return now }
func (h *fakeHost) Reject(status int, c reasons.MessageCode) error {
	return huma.NewError(status, reasons.Msg(c))
}
func (h *fakeHost) Complete(_ context.Context, c identity.Claims, s session.Session) (authprovider.Signed, error) {
	h.completed = append(h.completed, c)
	h.sessions = append(h.sessions, s)
	return authprovider.Signed{
		Cookie: http.Cookie{Name: "javo_session", Value: "tok"},
		Me:     authprovider.Me{ID: "alice-1", Username: c.Username, Groups: c.Groups, Roles: []string{"member"}},
	}, nil
}

func setup(t *testing.T) (*fakeHost, *fakeUsers, authprovider.Provider) {
	t.Helper()
	users := &fakeUsers{
		users: map[string]local.User{"alice": {ID: local.UserID("alice"), Name: "alice", Groups: []string{"javo-admins"}}},
		pw:    map[string]string{"alice": "alice-password"},
	}
	p := authlocal.New(authlocal.Config{Users: users, RevalidateEvery: time.Minute})
	_, api := humatest.New(t)
	h := &fakeHost{api: api}
	p.Mount(h)
	return h, users, p
}

func TestOptionIsAPasswordForm(t *testing.T) {
	_, _, p := setup(t)
	if p.ID() != "local" {
		t.Fatalf("id %q", p.ID())
	}
	if o := p.Option(); o.ID != "local" || o.Kind != authprovider.KindPassword || o.Label != reasons.Msg(reasons.MsgLocalSignInLabel) {
		t.Fatalf("option %+v", o)
	}
}

func TestLoginCompletesWithLocalClaimsAndSetsCookie(t *testing.T) {
	h, _, _ := setup(t)
	resp := h.api.Post("/api/auth/local/login", map[string]any{"username": "alice", "password": "alice-password"})
	if resp.Code != http.StatusOK {
		t.Fatalf("status %d: %s", resp.Code, resp.Body)
	}
	if len(h.completed) != 1 {
		t.Fatalf("completed %d times", len(h.completed))
	}
	c := h.completed[0]
	if c.Issuer != "local" || c.Subject != "alice" || c.Username != "alice" || len(c.Groups) != 1 || c.Groups[0] != "javo-admins" {
		t.Fatalf("claims %+v", c)
	}
	if s := h.sessions[0]; s.Provider != "local" || s.Kind != session.KindUI || !s.RevalidateAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("session %+v", s)
	}
	if got := resp.Result().Cookies(); len(got) != 1 || got[0].Value != "tok" {
		t.Fatalf("cookies %v", got)
	}
}

func TestWrongPasswordIs401AndCompletesNothing(t *testing.T) {
	h, _, _ := setup(t)
	resp := h.api.Post("/api/auth/local/login", map[string]any{"username": "alice", "password": "nope"})
	if resp.Code != http.StatusUnauthorized || len(h.completed) != 0 {
		t.Fatalf("status %d, completed %d", resp.Code, len(h.completed))
	}
}

func TestLimitedAttemptNeverChecksThePassword(t *testing.T) {
	h, _, _ := setup(t)
	h.limit = huma.NewError(http.StatusTooManyRequests, "slow down")
	resp := h.api.Post("/api/auth/local/login", map[string]any{"username": "alice", "password": "alice-password"})
	if resp.Code != http.StatusTooManyRequests || len(h.completed) != 0 {
		t.Fatalf("status %d, completed %d", resp.Code, len(h.completed))
	}
}

func liveSession() session.Session {
	return session.Session{
		Principal: authz.Principal{ID: local.UserID("alice"), Username: "alice", Issuer: "local"},
		Provider:  "local", Kind: session.KindUI,
		RevalidateAt: now.Add(-time.Second),
	}
}

func TestRevalidateReturnsCurrentGroupsAndMovesTheDeadline(t *testing.T) {
	_, users, p := setup(t)
	users.users["alice"] = local.User{ID: local.UserID("alice"), Name: "alice", Groups: []string{"javo-viewers"}}
	c, s, err := p.Revalidate(context.Background(), liveSession())
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "alice" || len(c.Groups) != 1 || c.Groups[0] != "javo-viewers" {
		t.Fatalf("claims %+v", c)
	}
	if !s.RevalidateAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("RevalidateAt %v", s.RevalidateAt)
	}
}

func TestRevalidateOfDeletedAccountIsRevoked(t *testing.T) {
	_, users, p := setup(t)
	delete(users.users, "alice")
	if _, _, err := p.Revalidate(context.Background(), liveSession()); !errors.Is(err, authprovider.ErrRevoked) {
		t.Fatalf("got %v", err)
	}
}

func TestRevalidateWhenSecretUnreadableIsUnavailable(t *testing.T) {
	_, users, p := setup(t)
	users.err = errors.New("apiserver down")
	if _, _, err := p.Revalidate(context.Background(), liveSession()); !errors.Is(err, authprovider.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}
