package egressproxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// origin is a web server that records the requests it gets.
func origin(t *testing.T, answer http.HandlerFunc) (addr string, seen func() []*http.Request) {
	t.Helper()
	var got []*http.Request
	lock := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		kept := r.Clone(r.Context())
		kept.Body = io.NopCloser(strings.NewReader(string(body)))
		lock <- struct{}{}
		got = append(got, kept)
		<-lock
		if answer != nil {
			answer(w, r)
			return
		}
		_, _ = io.WriteString(w, "from the origin")
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), func() []*http.Request {
		lock <- struct{}{}
		defer func() { <-lock }()
		return slices.Clone(got)
	}
}

// A request for an http URL is sent to the URL's host with what is meant
// for the origin and without what was meant for the proxy; nothing that
// names the app, the gateway or the proxy's software is added.
func TestForwardSendsOnlyWhatIsForTheOrigin(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.names["example.com"] = []string{"203.0.113.10"}
	addr, seen := origin(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "X-Hop")
		w.Header().Set("X-Hop", "for the proxy")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Proxy-Authenticate", "Basic")
		w.Header().Set("X-End", "for the app")
		_, _ = io.WriteString(w, "from the origin")
	})
	f.upstream = addr

	resp, _, r := f.send("POST http://alice:app-password@example.com/a/path?token=query-secret HTTP/1.1\r\n"+
		"Host: internal.example.net\r\n"+
		"Connection: keep-alive, X-Only-This-Hop\r\n"+
		"X-Only-This-Hop: 1\r\n"+
		"Keep-Alive: timeout=5\r\n"+
		"Proxy-Connection: keep-alive\r\n"+
		"Proxy-Authorization: Basic cHJveHktc2VjcmV0\r\n"+
		"TE: trailers\r\n"+
		"Upgrade: h2c\r\n"+
		"Authorization: Bearer for-the-origin\r\n"+
		"Cookie: session=the-apps-own\r\n"+
		"Content-Length: 4\r\n\r\nbody", http.MethodPost)
	body, _ := io.ReadAll(resp.Body)
	_ = r
	if resp.StatusCode != http.StatusOK || string(body) != "from the origin" {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	reqs := seen()
	if len(reqs) != 1 {
		t.Fatalf("the origin got %d requests", len(reqs))
	}
	got := reqs[0]
	sent, _ := io.ReadAll(got.Body)
	if got.Method != "POST" || got.Host != "example.com" || got.RequestURI != "/a/path?token=query-secret" || string(sent) != "body" {
		t.Errorf("the origin got %s %s for host %s with body %q", got.Method, got.RequestURI, got.Host, sent)
	}
	for _, gone := range []string{"X-Only-This-Hop", "Keep-Alive", "Proxy-Connection", "Proxy-Authorization", "Te", "Upgrade",
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-Ip", "User-Agent"} {
		if v, ok := got.Header[gone]; ok {
			t.Errorf("the origin got %s: %v", gone, v)
		}
	}
	if got.Header.Get("Authorization") != "Bearer for-the-origin" || got.Header.Get("Cookie") != "session=the-apps-own" {
		t.Errorf("the app's own credentials for the origin were not sent on: %v", got.Header)
	}
	if got.Header.Get("Via") != "1.1 proxy" {
		t.Errorf("Via = %q", got.Header.Get("Via"))
	}
	if _, _, ok := got.BasicAuth(); ok && got.Header.Get("Authorization") != "Bearer for-the-origin" {
		t.Errorf("the URL's user and password were sent on")
	}

	// The answer loses the origin's fields for one hop as well.
	for _, gone := range []string{"X-Hop", "Keep-Alive", "Proxy-Authenticate"} {
		if v := resp.Header.Get(gone); v != "" {
			t.Errorf("the app got %s: %s", gone, v)
		}
	}
	if resp.Header.Get("X-End") != "for the app" || resp.Header.Get("Via") != "1.1 proxy" {
		t.Errorf("answer header %v", resp.Header)
	}
	if got := f.connections(); !slices.Equal(got, []string{"203.0.113.10:80"}) {
		t.Errorf("connected to %v", got)
	}

	// The next request gets a connection of its own, and the origin is
	// not told to close: no connection to the outside is shared.
	if c := got.Header.Get("Connection"); c != "" {
		t.Errorf("the origin got Connection: %s", c)
	}
	again, _, _ := f.send("GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n", http.MethodGet)
	_, _ = io.ReadAll(again.Body)
	if got := f.connections(); len(got) != 2 {
		t.Errorf("%d connections for two requests, want one each", len(got))
	}

	// The log names the app and the host, and holds no path, query,
	// header or password.
	logs := f.logs.String()
	for _, want := range []string{"app=alice/demo", "method=POST", "host=example.com", "addr=203.0.113.10"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q: %s", want, logs)
		}
	}
	for _, secret := range []string{"/a/path", "query-secret", "app-password", "cHJveHktc2VjcmV0", "for-the-origin", "the-apps-own", "internal.example.net"} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds %q: %s", secret, logs)
		}
	}
}

// A request that could be split in two ways (a Content-Length and a
// chunked body, with a second request hidden in it) reaches the origin as
// the one request this proxy read, framed once (RFC 9112 section 6.3).
func TestForwardNeverPassesARequestOnAsItCame(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.names["example.com"] = []string{"203.0.113.10"}
	// The origin here keeps the bytes it is sent.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	raw := make(chan string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		req, err := http.ReadRequest(r)
		if err != nil {
			raw <- "unreadable: " + err.Error()
			return
		}
		body, _ := io.ReadAll(req.Body)
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		// Whatever follows the first request would be a second one.
		rest, _ := io.ReadAll(r)
		raw <- req.Header.Get("Content-Length") + "|" + strings.Join(req.TransferEncoding, ",") + "|" + string(body) + "|" + string(rest)
	}()
	f.upstream = l.Addr().String()

	smuggled := "GET http://example.com/hidden HTTP/1.1\r\nHost: example.com\r\n\r\n"
	chunked := "5\r\nhello\r\n0\r\n\r\n"
	resp, c, _ := f.send("POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n"+
		"Content-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n"+chunked+smuggled, http.MethodPost)
	_, _ = io.ReadAll(resp.Body)
	c.Close()
	got := <-raw
	t.Logf("status %d, origin read %q", resp.StatusCode, got)
	if resp.StatusCode != http.StatusOK || (got != "|chunked|hello|" && got != "5||hello|") {
		t.Errorf("the origin read (length|encoding|body|rest) %q, want one request with the body hello and nothing after it", got)
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("the hidden request reached the origin on the same connection: %q", got)
	}
}

// What is not a request for an http URL on port 80 is refused, and so is
// a request whose target is an address an app must not reach.
func TestForwardRefusesWhatItDoesNotCarry(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.names["example.com"] = []string{"203.0.113.10"}
	f.names["inside.example.com"] = []string{"10.0.0.5"}
	addr, seen := origin(t, nil)
	f.upstream = addr
	for request, want := range map[string]int{
		// Origin form: the Host header is never taken for a target.
		"GET /admin HTTP/1.1\r\nHost: example.com\r\n\r\n":                 http.StatusBadRequest,
		"GET /latest/meta-data/ HTTP/1.1\r\nHost: 169.254.169.254\r\n\r\n": http.StatusBadRequest,
		// Go's server answers this one itself; it goes nowhere.
		"OPTIONS * HTTP/1.1\r\nHost: example.com\r\n\r\n":                            http.StatusOK,
		"GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n":             http.StatusBadRequest,
		"GET ftp://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n":               http.StatusBadRequest,
		"GET http://example.com:8080/ HTTP/1.1\r\nHost: example.com:8080\r\n\r\n":    http.StatusForbidden,
		"GET http://example.com:443/ HTTP/1.1\r\nHost: example.com:443\r\n\r\n":      http.StatusForbidden,
		"GET http://inside.example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n":       http.StatusForbidden,
		"GET http://169.254.169.254/latest/ HTTP/1.1\r\nHost: example.com\r\n\r\n":   http.StatusForbidden,
		"GET http://[::ffff:10.0.0.5]/ HTTP/1.1\r\nHost: example.com\r\n\r\n":        http.StatusForbidden,
		"GET http://2130706433/ HTTP/1.1\r\nHost: example.com\r\n\r\n":               http.StatusBadRequest,
		"GET http://0x7f.0.0.1/ HTTP/1.1\r\nHost: example.com\r\n\r\n":               http.StatusBadRequest,
		"TRACE http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n":            http.StatusMethodNotAllowed,
		"OPTIONS http://example.com/ HTTP/1.1\r\nHost: x\r\nMax-Forwards: 0\r\n\r\n": http.StatusNoContent,
		"GET http://missing.example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n":      http.StatusBadGateway,
	} {
		method, _, _ := strings.Cut(request, " ")
		if resp, _, _ := f.send(request, method); resp.StatusCode != want {
			t.Errorf("%q: status %d, want %d", request, resp.StatusCode, want)
		}
	}
	if got := seen(); len(got) != 0 {
		t.Errorf("the origin got %d requests, want none", len(got))
	}
	if got := f.connections(); len(got) != 0 {
		t.Errorf("connected to %v, want nothing", got)
	}
	// The resolver's own words, which can name servers inside, are not
	// passed on.
	if strings.Contains(f.logs.String(), "10.0.0.53") {
		t.Errorf("the log holds the resolver's error: %s", f.logs.String())
	}

	// Max-Forwards above zero is sent on, one lower (RFC 9110 7.6.2).
	if resp, _, _ := f.send("OPTIONS http://example.com/ HTTP/1.1\r\nHost: x\r\nMax-Forwards: 3\r\n\r\n", "OPTIONS"); resp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS: status %d", resp.StatusCode)
	}
	if got := seen(); len(got) != 1 || got[0].Header.Get("Max-Forwards") != "2" {
		t.Errorf("Max-Forwards was not lowered: %v", got)
	}
}
