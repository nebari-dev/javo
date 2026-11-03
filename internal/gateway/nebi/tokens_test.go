package nebi_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

type nebiFixture struct {
	db     *store.Store
	opener *secrets.Opener
	src    *gatewaytest.NebiSource
	now    time.Time
	keeper *nebi.Tokens
	app    model.App
	tpl    *model.Template
}

func newNebiFixture(t *testing.T) *nebiFixture {
	t.Helper()
	db := storetest.New(t)
	opener := gatewaytest.Keyset(t, db)
	var err error
	f := &nebiFixture{db: db, opener: opener, now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	f.src = &gatewaytest.NebiSource{Now: func() time.Time { return f.now }}
	f.keeper = &nebi.Tokens{Store: db, Source: f.src, Now: func() time.Time { return f.now }}
	gatewaytest.Seed(t, db, []any{gatewaytest.Template("lab", "jupyterlab")})
	tpl, err := db.Templates().Get(context.Background(), "lab")
	if err != nil {
		t.Fatal(err)
	}
	f.tpl = &tpl
	f.tpl.Spec.NebiToken = true
	app, err := db.Apps().Create(context.Background(), model.Actor{Person: "alice"}, gatewaytest.App("alice", "lab-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.app = app
	return f
}

func (f *nebiFixture) stored(t *testing.T) (string, time.Time) {
	t.Helper()
	sealed, exp, err := f.db.NebiTokens().Get(context.Background(), f.app.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", time.Time{}
	}
	if err != nil {
		t.Fatal(err)
	}
	tok, err := f.opener.Open(sealed, secrets.NebiTokenAD(f.app.ID, f.app.Owner))
	if err != nil {
		t.Fatal(err)
	}
	return string(tok), exp
}

func idAlice(context.Context) (string, error) { return "id-alice", nil }

// The owner's use of the app stores their Nebi token, sealed so only the
// controller opens it; it is renewed once less than half its life is left,
// not on every request.
func TestTheOwnersUseKeepsTheAppsNebiTokenFresh(t *testing.T) {
	f := newNebiFixture(t)
	ctx := context.Background()
	f.keeper.Keep(ctx, &f.app, f.tpl, "alice", idAlice)
	if tok, exp := f.stored(t); tok != "nebi-1" || !exp.Equal(f.now.Add(24*time.Hour)) {
		t.Fatalf("stored %q expiring %v", tok, exp)
	}
	f.now = f.now.Add(2 * time.Hour)
	f.keeper.Keep(ctx, &f.app, f.tpl, "alice", idAlice)
	if tok, _ := f.stored(t); tok != "nebi-1" {
		t.Fatalf("renewed too early: %q", tok)
	}
	f.now = f.now.Add(11 * time.Hour)
	f.keeper.Keep(ctx, &f.app, f.tpl, "alice", idAlice)
	if tok, _ := f.stored(t); tok != "nebi-2" {
		t.Fatalf("not renewed with 11 h left: %q", tok)
	}
}

// Only the owner's own session mints the owner's token: someone the app is
// shared with never makes Javo ask Nebi as them, nor as the owner. Nothing
// happens for a template that does not ask, or a preview.
func TestOnlyTheOwnerOfAnAppThatAsksGetsANebiToken(t *testing.T) {
	f := newNebiFixture(t)
	ctx := context.Background()
	f.keeper.Keep(ctx, &f.app, f.tpl, "bob", idAlice)
	preview := f.app
	preview.ParentID = "p-1"
	f.keeper.Keep(ctx, &preview, f.tpl, "alice", idAlice)
	plain := *f.tpl
	plain.Spec.NebiToken = false
	f.keeper.Keep(ctx, &f.app, &plain, "alice", idAlice)
	if tok, _ := f.stored(t); tok != "" || f.src.Calls != 0 {
		t.Fatalf("stored %q after %d calls to Nebi", tok, f.src.Calls)
	}
	var off *nebi.Tokens
	off.Keep(ctx, &f.app, f.tpl, "alice", idAlice) // Nebi off: a nil keeper does nothing
}
