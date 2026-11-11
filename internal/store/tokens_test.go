package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func signedIn(t *testing.T, s *store.Store, subject string) model.Person {
	t.Helper()
	p, err := s.People(uidRange).Ensure(ctx, store.Claims{Issuer: "https://idp.example.com", Subject: subject, Username: subject})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func apiToken(id, owner string) store.APIToken {
	return store.APIToken{ID: id, Owner: owner, Name: "laptop", Scope: "read", Hash: "h-" + id, Prefix: "javo_" + id + "_", CreatedAt: t0}
}

func TestTokenRoundTripAndLookupByID(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	alicePerson := signedIn(t, s, "alice")
	exp := t0.Add(time.Hour)
	in := apiToken("t1", alicePerson.Name)
	in.ExpiresAt = exp
	if _, err := s.Tokens().Create(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Tokens().Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != alicePerson.Name || got.Name != "laptop" || got.Scope != "read" || got.Hash != "h-t1" ||
		got.Prefix != "javo_t1_" || !got.CreatedAt.Equal(t0) || !got.ExpiresAt.Equal(exp) || !got.LastUsedAt.IsZero() || got.App != nil {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.Tokens().Get(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(missing) = %v", err)
	}
}

func TestTokenOfUnknownPersonIsRefused(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	if _, err := s.Tokens().Create(ctx, alice, apiToken("t1", "nobody")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Create for an unknown person = %v", err)
	}
}

func TestAppTokenNamesItsApp(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	p := signedIn(t, s, "alice")
	app := createApp(t, s, p.Name, "agent")
	in := apiToken("t1", p.Name)
	in.App = &store.TokenApp{ObjectName: app.ObjectName()}
	if _, err := s.Tokens().Create(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Tokens().Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	want := store.TokenApp{ID: app.ID, Namespace: app.Namespace, ObjectName: app.ObjectName(), Name: "agent"}
	if got.App == nil || *got.App != want {
		t.Fatalf("App = %+v, want %+v", got.App, want)
	}
	missing := apiToken("t2", p.Name)
	missing.App = &store.TokenApp{ObjectName: "no-such-app"}
	if _, err := s.Tokens().Create(ctx, alice, missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Create for a missing app = %v", err)
	}
}

func TestTokenListIsOwnersNewestFirst(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a, b := signedIn(t, s, "alice"), signedIn(t, s, "bob")
	for i, tok := range []store.APIToken{apiToken("t2", a.Name), apiToken("t1", a.Name), apiToken("t3", b.Name)} {
		tok.CreatedAt = t0.Add(time.Duration(i/2) * time.Minute) // t2 and t1 tie: id breaks it
		if _, err := s.Tokens().Create(ctx, alice, tok); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.Tokens().List(ctx, a.Name)
	if err != nil || len(list) != 2 || list[0].ID != "t2" || list[1].ID != "t1" {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

func TestRevokeIsTheOwnersAndAudited(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a, b := signedIn(t, s, "alice"), signedIn(t, s, "bob")
	if _, err := s.Tokens().Create(ctx, alice, apiToken("t1", a.Name)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tokens().Revoke(ctx, alice, b.Name, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Revoke of another person's token = %v", err)
	}
	before := auditCount(t, s)
	tok, err := s.Tokens().Revoke(ctx, model.Actor{Person: a.Name, Token: "other"}, a.Name, "t1")
	if err != nil || tok.ID != "t1" {
		t.Fatalf("Revoke = %+v, %v", tok, err)
	}
	if _, err := s.Tokens().Get(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after revoke = %v", err)
	}
	events, _ := s.Audit().List(ctx, 1)
	if auditCount(t, s) != before+1 || events[0].Action != "token.revoke" || events[0].ObjectID != "t1" || events[0].Via != "token:other" {
		t.Fatalf("audit %+v", events)
	}
}

func TestDeletingAnAppEndsItsTokens(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	p := signedIn(t, s, "alice")
	app := createApp(t, s, p.Name, "agent")
	in := apiToken("t1", p.Name)
	in.App = &store.TokenApp{ObjectName: app.ObjectName()}
	if _, err := s.Tokens().Create(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tokens().Create(ctx, alice, apiToken("t2", p.Name)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Tokens().RevokeForApp(ctx, alice, app.ID, ""); err != nil || n != 1 {
		t.Fatalf("RevokeForApp = %d, %v", n, err)
	}
	if _, err := s.Tokens().Get(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("app token survived: %v", err)
	}
	if _, err := s.Tokens().Get(ctx, "t2"); err != nil {
		t.Fatalf("personal token went with the app: %v", err)
	}
	// Purging the row removes tokens the sweep has not reached yet.
	if _, err := s.Tokens().Create(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Apps().Purge(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tokens().Get(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("token outlived the purged app: %v", err)
	}
}

func TestDeleteForRemovesOnlyThatPersonsTokens(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	a, b := signedIn(t, s, "alice"), signedIn(t, s, "bob")
	for _, tok := range []store.APIToken{apiToken("t1", a.Name), apiToken("t2", a.Name), apiToken("t3", b.Name)} {
		if _, err := s.Tokens().Create(ctx, alice, tok); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Tokens().DeleteFor(ctx, alice, a.Name); err != nil || n != 2 {
		t.Fatalf("DeleteFor = %d, %v", n, err)
	}
	if _, err := s.Tokens().Get(ctx, "t3"); err != nil {
		t.Fatalf("bob's token: %v", err)
	}
	if n, err := s.Tokens().DeleteFor(ctx, alice, "nobody"); err != nil || n != 0 {
		t.Fatalf("DeleteFor unknown = %d, %v", n, err)
	}
}

func TestSweepRemovesExpiredAndTokensOfDeletedApps(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	p := signedIn(t, s, "alice")
	app := createApp(t, s, p.Name, "agent")
	expired := apiToken("t1", p.Name)
	expired.ExpiresAt = t0.Add(time.Minute)
	live := apiToken("t2", p.Name)
	live.ExpiresAt = t0.Add(time.Hour)
	forever := apiToken("t3", p.Name)
	ofApp := apiToken("t4", p.Name)
	ofApp.App = &store.TokenApp{ObjectName: app.ObjectName()}
	for _, tok := range []store.APIToken{expired, live, forever, ofApp} {
		if _, err := s.Tokens().Create(ctx, alice, tok); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Apps().MarkDeleted(ctx, alice, app.ID); err != nil {
		t.Fatal(err)
	}
	n, err := s.Tokens().Sweep(ctx, t0.Add(2*time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	for id, want := range map[string]bool{"t1": false, "t2": true, "t3": true, "t4": false} {
		_, err := s.Tokens().Get(ctx, id)
		if (err == nil) != want {
			t.Fatalf("token %s present=%v, want %v", id, err == nil, want)
		}
	}
}

func TestTouchWritesAtMostOncePerInterval(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	p := signedIn(t, s, "alice")
	if _, err := s.Tokens().Create(ctx, alice, apiToken("t1", p.Name)); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Hour)
	if err := s.Tokens().Touch(ctx, "t1", at, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Tokens().Touch(ctx, "t1", at.Add(30*time.Second), time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Tokens().Get(ctx, "t1")
	if !got.LastUsedAt.Equal(at) {
		t.Fatalf("LastUsedAt = %v, want %v (second touch was within a minute)", got.LastUsedAt, at)
	}
	later := at.Add(time.Minute)
	if err := s.Tokens().Touch(ctx, "t1", later, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Tokens().Get(ctx, "t1")
	if !got.LastUsedAt.Equal(later) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, later)
	}
}
