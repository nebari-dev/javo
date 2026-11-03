package ratelimit

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeLogin is a Login on a clock the test moves; waiting moves it too.
func fakeLogin() (*Login, *time.Time) {
	now := time.Unix(0, 0)
	l := NewLogin()
	l.now = func() time.Time { return now }
	l.sleep = func(_ context.Context, d time.Duration) error {
		now = now.Add(d)
		return nil
	}
	return l, &now
}

// attempt is one sign-in: the per-address check, the wait, then the outcome.
func attempt(l *Login, ip, username string, right bool) (time.Duration, bool) {
	if _, ok := l.Allow(ip, username); !ok {
		return 0, false
	}
	wait := l.Delay(username)
	if right {
		l.Succeeded(username)
	} else {
		l.Failed(username)
	}
	return wait, true
}

// Eight addresses guessing alice's password every second, for ten minutes,
// do not keep alice out: from her own address she is never refused, and
// waits at most maxDelay.
func TestGuessesFromManyAddressesDoNotLockTheUserOut(t *testing.T) {
	t.Parallel()
	l, now := fakeLogin()
	start := *now
	for sec := 0; sec < 600; sec++ {
		*now = start.Add(time.Duration(sec) * time.Second)
		for i := 0; i < 8; i++ {
			attempt(l, fmt.Sprintf("203.0.113.%d", i), "alice", false)
		}
		if sec%30 == 0 && sec > 60 {
			wait, ok := attempt(l, "198.51.100.7", "alice", true)
			if !ok {
				t.Fatalf("alice refused at %ds", sec)
			}
			if wait > maxDelay {
				t.Fatalf("alice waited %v at %ds", wait, sec)
			}
		}
	}
}

// Failures at one username make every next attempt at it wait longer,
// doubling up to maxDelay; a success, or a quiet spell, ends the wait.
func TestFailuresAtAUsernameSlowItsNextAttempts(t *testing.T) {
	t.Parallel()
	l, now := fakeLogin()
	for i := 0; i < freeFailures; i++ {
		if d := l.Delay("alice"); d != 0 {
			t.Fatalf("failure %d: delay %v", i, d)
		}
		l.Failed("alice")
	}
	var last time.Duration
	for i := 0; i < 20; i++ {
		d := l.Delay("alice")
		if d < last || d > maxDelay || d == 0 {
			t.Fatalf("failure %d: delay %v after %v", freeFailures+i, d, last)
		}
		last = d
		l.Failed("alice")
	}
	if last != maxDelay {
		t.Fatalf("delay stops at %v", last)
	}
	if d := l.Delay("bob"); d != 0 {
		t.Fatalf("another username waits %v", d)
	}
	l.Succeeded("alice")
	if d := l.Delay("alice"); d != 0 {
		t.Fatalf("after a success: %v", d)
	}
	for i := 0; i < freeFailures+3; i++ {
		l.Failed("alice")
	}
	*now = now.Add(limiterIdle)
	if d := l.Delay("alice"); d != 0 {
		t.Fatalf("after a quiet spell: %v", d)
	}
}

// Wait sleeps for the username's delay, and stops when the request ends.
func TestWaitSleepsForTheDelay(t *testing.T) {
	t.Parallel()
	l, now := fakeLogin()
	for i := 0; i < freeFailures+1; i++ {
		l.Failed("alice")
	}
	before := *now
	if err := l.Wait(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if got := now.Sub(before); got != firstDelay*2 {
		t.Fatalf("slept %v", got)
	}
	real := NewLogin()
	for i := 0; i < freeFailures+1; i++ {
		real.Failed("alice")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := real.Wait(ctx, "alice"); err == nil {
		t.Fatal("a cancelled request still waited")
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
