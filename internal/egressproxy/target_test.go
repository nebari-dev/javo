package egressproxy

import "testing"

func TestParseTargetReadsAHostAndAPort(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"example.com:443":        "example.com",
		"Example.COM:443":        "example.com",
		"example.com.:443":       "example.com",
		"a_b.example.com:80":     "a_b.example.com",
		"203.0.113.10:443":       "203.0.113.10",
		"203.0.113.10.:443":      "203.0.113.10",
		"127.0.0.1.:443":         "127.0.0.1",
		"[2001:db8::1]:443":      "2001:db8::1",
		"[::ffff:127.0.0.1]:443": "::ffff:127.0.0.1",
		"localhost.:443":         "localhost",
	} {
		got, err := parseTarget(in)
		if err != nil || got.host() != want {
			t.Errorf("%q: host %q, %v; want %q", in, got.host(), err, want)
		}
	}
}

// What a resolver could read as another address than this proxy does, and
// what is no host and port at all, is refused before anything is
// resolved.
func TestParseTargetRefusesWhatCanBeReadTwoWays(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"2130706433:443",           // 127.0.0.1 as one decimal number
		"0177.0.0.1:443",           // octal
		"0x7f.0.0.1:443",           // hexadecimal
		"0x7f000001:443",           //
		"127.1:443",                // short form
		"127.0.1:443",              //
		"example.123:443",          // a name that ends in a number
		"1.2.3.4.5:443",            //
		"[fe80::1%eth0]:443",       // a zone
		"example.com:",             // no port
		"example.com",              //
		"example.com:0",            //
		"example.com:65536",        //
		"example.com:+443",         //
		"example.com:https",        //
		"example.com:44 3",         //
		":443",                     // no host
		".:443",                    //
		"exa mple.com:443",         //
		"alice:pw@example.com:443", // a user and password
		"alice@example.com:443",    //
		"example.com/path:443",     //
		"http://example.com:443",   //
		"/path",                    //
		"example..com:443",         //
		"",
	} {
		if got, err := parseTarget(in); err == nil {
			t.Errorf("%q is read as %q port %d", in, got.host(), got.port)
		}
	}
}
