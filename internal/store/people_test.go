package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func claims(n int) store.Claims {
	return store.Claims{
		Issuer: "https://idp.example.com", Subject: fmt.Sprintf("sub-%d", n),
		Username: fmt.Sprintf("user%d", n), Email: fmt.Sprintf("user%d@example.com", n),
		Groups: []string{"data"},
	}
}

func TestEnsureAllocatesUniqueUIDs(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	const n = 50
	uids := make([]int64, n)
	names := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := people.Ensure(ctx, claims(i))
			uids[i], names[i], errs[i] = p.UID, p.Name, err
		}()
	}
	wg.Wait()
	seenUID, seenName := map[int64]bool{}, map[string]bool{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("login %d: %v", i, errs[i])
		}
		if uids[i] < uidRange.First || uids[i] > uidRange.Last {
			t.Errorf("uid %d outside %v", uids[i], uidRange)
		}
		if seenUID[uids[i]] || seenName[names[i]] {
			t.Errorf("duplicate uid %d or name %s", uids[i], names[i])
		}
		seenUID[uids[i]], seenName[names[i]] = true, true
	}
}

func TestEnsureSamePersonConcurrentlyCreatesOne(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	const n = 10
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := people.Ensure(ctx, claims(1))
			if err != nil {
				t.Errorf("login %d: %v", i, err)
			}
			got[i] = p.Name
		}()
	}
	wg.Wait()
	for _, name := range got {
		if name != got[0] {
			t.Fatalf("names = %v, want all equal", got)
		}
	}
	found, err := people.Search(ctx, "user1", 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("people named user1 = %d, err %v", len(found), err)
	}
}

func TestEnsureKeepsNameAndUIDAndRefreshesLogin(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	first, err := people.Ensure(ctx, claims(1))
	if err != nil {
		t.Fatal(err)
	}
	c := claims(1)
	c.Username, c.Groups = "renamed", []string{"ops", "data"}
	second, err := people.Ensure(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != first.Name || second.UID != first.UID {
		t.Errorf("name/uid changed: %s/%d -> %s/%d", first.Name, first.UID, second.Name, second.UID)
	}
	if second.Username != "renamed" || len(second.Groups) != 2 || !second.LastLoginAt.After(first.LastLoginAt.Add(-1)) {
		t.Errorf("login not recorded: %+v", second)
	}
	byName, err := people.ByName(ctx, first.Name)
	if err != nil || byName.Username != "renamed" || byName.Groups[0] != "ops" {
		t.Errorf("ByName = %+v, %v", byName, err)
	}
	if _, err := people.ByName(ctx, "nobody"); err != store.ErrNotFound {
		t.Errorf("ByName(nobody) err = %v, want ErrNotFound", err)
	}
}

func TestEnsureWithoutRangeAllocatesNoUID(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	p, err := s.People(store.IDRange{}).Ensure(ctx, claims(1))
	if err != nil || p.UID != 0 {
		t.Fatalf("person = %+v, err %v; want uid 0", p, err)
	}
}

func TestEnsureFailsWhenRangeIsUsedUp(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(store.IDRange{First: 500, Last: 501})
	for i := range 2 {
		if _, err := people.Ensure(ctx, claims(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := people.Ensure(ctx, claims(2)); err == nil {
		t.Fatal("third person got a uid outside the range")
	}
	if found, _ := people.Search(ctx, "user2", 10); len(found) != 0 {
		t.Fatalf("failed login left a person behind: %+v", found)
	}
}

func TestSearchMatchesUsernameOrEmailPrefixIgnoringCase(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	for _, c := range []store.Claims{
		{Issuer: "i", Subject: "1", Username: "Alice", Email: "a@example.com"},
		{Issuer: "i", Subject: "2", Username: "bob", Email: "ALINA@example.com"},
		{Issuer: "i", Subject: "3", Username: "carol", Email: "c@example.com"},
	} {
		if _, err := people.Ensure(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	found, err := people.Search(ctx, "AL", 10)
	if err != nil || len(found) != 2 {
		t.Fatalf("Search(AL) = %d people, err %v; want alice and bob", len(found), err)
	}
	if one, _ := people.Search(ctx, "al", 1); len(one) != 1 {
		t.Errorf("limit 1 returned %d", len(one))
	}
	if short, _ := people.Search(ctx, "a", 10); len(short) != 0 {
		t.Errorf("a one-letter prefix listed %d people", len(short))
	}
}

func TestEnsureGroupAllocatesStableUniqueGIDs(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	r := store.IDRange{First: 200000, Last: 299999}
	groups := s.Groups(r)
	gids := map[int64]string{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("group-%d", i%10)
			gid, err := groups.Ensure(ctx, alice, name)
			if err != nil {
				t.Errorf("ensure %s: %v", name, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if prev, ok := gids[gid]; ok && prev != name {
				t.Errorf("gid %d given to %s and %s", gid, prev, name)
			}
			gids[gid] = name
		}()
	}
	wg.Wait()
	if len(gids) != 10 {
		t.Fatalf("10 groups got %d gids", len(gids))
	}
	again, err := groups.Ensure(ctx, alice, "group-3")
	if err != nil || gids[again] != "group-3" {
		t.Fatalf("second Ensure of group-3 = %d, %v", again, err)
	}
	// One audit row per gid handed out; finding an existing group writes none.
	if n := auditCount(t, s); n != 10 {
		t.Errorf("10 gid allocations left %d audit rows", n)
	}
}

// Rows written by another path (an import) may already hold uids in range:
// the next first login must skip them instead of failing for good.
func TestEnsureSkipsUIDsAlreadyInUse(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	for i, uid := range []int64{uidRange.First, uidRange.First + 1} {
		_, err := s.DB().ExecContext(ctx, `INSERT INTO people (id, name, issuer, subject, username, email, display_name,
			username_lower, email_lower, uid, groups, last_login_at, created_at)
			VALUES ($1, $2, 'https://idp.example.com', $3, 'x', 'x@example.com', '', 'x', 'x@example.com', $4, '[]', 0, 0)`,
			fmt.Sprintf("imp-%d", i), fmt.Sprintf("imported-%d", i), fmt.Sprintf("imp-sub-%d", i), uid)
		if err != nil {
			t.Fatal(err)
		}
	}
	people := s.People(uidRange)
	for i := range 2 {
		p, err := people.Ensure(ctx, claims(i))
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
		if want := uidRange.First + 2 + int64(i); p.UID != want {
			t.Fatalf("login %d uid = %d, want %d", i, p.UID, want)
		}
	}
}

func TestGroupsEnsureSkipsGIDsAlreadyInUse(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	gids := store.IDRange{First: 200000, Last: 200100}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO groups (name, gid) VALUES ('imported', $1)`, gids.First); err != nil {
		t.Fatal(err)
	}
	gid, err := s.Groups(gids).Ensure(ctx, alice, "data")
	if err != nil || gid != gids.First+1 {
		t.Fatalf("Ensure = %d, %v; want %d", gid, err, gids.First+1)
	}
}

func TestEnsureSeparatesIssuers(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	a, err := people.Ensure(ctx, store.Claims{Issuer: "https://kc.example.com", Subject: "s1", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := people.Ensure(ctx, store.Claims{Issuer: "https://dex.example.com", Subject: "s1", Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Name == b.Name {
		t.Fatalf("one username from two issuers must be two people: %q %q", a.Name, b.Name)
	}
}

func TestEnsureTruncatesTheGroupsSnapshot(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := claims(1)
	for i := range 205 {
		c.Groups = append(c.Groups, fmt.Sprintf("g%d", i))
	}
	got, err := s.People(uidRange).Ensure(ctx, c)
	if err != nil {
		t.Fatalf("login must not fail on many groups: %v", err)
	}
	if len(got.Groups) != 200 {
		t.Fatalf("stored %d groups, want 200", len(got.Groups))
	}
}

func TestByIDFindsThePersonTheChangeNamed(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	p, err := people.Ensure(ctx, claims(1))
	if err != nil {
		t.Fatal(err)
	}
	got, err := people.ByID(ctx, p.ID)
	if err != nil || got.Name != p.Name || got.UID != p.UID {
		t.Fatalf("ByID = %+v, %v; want %+v", got, err, p)
	}
	if _, err := people.ByID(ctx, "no-such-id"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ByID of a missing person = %v", err)
	}
}

func TestIDsListsEveryPerson(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	want := map[string]bool{}
	for i := range 3 {
		p, err := people.Ensure(ctx, claims(i))
		if err != nil {
			t.Fatal(err)
		}
		want[p.ID] = true
	}
	got, err := people.IDs(ctx)
	if err != nil || len(got) != len(want) {
		t.Fatalf("IDs = %v, %v; want %d ids", got, err, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unknown id %q in %v", id, got)
		}
	}
}

// ByLogin finds the people whose username or email is the value, ignoring
// case, whatever its length; BySubject finds one by provider account.
func TestByLoginAndBySubject(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	a, err := people.Ensure(ctx, store.Claims{Issuer: "https://idp.example.com", Subject: "s-a", Username: "Alice", Email: "alice@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := people.Ensure(ctx, store.Claims{Issuer: "https://idp.example.com", Subject: "s-b", Username: "alicebob", Email: "b@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, login := range []string{"alice", "ALICE@example.com"} {
		got, err := people.ByLogin(ctx, login)
		if err != nil || len(got) != 1 || got[0].Name != a.Name {
			t.Errorf("ByLogin(%q) = %+v, %v", login, got, err)
		}
	}
	if got, err := people.ByLogin(ctx, "b"); err != nil || len(got) != 0 {
		t.Errorf("ByLogin(b) = %+v, %v", got, err)
	}
	if got, err := people.BySubject(ctx, "https://idp.example.com", "s-a"); err != nil || got.Name != a.Name {
		t.Errorf("BySubject = %+v, %v", got, err)
	}
	if _, err := people.BySubject(ctx, "https://idp.example.com", "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("BySubject(nobody) err = %v", err)
	}
}

// The provider's roles are kept with the person like the groups: set at
// first sign-in, replaced at each later one, and capped so a long list
// cannot fail a sign-in. A scheduled run reads this snapshot.
func TestEnsureKeepsTheProviderRolesSnapshot(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	people := s.People(uidRange)
	c := claims(1)
	c.ProviderRoles = []string{"gpu-users"}
	first, err := people.Ensure(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ProviderRoles) != 1 || first.ProviderRoles[0] != "gpu-users" {
		t.Fatalf("first sign-in: %v", first.ProviderRoles)
	}
	c.ProviderRoles = nil
	for i := range 205 {
		c.ProviderRoles = append(c.ProviderRoles, fmt.Sprintf("r%d", i))
	}
	if _, err := people.Ensure(ctx, c); err != nil {
		t.Fatalf("login must not fail on many roles: %v", err)
	}
	got, err := people.ByName(ctx, first.Name)
	if err != nil || len(got.ProviderRoles) != 200 || got.ProviderRoles[0] != "r0" {
		t.Fatalf("after the second sign-in: %d roles, %v", len(got.ProviderRoles), err)
	}
}
