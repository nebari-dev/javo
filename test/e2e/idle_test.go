//go:build e2e

package e2e

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// idleApp is the part of an app's view the idle test reads.
type idleApp struct {
	app
	IdleStopMinutes  int32
	StoppedIdleAfter int32
}

// Three Streamlit samples stop after 1 minute without use: one nobody uses
// is stopped and says why, one with a request every 10 seconds and one
// holding an open websocket (and nothing else) keep running. The dev values
// allow 1 minute; an install whose shortest timeout is longer skips this.
func TestStopWhenIdle(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var policy struct{ MinMinutes int32 }
	s.json(http.MethodGet, "/api/idle-stop", nil, http.StatusOK, &policy)
	if policy.MinMinutes > 1 {
		t.Skipf("the shortest idle timeout here is %d minutes", policy.MinMinutes)
	}
	run := time.Now().Format("150405")
	create := func(name string) idleApp {
		var a idleApp
		s.json(http.MethodPost, "/api/workspaces", map[string]any{
			"displayName": name + " " + run, "template": "streamlit", "sample": true, "idleStopMinutes": 1,
		}, http.StatusCreated, &a)
		t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
		if a.IdleStopMinutes != 1 {
			t.Fatalf("%s: idleStopMinutes %d, want 1", a.Name, a.IdleStopMinutes)
		}
		return a
	}
	quiet, busy, held := create("e2e idle quiet"), create("e2e idle busy"), create("e2e idle held")
	wait := func(a idleApp) app {
		return s.waitFor(a.Name, 4*time.Minute, func(a app) bool {
			if a.Phase == "Failed" {
				t.Fatalf("%s failed: %+v", a.Name, a.Steps)
			}
			return a.Phase == "Running"
		})
	}

	// busy: a request every 10 seconds from now until the test ends.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Second):
				if r, err := s.http.Get(s.abs(busy.URL + "_stcore/health")); err == nil {
					r.Body.Close()
				}
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()

	// held: one open websocket, as a Streamlit page keeps, and no requests,
	// opened as soon as it runs.
	heldURL := wait(held).URL
	// Opening the page first gets the session for the app's host.
	if r, body := s.do(http.MethodGet, heldURL, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %.200s", heldURL, r.StatusCode, body)
	}
	conn, br, resp := dialWebsocket(t, s, heldURL+"_stcore/stream")
	defer conn.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket to %s: status %d", held.Name, resp.StatusCode)
	}
	// A browser answers the server's pings, or the server closes the socket.
	go answerPings(conn, br)
	wait(busy)
	// quiet may already have run a minute while busy waited for room on the
	// node, and been stopped.
	s.waitFor(quiet.Name, 4*time.Minute, func(a app) bool {
		if a.Phase == "Failed" {
			t.Fatalf("%s failed: %+v", a.Name, a.Steps)
		}
		return a.Phase == "Running" || a.Phase == "Stopped"
	})

	start := time.Now()
	got := s.waitFor(quiet.Name, 4*time.Minute, func(a app) bool { return a.Phase == "Stopped" })
	t.Logf("%s stopped %s after the others were running", quiet.Name, time.Since(start).Round(time.Second))
	var stopped idleApp
	s.json(http.MethodGet, "/api/workspaces/"+quiet.Name, nil, http.StatusOK, &stopped)
	if stopped.StoppedIdleAfter != 1 {
		t.Errorf("stoppedIdleAfter %d, want 1", stopped.StoppedIdleAfter)
	}
	first := got.Steps[0]
	if first.Reason != "StoppedIdle" || !strings.Contains(strings.Join(first.Evidence, "\n"), "Stopped after 1 minute without use") {
		t.Errorf("stopped app's step: %+v", first)
	}

	// The quiet app had at least a minute and a half; give the others as long again.
	time.Sleep(90 * time.Second)
	for _, a := range []idleApp{busy, held} {
		var now idleApp
		s.json(http.MethodGet, "/api/workspaces/"+a.Name, nil, http.StatusOK, &now)
		if now.Phase != "Running" {
			t.Errorf("%s is %s, want Running: it was in use", a.Name, now.Phase)
		}
	}

	// Started again it runs, and no longer says it was stopped for being idle.
	var started idleApp
	s.json(http.MethodPost, "/api/workspaces/"+quiet.Name+"/start", nil, http.StatusOK, &started)
	if started.StoppedIdleAfter != 0 {
		t.Errorf("after start stoppedIdleAfter %d", started.StoppedIdleAfter)
	}
}

// answerPings reads websocket frames from a client connection and answers
// each ping with a pong, as a browser does, until the connection ends. It
// sends nothing else.
func answerPings(conn net.Conn, r *bufio.Reader) {
	for {
		var head [2]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return
		}
		n := uint64(head[1] & 0x7f)
		switch n {
		case 126:
			var b [2]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return
			}
			n = binary.BigEndian.Uint64(b[:])
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return
		}
		if head[0]&0x0f != 0x9 || n > 125 {
			continue
		}
		// A client's frames are masked; a zero mask leaves the payload as is.
		pong := append([]byte{0x8a, 0x80 | byte(n), 0, 0, 0, 0}, payload...)
		if _, err := conn.Write(pong); err != nil {
			return
		}
	}
}
