package docker

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Container is what the daemon says about one container.
type Container struct {
	ID string `json:"Id"`
	// Name starts with a slash, as the daemon gives it.
	Name   string
	Image  string
	State  ContainerState
	Config struct {
		Image  string
		Labels map[string]string
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

// ContainerState is where a container is in its life.
type ContainerState struct {
	// Status is created, running, paused, restarting, removing, exited or
	// dead.
	Status   string
	Running  bool
	ExitCode int
	// OOMKilled is not set by every daemon for a container the kernel
	// killed for memory (see the package's daemon tests): exit code 137
	// under a memory limit is the sign that always shows.
	OOMKilled bool
	Error     string
	// StartedAt and FinishedAt are the zero time until they happen.
	StartedAt, FinishedAt time.Time
}

// IP is the container's address on the network name, or "".
func (c Container) IP(network string) string { return c.NetworkSettings.Networks[network].IPAddress }

// ContainerCreate makes a container called name from body and returns its
// id. A name in use is ErrConflict, an image the daemon does not have
// ErrNotFound.
func (c *Client) ContainerCreate(ctx context.Context, name string, body Body) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.post(ctx, "/containers/create", url.Values{"name": {name}}, body, &out)
	return out.ID, err
}

// ContainerStart starts a container. Starting one that runs changes
// nothing.
func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
}

// ContainerStop asks a container to end (SIGTERM, or the signal its image
// names) and kills it after grace. Stopping one that does not run changes
// nothing.
func (c *Client) ContainerStop(ctx context.Context, id string, grace time.Duration) error {
	q := url.Values{"t": {strconv.Itoa(int(grace.Seconds()))}}
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/stop", q, nil, nil)
}

// ContainerKill sends signal ("SIGKILL", "SIGTERM") to a container. One
// that does not run is ErrConflict.
func (c *Client) ContainerKill(ctx context.Context, id, signal string) error {
	return c.post(ctx, "/containers/"+url.PathEscape(id)+"/kill", url.Values{"signal": {signal}}, nil, nil)
}

// ContainerInspect reads one container, by id or name.
func (c *Client) ContainerInspect(ctx context.Context, id string) (Container, error) {
	var out Container
	err := c.get(ctx, "/containers/"+url.PathEscape(id)+"/json", nil, &out)
	return out, err
}

// ContainerWait blocks until the container no longer runs and returns its
// exit code.
func (c *Client) ContainerWait(ctx context.Context, id string) (int, error) {
	var out struct{ StatusCode int }
	err := c.post(ctx, "/containers/"+url.PathEscape(id)+"/wait", url.Values{"condition": {"not-running"}}, nil, &out)
	return out.StatusCode, err
}

// ContainerRemove kills and removes one of Javo's containers, with the
// unnamed volumes its image declared. One that is gone is not an error; one
// that does not carry Javo's label is ErrForeign and stays.
func (c *Client) ContainerRemove(ctx context.Context, id string) error {
	have, err := c.ContainerInspect(ctx, id)
	if err != nil {
		return gone(err)
	}
	if !Ours(have.Config.Labels) {
		return foreign("container", id)
	}
	q := url.Values{"force": {"1"}, "v": {"1"}}
	return gone(c.call(ctx, request{method: http.MethodDelete, path: "/containers/" + url.PathEscape(have.ID), query: q}, nil))
}

// ContainerSummary is one container of a list.
type ContainerSummary struct {
	ID     string `json:"Id"`
	Names  []string
	State  string
	Labels map[string]string
}

// ContainerList is every container, running or not, that carries all of
// labels ("key" or "key=value").
func (c *Client) ContainerList(ctx context.Context, labels ...string) ([]ContainerSummary, error) {
	q := labelFilter(labels, nil)
	q.Set("all", "1")
	var out []ContainerSummary
	err := c.get(ctx, "/containers/json", q, &out)
	return out, err
}

// PathExists reports whether path exists in the container, which need not
// run.
func (c *Client) PathExists(ctx context.Context, id, path string) (bool, error) {
	resp, err := c.do(ctx, request{method: http.MethodHead, path: "/containers/" + url.PathEscape(id) + "/archive", query: url.Values{"path": {path}}})
	if err != nil {
		// A HEAD has no body to tell a missing path from a missing
		// container; the caller made the container.
		if e, ok := err.(*Error); ok && e.Status == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	resp.Body.Close()
	return true, nil
}

// Extract unpacks a tar stream at path in the container. The daemon does
// the writing: no process of the container runs for it.
func (c *Client) Extract(ctx context.Context, id, path string, tar io.Reader) error {
	return c.call(ctx, request{method: http.MethodPut, path: "/containers/" + url.PathEscape(id) + "/archive", query: url.Values{"path": {path}}, body: tar}, nil)
}

// gone turns "it does not exist" into success, for removals.
func gone(err error) error {
	if e, ok := err.(*Error); ok && e.Status == http.StatusNotFound {
		return nil
	}
	return err
}
