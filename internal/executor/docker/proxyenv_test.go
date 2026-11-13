package docker

import (
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/workload"
)

// With a proxy, every unit is told to use it for HTTP and HTTPS and not
// for the gateway or itself. A value the app set under one of those
// names is replaced: there is no other way out, so its own proxy or its
// own exceptions could only break it.
func TestContainerBodyTellsUnitsAboutTheProxy(t *testing.T) {
	t.Parallel()
	s := testSpec("proxied")
	u := sh("app", "true")
	u.Env = []workload.Env{
		{Name: "GREETING", Value: "hello"},
		{Name: "HTTP_PROXY", Value: "http://proxy.example.com:8080"},
		{Name: "no_proxy", Value: "*"},
	}
	b, err := ContainerBody(s, u, Inputs{Proxy: "http://javo-gateway:3128"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GREETING=hello",
		"HTTP_PROXY=http://javo-gateway:3128", "HTTPS_PROXY=http://javo-gateway:3128",
		"http_proxy=http://javo-gateway:3128", "https_proxy=http://javo-gateway:3128",
		"NO_PROXY=javo-gateway,localhost,127.0.0.1,::1", "no_proxy=javo-gateway,localhost,127.0.0.1,::1",
	}
	if !slices.Equal(b.Env, want) {
		t.Errorf("Env = %q\nwant  %q", b.Env, want)
	}

	// Without a proxy nothing is added and the app's own values stay.
	b, err = ContainerBody(s, u, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"GREETING=hello", "HTTP_PROXY=http://proxy.example.com:8080", "no_proxy=*"}; !slices.Equal(b.Env, want) {
		t.Errorf("Env = %q, want %q", b.Env, want)
	}
}

// The proxy is an http URL of a host and a port and nothing more.
func TestProxyHostRefusesWhatIsNoProxyURL(t *testing.T) {
	t.Parallel()
	if host, err := proxyHost("http://javo-gateway:3128"); err != nil || host != "javo-gateway" {
		t.Errorf("host %q, %v", host, err)
	}
	for _, bad := range []string{
		"javo-gateway:3128", "https://javo-gateway:3128", "http://javo-gateway", "http://:3128",
		"http://alice:secret@javo-gateway:3128", "http://javo-gateway:3128/path", "http://javo-gateway:3128?x=1", "socks5://javo-gateway:1080",
	} {
		if _, err := proxyHost(bad); err == nil {
			t.Errorf("%q passes for a proxy URL", bad)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("the error holds the password: %v", err)
		}
	}
}
