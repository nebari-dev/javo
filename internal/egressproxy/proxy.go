package egressproxy

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// The ports an app may reach, on any host: HTTPS through CONNECT and
// plain HTTP. RFC 9110 section 9.3.6 asks a proxy to keep CONNECT to a
// short list of known ports, since a tunnel carries any protocol: with
// 443 only, an app cannot use the gateway to send mail, to speak SSH or
// to reach a database or an admin port of a host on the internet. The
// address rules do not depend on this; it is a second fence. There is no
// setting for it.
const (
	connectPort = 443
	httpPort    = 80
)

// Options are what a Proxy is made of.
type Options struct {
	// Rules are the operator's egress settings.
	Rules Rules
	// Networks reads the platform's networks; it is asked once per
	// connection. Required.
	Networks NetworksFunc
	// PerApp is how many connections one app may have open at a time;
	// zero is 128.
	PerApp int
	// Idle is how long a connection may carry nothing before it is
	// closed; zero is five minutes.
	Idle time.Duration
	// Log is where connections are noted; nil is slog's default.
	Log *slog.Logger
}

// Proxy is the forward proxy. It is an http.Handler only for its own
// Serve: a request that did not come through Serve's listener is
// refused, since nothing says which app it is from.
type Proxy struct {
	rules     Rules
	networks  NetworksFunc
	perApp    int
	idle      time.Duration
	log       *slog.Logger
	transport *http.Transport

	// What a test replaces: the resolver, the dialer, the gateway's own
	// addresses, and a call when an app's place is given back.
	resolver resolver
	dialTo   dialFunc
	own      func() []netip.Addr
	left     func()

	mu   sync.Mutex
	open map[string]int // connections open, by app
}

// New makes a Proxy.
func New(o Options) (*Proxy, error) {
	if o.Networks == nil {
		return nil, errors.New("egress proxy: nothing to read the platform's networks with")
	}
	p := &Proxy{
		rules: o.Rules, networks: o.Networks, perApp: o.PerApp, idle: o.Idle, log: o.Log,
		resolver: net.DefaultResolver, dialTo: dialChecked, own: interfaceAddrs,
		open: map[string]int{},
	}
	if p.perApp <= 0 {
		p.perApp = 128
	}
	if p.idle <= 0 {
		p.idle = 5 * time.Minute
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	p.transport = p.newTransport()
	return p, nil
}

// interfaceAddrs are the addresses of this machine's interfaces. When
// they cannot be read there are none, and the other rules still hold:
// inside the gateway container each of them is a loopback address or
// lies in a network of the daemon.
func interfaceAddrs() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if prefix, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, prefix.Addr().Unmap())
		}
	}
	return out
}

type connKey struct{}

// Serve answers the connections of l until ctx ends or l fails. l should
// listen on a port of its own on every address of the gateway container;
// who may use it is decided per connection (clientConn).
func (p *Proxy) Serve(ctx context.Context, l net.Listener) error {
	srv := &http.Server{
		Handler: p,
		// A peer has ten seconds to send its request line and headers,
		// and a kept connection a minute to send the next.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(slog.DiscardHandler, slog.LevelError),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, connKey{}, c)
		},
	}
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	defer stop()
	err := srv.Serve(listener{Listener: l, p: p})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// ServeHTTP answers one request of an app: a CONNECT, or a request for
// an http URL. Anything else is not a request to a proxy.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, ok := r.Context().Value(connKey{}).(*clientConn)
	if !ok {
		http.Error(w, "egress proxy: not a connection of an app", http.StatusForbidden)
		return
	}
	app := appName(c.app)
	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r, app)
	case r.URL.Scheme != "" && r.URL.Host != "":
		p.forward(w, r, app)
	default:
		// A request in origin form ("GET /path") asks the proxy itself
		// for a page. It has none, and the Host header of such a request
		// is never taken for a target.
		p.refuse(w, app, r.Method, target{}, http.StatusBadRequest, "not a request to a proxy")
	}
}

// refuse answers a request that goes nowhere and notes it.
func (p *Proxy) refuse(w http.ResponseWriter, app, method string, t target, status int, why string) {
	attrs := []any{"app", app, "method", method, "status", status, "why", why}
	if t.port != 0 {
		attrs = append(attrs, "host", t.host(), "port", t.port)
	}
	p.log.Warn("egress proxy: request refused", attrs...)
	// The peer is told to start over on a new connection: what it sent
	// after a refused request is not read.
	w.Header().Set("Connection", "close")
	http.Error(w, "egress proxy: "+why, status)
}

// failed answers a request whose target could not be connected to.
func (p *Proxy) failed(w http.ResponseWriter, app, method string, t target, err error) {
	var denied *DeniedError
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &denied):
		p.refuse(w, app, method, t, http.StatusForbidden, denied.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		p.refuse(w, app, method, t, http.StatusGatewayTimeout, "the target did not answer in time")
	case errors.Is(err, errResolve):
		p.refuse(w, app, method, t, http.StatusBadGateway, "the name does not resolve")
	default:
		p.refuse(w, app, method, t, http.StatusBadGateway, "the target cannot be connected to")
	}
}
