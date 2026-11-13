package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// MinAPI is the oldest Engine API this package speaks, and the version it
// asks every call in: 1.48, which Docker Engine 28.0 brought. It is the
// first with the "isolated" gateway mode of a bridge network, without
// which a container on an internal network reaches every service the host
// listens with on the bridge's address (Network). Mounting a folder of a
// volume (Mount.SubPath) needs 1.45.
const MinAPI = "1.48"

// ErrTooOld means the daemon speaks an API older than MinAPI.
var ErrTooOld = errors.New("the Docker daemon is too old")

// Daemon is what a Docker daemon says about itself.
type Daemon struct {
	// Version is the engine's, APIVersion the newest API it speaks.
	Version, APIVersion string
	// Rootless is true when the daemon itself does not run as root.
	Rootless bool
	// Runtimes are the names of the container runtimes it knows.
	Runtimes []string
	// CgroupVersion is "1" or "2".
	CgroupVersion string
}

// HasRuntime reports whether the daemon knows the runtime name.
func (d Daemon) HasRuntime(name string) bool { return slices.Contains(d.Runtimes, name) }

// Daemon asks the daemon what it is. It fails when none answers, and with
// ErrTooOld when the one that does speaks an API older than MinAPI.
func (c *Client) Daemon(ctx context.Context) (Daemon, error) {
	var v struct{ Version, ApiVersion string }
	if err := c.call(ctx, request{method: http.MethodGet, path: "/version", unversioned: true}, &v); err != nil {
		return Daemon{}, err
	}
	if !atLeast(v.ApiVersion, MinAPI) {
		return Daemon{}, fmt.Errorf("%w: Docker %s speaks API %q, and Javo needs %s (Docker 28.0)", ErrTooOld, v.Version, v.ApiVersion, MinAPI)
	}
	var info struct {
		SecurityOptions []string
		Runtimes        map[string]any
		CgroupVersion   string
	}
	if err := c.get(ctx, "/info", nil, &info); err != nil {
		return Daemon{}, err
	}
	d := Daemon{Version: v.Version, APIVersion: v.ApiVersion, CgroupVersion: info.CgroupVersion}
	for name := range info.Runtimes {
		d.Runtimes = append(d.Runtimes, name)
	}
	slices.Sort(d.Runtimes)
	for _, o := range info.SecurityOptions {
		d.Rootless = d.Rootless || slices.Contains(strings.Split(o, ","), "name=rootless")
	}
	return d, nil
}

// atLeast reports whether the API version have ("1.53") is min or newer.
// A version that cannot be read is not.
func atLeast(have, min string) bool {
	h, ok := apiVersion(have)
	m, _ := apiVersion(min)
	return ok && (h[0] > m[0] || (h[0] == m[0] && h[1] >= m[1]))
}

func apiVersion(s string) ([2]int, bool) {
	major, minor, ok := strings.Cut(s, ".")
	a, err1 := strconv.Atoi(major)
	b, err2 := strconv.Atoi(minor)
	return [2]int{a, b}, ok && err1 == nil && err2 == nil
}
