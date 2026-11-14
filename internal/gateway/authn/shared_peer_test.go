package authn_test

import (
	"fmt"
	"net/http"
	"net/netip"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Behind an ingress with no trustedProxies every sign-in comes from the
// ingress's in-cluster address, so a bucket for that address would be one
// bucket for everybody: one client spending it locks everyone out. Such an
// address is not used as a client key; per-username delays still slow
// guesses at any one account.
func TestOneClientBehindAnUntrustedProxyCannotLockEveryoneOut(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	for i := range 30 {
		if w := f.LoginFrom("10.42.0.9", fmt.Sprintf("guess%d", i), "wrong"); w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d from the shared address was refused, so everyone behind it would be", i)
		}
	}
	if w := f.LoginFrom("10.42.0.9", "alice", "alice-password"); w.Code != http.StatusOK {
		t.Fatalf("alice through the same ingress: %d %s", w.Code, w.Body)
	}
}

// With the proxy trusted, the per-address limit applies to the real client.
func TestATrustedProxysClientsHaveTheirOwnBuckets(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{Trusted: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16")}})
	limited := false
	for i := range 30 {
		limited = limited || f.LoginVia("10.42.0.9", "198.51.100.7", fmt.Sprintf("guess%d", i), "wrong").Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("one client's flood was never limited")
	}
	if w := f.LoginVia("10.42.0.9", "198.51.100.8", "alice", "alice-password"); w.Code != http.StatusOK {
		t.Fatalf("another client: %d", w.Code)
	}
}

// Wrong passwords at one account spend that account's budget wherever they
// come from, the shared address of an untrusted ingress included: once it is
// spent, even the right password is refused until the window moves, and
// other accounts sign in as before.
func TestWrongPasswordsAtOneAccountAreRefusedAfterTheBudget(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	for i := range ratelimit.LoginFailures {
		// Each from another client: no address has to repeat.
		if w := f.LoginFrom(fmt.Sprintf("10.42.0.%d", i+1), "Alice", "wrong"); w.Code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d %s", i, w.Code, w.Body)
		}
	}
	for _, pw := range []string{"wrong", "alice-password"} {
		w := f.LoginFrom("10.42.0.200", "alice", pw)
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" ||
			apitest.Decode[apitest.Problem](t, w).Title != reasons.Msg(reasons.MsgTooManyWrongLogins, 15) {
			t.Fatalf("%q past the budget: %d %s, Retry-After %q", pw, w.Code, w.Body, w.Header().Get("Retry-After"))
		}
	}
	if w := f.LoginFrom("10.42.0.200", "carol", "wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("another account: %d %s", w.Code, w.Body)
	}
}
