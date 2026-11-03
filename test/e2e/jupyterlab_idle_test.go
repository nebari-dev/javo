//go:build e2e

package e2e

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// JupyterLab ships its extensions, shuts down idle kernels itself (open
// websocket or not, but never one running code), and an app whose only
// client is an open but unused tab (a kernel websocket that answers pings
// and sends nothing) stops at its idle limit, while one whose kernel runs
// code keeps running. The kernel timeout is shortened to 60
// seconds by rewriting the environment's jupyter_server_config.json from a
// kernel and restarting the app.
func TestJupyterLabIdle(t *testing.T) {
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
			"displayName": name + " " + run, "template": "jupyterlab", "idleStopMinutes": 1,
		}, http.StatusCreated, &a)
		t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
		return a
	}
	running := func(name string) app {
		return s.waitFor(name, 10*time.Minute, func(a app) bool {
			if a.Phase == "Failed" {
				t.Fatalf("%s failed: %+v", a.Name, a.Steps)
			}
			return a.Phase == "Running"
		})
	}

	idle := create("e2e lab idle")
	got := running(idle.Name)

	// The extensions are installed and enabled: JupyterLab lists them in
	// the page it serves.
	resp, page := s.do(http.MethodGet, got.URL+"lab", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET lab: %d", resp.StatusCode)
	}
	for _, ext := range []string{"@jupyterlab/git", "dask-labextension", "@jupyterhub/jupyter-server-proxy", "@jupyter-server/resource-usage", "jupyter-matplotlib"} {
		if !strings.Contains(string(page), `"name": "`+ext+`"`) && !strings.Contains(string(page), `"name":"`+ext+`"`) {
			t.Errorf("JupyterLab page lists no extension %s", ext)
		}
	}

	// Shorten the kernel and terminal timeouts in the environment, then
	// restart so the server reads them.
	k := startKernel(t, s, got.URL)
	conn, br := kernelChannels(t, s, got.URL, k)
	execute(t, conn, br, `import json, os
p = os.path.join(os.environ["CONDA_PREFIX"], "etc", "jupyter", "jupyter_server_config.json")
c = json.load(open(p))
assert c["MappingKernelManager"]["cull_idle_timeout"] == 900, c
c["MappingKernelManager"].update(cull_idle_timeout=60, cull_interval=10)
c["TerminalManager"].update(cull_inactive_timeout=60, cull_interval=10)
json.dump(c, open(p, "w"))`, false)
	conn.Close()
	s.json(http.MethodPost, "/api/workspaces/"+idle.Name+"/stop", nil, http.StatusOK, nil)
	s.waitFor(idle.Name, 3*time.Minute, func(a app) bool { return a.Phase == "Stopped" })
	busy := create("e2e lab busy")
	s.json(http.MethodPost, "/api/workspaces/"+idle.Name+"/start", nil, http.StatusOK, nil)
	got = running(idle.Name)
	busyURL := running(busy.Name).URL

	// busy: a kernel running code for ten minutes and an idle one, both
	// with their websockets open.
	bk := startKernel(t, s, busyURL)
	bconn, bbr := kernelChannels(t, s, busyURL, bk)
	defer bconn.Close()
	execute(t, bconn, bbr, "import time; time.sleep(600)", true)
	go answerPings(bconn, bbr)
	ik := startKernel(t, s, busyURL)
	iconn, ibr := kernelChannels(t, s, busyURL, ik)
	defer iconn.Close()
	go answerPings(iconn, ibr)

	// idle: an open tab's kernel websocket and nothing else.
	k = startKernel(t, s, got.URL)
	conn, br = kernelChannels(t, s, got.URL, k)
	defer conn.Close()
	go answerPings(conn, br)
	opened := time.Now()

	stopped := s.waitFor(idle.Name, 5*time.Minute, func(a app) bool { return a.Phase == "Stopped" })
	t.Logf("%s stopped %s after its kernel opened", idle.Name, time.Since(opened).Round(time.Second))
	if first := stopped.Steps[0]; first.Reason != "StoppedIdle" {
		t.Errorf("stopped app's step: %+v", first)
	}

	// By now (over 90 seconds) the busy app's idle kernel was shut down
	// although its websocket is open, and the one running code was not;
	// the app runs.
	var now idleApp
	s.json(http.MethodGet, "/api/workspaces/"+busy.Name, nil, http.StatusOK, &now)
	if now.Phase != "Running" {
		t.Errorf("%s is %s, want Running: its kernel runs code", busy.Name, now.Phase)
	}
	var kernels []struct {
		ID    string
		State string `json:"execution_state"`
	}
	s.json(http.MethodGet, busyURL+"api/kernels", nil, http.StatusOK, &kernels)
	state := map[string]string{}
	for _, k := range kernels {
		state[k.ID] = k.State
	}
	if _, ok := state[ik]; ok {
		t.Errorf("the idle kernel was not shut down: %v", kernels)
	}
	if state[bk] != "busy" {
		t.Errorf("the kernel running code is %q, want busy: %v", state[bk], kernels)
	}
}

// kernelChannels opens the kernel's websocket the way JupyterLab does.
func kernelChannels(t *testing.T, s *session, base, kernel string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, br, resp := dialWebsocket(t, s, base+"api/kernels/"+kernel+"/channels?session_id=e2e")
	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("kernel websocket: status %d", resp.StatusCode)
	}
	return conn, br
}

// execute runs code in the kernel over its websocket (Jupyter's JSON
// protocol) and, unless async, waits for an ok reply.
func execute(t *testing.T, conn net.Conn, br *bufio.Reader, code string, async bool) {
	t.Helper()
	id := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	msg, _ := json.Marshal(map[string]any{
		"header":        map[string]any{"msg_id": id, "username": "e2e", "session": "e2e", "msg_type": "execute_request", "version": "5.3"},
		"parent_header": map[string]any{}, "metadata": map[string]any{}, "buffers": []any{}, "channel": "shell",
		"content": map[string]any{"code": code, "silent": false, "store_history": false, "user_expressions": map[string]any{}, "allow_stdin": false, "stop_on_error": true},
	})
	if err := writeText(conn, msg); err != nil {
		t.Fatal(err)
	}
	if async {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Minute))
	defer conn.SetReadDeadline(time.Time{})
	for {
		op, payload, err := readFrame(br)
		if err != nil {
			t.Fatalf("waiting for the reply to %s: %v", id, err)
		}
		if op == 0x9 {
			_, _ = conn.Write(append([]byte{0x8a, 0x80 | byte(len(payload)), 0, 0, 0, 0}, payload...))
			continue
		}
		var m struct {
			Header struct {
				MsgType string `json:"msg_type"`
			} `json:"header"`
			ParentHeader struct {
				MsgID string `json:"msg_id"`
			} `json:"parent_header"`
			Content json.RawMessage `json:"content"`
		}
		if op != 0x1 || json.Unmarshal(payload, &m) != nil || m.ParentHeader.MsgID != id || m.Header.MsgType != "execute_reply" {
			continue
		}
		if !strings.Contains(string(m.Content), `"status": "ok"`) && !strings.Contains(string(m.Content), `"status":"ok"`) {
			t.Fatalf("execute: %s", m.Content)
		}
		return
	}
}

// writeText sends one masked (zero mask) text frame, as a client must.
func writeText(conn net.Conn, payload []byte) error {
	head := []byte{0x81}
	switch n := len(payload); {
	case n < 126:
		head = append(head, 0x80|byte(n))
	case n < 1<<16:
		head = append(head, 0x80|126, byte(n>>8), byte(n))
	default:
		head = binary.BigEndian.AppendUint64(append(head, 0x80|127), uint64(n))
	}
	_, err := conn.Write(append(append(head, 0, 0, 0, 0), payload...))
	return err
}

// readFrame reads one unmasked server frame.
func readFrame(r *bufio.Reader) (op byte, payload []byte, err error) {
	var head [2]byte
	if _, err = io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(r, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return head[0] & 0x0f, payload, err
}
