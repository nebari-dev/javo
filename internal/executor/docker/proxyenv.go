package docker

import (
	"errors"
	"net/url"
	"slices"
	"strings"
)

// proxyVars are the variables that tell a program which proxy to use,
// and noProxyVars the ones that say where not to use it. Programs read
// the upper-case names, the lower-case ones or both.
var (
	proxyVars   = []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"}
	noProxyVars = []string{"NO_PROXY", "no_proxy"}
)

// proxyHost is the host of the proxy URL, which must be an http URL of a
// host and a port and nothing else (a user and password in it would
// show in every container's variables).
func proxyHost(proxy string) (string, error) {
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.Port() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		// The URL is not repeated: it may hold a password.
		return "", errors.New("the proxy must be an http URL of a host and a port, such as http://javo:3128")
	}
	return u.Hostname(), nil
}

// withProxy is env with the variables that send a unit's HTTP and HTTPS
// through proxy, the gateway's forward proxy (internal/egressproxy),
// except for the gateway itself and the unit's own loopback.
//
// A workload's network has no other way out, so a value the app set
// under one of these names is replaced rather than kept: its own proxy
// could not be reached, and its own exceptions would only send requests
// straight into a network that goes nowhere. Neither could get it past
// the isolation. The names are not reserved (model.EnvNameProblem): on
// Kubernetes they are the app's to set.
func withProxy(env []string, proxy string) ([]string, error) {
	host, err := proxyHost(proxy)
	if err != nil {
		return nil, err
	}
	set := map[string]string{}
	for _, name := range proxyVars {
		set[name] = strings.TrimSuffix(proxy, "/")
	}
	for _, name := range noProxyVars {
		set[name] = host + ",localhost,127.0.0.1,::1"
	}
	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		_, ours := set[name]
		return ours
	})
	for _, name := range slices.Concat(proxyVars, noProxyVars) {
		out = append(out, name+"="+set[name])
	}
	return out, nil
}
