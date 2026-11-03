package local_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var actor = model.Actor{Person: "alice"}

func newUsers(t *testing.T) (*local.DBUsers, *store.Store) {
	t.Helper()
	db := storetest.New(t)
	return &local.DBUsers{Accounts: db.LocalAccounts()}, db
}

// usersWith returns users holding the given accounts: name to stored hash
// (the store never reads it, so a test may use any string) and groups.
func usersWith(t *testing.T, accounts map[string]store.LocalAccount) *local.DBUsers {
	t.Helper()
	u, _ := newUsers(t)
	for name, a := range accounts {
		a.Username = name
		if err := u.Accounts.Put(context.Background(), actor, a); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func TestHashPasswordFormatAndRoundTrip(t *testing.T) {
	h, err := local.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %q", h)
	}
	if !local.VerifyPassword(h, "correct horse") {
		t.Fatal("correct password rejected")
	}
	if local.VerifyPassword(h, "wrong") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := local.HashPassword("correct horse")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestVerifyPasswordRejectsMalformedAndOversizedParams(t *testing.T) {
	for _, enc := range []string{
		"", "plain", "$argon2id$v=19$m=19456,t=2,p=1$onlyfour",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2g",
		"$argon2id$v=19$m=4294967295,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2g",
		"$argon2id$v=19$m=19456,t=2,p=1$!!$!!",
	} {
		if local.VerifyPassword(enc, "x") {
			t.Errorf("accepted malformed hash %q", enc)
		}
	}
}

func TestVerify(t *testing.T) {
	h, _ := local.HashPassword("s3cret")
	u := usersWith(t, map[string]store.LocalAccount{"alice": {Hash: h}})
	ctx := context.Background()

	got, err := u.Verify(ctx, "alice", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != local.UserID("alice") || got.Name != "alice" {
		t.Fatalf("unexpected user %+v", got)
	}

	_, errWrong := u.Verify(ctx, "alice", "nope")
	_, errUnknown := u.Verify(ctx, "bob", "nope")
	_, errBadName := u.Verify(ctx, "../x", "nope")
	if !errors.Is(errWrong, local.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", errWrong)
	}
	if errWrong != errUnknown || errWrong != errBadName { //nolint:errorlint // identity is the point
		t.Fatalf("errors differ: %v / %v / %v", errWrong, errUnknown, errBadName)
	}
}

func TestVerifyWithNoAccountsIsInvalidCredentials(t *testing.T) {
	u, _ := newUsers(t)
	if _, err := u.Verify(context.Background(), "alice", "x"); !errors.Is(err, local.ErrInvalidCredentials) {
		t.Fatalf("got %v", err)
	}
}

func TestAdd(t *testing.T) {
	ctx := context.Background()
	u, db := newUsers(t)

	if err := u.Add(ctx, "alice", "pw1", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := u.Add(ctx, "bob", "pw2", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := u.Add(ctx, "alice", "pw3", nil, false); !errors.Is(err, local.ErrUserExists) {
		t.Fatalf("want ErrUserExists, got %v", err)
	}
	hashOf := func(name string) string {
		t.Helper()
		a, err := db.LocalAccounts().Get(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return a.Hash
	}
	if hashOf("alice") != "pw1" || hashOf("bob") != "pw2" {
		t.Fatalf("unexpected hashes %q %q", hashOf("alice"), hashOf("bob"))
	}
	if err := u.Add(ctx, "alice", "pw3", nil, true); err != nil {
		t.Fatal(err)
	}
	if hashOf("alice") != "pw3" || hashOf("bob") != "pw2" {
		t.Fatalf("force did not replace only alice: %q %q", hashOf("alice"), hashOf("bob"))
	}
	if err := u.Add(ctx, "a b", "x", nil, false); err == nil {
		t.Fatal("invalid username accepted")
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	u, db := newUsers(t)
	if err := u.Remove(ctx, "alice"); !errors.Is(err, local.ErrUserNotFound) {
		t.Fatalf("no accounts: want ErrUserNotFound, got %v", err)
	}
	for _, name := range []string{"alice", "bob"} {
		if err := u.Add(ctx, name, "h-"+name, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.Remove(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.LocalAccounts().Get(ctx, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("alice still stored: %v", err)
	}
	if b, err := db.LocalAccounts().Get(ctx, "bob"); err != nil || b.Hash != "h-bob" {
		t.Fatalf("bob: %+v, %v", b, err)
	}
	if err := u.Remove(ctx, "alice"); !errors.Is(err, local.ErrUserNotFound) {
		t.Fatalf("second remove: want ErrUserNotFound, got %v", err)
	}
}

func TestUserIDIsAnIdentityName(t *testing.T) {
	id := local.UserID("alice")
	if errs := validation.IsDNS1123Label(id); len(errs) > 0 || !strings.HasPrefix(id, "alice-") {
		t.Fatalf("UserID = %q (%v), want a DNS label starting alice-", id, errs)
	}
	if id == local.UserID("bob") || id == local.UserID("Alice") {
		t.Fatal("distinct usernames must get distinct IDs")
	}
}

func TestVerifyReturnsStoredGroups(t *testing.T) {
	h, _ := local.HashPassword("s3cret")
	u := usersWith(t, map[string]store.LocalAccount{
		"alice": {Hash: h, Groups: []string{"javo-admins", "/org/data-science"}},
		"bob":   {Hash: h},
	})
	ctx := context.Background()

	got, err := u.Verify(ctx, "alice", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Groups, ",") != "javo-admins,/org/data-science" {
		t.Fatalf("groups = %v", got.Groups)
	}
	if _, err := u.Verify(ctx, "alice", "nope"); !errors.Is(err, local.ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	got, err = u.Verify(ctx, "bob", "s3cret")
	if err != nil || len(got.Groups) != 0 {
		t.Fatalf("no groups: %+v, %v", got, err)
	}
}

func TestAddWithGroupsRoundTrips(t *testing.T) {
	ctx := context.Background()
	u, _ := newUsers(t)
	h, _ := local.HashPassword("s3cret")
	if err := u.Add(ctx, "alice", h, []string{"javo-admins"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := u.Verify(ctx, "alice", "s3cret")
	if err != nil || strings.Join(got.Groups, ",") != "javo-admins" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestLookup(t *testing.T) {
	h, _ := local.HashPassword("s3cret")
	u := usersWith(t, map[string]store.LocalAccount{"alice": {Hash: h, Groups: []string{"javo-admins"}}})
	ctx := context.Background()
	got, err := u.Lookup(ctx, "alice")
	if err != nil || got.ID != local.UserID("alice") || strings.Join(got.Groups, ",") != "javo-admins" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := u.Lookup(ctx, "bob"); !errors.Is(err, local.ErrUserNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}
