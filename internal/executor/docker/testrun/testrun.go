// Package testrun is the one way the tests of this repository share a
// real Docker daemon: every test process stamps what it creates with a
// label holding an id of its own, names things after that id, and removes
// what carries the id and nothing else. Two test runs against one daemon
// never see or remove each other's things. It imports nothing of Javo, so
// the docker package's own tests and the helper for tests outside it
// (dockerlive) both stand on it.
package testrun

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Label is on everything a test creates on the daemon. Its value is the
// id of the test process that made the thing, never a value two share.
const Label = "javo.test"

// ID names this test process.
var ID = strings.ToLower(rand.Text())

// Stamp is the labels every creation of this test process carries.
func Stamp() map[string]string { return map[string]string{Label: ID} }

// Mark is the label filter that finds what this test process made.
func Mark() string { return Label + "=" + ID }

// Socket is the unix socket of the Docker daemon of this machine: the one
// DOCKER_HOST names, or else the first usual place where one answers. The
// test is skipped when none does.
func Socket(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	sockets := []string{"/var/run/docker.sock", filepath.Join(home, ".colima/default/docker.sock"), filepath.Join(home, ".docker/run/docker.sock")}
	if h, ok := strings.CutPrefix(os.Getenv("DOCKER_HOST"), "unix://"); ok {
		sockets = []string{h}
	}
	for _, sock := range sockets {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := Call(ctx, sock, http.MethodGet, "/version", nil, nil, nil)
		cancel()
		if err == nil {
			return sock
		}
	}
	t.Skip("no Docker daemon answers")
	return ""
}

// Call sends one request of the Engine API to the daemon at socket, as it
// is: body is sent as JSON, or as a tar stream when it is a reader, and
// the answer is read into out unless that is nil. An answer of 400 or
// above is an error that names its status ("HTTP 404").
func Call(ctx context.Context, socket, method, path string, query url.Values, body, out any) error {
	var send io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case io.Reader:
		send, contentType = b, "application/x-tar"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		send = bytes.NewReader(raw)
	}
	u := "http://docker" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, send)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// thing is one container, network, volume or image the daemon lists, and
// when it was made.
type thing struct {
	// path deletes it.
	path  string
	query url.Values
	made  time.Time
}

// things are the containers, networks, volumes and images on the daemon
// that carry label ("key" or "key=value").
func things(ctx context.Context, socket, label string) ([]thing, error) {
	filter, _ := json.Marshal(map[string][]string{"label": {label}})
	q := url.Values{"filters": {string(filter)}}
	var out []thing
	var containers []struct {
		ID      string `json:"Id"`
		Created int64
	}
	if err := Call(ctx, socket, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": q["filters"]}, nil, &containers); err != nil {
		return nil, err
	}
	for _, x := range containers {
		out = append(out, thing{"/containers/" + x.ID, url.Values{"force": {"1"}, "v": {"1"}}, time.Unix(x.Created, 0)})
	}
	var networks []struct {
		ID      string `json:"Id"`
		Created time.Time
	}
	if err := Call(ctx, socket, http.MethodGet, "/networks", q, nil, &networks); err != nil {
		return nil, err
	}
	for _, x := range networks {
		out = append(out, thing{"/networks/" + x.ID, nil, x.Created})
	}
	var volumes struct {
		Volumes []struct {
			Name      string
			CreatedAt time.Time
		}
	}
	if err := Call(ctx, socket, http.MethodGet, "/volumes", q, nil, &volumes); err != nil {
		return nil, err
	}
	for _, x := range volumes.Volumes {
		out = append(out, thing{"/volumes/" + url.PathEscape(x.Name), nil, x.CreatedAt})
	}
	var images []struct {
		ID      string `json:"Id"`
		Created int64
	}
	if err := Call(ctx, socket, http.MethodGet, "/images/json", q, nil, &images); err != nil {
		return nil, err
	}
	for _, x := range images {
		out = append(out, thing{"/images/" + x.ID, url.Values{"force": {"1"}}, time.Unix(x.Created, 0)})
	}
	return out, nil
}

// remove deletes each of all that was made before before (all of them
// when that is zero), containers first, and returns how many went.
func remove(ctx context.Context, t *testing.T, socket string, all []thing, before time.Time) int {
	t.Helper()
	n := 0
	for _, x := range all {
		if !before.IsZero() && !x.made.Before(before) {
			continue
		}
		if err := Call(ctx, socket, http.MethodDelete, x.path, x.query, nil, nil); err != nil && !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("cleanup of %s: %v", x.path, err)
			continue
		}
		n++
	}
	return n
}

// Sweep removes what this test process created on the daemon at socket,
// and only that. What a test process that was killed left behind is
// removed on purpose, by age (TestSweepLeftovers).
func Sweep(t *testing.T, socket string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	all, err := things(ctx, socket, Mark())
	if err != nil {
		t.Errorf("cleanup: %v", err)
	}
	remove(ctx, t, socket, all, time.Time{})
}
