package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to one Docker daemon over its unix socket. Every call takes
// a context and ends with it.
type Client struct {
	// Labels are added to every container, network and volume the client
	// creates, on top of Javo's own.
	Labels map[string]string

	http *http.Client
}

// stamped is labels with the client's own added.
func (c *Client) stamped(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+len(c.Labels))
	for k, v := range labels {
		out[k] = v
	}
	for k, v := range c.Labels {
		out[k] = v
	}
	return out
}

// New is a client of the daemon listening on the unix socket at path.
func New(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}}
}

// ErrNotFound and ErrConflict are what errors.Is finds in the daemon's
// answer for a thing that does not exist, and for one that is in the way
// (a name in use, a container that is not in the state the call needs).
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Error is an answer of the daemon that is not a success.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.Status) }

func (e *Error) Is(target error) bool {
	return (target == ErrNotFound && e.Status == http.StatusNotFound) ||
		(target == ErrConflict && e.Status == http.StatusConflict)
}

// request is one call of the API.
type request struct {
	method, path string
	query        url.Values
	// body is sent as JSON, or as it is when it is an io.Reader.
	body   any
	header http.Header
	// unversioned asks the daemon in whatever API it speaks (only to learn
	// which that is).
	unversioned bool
}

// do sends r and returns the answer, whose body the caller closes. An
// answer of 400 or above is an *Error.
func (c *Client) do(ctx context.Context, r request) (*http.Response, error) {
	var body io.Reader
	contentType := ""
	switch b := r.body.(type) {
	case nil:
	case io.Reader:
		body, contentType = b, "application/x-tar"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		body, contentType = bytes.NewReader(raw), "application/json"
	}
	u := "http://docker"
	if !r.unversioned {
		u += "/v" + MinAPI
	}
	u += r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error of net/http names the URL, never the body.
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readError(resp)
	}
	return resp, nil
}

func readError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Message == "" {
		m.Message = strings.TrimSpace(string(raw))
	}
	return &Error{Status: resp.StatusCode, Message: m.Message}
}

// call sends r, reads the answer into out (unless nil) and closes it.
func (c *Client) call(ctx context.Context, r request, out any) error {
	resp, err := c.do(ctx, r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// get reads the JSON at path into out.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.call(ctx, request{method: http.MethodGet, path: path, query: query}, out)
}

// post sends body to path.
func (c *Client) post(ctx context.Context, path string, query url.Values, body, out any) error {
	return c.call(ctx, request{method: http.MethodPost, path: path, query: query, body: body}, out)
}

// labelFilter is the filters parameter that keeps what carries every one
// of labels ("key" or "key=value"), with any more filters in extra.
func labelFilter(labels []string, extra map[string][]string) url.Values {
	f := map[string][]string{}
	for k, v := range extra {
		f[k] = v
	}
	if len(labels) > 0 {
		f["label"] = labels
	}
	raw, _ := json.Marshal(f)
	return url.Values{"filters": {string(raw)}}
}
