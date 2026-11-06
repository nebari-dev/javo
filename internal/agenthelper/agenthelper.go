// Package agenthelper is the credential helper of a coding-agent app: a
// small reverse proxy in a sidecar of the app's pod that holds the app's API
// token and its model key, so the agent's own container never has them. The
// agent calls the helper without credentials (javo-api on a unix socket, the
// agent's model client on a local TCP address); the helper drops whatever
// credentials the request carries and adds its own. It reaches only the
// Javo API (paths under /api/) and the model's base URL (under ModelPrefix),
// nothing else, so it is no open proxy. It logs each request's method and
// path, never a header or a body.
package agenthelper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// ModelPrefix is where the helper serves the model: a request for
// ModelPrefix/x goes to the model's base URL + /x.
const ModelPrefix = "/model"

const apiPrefix = "/api/"

// Config is what the helper forwards to and with which credentials.
type Config struct {
	// API is the Javo gateway's origin; Token is the app's API token.
	API   *url.URL
	Token string
	// Model is the model's OpenAI-compatible base URL (nil: no model
	// route), called with ModelKey.
	Model    *url.URL
	ModelKey string
	Log      *slog.Logger
}

// ConfigFromEnv reads the helper's container variables: the gateway at
// JAVO_API_URL (its address inside the cluster) or else JAVO_URL, the token
// JAVO_TOKEN, and the model at JAVO_AGENT_BASE_URL (none when empty) with
// the key JAVO_AGENT_KEY. Its Log is slog's default.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{Token: getenv("JAVO_TOKEN"), ModelKey: getenv("JAVO_AGENT_KEY"), Log: slog.Default()}
	api := getenv("JAVO_API_URL")
	if api == "" {
		api = getenv("JAVO_URL")
	}
	var err error
	if c.API, err = absURL("the Javo API", api); err != nil {
		return Config{}, err
	}
	if m := getenv("JAVO_AGENT_BASE_URL"); m != "" {
		if c.Model, err = absURL("the agent model", m); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

func absURL(what, s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s: %q is not an http(s) URL", what, s)
	}
	return u, nil
}

type routeKey struct{}

type route struct {
	target *url.URL
	key    string
	path   string
}

// Handler serves the helper's two routes and refuses everything else.
func Handler(c Config) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			rt := pr.In.Context().Value(routeKey{}).(route)
			pr.Out.URL.Path, pr.Out.URL.RawPath = rt.path, ""
			pr.SetURL(rt.target)
			for _, h := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
				pr.Out.Header.Del(h)
			}
			pr.Out.Header.Set("Authorization", "Bearer "+rt.key)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			c.Log.Warn("upstream failed", "method", r.Method, "path", r.URL.Path, "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := c.routeOf(r.URL.Path)
		c.Log.Info("request", "method", r.Method, "path", r.URL.Path, "allowed", ok)
		if !ok {
			http.NotFound(w, r)
			return
		}
		rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeKey{}, rt)))
	})
}

// routeOf is where the request for path p goes, if anywhere.
func (c Config) routeOf(p string) (route, bool) {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return route{}, false
		}
	}
	switch {
	case c.Token != "" && strings.HasPrefix(p, apiPrefix):
		return route{target: origin(c.API), key: c.Token, path: p}, true
	case c.Model != nil && (p == ModelPrefix || strings.HasPrefix(p, ModelPrefix+"/")):
		return route{target: c.Model, key: c.ModelKey, path: strings.TrimPrefix(p, ModelPrefix)}, true
	}
	return route{}, false
}

// origin is u without its path: API paths are the gateway's own.
func origin(u *url.URL) *url.URL {
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

// Run serves Handler(c) on the unix socket sock (one left by an earlier run
// is replaced) and on the TCP address addr until ctx ends.
func Run(ctx context.Context, c Config, sock, addr string) error {
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	unix, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	tcp, err := net.Listen("tcp", addr)
	if err != nil {
		unix.Close()
		return err
	}
	srv := &http.Server{Handler: Handler(c), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 2)
	for _, ln := range []net.Listener{unix, tcp} {
		go func() { errs <- srv.Serve(ln) }()
	}
	c.Log.Info("serving", "socket", sock, "address", addr)
	select {
	case err := <-errs:
		srv.Close()
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

// Ready reports whether a helper answers on the socket sock.
func Ready(sock string) error {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}
