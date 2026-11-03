package token_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var ctx = context.Background()

// env is a token store over a fresh database with two people signed in.
type env struct {
	st         *token.DBStore
	db         *store.Store
	now        *time.Time
	alice, bob string // the people's names
}

func newEnv(t *testing.T, now time.Time) *env {
	t.Helper()
	e := &env{db: storetest.New(t), now: &now}
	e.st = &token.DBStore{Tokens: e.db.Tokens(), Now: func() time.Time { return *e.now }}
	people := e.db.People(store.IDRange{First: 100000, Last: 199999})
	signIn := func(username string) string {
		p, err := people.Ensure(ctx, store.Claims{Issuer: "https://idp.example.com", Subject: username, Username: username})
		if err != nil {
			t.Fatal(err)
		}
		return p.Name
	}
	e.alice, e.bob = signIn("alice"), signIn("bob")
	return e
}

func (e *env) actor() model.Actor { return model.Actor{Person: e.alice} }

func (e *env) create(t *testing.T, tok token.Token) (string, token.Token) {
	t.Helper()
	text, created, err := e.st.Create(ctx, e.actor(), tok)
	if err != nil {
		t.Fatal(err)
	}
	return text, created
}

func TestCreateShowsTheSecretOnceAndStoresOnlyItsHash(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := newEnv(t, now)
	text, created := e.create(t, token.Token{Name: "laptop", Owner: e.alice, Scope: token.ScopeRead})
	if !strings.HasPrefix(text, token.Prefix) {
		t.Fatalf("token %q lacks the prefix %q", text, token.Prefix)
	}
	if created.ID == "" || !strings.HasPrefix(text, created.Prefix()) || created.Prefix() == text {
		t.Fatalf("prefix %q of %q", created.Prefix(), text)
	}
	if !created.CreatedAt.Equal(now) {
		t.Fatalf("createdAt %v", created.CreatedAt)
	}
	row, err := e.db.Tokens().Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret := text[len(created.Prefix()):]
	for _, stored := range []string{row.ID, row.Name, row.Hash, row.Prefix} {
		if strings.Contains(stored, secret) {
			t.Fatalf("stored value %q holds the secret part of the token", stored)
		}
	}
	listed, err := e.st.List(ctx, e.alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID || listed[0].Name != "laptop" || listed[0].Scope != token.ScopeRead {
		t.Fatalf("listed %+v", listed)
	}
}

func TestCreateForAnUnknownPersonFails(t *testing.T) {
	e := newEnv(t, time.Now())
	if _, _, err := e.st.Create(ctx, e.actor(), token.Token{Name: "x", Owner: "nobody", Scope: token.ScopeRead}); err == nil {
		t.Fatal("a token for nobody was created")
	}
}

func TestTwoTokensDiffer(t *testing.T) {
	e := newEnv(t, time.Now())
	a, _ := e.create(t, token.Token{Name: "a", Owner: e.alice, Scope: token.ScopeRead})
	b, _ := e.create(t, token.Token{Name: "b", Owner: e.alice, Scope: token.ScopeRead})
	if a == b {
		t.Fatal("two tokens are equal")
	}
}

func TestAuthenticate(t *testing.T) {
	e := newEnv(t, time.Unix(1_800_000_000, 0))
	text, created := e.create(t, token.Token{Name: "ci", Owner: e.alice, Scope: token.ScopeReadWrite})
	got, err := e.st.Authenticate(ctx, text)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.Owner != e.alice || got.Scope != token.ScopeReadWrite {
		t.Fatalf("got %+v", got)
	}
	// Same id, wrong secret: the stored hash decides.
	forged := created.Prefix() + strings.Repeat("A", len(text)-len(created.Prefix()))
	for _, bad := range []string{"", "javo_", "Bearer " + text, text + "x", forged, strings.ToUpper(text), "javo_../../x_y"} {
		if _, err := e.st.Authenticate(ctx, bad); !errors.Is(err, token.ErrInvalid) {
			t.Errorf("%q: err %v, want ErrInvalid", bad, err)
		}
	}
}

func TestExpiredTokenIsInvalid(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := newEnv(t, now)
	exp := now.Add(time.Hour)
	text, _ := e.create(t, token.Token{Name: "short", Owner: e.alice, Scope: token.ScopeRead, ExpiresAt: &exp})
	if _, err := e.st.Authenticate(ctx, text); err != nil {
		t.Fatal(err)
	}
	*e.now = exp
	if _, err := e.st.Authenticate(ctx, text); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("at expiry: %v", err)
	}
}

func TestRevokeOnlyTheOwnersToken(t *testing.T) {
	e := newEnv(t, time.Now())
	text, created := e.create(t, token.Token{Name: "ci", Owner: e.alice, Scope: token.ScopeRead})
	if _, err := e.st.Revoke(ctx, e.actor(), e.bob, created.ID); !errors.Is(err, token.ErrNotFound) {
		t.Fatalf("bob revoking alice's token: %v", err)
	}
	if _, err := e.st.Authenticate(ctx, text); err != nil {
		t.Fatalf("after bob's attempt: %v", err)
	}
	if _, err := e.st.Revoke(ctx, e.actor(), e.alice, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Authenticate(ctx, text); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("after revoke: %v", err)
	}
	if _, err := e.st.Revoke(ctx, e.actor(), e.alice, created.ID); !errors.Is(err, token.ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
}

func TestListIsPerOwnerOldestFirst(t *testing.T) {
	e := newEnv(t, time.Unix(1_800_000_000, 0))
	e.create(t, token.Token{Name: "first", Owner: e.alice, Scope: token.ScopeRead})
	*e.now = e.now.Add(time.Minute)
	e.create(t, token.Token{Name: "bobs", Owner: e.bob, Scope: token.ScopeRead})
	e.create(t, token.Token{Name: "second", Owner: e.alice, Scope: token.ScopeRead})
	l, err := e.st.List(ctx, e.alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[0].Name != "first" || l[1].Name != "second" {
		t.Fatalf("list %+v", l)
	}
}

func TestTouchRecordsLastUseAtMostOnceAMinute(t *testing.T) {
	e := newEnv(t, time.Unix(1_800_000_000, 0))
	text, _ := e.create(t, token.Token{Name: "ci", Owner: e.alice, Scope: token.ScopeRead})
	got, _ := e.st.Authenticate(ctx, text)
	if got.LastUsedAt != nil {
		t.Fatalf("new token has last use %v", got.LastUsedAt)
	}
	e.st.Touch(ctx, got)
	first := *e.now
	*e.now = e.now.Add(30 * time.Second)
	got, _ = e.st.Authenticate(ctx, text)
	e.st.Touch(ctx, got)
	got, _ = e.st.Authenticate(ctx, text)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(first) {
		t.Fatalf("within a minute: last use %v, want %v", got.LastUsedAt, first)
	}
	*e.now = e.now.Add(time.Minute)
	e.st.Touch(ctx, got)
	got, _ = e.st.Authenticate(ctx, text)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(*e.now) {
		t.Fatalf("after a minute: last use %v, want %v", got.LastUsedAt, *e.now)
	}
}

func (e *env) newApp(t *testing.T, name string) model.App {
	t.Helper()
	app, err := e.db.Apps().Create(ctx, e.actor(), &model.App{
		Namespace: "javo-apps", Name: name, Owner: e.alice, Template: "jupyterlab", DisplayName: name, Size: "small",
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestAppTokensAreRevokedWithTheirApp(t *testing.T) {
	e := newEnv(t, time.Now())
	app := e.newApp(t, "agent")
	want := &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: "agent"}
	text, _ := e.create(t, token.Token{Name: "agent", Owner: e.alice, Scope: token.ScopeReadWrite, App: want})
	other, _ := e.create(t, token.Token{Name: "laptop", Owner: e.alice, Scope: token.ScopeRead})
	got, err := e.st.Authenticate(ctx, text)
	if err != nil || got.App == nil || *got.App != *want {
		t.Fatalf("app token %+v, %v", got, err)
	}
	n, err := e.st.RevokeForApp(ctx, e.actor(), app.ID, "")
	if err != nil || n != 1 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, err := e.st.Authenticate(ctx, text); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("app token after its app: %v", err)
	}
	if _, err := e.st.Authenticate(ctx, other); err != nil {
		t.Fatalf("personal token after the app went: %v", err)
	}
}

// A changed API access keeps the token of the new scope.
func TestRevokeForAppKeepsOneScope(t *testing.T) {
	e := newEnv(t, time.Now())
	app := e.newApp(t, "agent")
	ref := &token.App{Name: app.ObjectName()}
	old, _ := e.create(t, token.Token{Name: "agent", Owner: e.alice, Scope: token.ScopeReadWrite, App: ref})
	fresh, _ := e.create(t, token.Token{Name: "agent", Owner: e.alice, Scope: token.ScopeRead, App: ref})
	if n, err := e.st.RevokeForApp(ctx, e.actor(), app.ID, token.ScopeRead); err != nil || n != 1 {
		t.Fatalf("revoked %d, %v", n, err)
	}
	if _, err := e.st.Authenticate(ctx, old); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("read-write token: %v", err)
	}
	if _, err := e.st.Authenticate(ctx, fresh); err != nil {
		t.Fatalf("read token: %v", err)
	}
}

func TestSweepRemovesExpiredDeletedAndUnwantedAppTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := newEnv(t, now)
	exp := now.Add(time.Hour)
	e.create(t, token.Token{Name: "expiring", Owner: e.alice, Scope: token.ScopeRead, ExpiresAt: &exp})
	keep, _ := e.create(t, token.Token{Name: "forever", Owner: e.alice, Scope: token.ScopeRead})
	live, gone, rescoped := e.newApp(t, "live"), e.newApp(t, "gone"), e.newApp(t, "rescoped")
	appToken := func(app model.App) string {
		text, _ := e.create(t, token.Token{Name: app.Name, Owner: e.alice, Scope: token.ScopeReadWrite,
			App: &token.App{Name: app.ObjectName()}})
		return text
	}
	liveText, goneText, rescopedText := appToken(live), appToken(gone), appToken(rescoped)
	if err := e.db.Apps().MarkDeleted(ctx, e.actor(), gone.ID); err != nil {
		t.Fatal(err)
	}
	*e.now = exp
	// live wants its read-write token; rescoped now has read access.
	wanted := func(_ context.Context, a token.App, s token.Scope) (bool, error) {
		return a.Short == "live" || a.Short == "rescoped" && s == token.ScopeRead, nil
	}
	n, err := e.st.Sweep(ctx, wanted)
	if err != nil || n != 3 {
		t.Fatalf("swept %d, %v", n, err)
	}
	for _, text := range []string{keep, liveText} {
		if _, err := e.st.Authenticate(ctx, text); err != nil {
			t.Fatalf("kept token: %v", err)
		}
	}
	for _, text := range []string{goneText, rescopedText} {
		if _, err := e.st.Authenticate(ctx, text); !errors.Is(err, token.ErrInvalid) {
			t.Fatalf("unwanted app token: %v", err)
		}
	}
}

func TestDeleteForEndsEveryTokenOfOnePerson(t *testing.T) {
	e := newEnv(t, time.Now())
	a, _ := e.create(t, token.Token{Name: "a", Owner: e.alice, Scope: token.ScopeRead})
	b, _ := e.create(t, token.Token{Name: "b", Owner: e.bob, Scope: token.ScopeRead})
	if n, err := e.st.DeleteFor(ctx, e.actor(), e.alice); err != nil || n != 1 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	if _, err := e.st.Authenticate(ctx, a); !errors.Is(err, token.ErrInvalid) {
		t.Fatalf("alice's token: %v", err)
	}
	if _, err := e.st.Authenticate(ctx, b); err != nil {
		t.Fatalf("bob's token: %v", err)
	}
}

func TestScopeAllows(t *testing.T) {
	for _, m := range []string{"GET", "HEAD"} {
		if !token.ScopeRead.Allows(m) {
			t.Errorf("read refuses %s", m)
		}
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if token.ScopeRead.Allows(m) {
			t.Errorf("read allows %s", m)
		}
		if !token.ScopeReadWrite.Allows(m) {
			t.Errorf("read-write refuses %s", m)
		}
	}
	if token.Scope("admin").Allows("GET") {
		t.Error("an unknown scope allows GET")
	}
}
