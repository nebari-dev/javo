package session_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func newStore(t *testing.T, now *time.Time) (*session.DBStore, *store.Store) {
	t.Helper()
	db := storetest.New(t)
	return &session.DBStore{Sessions: db.Sessions(), Now: func() time.Time { return *now }}, db
}

var (
	alice = authz.Principal{ID: "alice-1a2b3c4d", Username: "alice", Groups: []string{"dev"}}
	bob   = authz.Principal{ID: "bob-5e6f7a8b", Username: "bob"}
)

func ui(p authz.Principal) session.Session {
	return session.Session{Principal: p, Provider: "local", Kind: session.KindUI}
}

func mustCreate(t *testing.T, st *session.DBStore, s session.Session) string {
	t.Helper()
	tok, err := st.Create(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestCreateGetRoundTrip(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	ctx := context.Background()

	in := ui(alice)
	in.Refresh, in.IDToken = "refresh-1", "id-token-1"
	in.RevalidateAt = now.Add(time.Minute)
	tok := mustCreate(t, st, in)
	if len(tok) != 43 { // 32 bytes base64url, no padding
		t.Fatalf("token length %d", len(tok))
	}
	got, err := st.Get(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.Principal.ID != alice.ID || got.Principal.Username != "alice" || len(got.Principal.Groups) != 1 ||
		got.Provider != "local" || got.Kind != session.KindUI || got.Refresh != "refresh-1" || got.IDToken != "id-token-1" ||
		!got.Expires.Equal(now.Add(session.DefaultTTL)) || !got.RevalidateAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected %+v", got)
	}
	if tok2 := mustCreate(t, st, ui(alice)); tok == tok2 {
		t.Fatal("tokens must be unique")
	}
}

func TestCreateRejectsUnknownKind(t *testing.T) {
	now := time.Now()
	st, _ := newStore(t, &now)
	if _, err := st.Create(context.Background(), session.Session{Principal: alice, Kind: "bogus"}); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestRowHoldsNoRawToken(t *testing.T) {
	now := time.Now()
	st, db := newStore(t, &now)
	tok := mustCreate(t, st, ui(alice))

	row, err := db.Sessions().Get(context.Background(), session.ID(tok))
	if err != nil {
		t.Fatal(err)
	}
	if len(row.ID) != 32 || row.ID == tok || strings.Contains(row.ID, tok[:8]) {
		t.Fatalf("row id %q is not a hash of the token", row.ID)
	}
	for name, v := range map[string]string{"id": row.ID, "principal": row.Principal, "provider": row.Provider, "refresh": row.Refresh, "id_token": row.IDToken} {
		if strings.Contains(v, tok) {
			t.Fatalf("%s holds the raw token", name)
		}
	}
}

func TestChildRowNamesItsParentAndPerson(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, db := newStore(t, &now)
	uiTok := mustCreate(t, st, ui(alice))
	appTok := mustCreate(t, st, session.Session{Principal: alice, Kind: session.KindApp, Parent: session.ID(uiTok), Workspace: "javo-apps/sales"})

	for tok, want := range map[string]struct{ kind, parent string }{
		uiTok:  {session.KindUI, ""},
		appTok: {session.KindApp, session.ID(uiTok)},
	} {
		row, err := db.Sessions().Get(context.Background(), session.ID(tok))
		if err != nil {
			t.Fatal(err)
		}
		if row.Person != alice.ID || row.Kind != want.kind || row.ParentID != want.parent ||
			!row.ExpiresAt.Equal(now.Add(session.DefaultTTL)) {
			t.Fatalf("%s row: %+v", want.kind, row)
		}
	}
}

func TestExpiredSessionIsRemovedOnGet(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, db := newStore(t, &now)
	ctx := context.Background()
	tok := mustCreate(t, st, ui(alice))

	now = now.Add(session.DefaultTTL - time.Second)
	if _, err := st.Get(ctx, tok); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	now = now.Add(time.Second)
	if _, err := st.Get(ctx, tok); !errors.Is(err, session.ErrSessionExpired) {
		t.Fatalf("want ErrSessionExpired, got %v", err)
	}
	if _, err := db.Sessions().Get(ctx, session.ID(tok)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired session row not removed: %v", err)
	}
}

func TestExpiryOnGetIsNotAuditedAsSignOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, db := newStore(t, &now)
	ctx := context.Background()
	tok := mustCreate(t, st, ui(alice))
	now = now.Add(session.DefaultTTL)
	if _, err := st.Get(ctx, tok); !errors.Is(err, session.ErrSessionExpired) {
		t.Fatalf("want ErrSessionExpired, got %v", err)
	}
	events, err := db.Audit().List(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Action == "session.delete" {
			t.Fatalf("expiry on read audited as sign-out: %+v", e)
		}
	}
}

func TestCustomTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	st.TTL = time.Minute
	tok := mustCreate(t, st, ui(alice))
	now = now.Add(time.Minute)
	if _, err := st.Get(context.Background(), tok); !errors.Is(err, session.ErrSessionExpired) {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteThenGetFails(t *testing.T) {
	now := time.Now()
	st, _ := newStore(t, &now)
	ctx := context.Background()
	tok := mustCreate(t, st, ui(alice))
	if err := st.Delete(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, tok); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("got %v", err)
	}
	if err := st.Delete(ctx, tok); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
}

func TestGetUnknownAndEmptyToken(t *testing.T) {
	now := time.Now()
	st, _ := newStore(t, &now)
	for _, tok := range []string{"", "nope"} {
		if _, err := st.Get(context.Background(), tok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Errorf("token %q: got %v", tok, err)
		}
	}
}

func TestAppSessionDiesWithItsParent(t *testing.T) {
	now := time.Now()
	st, _ := newStore(t, &now)
	ctx := context.Background()
	uiTok := mustCreate(t, st, ui(alice))
	app := session.Session{Principal: alice, Kind: session.KindApp, Parent: session.ID(uiTok), Workspace: "javo-apps/sales"}
	appTok := mustCreate(t, st, app)
	ticket := session.Session{Principal: alice, Kind: session.KindTicket, Parent: session.ID(uiTok), Workspace: "javo-apps/sales"}
	ticketTok := mustCreate(t, st, ticket)

	got, err := st.Get(ctx, appTok)
	if err != nil || got.Workspace != "javo-apps/sales" || got.Parent != session.ID(uiTok) {
		t.Fatalf("live parent: %+v %v", got, err)
	}
	if err := st.Delete(ctx, uiTok); err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{"app": appTok, "ticket": ticketTok} {
		if _, err := st.Get(ctx, tok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Errorf("%s session with no parent: got %v", name, err)
		}
	}
}

func TestAppSessionRejectsUnfitParent(t *testing.T) {
	ctx := context.Background()
	child := func(p authz.Principal, parent string, exp time.Time) session.Session {
		return session.Session{Principal: p, Kind: session.KindApp, Parent: parent, Workspace: "javo-apps/sales", Expires: exp}
	}
	t.Run("parent expired and not yet swept", func(t *testing.T) {
		now := time.Now()
		st, _ := newStore(t, &now)
		uiTok := mustCreate(t, st, session.Session{Principal: alice, Kind: session.KindUI, Expires: now.Add(time.Hour)})
		appTok := mustCreate(t, st, child(alice, session.ID(uiTok), now.Add(24*time.Hour)))
		now = now.Add(2 * time.Hour)
		if _, err := st.Get(ctx, appTok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("parent is not a ui session", func(t *testing.T) {
		now := time.Now()
		st, _ := newStore(t, &now)
		uiTok := mustCreate(t, st, ui(alice))
		mid := mustCreate(t, st, child(alice, session.ID(uiTok), time.Time{}))
		leaf := mustCreate(t, st, child(alice, session.ID(mid), time.Time{}))
		if _, err := st.Get(ctx, leaf); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("parent belongs to another identity", func(t *testing.T) {
		now := time.Now()
		st, _ := newStore(t, &now)
		bobTok := mustCreate(t, st, ui(bob))
		appTok := mustCreate(t, st, child(alice, session.ID(bobTok), time.Time{}))
		if _, err := st.Get(ctx, appTok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestUpdateIsOptimistic(t *testing.T) {
	now := time.Now()
	st, _ := newStore(t, &now)
	ctx := context.Background()
	tok := mustCreate(t, st, ui(alice))

	a, err := st.Get(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Get(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	a.Refresh = "rotated-by-a"
	if err := st.Update(ctx, tok, a); err != nil {
		t.Fatal(err)
	}
	b.Refresh = "rotated-by-b"
	if err := st.Update(ctx, tok, b); !errors.Is(err, session.ErrSessionChanged) {
		t.Fatalf("want ErrSessionChanged, got %v", err)
	}
	got, _ := st.Get(ctx, tok)
	if got.Refresh != "rotated-by-a" {
		t.Fatalf("refresh %q", got.Refresh)
	}
	// A fresh read can be updated again.
	got.Refresh = "next"
	if err := st.Update(ctx, tok, got); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateStoresTheNewExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, db := newStore(t, &now)
	ctx := context.Background()
	tok := mustCreate(t, st, ui(alice))
	got, _ := st.Get(ctx, tok)
	got.Expires = now.Add(48 * time.Hour)
	if err := st.Update(ctx, tok, got); err != nil {
		t.Fatal(err)
	}
	row, err := db.Sessions().Get(ctx, session.ID(tok))
	if err != nil || !row.ExpiresAt.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("row %+v, %v", row, err)
	}
}

func TestDeleteForEndsOnlyThatIdentitysSessions(t *testing.T) {
	now := time.Now()
	st, _ := newStore(t, &now)
	ctx := context.Background()
	a1 := mustCreate(t, st, ui(alice))
	a2 := mustCreate(t, st, ui(alice))
	app := session.Session{Principal: alice, Kind: session.KindApp, Parent: session.ID(a1), Workspace: "javo-apps/sales"}
	a3 := mustCreate(t, st, app)
	b1 := mustCreate(t, st, ui(bob))

	n, err := st.DeleteFor(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // the two sign-ins; the app session goes with its parent
		t.Fatalf("deleted %d, want 2", n)
	}
	for _, tok := range []string{a1, a2, a3} {
		if _, err := st.Get(ctx, tok); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("alice session still valid: %v", err)
		}
	}
	if _, err := st.Get(ctx, b1); err != nil {
		t.Fatalf("bob's session ended: %v", err)
	}
}

func TestSweepDeletesExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	ctx := context.Background()
	mustCreate(t, st, ui(alice))
	start := now

	now = start.Add(session.DefaultTTL - time.Second)
	if n, err := st.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("before expiry: deleted %d, err %v", n, err)
	}
	now = start.Add(session.DefaultTTL + time.Second)
	if n, err := st.Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("after expiry: deleted %d, err %v", n, err)
	}
}

func ticketFor(parentTok string) session.Session {
	return session.Session{Principal: alice, Provider: "local", Kind: session.KindTicket,
		Parent: session.ID(parentTok), Workspace: "javo-apps/alice-1a2b3c4d-sales"}
}

func TestRedeemWorksOnceAndOnlyForTickets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	ctx := context.Background()
	parent := mustCreate(t, st, ui(alice))
	tk := ticketFor(parent)
	tk.Expires = now.Add(time.Minute)
	tok := mustCreate(t, st, tk)

	got, err := st.Redeem(ctx, tok)
	if err != nil || got.Workspace != tk.Workspace || got.Parent != tk.Parent {
		t.Fatalf("redeem: %+v %v", got, err)
	}
	if _, err := st.Redeem(ctx, tok); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("second redeem: %v", err)
	}
	if _, err := st.Get(ctx, parent); err != nil {
		t.Fatalf("redeeming a ticket ended its parent: %v", err)
	}
	if _, err := st.Redeem(ctx, parent); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("a ui session must not be redeemable: %v", err)
	}
	if _, err := st.Get(ctx, parent); err != nil {
		t.Fatalf("refused redeem still deleted the ui session: %v", err)
	}
}

// Ten gateway replicas (or requests) redeem the same ticket at once: exactly
// one gets it.
func TestRedeemTicketOnce(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	parent := mustCreate(t, st, ui(alice))
	tok := mustCreate(t, st, ticketFor(parent))

	const racers = 10
	var wg sync.WaitGroup
	results := make(chan error, racers)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.Redeem(context.Background(), tok)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, session.ErrSessionNotFound):
			t.Errorf("a loser got %v, want ErrSessionNotFound", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d redeems won, want exactly 1", wins)
	}
}

func TestRedeemRefusesAnExpiredTicket(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	parent := mustCreate(t, st, ui(alice))
	tk := ticketFor(parent)
	tk.Expires = now.Add(60 * time.Second)
	tok := mustCreate(t, st, tk)
	now = now.Add(61 * time.Second)
	if _, err := st.Redeem(context.Background(), tok); !errors.Is(err, session.ErrSessionExpired) {
		t.Fatalf("expired ticket: %v", err)
	}
}

func TestAppSessionSeesTheParentsCurrentGroups(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	ctx := context.Background()
	uiTok := mustCreate(t, st, ui(alice))
	appTok := mustCreate(t, st, session.Session{Principal: alice, Kind: session.KindApp, Parent: session.ID(uiTok), Workspace: "javo-apps/sales"})

	parent, err := st.Get(ctx, uiTok)
	if err != nil {
		t.Fatal(err)
	}
	parent.Principal.Groups = []string{"ops"}
	if err := st.Update(ctx, uiTok, parent); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(ctx, appTok)
	if err != nil || len(got.Principal.Groups) != 1 || got.Principal.Groups[0] != "ops" {
		t.Fatalf("app session groups: %+v %v", got.Principal, err)
	}
}

func TestUISessionCanBeLoadedUpdatedAndDeletedByID(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st, _ := newStore(t, &now)
	ctx := context.Background()
	uiTok := mustCreate(t, st, ui(alice))
	appTok := mustCreate(t, st, session.Session{Principal: alice, Kind: session.KindApp, Parent: session.ID(uiTok), Workspace: "javo-apps/sales"})

	got, err := st.GetByID(ctx, session.ID(uiTok))
	if err != nil || got.Principal.ID != alice.ID {
		t.Fatalf("GetByID: %+v %v", got, err)
	}
	got.Principal.Groups = nil
	if err := st.UpdateByID(ctx, session.ID(uiTok), got); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.Get(ctx, uiTok); len(again.Principal.Groups) != 0 {
		t.Fatalf("update by ID not stored: %+v", again.Principal)
	}
	// An ID names a ui session only: app sessions and unknown IDs are not found.
	for _, id := range []string{session.ID(appTok), "nope"} {
		if _, err := st.GetByID(ctx, id); !errors.Is(err, session.ErrSessionNotFound) {
			t.Errorf("GetByID(%q): %v", id, err)
		}
	}
	if err := st.DeleteByID(ctx, session.ID(uiTok)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get(ctx, appTok); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("app session outlived its parent: %v", err)
	}
	if err := st.DeleteByID(ctx, session.ID(uiTok)); err != nil {
		t.Fatalf("delete must be idempotent: %v", err)
	}
}
