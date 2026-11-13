package netprobe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDialReportsWhichURLsAnswer(t *testing.T) {
	up := httptest.NewServer(Listener())
	defer up.Close()
	down := httptest.NewServer(Listener())
	down.Close()

	got := Dial(context.Background(), up.URL, down.URL, time.Second)
	if !got.Control || got.Target {
		t.Fatalf("control up, target down: got %+v", got)
	}
	got = Dial(context.Background(), up.URL, up.URL, time.Second)
	if !got.Control || !got.Target {
		t.Fatalf("both up: got %+v", got)
	}
}

func TestDialTreatsNon2xxAsUnreachable(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer bad.Close()
	if got := Dial(context.Background(), bad.URL, bad.URL, time.Second); got.Control || got.Target {
		t.Fatalf("502 counted as reachable: %+v", got)
	}
}

// A port that answers is connected, one nothing listens on is refused:
// both mean the address was reached.
func TestConnectTellsHowAnAddressAnswered(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := l.Addr().String()
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	shut := closed.Addr().String()
	closed.Close()
	defer l.Close()

	got := Connect(context.Background(), []string{open, shut}, time.Second)
	if got[open] != Connected || got[shut] != Refused {
		t.Errorf("Connect = %v, want %s connected and %s refused", got, open, shut)
	}
	for _, how := range []string{Connected, Refused} {
		if !Reached(how) {
			t.Errorf("Reached(%q) = false", how)
		}
	}
	for _, how := range []string{NoRoute, TimedOut, "failed: boom", ""} {
		if Reached(how) {
			t.Errorf("Reached(%q) = true", how)
		}
	}
}

// A name of the reserved top-level domain "invalid" never resolves.
func TestResolveLeavesOutNamesThatDoNotResolve(t *testing.T) {
	t.Parallel()
	if got := Resolve(context.Background(), []string{"nothing.invalid"}, 2*time.Second); len(got) != 0 {
		t.Errorf("Resolve = %v, want nothing", got)
	}
}
