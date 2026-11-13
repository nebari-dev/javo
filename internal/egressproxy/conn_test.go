package egressproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/workload"
)

// hungUp reports whether the proxy closes a new connection without an
// answer, whatever is sent on it.
func hungUp(t *testing.T, addr string) bool {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	got, err := io.ReadAll(c)
	return len(got) == 0 && (err == nil || !errors.Is(err, context.DeadlineExceeded))
}

// Only an app may use the proxy: a peer whose address is on no
// workload's network, the gateway itself, and anyone while the networks
// cannot be read are hung up on without an answer.
func TestOnlyAPeerOnAWorkloadsNetworkIsServed(t *testing.T) {
	t.Parallel()
	elsewhere := Networks{Apps: []AppNetwork{{Range: netip.MustParsePrefix("10.213.0.16/28"), App: workload.Ref{Namespace: "alice", Name: "demo"}}}}
	for name, change := range map[string]func(*Proxy){
		"a peer on no workload's network": func(p *Proxy) {
			p.networks = func(context.Context) (Networks, error) { return elsewhere, nil }
		},
		"the gateway itself": func(p *Proxy) {
			p.own = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }
		},
		"networks that cannot be read": func(p *Proxy) {
			p.networks = func(context.Context) (Networks, error) { return Networks{}, errors.New("the daemon is away") }
		},
	} {
		f := start(t, Rules{}, change)
		f.names["example.com"] = []string{"203.0.113.10"}
		f.upstream = echo(t)
		if !hungUp(t, f.addr) {
			t.Errorf("%s: the proxy answered", name)
		}
		if got := f.connections(); len(got) != 0 {
			t.Errorf("%s: connected to %v", name, got)
		}
		if !strings.Contains(f.logs.String(), "connection refused") {
			t.Errorf("%s: the refusal is not in the log: %s", name, f.logs.String())
		}
	}
}

// One app holds so many connections and no more: the next is hung up on
// until one of them ends.
func TestAnAppHasALimitOfConnections(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, func(p *Proxy) { p.perApp = 2 })
	f.names["example.com"] = []string{"203.0.113.10"}
	f.upstream = echo(t)
	_, first, _ := f.connect("example.com:443")
	if resp, _, _ := f.connect("example.com:443"); resp.StatusCode != http.StatusOK {
		t.Fatalf("second tunnel: status %d", resp.StatusCode)
	}
	if !hungUp(t, f.addr) {
		t.Fatal("a third connection was served")
	}
	first.Close()
	<-f.left
	if resp, _, _ := f.connect("example.com:443"); resp.StatusCode != http.StatusOK {
		t.Fatalf("after one ended: status %d", resp.StatusCode)
	}
}

// A tunnel that carries nothing for the idle time is closed, and one
// that carries something is not.
func TestAnIdleTunnelIsClosed(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, func(p *Proxy) { p.idle = 300 * time.Millisecond })
	f.names["example.com"] = []string{"203.0.113.10"}
	f.upstream = echo(t)
	_, c, r := f.connect("example.com:443")
	started := time.Now()
	// Busy for longer than the idle time: each echo keeps it open.
	for time.Since(started) < 600*time.Millisecond {
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatalf("the busy tunnel was closed: %v", err)
		}
		if _, err := r.ReadByte(); err != nil {
			t.Fatalf("the busy tunnel was closed: %v", err)
		}
	}
	// Then silent: the proxy closes it.
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("read from the idle tunnel: %v, want its end", err)
	}
	<-f.left
}

// A proxy that cannot tell an app from anything else is not made.
func TestNewNeedsTheNetworks(t *testing.T) {
	t.Parallel()
	if _, err := New(Options{}); err == nil {
		t.Fatal("a proxy without a way to read the networks was made")
	}
}

// A request that did not come in through Serve has no app: refused.
func TestARequestOutsideServeIsRefused(t *testing.T) {
	t.Parallel()
	p, err := New(Options{Networks: func(context.Context) (Networks, error) { return testNets, nil }})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: p}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	resp, err := http.Get("http://" + l.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status %d, want 403", resp.StatusCode)
	}
}
