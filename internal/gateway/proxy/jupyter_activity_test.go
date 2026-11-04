package proxy

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// A Jupyter server's last use is its own /api/status last_activity, or now
// while one of its kernels runs code. The kernels are listed without
// counting as use (no_track_activity), and status never counts.
func TestJupyterProbeReadsTheServersOwnLastUse(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	state := "idle"
	var tracked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/u/alice/lab/api/status":
			w.Write([]byte(`{"started":"2026-11-02T10:00:00.000000Z","last_activity":"2026-11-02T11:40:05.123456Z","connections":1,"kernels":1}`))
		case "/u/alice/lab/api/kernels":
			if r.URL.Query().Get("no_track_activity") == "" {
				tracked = append(tracked, r.URL.String())
			}
			w.Write([]byte(`[{"id":"k1","name":"python3","last_activity":"2026-11-02T11:30:00.000000Z","execution_state":"` + state + `","connections":1}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	probe := jupyterProbe(srv.Client(), Backend{URL: u, Prefix: "/u/alice/lab"}, func() time.Time { return now })

	got, err := probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 2, 11, 40, 5, 123456000, time.UTC); !got.Equal(want) {
		t.Fatalf("idle server: %v, want its last_activity %v", got, want)
	}
	state = "busy"
	if got, err = probe(context.Background()); err != nil || !got.Equal(now) {
		t.Fatalf("busy kernel: %v %v, want now", got, err)
	}
	if len(tracked) > 0 {
		t.Fatalf("kernels listed as use: %v", tracked)
	}

	gone := jupyterProbe(srv.Client(), Backend{URL: u, Prefix: "/u/bob/lab"}, func() time.Time { return now })
	if _, err := gone(context.Background()); err == nil {
		t.Fatal("want an error from a server that does not answer the status API")
	}
}

// An open websocket to an app whose template says Jupyter keeps its own
// account of use is not use by itself: each flush writes what the server
// reports.
func TestJupyterAppsOpenWebsocketIsNotUseByItself(t *testing.T) {
	t.Parallel()
	reported := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	f := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			w.Write([]byte(`{"last_activity":"` + reported.Format(time.RFC3339Nano) + `"}`))
		case "/api/kernels":
			w.Write([]byte(`[]`))
		default:
			gatewaytest.EchoBackend(w, r)
		}
	}))
	tpl := gatewaytest.Template("t", "jupyterlab")
	tpl.Spec.Activity = model.ActivityJupyter
	gatewaytest.Seed(t, f.db, []any{tpl})
	srv := httptest.NewServer(f.proxy)
	defer srv.Close()
	cookie := f.login(t, "alice-aaaaaaaa", "alice")
	id := f.appID(t, f.aliceSl, "nb1")

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /u/"+f.aliceSl+"/nb1/api/kernels/k1/channels HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nCookie: "+cookie.String()+"\r\n\r\n")
	if resp, err := http.ReadResponse(bufio.NewReader(conn), nil); err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	gatewaytest.Flushed(t, f.activity) // the upgrade request itself
	if got := gatewaytest.Flushed(t, f.activity)[id]; !got.Equal(reported) {
		t.Fatalf("open Jupyter websocket flushed %v, want the server's last use %v", got, reported)
	}
}

// The app owner runs the server the probe asks, so a reply too big for a
// kernel list is an error, not something the gateway reads whole.
func TestJupyterProbeRejectsAnOversizedReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/u/alice/lab/api/status" {
			w.Write([]byte(`{"last_activity":"2026-11-02T11:40:05Z"}`))
			return
		}
		w.Write([]byte("["))
		w.Write([]byte(strings.Repeat(" ", 8<<20)))
		w.Write([]byte("]"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	probe := jupyterProbe(srv.Client(), Backend{URL: u, Prefix: "/u/alice/lab"}, time.Now)
	if _, err := probe(context.Background()); err == nil {
		t.Fatal("want an error from an oversized kernel list")
	}
}

// The app's owner controls the server the probe asks. A redirect must not
// send the gateway to another address (cloud metadata, the controller's
// metrics, any in-cluster service) with the template's proxy headers.
func TestJupyterProbeDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		_, _ = w.Write([]byte("[]"))
	}))
	defer internal.Close()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/latest/meta-data/iam/security-credentials/", http.StatusFound)
	}))
	defer app.Close()
	u, _ := url.Parse(app.URL)
	probe := jupyterProbe(app.Client(), Backend{URL: u, Prefix: "/u/alice/lab", Headers: http.Header{"X-Tpl": {"1"}}}, time.Now)
	if _, err := probe(context.Background()); err == nil {
		t.Error("a redirect answered the probe; want an error")
	}
	if hit.Load() {
		t.Error("the probe followed the app's redirect to another server")
	}
}
