package egressproxy

import (
	"context"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/workload"
)

// admitTimeout is how long finding a connection's app may take.
const admitTimeout = 5 * time.Second

// listener hands out the connections of a listener as clientConns.
type listener struct {
	net.Listener
	p *Proxy
}

func (l listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &clientConn{Conn: c, p: l.p}, nil
}

// clientConn is a connection to the proxy. Nothing is read from it until
// its peer is known to be an app with room for one more connection
// (admit), which is found out on the first read, so that a slow answer
// about one peer never keeps the others from being accepted.
type clientConn struct {
	net.Conn
	p *Proxy

	admitOnce sync.Once
	app       workload.Ref
	refused   error
	closeOnce sync.Once
}

func (c *clientConn) Read(b []byte) (int, error) {
	c.admitOnce.Do(c.admit)
	if c.refused != nil {
		return 0, c.refused
	}
	return c.Conn.Read(b)
}

// Close also gives the app's place back.
func (c *clientConn) Close() error {
	c.closeOnce.Do(func() {
		c.admitOnce.Do(func() { c.refused = net.ErrClosed })
		if c.refused == nil {
			c.p.leave(c.app)
		}
	})
	return c.Conn.Close()
}

// admit decides whether the peer may use the proxy: its address must lie
// in a workload's network and not be the gateway's own, and its app must
// have a connection to spare. Whatever cannot be told is a refusal.
func (c *clientConn) admit() {
	refuse := func(why string, attrs ...any) {
		c.p.log.Warn("egress proxy: connection refused", append([]any{"why", why, "peer", c.RemoteAddr().String()}, attrs...)...)
		// The end of the stream is the one error the server answers
		// with nothing: the peer is hung up on, not talked to.
		c.refused = io.EOF
		_ = c.Conn.Close()
	}
	peer, err := netip.ParseAddrPort(c.RemoteAddr().String())
	if err != nil {
		refuse("the peer has no address")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), admitTimeout)
	defer cancel()
	nets, err := c.p.networks(ctx)
	if err != nil {
		refuse("the platform's networks cannot be read", "err", err)
		return
	}
	addr := peer.Addr().Unmap()
	app, ok := nets.app(addr)
	if !ok {
		refuse("the peer is not on a workload's network")
		return
	}
	for _, own := range c.p.own() {
		if own == addr {
			refuse("the peer is the gateway itself")
			return
		}
	}
	if !c.p.enter(app) {
		refuse("the app has too many connections open", "app", appName(app))
		return
	}
	c.app = app
}

// appName is how the log names an app.
func appName(ref workload.Ref) string { return ref.Namespace + "/" + ref.Name }

// enter takes one of app's places, and reports whether there was one.
func (p *Proxy) enter(app workload.Ref) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := appName(app)
	if p.open[key] >= p.perApp {
		return false
	}
	p.open[key]++
	return true
}

// leave gives a place of app back.
func (p *Proxy) leave(app workload.Ref) {
	p.mu.Lock()
	key := appName(app)
	if p.open[key]--; p.open[key] <= 0 {
		delete(p.open, key)
	}
	p.mu.Unlock()
	if p.left != nil {
		p.left()
	}
}

// idleConn is a connection to the outside that ends when nothing has
// been read from it or written to it for the idle time. A read and a
// write each give both directions that long again, so a long download is
// not cut off for want of an upload.
type idleConn struct {
	net.Conn
	idle time.Duration
}

func newIdleConn(c net.Conn, idle time.Duration) *idleConn {
	ic := &idleConn{Conn: c, idle: idle}
	ic.touch()
	return ic
}

func (c *idleConn) touch() { _ = c.Conn.SetDeadline(time.Now().Add(c.idle)) }

func (c *idleConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *idleConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}
