package egressproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"
)

// fixture is a running proxy whose names, connections and networks are
// the test's. The test connects from loopback, which is the network of
// the app alice/demo.
type fixture struct {
	t    *testing.T
	p    *Proxy
	addr string
	logs *syncBuffer

	mu sync.Mutex
	// names is what each name resolves to; lookups counts the questions.
	names   map[string][]string
	lookups map[string]int
	// rebind, when set, answers instead of names: the n-th question
	// (from 0) about a name.
	rebind func(name string, n int) []string
	// upstream is where every allowed connection really goes, whatever
	// address it is for; empty refuses them all.
	upstream string
	// dialled are the addresses the proxy connected to.
	dialled []string
	left    chan struct{}
}

// syncBuffer is a log two goroutines may write.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (f *fixture) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.lookups[host]
	f.lookups[host]++
	answer := f.names[host]
	if f.rebind != nil {
		answer = f.rebind(host, n)
	}
	if len(answer) == 0 {
		return nil, errors.New("no such host at 10.0.0.53")
	}
	var out []netip.Addr
	for _, a := range answer {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func (f *fixture) dial(ctx context.Context, addr netip.AddrPort, check func(netip.Addr) error) (net.Conn, error) {
	// As the real dialer does on the socket.
	if err := check(addr.Addr()); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.dialled = append(f.dialled, addr.String())
	upstream := f.upstream
	f.mu.Unlock()
	if upstream == "" {
		return nil, errors.New("connection refused")
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", upstream)
}

func (f *fixture) connections() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialled...)
}

// start runs a proxy with rules; change may adjust it before it serves.
func start(t *testing.T, rules Rules, change func(*Proxy)) *fixture {
	t.Helper()
	f := &fixture{t: t, logs: &syncBuffer{}, names: map[string][]string{}, lookups: map[string]int{}, left: make(chan struct{}, 64)}
	p, err := New(Options{
		Rules:    rules,
		Networks: func(context.Context) (Networks, error) { return testNets, nil },
		Log:      slog.New(slog.NewTextHandler(f.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	p.resolver, p.dialTo = f, f.dial
	p.own = func() []netip.Addr { return testOwn }
	p.left = func() { f.left <- struct{}{} }
	if change != nil {
		change(p)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	f.p, f.addr = p, l.Addr().String()
	return f
}

// echo is a server that sends back what it is sent, as the far end of a
// tunnel.
func echo(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().String()
}

// send writes raw to a new connection to the proxy and reads one answer.
// The connection is returned open, with the reader that holds what came
// after the answer's header.
func (f *fixture) send(raw, method string) (*http.Response, net.Conn, *bufio.Reader) {
	f.t.Helper()
	c, err := net.Dial("tcp", f.addr)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(raw)); err != nil {
		f.t.Fatal(err)
	}
	r := bufio.NewReader(c)
	resp, err := http.ReadResponse(r, &http.Request{Method: method})
	if err != nil {
		f.t.Fatalf("no answer to %q: %v", raw, err)
	}
	return resp, c, r
}

// connect asks for a tunnel to target.
func (f *fixture) connect(target string) (*http.Response, net.Conn, *bufio.Reader) {
	f.t.Helper()
	return f.send(fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target), http.MethodConnect)
}
