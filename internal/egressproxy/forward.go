package egressproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// via is this proxy's entry in the Via field. RFC 9110 section 7.6.3: a
// proxy MUST send Via in each message it forwards, and may name itself
// by a pseudonym. This one says nothing about the app, the gateway or
// the product.
const via = "1.1 proxy"

// hopByHop are the fields that are for the next hop only and are never
// passed on (RFC 9110 section 7.6.1 lists Proxy-Connection, Keep-Alive,
// TE, Transfer-Encoding and Upgrade). Proxy-Authorization and
// Proxy-Authenticate are between a client and its proxy (sections 11.7.1
// and 11.7.2): this proxy asks for no login, and what an app sends it as
// one goes no further. Trailer goes because trailers are not passed on.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Te", "Transfer-Encoding", "Upgrade",
	"Proxy-Authorization", "Proxy-Authenticate", "Trailer",
}

// endToEnd is h without the fields of this hop. RFC 9110 section 7.6.1:
// an intermediary MUST remove every field named in Connection, then
// Connection itself, and the fields known to be for one hop whether
// Connection names them or not.
func endToEnd(h http.Header) http.Header {
	out := h.Clone()
	for _, value := range h.Values("Connection") {
		for _, option := range strings.Split(value, ",") {
			if name := textproto.TrimString(option); name != "" {
				out.Del(name)
			}
		}
	}
	for _, name := range hopByHop {
		out.Del(name)
	}
	return out
}

type dialledKey struct{}

// newTransport sends the requests of plain HTTP. It connects through
// dial and nowhere else: it reads no proxy from the gateway's own
// environment, keeps no cookies, follows no redirect (a transport never
// does) and asks for no compression of its own. Each request gets a
// connection of its own, closed after it: a connection kept for the next
// request could carry what one app proved to a server (a login tied to
// the connection) into another app's request. No connection is kept
// (MaxIdleConnsPerHost below zero) rather than keep-alive turned off,
// which would have the transport add "Connection: close" to the request:
// an origin that answers in kind has its whole Connection field dropped
// by Go's reader before endToEnd can see which fields it names.
func (p *Proxy) newTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			t, err := parseTarget(address)
			if err != nil {
				return nil, err
			}
			conn, addr, err := p.dial(ctx, t)
			if err != nil {
				return nil, err
			}
			if to, ok := ctx.Value(dialledKey{}).(*netip.Addr); ok {
				*to = addr
			}
			return newIdleConn(conn, p.idle), nil
		},
		MaxIdleConnsPerHost:    -1,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	}
}

// forward answers a request for an http URL: it sends the request to the
// URL's host and the answer back.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, app string) {
	if r.URL.Scheme != "http" {
		p.refuse(w, app, r.Method, target{}, http.StatusBadRequest, "only http URLs are fetched; https goes through CONNECT")
		return
	}
	// The target is the URL of the request line. The Host header is not
	// looked at: a request whose two disagree goes where its URL says,
	// and is sent on with that host (RFC 9112 section 3.2.2).
	hostport := r.URL.Host
	if r.URL.Port() == "" {
		hostport = net.JoinHostPort(r.URL.Hostname(), strconv.Itoa(httpPort))
	}
	t, err := parseTarget(hostport)
	if err != nil {
		p.refuse(w, app, r.Method, target{}, http.StatusBadRequest, "the URL has no usable host")
		return
	}
	if t.port != httpPort {
		p.refuse(w, app, r.Method, t, http.StatusForbidden, "plain HTTP is for port 80 only")
		return
	}
	header := endToEnd(r.Header)
	switch r.Method {
	case http.MethodTrace:
		// TRACE has the far side send the request's headers back, which
		// is of use only to look into the proxies on the way.
		p.refuse(w, app, r.Method, t, http.StatusMethodNotAllowed, "TRACE is not forwarded")
		return
	case http.MethodOptions:
		// RFC 9110 section 7.6.2: an OPTIONS with Max-Forwards of 0 MUST
		// NOT be forwarded and is answered here; a higher one is sent
		// on lowered by one.
		if raw := r.Header.Get("Max-Forwards"); raw != "" {
			left, err := strconv.ParseUint(raw, 10, 31)
			if err != nil {
				p.refuse(w, app, r.Method, t, http.StatusBadRequest, "Max-Forwards is not a number")
				return
			}
			if left == 0 {
				w.Header().Set("Allow", "CONNECT, GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			header.Set("Max-Forwards", strconv.FormatUint(left-1, 10))
		}
	}
	header.Add("Via", via)
	if _, ok := header["User-Agent"]; !ok {
		// Without one the transport would name itself.
		header["User-Agent"] = nil
	}

	// The request is built anew from its parsed parts and written by the
	// transport, with one framing of its body. Nothing of the bytes the
	// app sent is passed through as it came, so a request that two
	// parsers would split differently reaches the server as the single
	// request this one read. The user and password of a URL are for the
	// app's own client to turn into a header; they are not sent on.
	var addr netip.Addr
	ctx := context.WithValue(r.Context(), dialledKey{}, &addr)
	out := (&http.Request{
		Method: r.Method,
		URL:    &url.URL{Scheme: "http", Host: r.URL.Host, Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery, ForceQuery: r.URL.ForceQuery},
		Header: header, ContentLength: r.ContentLength,
	}).WithContext(ctx)
	rc := http.NewResponseController(w)
	alive := func() {
		until := time.Now().Add(p.idle)
		_ = rc.SetReadDeadline(until)
		_ = rc.SetWriteDeadline(until)
	}
	// The server sets its own deadline for the next request's header but
	// none for writing: ours must not outlive this request.
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	if r.ContentLength != 0 {
		out.Body = &touched{ReadCloser: r.Body, touch: alive}
	}
	alive()
	resp, err := p.transport.RoundTrip(out)
	if err != nil {
		p.failed(w, app, r.Method, t, err)
		return
	}
	defer resp.Body.Close()
	p.log.Info("egress proxy: request", "app", app, "method", r.Method, "host", t.host(), "port", t.port, "addr", addr.String(), "status", resp.StatusCode)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		// Upgrade is never sent on, so no server has been asked to
		// change protocols.
		p.refuse(w, app, r.Method, t, http.StatusBadGateway, "the target changed protocols unasked")
		return
	}
	answer := endToEnd(resp.Header)
	answer.Add("Via", via)
	for name, values := range answer {
		w.Header()[name] = values
	}
	w.WriteHeader(resp.StatusCode)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			alive()
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			if err != io.EOF {
				// The answer was cut short: so is ours.
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}

// touched is a request's body that marks the connection alive on each
// read.
type touched struct {
	io.ReadCloser
	touch func()
}

func (t *touched) Read(b []byte) (int, error) {
	n, err := t.ReadCloser.Read(b)
	if n > 0 {
		t.touch()
	}
	return n, err
}
