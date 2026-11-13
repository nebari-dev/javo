package egressproxy

import (
	"io"
	"net"
	"net/http"
	"time"
)

// connect answers a CONNECT: it connects to the target and then carries
// bytes both ways, without reading them (RFC 9110 section 9.3.6).
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, app string) {
	// The target of a CONNECT is a host and a port and nothing else, and
	// a request with an empty or invalid port MUST be rejected. Go also
	// reads "CONNECT /path", which is not a tunnel: the request line as
	// it was sent is what is parsed.
	t, err := parseTarget(r.RequestURI)
	if err != nil {
		p.refuse(w, app, r.Method, target{}, http.StatusBadRequest, "the target of CONNECT must be host:port")
		return
	}
	// "A CONNECT request message does not have content." One that says
	// it has is refused, so that no proxy in front and this one can
	// disagree about where the tunnel's bytes begin.
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		p.refuse(w, app, r.Method, t, http.StatusBadRequest, "a CONNECT request has no content")
		return
	}
	if t.port != connectPort {
		p.refuse(w, app, r.Method, t, http.StatusForbidden, "CONNECT is for port 443 only")
		return
	}
	upstream, addr, err := p.dial(r.Context(), t)
	if err != nil {
		p.failed(w, app, r.Method, t, err)
		return
	}
	defer upstream.Close()
	client, buffered, err := http.NewResponseController(w).Hijack()
	if err != nil {
		p.refuse(w, app, r.Method, t, http.StatusInternalServerError, "the connection cannot be taken over")
		return
	}
	defer client.Close()
	p.log.Info("egress proxy: tunnel", "app", app, "host", t.host(), "port", t.port, "addr", addr.String())

	// From here on the two connections live and die together: either
	// carries something every idle time, or both are closed.
	touch := func() {
		until := time.Now().Add(p.idle)
		_ = client.SetDeadline(until)
		_ = upstream.SetDeadline(until)
	}
	touch()
	// A 2xx answer to CONNECT MUST NOT carry Content-Length or
	// Transfer-Encoding: the tunnel starts right after the header.
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// "A tunnel is closed when a tunnel intermediary detects that either
	// side has closed its connection: the intermediary MUST attempt to
	// send any outstanding data that came from the closed side to the
	// other side, close both connections, and then discard any remaining
	// data left undelivered." Each copy ends when its source does, after
	// writing all it read; the first to end closes both.
	done := make(chan struct{}, 2)
	// What the client sent right behind its request is already in the
	// server's buffer: the tunnel's first bytes are read from there.
	go pipe(upstream, buffered.Reader, touch, done)
	go pipe(client, upstream, touch, done)
	<-done
}

// pipe copies src to dst until src ends or either fails.
func pipe(dst net.Conn, src io.Reader, touch func(), done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			touch()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
