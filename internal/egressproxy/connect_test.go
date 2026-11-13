package egressproxy

import (
	"errors"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// A CONNECT to a public host on port 443 becomes a tunnel to the address
// the name resolved to, and bytes pass both ways.
func TestConnectTunnelsToAPublicHost(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.names["example.com"] = []string{"203.0.113.10"}
	f.upstream = echo(t)

	// The client's first bytes come right behind its request.
	resp, c, r := f.send("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\nearly", http.MethodConnect)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	// RFC 9110 section 9.3.6: no Content-Length or Transfer-Encoding in
	// a 2xx answer to CONNECT.
	if resp.Header.Get("Content-Length") != "" || len(resp.TransferEncoding) > 0 {
		t.Errorf("the answer frames a body: %v %v", resp.Header, resp.TransferEncoding)
	}
	if _, err := c.Write([]byte(" and late")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("early and late"))
	if _, err := io.ReadFull(r, got); err != nil || string(got) != "early and late" {
		t.Fatalf("through the tunnel: %q, %v", got, err)
	}
	if got := f.connections(); !slices.Equal(got, []string{"203.0.113.10:443"}) {
		t.Errorf("connected to %v, want the resolved address", got)
	}
	// When one side closes, so does the other.
	c.Close()
	<-f.left
	logs := f.logs.String()
	for _, want := range []string{"app=alice/demo", "host=example.com", "port=443", "addr=203.0.113.10"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q: %s", want, logs)
		}
	}
}

// Whatever a name resolves to, the address decides: a name that leads to
// an address an app must not reach is refused and never connected to.
func TestConnectRefusesByTheAddressBehindTheName(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{Deny: prefixes("203.0.113.64/26")}, nil)
	f.upstream = echo(t)
	for name, addr := range map[string]string{
		"loopback.example.com":  "127.0.0.1",
		"metadata.example.com":  "169.254.169.254",
		"linklocal.example.com": "fe80::1",
		"ten.example.com":       "10.0.0.5",
		"oneseven.example.com":  "172.16.3.4",
		"onenine.example.com":   "192.168.1.1",
		"cgnat.example.com":     "100.64.0.1",
		"ula.example.com":       "fd00::1",
		"multicast.example.com": "224.0.0.1",
		"zero.example.com":      "0.0.0.0",
		"docker.example.com":    "172.17.0.1",
		"pool.example.com":      "10.213.0.2",
		"denied.example.com":    "203.0.113.65",
		"self.example.com":      "198.51.100.1",
		"mapped.example.com":    "::ffff:10.0.0.5",
		"nat64.example.com":     "64:ff9b::a00:5",
		"localhost":             "127.0.0.1",
	} {
		f.mu.Lock()
		f.names[name] = []string{addr}
		f.mu.Unlock()
		resp, _, _ := f.connect(name + ":443")
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s (%s): status %d, want 403", name, addr, resp.StatusCode)
		}
	}
	// The same addresses written out, with the forms that hide them.
	for target, want := range map[string]int{
		"127.0.0.1:443":                http.StatusForbidden,
		"169.254.169.254:443":          http.StatusForbidden,
		"10.0.0.5:443":                 http.StatusForbidden,
		"172.17.0.1:443":               http.StatusForbidden,
		"[::1]:443":                    http.StatusForbidden,
		"[::ffff:127.0.0.1]:443":       http.StatusForbidden,
		"[::ffff:169.254.169.254]:443": http.StatusForbidden,
		"[::ffff:a00:5]:443":           http.StatusForbidden,
		"127.0.0.1.:443":               http.StatusForbidden,
		"localhost.:443":               http.StatusForbidden,
		"198.51.100.1:443":             http.StatusForbidden, // the proxy itself: no loop
		"2130706433:443":               http.StatusBadRequest,
		"0177.0.0.1:443":               http.StatusBadRequest,
		"0x7f.0.0.1:443":               http.StatusBadRequest,
		"127.1:443":                    http.StatusBadRequest,
		"[fe80::1%25eth0]:443":         http.StatusBadRequest,
	} {
		resp, _, _ := f.connect(target)
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", target, resp.StatusCode, want)
		}
	}
	if got := f.connections(); len(got) != 0 {
		t.Errorf("connected to %v, want nothing", got)
	}
	if f.lookups["2130706433"]+f.lookups["0177.0.0.1"]+f.lookups["127.1"] != 0 {
		t.Errorf("an address in an odd form was given to the resolver: %v", f.lookups)
	}
}

// DNS rebinding: a name that is public for the first question and
// private for the next gains nothing. Each connection asks once and
// connects to the address it checked, written out; the next connection
// asks again and is refused.
func TestConnectChecksTheAddressOfEachConnection(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.upstream = echo(t)
	f.rebind = func(_ string, n int) []string {
		if n == 0 {
			return []string{"203.0.113.10"}
		}
		return []string{"10.0.0.5"}
	}
	if resp, _, _ := f.connect("rebind.example.com:443"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first connection: status %d", resp.StatusCode)
	}
	if resp, _, _ := f.connect("rebind.example.com:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("second connection: status %d, want 403", resp.StatusCode)
	}
	if got := f.connections(); !slices.Equal(got, []string{"203.0.113.10:443"}) {
		t.Errorf("connected to %v, want only the address that was checked", got)
	}
	if f.lookups["rebind.example.com"] != 2 {
		t.Errorf("%d questions for two connections, want one each", f.lookups["rebind.example.com"])
	}
}

// Of a name with a refused and an allowed address, only the allowed one
// is ever connected to; a name with refused addresses only is refused.
func TestConnectSkipsTheRefusedAddressesOfAName(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.upstream = echo(t)
	f.names["mixed.example.com"] = []string{"10.0.0.5", "64:ff9b::cb00:710a", "203.0.113.10"}
	f.names["inside.example.com"] = []string{"10.0.0.5", "169.254.169.254"}
	if resp, _, _ := f.connect("mixed.example.com:443"); resp.StatusCode != http.StatusOK {
		t.Fatalf("mixed: status %d", resp.StatusCode)
	}
	if resp, _, _ := f.connect("inside.example.com:443"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("inside: status %d, want 403", resp.StatusCode)
	}
	if got := f.connections(); !slices.Equal(got, []string{"203.0.113.10:443"}) {
		t.Errorf("connected to %v", got)
	}
}

// A tunnel is for port 443: any other port is refused, also on a public
// host, and a target without a usable port is a bad request.
func TestConnectIsForPort443Only(t *testing.T) {
	t.Parallel()
	f := start(t, Rules{}, nil)
	f.names["example.com"] = []string{"203.0.113.10"}
	f.upstream = echo(t)
	for target, want := range map[string]int{
		"example.com:22":    http.StatusForbidden,
		"example.com:25":    http.StatusForbidden,
		"example.com:80":    http.StatusForbidden,
		"example.com:8443":  http.StatusForbidden,
		"example.com:5432":  http.StatusForbidden,
		"example.com:":      http.StatusBadRequest,
		"example.com:0":     http.StatusBadRequest,
		"example.com:99999": http.StatusBadRequest,
	} {
		if resp, _, _ := f.connect(target); resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", target, resp.StatusCode, want)
		}
	}
	// A CONNECT that says it has content is refused.
	resp, _, _ := f.send("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\nContent-Length: 5\r\n\r\nhello", http.MethodConnect)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("CONNECT with content: status %d, want 400", resp.StatusCode)
	}
	if got := f.connections(); len(got) != 0 {
		t.Errorf("connected to %v, want nothing", got)
	}
}

// The real dialer asks about the address of the socket before it
// connects: a refused address gets no connection, whoever picked it.
func TestDialCheckedAsksBeforeTheSocketConnects(t *testing.T) {
	t.Parallel()
	upstream := netip.MustParseAddrPort(echo(t))
	var asked []netip.Addr
	_, err := dialChecked(t.Context(), upstream, func(a netip.Addr) error {
		asked = append(asked, a)
		return &DeniedError{Why: "a loopback address"}
	})
	var denied *DeniedError
	if err == nil || !errors.As(err, &denied) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if !slices.Equal(asked, []netip.Addr{upstream.Addr()}) {
		t.Errorf("asked about %v, want %v", asked, upstream.Addr())
	}
	c, err := dialChecked(t.Context(), upstream, func(netip.Addr) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
