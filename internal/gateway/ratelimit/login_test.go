package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// fakeLogin is a Login on a clock the test moves.
func fakeLogin() (*Login, *time.Time) {
	now := time.Unix(0, 0)
	l := NewLogin()
	l.now = func() time.Time { return now }
	return l, &now
}

// fail is n wrong passwords at username; it reports how many were admitted.
func fail(l *Login, username string, n int) (admitted int) {
	for range n {
		if settle, _, ok := l.Attempt(username); ok {
			settle(Wrong)
			admitted++
		}
	}
	return admitted
}

// After LoginFailures wrong passwords a username is refused, with the time
// left, until loginWindow has passed since the first of them. Attempts
// refused meanwhile do not keep it shut: nobody is locked out for good.
func TestAUsernameIsRefusedAfterItsFailuresUntilTheWindowEnds(t *testing.T) {
	t.Parallel()
	l, now := fakeLogin()
	if got := fail(l, "alice", LoginFailures-1); got != LoginFailures-1 {
		t.Fatalf("admitted %d of the first %d", got, LoginFailures-1)
	}
	*now = now.Add(5 * time.Minute)
	fail(l, "alice", 1)
	for elapsed := 5 * time.Minute; elapsed < loginWindow; elapsed += time.Minute {
		_, wait, ok := l.Attempt("alice")
		if ok || wait != loginWindow-elapsed {
			t.Fatalf("%v after the first failure: admitted %v, wait %v", elapsed, ok, wait)
		}
		*now = now.Add(time.Minute)
	}
	if _, _, ok := l.Attempt("bob"); !ok {
		t.Fatal("another username was refused")
	}
	settle, _, ok := l.Attempt("alice")
	if !ok {
		t.Fatal("still refused when the window has ended")
	}
	settle(Wrong)
	if got := fail(l, "alice", LoginFailures); got != LoginFailures-1 {
		t.Fatalf("the new window admitted %d more failures", got)
	}
}

// The right password empties the count; an attempt whose password was never
// compared leaves it as it was.
func TestOnlyWrongPasswordsCount(t *testing.T) {
	t.Parallel()
	l, _ := fakeLogin()
	fail(l, "alice", LoginFailures-1)
	for range 3 {
		settle, _, ok := l.Attempt("alice")
		if !ok {
			t.Fatal("refused before the last failure")
		}
		settle(Unchecked)
	}
	settle, _, _ := l.Attempt("alice")
	settle(Right)
	if got := fail(l, "alice", LoginFailures+1); got != LoginFailures {
		t.Fatalf("after a right password %d failures were admitted", got)
	}
}

// Attempts made at once each hold a place until their password is checked,
// so a burst cannot try more passwords than one attempt after another.
func TestAttemptsInFlightCountTowardsTheBudget(t *testing.T) {
	t.Parallel()
	l, _ := fakeLogin()
	var settles []func(Outcome)
	for range LoginFailures + 5 {
		if settle, _, ok := l.Attempt("alice"); ok {
			settles = append(settles, settle)
		}
	}
	if len(settles) != LoginFailures {
		t.Fatalf("%d attempts were being checked at once", len(settles))
	}
	settles[0](Unchecked)
	settles[0](Wrong) // only the first report counts
	if _, _, ok := l.Attempt("alice"); !ok {
		t.Fatal("an unchecked attempt kept its place")
	}
}

// Someone failing at ever new usernames cannot grow the table without limit.
func TestTheFailureTableIsBounded(t *testing.T) {
	t.Parallel()
	l, _ := fakeLogin()
	for i := range 3 * loginSlots {
		fail(l, fmt.Sprintf("user%d", i), 1)
	}
	if len(l.failed) > loginSlots {
		t.Fatalf("%d entries", len(l.failed))
	}
}

// One IPv6 /64 is one address for the per-address limits: a client cannot
// get a fresh budget by changing the low bits.
func TestIPv6AddressesShareTheirSlash64(t *testing.T) {
	t.Parallel()
	l, _ := fakeLogin()
	refused := false
	for i := 1; i <= perIPLimit.Burst+1 && !refused; i++ {
		_, ok := l.Allow(fmt.Sprintf("2001:db8:1:2::%x", i), fmt.Sprintf("user%d", i))
		refused = !ok
	}
	if !refused {
		t.Fatal("addresses of one /64 were limited apart")
	}
	if _, ok := l.Allow("2001:db8:1:3::1", "user1"); !ok {
		t.Fatal("another /64 was limited")
	}
	if _, ok := l.Allow("192.0.2.1", "user1"); !ok {
		t.Fatal("an IPv4 address was limited")
	}
}
