//go:build linux || darwin

package process

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/nebari-dev/javo/internal/executor/executortest"
)

// shallow is m with a state directory of its own in /tmp, where a path is
// short enough for tmux's socket to be kept in a workload's folder (a
// test's temporary folder on macOS is too deep for that).
func (m *machine) shallow(t *testing.T) *machine {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "javo-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	x, err := New(Options{Dir: dir, Secrets: &executortest.Secrets{}, RunnerImage: e2eRunner, Pixi: m.pixi, Files: m.x.eng.files})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	x.eng.self = m.x.eng.self
	short := *m
	short.x = x
	return &short
}

// within polls ok until it holds, for no longer than a slow machine needs.
func within(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never %s", what)
}

// process is a line of the machine's process table.
type process struct {
	pid, parent int
	command     string
}

func processTable(t *testing.T) []process {
	t.Helper()
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var all []process
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		parent, _ := strconv.Atoi(f[1])
		all = append(all, process{pid: pid, parent: parent, command: strings.Join(f[2:], " ")})
	}
	return all
}

// runs reports whether p runs the program called name.
func (p process) runs(name string) bool {
	return slices.ContainsFunc(strings.Fields(p.command), func(w string) bool { return filepath.Base(w) == name })
}

// below are the processes of all that pid started, and those they
// started, for as long as each one's parent is still there.
func below(all []process, pid int) []process {
	var out []process
	for _, p := range all {
		if p.parent == pid {
			out = append(out, p)
			out = append(out, below(all, p.pid)...)
		}
	}
	return out
}

// started are processes by pid, each with the time it started: the two
// together name a process (startTime).
type started map[int]string

func (s started) add(t *testing.T, pids ...int) {
	t.Helper()
	for _, pid := range pids {
		began, err := startTime(pid)
		if err != nil {
			t.Fatalf("process %d: %v", pid, err)
		}
		s[pid] = began
	}
}

// left are the processes of s that are still there.
func (s started) left() []int {
	var left []int
	for pid, began := range s {
		if now, err := startTime(pid); err == nil && now == began {
			left = append(left, pid)
		}
	}
	return left
}

// record is what the executor noted of the app's main unit: its pid (ttyd,
// for a terminal template) and the mark it was started with.
func (app served) record(t *testing.T) (leader int, mark string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(app.folder, "pids", app.spec.Main.Name))
	if err != nil {
		t.Fatal(err)
	}
	var began string
	if _, err := fmt.Sscanf(string(b), "%d %s %s", &leader, &began, &mark); err != nil {
		t.Fatalf("the unit's record is %q: %v", b, err)
	}
	return leader, mark
}

// carrying are the processes that have mark in their environment.
func carrying(mark string) []int {
	var pids []int
	for _, p := range processes() {
		if env, err := environ(p.pid); err == nil && bytes.Contains(env, []byte("\x00"+markEnv+"="+mark+"\x00")) {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

// runningNow are the processes of this machine that run the program
// named, before an app is started: the person's own, when they use it.
func runningNow(t *testing.T, program string) started {
	t.Helper()
	own := started{}
	for _, p := range processTable(t) {
		if p.runs(program) {
			if began, err := startTime(p.pid); err == nil {
				own[p.pid] = began
			}
		}
	}
	return own
}

// socketLimit is how long the path of a socket may be for socket-dir.sh
// to leave it in the workload's folder.
const socketLimit = 103

// socketFolders are the folders socket-dir.sh has made in /tmp for
// sockets that did not fit in a workload's folder.
func socketFolders() []string {
	dirs, _ := filepath.Glob("/tmp/javo-sock.*")
	return dirs
}

// socketFolder is where a program of the app keeps the socket called
// name, when the plan names dir for it: dir, which is in the workload's
// folder, or the one folder made in /tmp since before, when the socket
// does not fit in dir. made says which.
func (app served) socketFolder(t *testing.T, before []string, dir, name string) (folder string, made bool) {
	t.Helper()
	if !strings.HasPrefix(dir, app.folder+string(filepath.Separator)) {
		t.Fatalf("the socket's folder is %q, outside the workload's folder %s", dir, app.folder)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	fresh := slices.DeleteFunc(socketFolders(), func(d string) bool { return slices.Contains(before, d) })
	if len(filepath.Join(real, name)) <= socketLimit {
		if len(fresh) != 0 {
			t.Errorf("the socket fits in the workload's folder, and yet there is %q", fresh)
		}
		return real, false
	}
	if len(fresh) != 1 {
		t.Fatalf("%s is too deep for a socket, and the new folders for it are %q", real, fresh)
	}
	if info, err := os.Stat(fresh[0]); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the socket's folder %s: %v, %v", fresh[0], info.Mode(), err)
	}
	if real, err = filepath.EvalSymlinks(fresh[0]); err != nil {
		t.Fatal(err)
	}
	return real, true
}

// envOf is the value the app's main unit has for the variable name.
func (app served) envOf(name string) string {
	for _, e := range app.spec.Main.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// openTab opens the app's terminal as a browser tab does: ttyd's
// websocket, from the page's own origin, then the size of the terminal,
// on which ttyd starts the tab's program. What the program prints is read
// and dropped until the tab is closed.
func openTab(t *testing.T, app served) (closeTab func()) {
	t.Helper()
	cfg, err := websocket.NewConfig("ws://"+app.addr+"/ws", "http://"+app.addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Protocol = []string{"tty"}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("opening a tab: %v", err)
	}
	if err := websocket.Message.Send(ws, `{"AuthToken":"","columns":120,"rows":40}`); err != nil {
		t.Fatalf("opening a tab: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, ws)
	}()
	return func() {
		_ = ws.Close()
		<-done
	}
}

// agent looks at a coding agent's app, whose agent is the program named:
// its session is in a tmux server of the app's own, with its socket in
// the workload's folder (or, where that is too deep for a socket, in a
// new folder in /tmp), before any tab is open; a tab attaches to it, and
// the agent is the same process after the tab closed and another opened.
// Once the app is stopped, ttyd, the tmux server, the agent and whatever
// else the app started are gone, and the tmux servers that ran before
// the app, the person's own, still run.
func (m *machine) agent(t *testing.T, program string) func(*testing.T, served) func() {
	t.Helper()
	own := runningNow(t, "tmux")
	before := socketFolders()
	return func(t *testing.T, app served) func() {
		t.Helper()
		sub := filepath.Join(fmt.Sprintf("tmux-%d", os.Getuid()), "default")
		folder, made := app.socketFolder(t, before, app.envOf("TMUX_TMPDIR"), sub)
		socket := filepath.Join(folder, sub)
		t.Logf("tmux's socket: %s", socket)

		// The environment's own tmux, on the app's socket and no other.
		tmux := func(args ...string) (string, error) {
			script := `. "$1/activate.sh" && shift && exec tmux "$@"`
			cmd := exec.Command("bash", append([]string{"-c", script, "bash", app.envOf("JAVO_ENV_DIR"), "-S", socket}, args...)...)
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + m.home}
			out, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		// session is the tmux server, the first process of the agent's
		// window and how many tabs are attached.
		session := func() (server, pane, attached int) {
			out, err := tmux("list-panes", "-t", "agent", "-F", "#{pid} #{pane_pid} #{session_attached} #{socket_path}")
			if err != nil {
				return 0, 0, -1
			}
			var at string
			if _, err := fmt.Sscanf(out, "%d %d %d %s", &server, &pane, &attached, &at); err != nil || at != socket {
				t.Fatalf("the agent's session: %q (%v), want one window on %s", out, err, socket)
			}
			return server, pane, attached
		}

		// No tab was opened: the session is there since the app started.
		var server, pane int
		within(t, "a session called agent on the app's own socket", func() bool {
			server, pane, _ = session()
			return server != 0
		})
		if _, mine := own[server]; mine {
			t.Fatalf("the agent's session is in the tmux server %d, which ran before the app", server)
		}
		var agent int
		within(t, "running "+program+" in the session", func() bool {
			for _, p := range below(processTable(t), pane) {
				if p.runs(program) {
					agent = p.pid
					return true
				}
			}
			return false
		})

		// A tab attaches to the session; closing it leaves the agent
		// running, and the next tab finds the same one.
		for range 2 {
			closeTab := openTab(t, app)
			within(t, "a tab attached to the session", func() bool { _, _, n := session(); return n == 1 })
			closeTab()
			within(t, "the tab detached", func() bool { _, _, n := session(); return n == 0 })
			if s, p, _ := session(); s != server || p != pane {
				t.Fatalf("after a tab closed the session is in server %d with pane %d, was %d and %d", s, p, server, pane)
			}
		}

		leader, mark := app.record(t)
		mine := started{}
		mine.add(t, leader, server, pane)
		for _, p := range below(processTable(t), pane) {
			if began, err := startTime(p.pid); err == nil {
				mine[p.pid] = began
			}
		}
		if _, ok := mine[agent]; !ok {
			t.Fatalf("%s (process %d) is gone before the app is stopped", program, agent)
		}
		t.Logf("the app's processes: ttyd %d, tmux server %d, the window's first %d, %s %d; %d in all", leader, server, pane, program, agent, len(carrying(mark)))

		return func() {
			t.Helper()
			gone(t, mine, mark)
			if left := own.left(); len(left) != len(own) {
				t.Errorf("of the tmux servers that ran before the app, %d are left of %d", len(left), len(own))
			}
			t.Logf("tmux servers that ran before the app and still do: %d", len(own))
			if made {
				// It stays (socket-dir.sh); this one was the test's.
				_ = os.RemoveAll(folder)
			}
		}
	}
}

// gone waits until none of the app's processes is left and nothing
// carries its mark.
func gone(t *testing.T, mine started, mark string) {
	t.Helper()
	within(t, "rid of what the app started", func() bool { return len(mine.left()) == 0 })
	if left := carrying(mark); len(left) != 0 {
		t.Errorf("processes %v still carry the app's mark", left)
	}
}

// herdr looks at the herdr app: a tab starts herdr, which keeps a server
// of its own in the background, outside the unit's process group, with
// its socket in the workload's folder (or, where that is too deep, in a
// new folder in /tmp). The server stays when the tab closes, and the
// next tab finds it. Once the app is stopped none of it is left, and
// the herdr that ran before the app, the person's own, still runs.
func (m *machine) herdr(t *testing.T) func(*testing.T, served) func() {
	t.Helper()
	own := runningNow(t, "herdr")
	before := socketFolders()
	return func(t *testing.T, app served) func() {
		t.Helper()
		leader, mark := app.record(t)
		// server is herdr's server of this app, once it listens.
		var folder string
		var made bool
		server := func() int {
			var pids []int
			for _, p := range processTable(t) {
				if p.runs("herdr") && strings.HasSuffix(p.command, " server") && slices.Contains(carrying(mark), p.pid) {
					pids = append(pids, p.pid)
				}
			}
			if len(pids) > 1 {
				t.Fatalf("the app has %d herdr servers: %v", len(pids), pids)
			}
			if _, err := os.Stat(filepath.Join(folder, "herdr.sock")); err != nil || len(pids) == 0 {
				return 0
			}
			return pids[0]
		}
		closeTab := openTab(t, app)
		// herdr.sh picks the folder when the tab starts it.
		within(t, "starting herdr in the tab", func() bool {
			return slices.ContainsFunc(below(processTable(t), leader), func(p process) bool { return p.runs("herdr") })
		})
		folder, made = app.socketFolder(t, before, app.envOf("TMPDIR"), "herdr-client.sock")
		t.Logf("herdr's socket: %s", filepath.Join(folder, "herdr.sock"))
		var pid int
		within(t, "running a herdr server on the app's own socket", func() bool { pid = server(); return pid != 0 })
		if pgid, err := syscall.Getpgid(pid); err != nil || pgid == leader {
			t.Fatalf("herdr's server is in the process group %d (%v), the unit's", pgid, err)
		}
		mine := started{}
		mine.add(t, leader, pid)
		closeTab()
		within(t, "the tab's herdr gone", func() bool {
			return !slices.ContainsFunc(below(processTable(t), leader), func(p process) bool { return p.runs("herdr") })
		})
		closeTab = openTab(t, app)
		within(t, "starting herdr in the next tab", func() bool {
			return slices.ContainsFunc(below(processTable(t), leader), func(p process) bool { return p.runs("herdr") })
		})
		// The tab was told the first one's folder, where the server is.
		if again := server(); again != pid {
			t.Fatalf("the next tab has the herdr server %d, the first had %d", again, pid)
		}
		if fresh := slices.DeleteFunc(socketFolders(), func(d string) bool { return slices.Contains(before, d) }); len(fresh) > 1 {
			t.Fatalf("the tabs made the socket folders %q, want one", fresh)
		}
		// Left open: the app is stopped with a tab on it.
		t.Cleanup(closeTab)
		for _, p := range carrying(mark) {
			mine.add(t, p)
			for _, c := range below(processTable(t), p) {
				if began, err := startTime(c.pid); err == nil {
					mine[c.pid] = began
				}
			}
		}
		t.Logf("the app's processes: ttyd %d, herdr's server %d; %d in all", leader, pid, len(mine))
		return func() {
			t.Helper()
			gone(t, mine, mark)
			if left := own.left(); len(left) != len(own) {
				t.Errorf("of the herdr processes that ran before the app, %d are left of %d", len(left), len(own))
			}
			if made {
				_ = os.RemoveAll(folder)
			}
		}
	}
}
