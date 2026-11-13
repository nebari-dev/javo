//go:build linux || darwin

package process_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	"github.com/nebari-dev/javo/internal/executor/process"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// selfEnv makes this test binary, run as a unit, print selfWords and exit.
const (
	selfEnv   = "JAVO_TEST_ACT_AS_JAVO"
	selfWords = "the running binary, with"
)

// listenEnv makes this test binary, run as a unit, serve HTTP on the
// address it holds.
const listenEnv = "JAVO_TEST_LISTEN"

// daemonEnv makes this test binary, run by a unit, a daemon: it leaves the
// unit's session and process group, prints its pid and stays, for no
// longer than a failing test waits for it to go.
// With the value daemonWithChild it first starts a process with an empty
// environment and prints that one's pid too. Once it has printed, it makes
// the file daemonUpEnv names, if any.
const (
	daemonEnv       = "JAVO_TEST_DAEMON"
	daemonWithChild = "with a child"
	daemonUpEnv     = "JAVO_TEST_DAEMON_UP"
)

func TestMain(m *testing.M) {
	if os.Getenv(daemonEnv) != "" {
		if _, err := syscall.Setsid(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println(os.Getpid())
		if os.Getenv(daemonEnv) == daemonWithChild {
			// A process of the daemon's that has nothing of its environment.
			child := exec.Command("sleep", "60")
			child.Env = []string{}
			if err := child.Start(); err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
			fmt.Println(child.Process.Pid)
		}
		if up := os.Getenv(daemonUpEnv); up != "" {
			_ = os.WriteFile(up, nil, 0o600)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	if addr := os.Getenv(listenEnv); addr != "" {
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		_ = http.Serve(lis, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "hello") }))
		os.Exit(1)
	}
	if os.Getenv(selfEnv) != "" {
		fmt.Println(selfWords, strings.Join(os.Args[1:], " "))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var ref = workload.Ref{Namespace: "alice", Name: "notes", ID: "a-1", Owner: "alice"}

// quick is the tests' wait between two readiness checks and before a
// restart, in place of the second an install waits.
const quick = 50 * time.Millisecond

// build makes an executor on a fresh state directory and closes it when
// the test ends.
func build(t *testing.T, o process.Options) (*process.Executor, *executortest.Secrets) {
	t.Helper()
	secrets := &executortest.Secrets{}
	if o.Dir == "" {
		// New makes it, closed to everyone else.
		o.Dir = filepath.Join(t.TempDir(), "state")
	}
	o.Secrets = secrets
	o.Every, o.Backoff = quick, quick
	x, err := process.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	return x, secrets
}

// sh is a unit that runs script in the machine's sh.
func sh(name, script string, args ...string) workload.Unit {
	return workload.Unit{Name: name, Command: []string{"sh", "-c", script, "sh"}, Args: args}
}

// app is a workload of ref whose main unit runs script.
func app(script string, args ...string) workload.Spec {
	return workload.Spec{Ref: ref, Main: sh(workload.MainUnit, script, args...)}
}

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never %s", what)
}

// output is what the unit of ref printed so far.
func output(t *testing.T, x *process.Executor, o executor.LogOptions) string {
	t.Helper()
	rc, err := x.Logs(t.Context(), ref, o)
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// printed waits until the main unit has printed n lines, and returns them.
func printed(t *testing.T, x *process.Executor, n int) []string {
	t.Helper()
	var lines []string
	until(t, fmt.Sprintf("printing %d lines", n), func() bool {
		lines = strings.Split(strings.TrimSuffix(output(t, x, executor.LogOptions{}), "\n"), "\n")
		return len(lines) >= n && lines[0] != ""
	})
	return lines
}

// refused starts s and wants it refused as something that cannot run
// without containers, about the unit named, and nothing started.
func refused(t *testing.T, x *process.Executor, s workload.Spec, unit, why string) {
	t.Helper()
	err := x.Start(t.Context(), s)
	var no *process.UnsupportedError
	if !errors.As(err, &no) {
		t.Fatalf("Start: %v, want an *UnsupportedError", err)
	}
	if no.Unit != unit || !strings.Contains(no.Why, why) {
		t.Fatalf("refused unit %q because %q, want unit %q and %q", no.Unit, no.Why, unit, why)
	}
	if st, _ := x.Observe(t.Context(), s.Ref); st != nil {
		t.Fatalf("a refused workload has a state: %+v", st)
	}
}

// alive reports whether a process pid exists.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func pidOf(t *testing.T, line string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || pid <= 1 {
		t.Fatalf("%q is not a pid", line)
	}
	return pid
}

func TestConformance(t *testing.T) {
	executortest.Run(t, func(t *testing.T) executortest.Target {
		x, secrets := build(t, process.Options{})
		return executortest.Target{Executor: x, Secrets: secrets}
	})
}

// A unit that needs an image is refused, and a job run is not an app.
func TestRefusesImagesAndRuns(t *testing.T) {
	x, _ := build(t, process.Options{RunnerImage: "registry.example.com/javo:1"})
	s := app("sleep 300")
	s.Main.Image = "registry.example.com/python:3"
	refused(t, x, s, workload.MainUnit, "needs the image registry.example.com/python:3")

	// The platform's own image stands for one program only.
	s.Main.Image = "registry.example.com/javo:1"
	refused(t, x, s, workload.MainUnit, "needs the image")

	s = app("sleep 300")
	s.Main.Command, s.Main.Args = nil, nil
	refused(t, x, s, workload.MainUnit, "names no command")

	s = app("sleep 300")
	s.Batch = &workload.Batch{}
	if err := x.Start(t.Context(), s); !errors.Is(err, process.ErrBatch) {
		t.Fatalf("a job run: %v, want ErrBatch", err)
	}
}

// A step of the platform's own image that runs /javo runs the binary Javo
// itself runs from.
func TestPlatformStepsRunTheRunningBinary(t *testing.T) {
	x, _ := build(t, process.Options{RunnerImage: "registry.example.com/javo:1"})
	s := app("sleep 300")
	s.Steps = []workload.Unit{{
		Name: "javo-files", Image: "registry.example.com/javo:1",
		Command: []string{"/javo", "pod-files"}, Args: []string{"--to", "there"},
		Env: []workload.Env{{Name: selfEnv, Value: "1"}},
	}}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	want := selfWords + " pod-files --to there\n"
	until(t, "run by this binary", func() bool { return output(t, x, executor.LogOptions{Unit: "javo-files"}) == want })
}

// The step that writes the container's passwd file is left out with the
// mounts of what it writes: this machine knows its user.
func TestContainerIdentityStepIsLeftOut(t *testing.T) {
	x, _ := build(t, process.Options{})
	s := app("echo up; sleep 300")
	s.Volumes = []workload.Volume{{Name: "javo-etc"}}
	s.Steps = []workload.Unit{{
		Name: podfiles.IdentityContainer, Image: "registry.example.com/python:3",
		Command: []string{"sh", "-c", "exit 1"},
		Mounts:  []workload.Mount{{Volume: "javo-etc", Path: "/opt/javo-etc"}},
	}}
	s.Main.Mounts = []workload.Mount{{Volume: "javo-etc", Path: "/etc/passwd", SubPath: "passwd", ReadOnly: true}}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	printed(t, x, 1)
	if got := output(t, x, executor.LogOptions{Unit: podfiles.IdentityContainer}); got != "" {
		t.Fatalf("the identity step ran: %q", got)
	}
}

// A volume is a folder of this machine, shared by the units that mount it
// at that folder; a mount at any other path is refused, and so is a
// working folder outside the unit's mounts.
func TestVolumesAreFoldersAndMountsMustNameThem(t *testing.T) {
	x, _ := build(t, process.Options{})
	home := t.TempDir()
	scratch := x.VolumeDir(ref, "scratch")
	cache := filepath.Join(scratch, "cache")
	s := app(`cat "$1/note" "$2/note"; pwd; sleep 300`, home, cache)
	s.Volumes = []workload.Volume{{Name: "home", Source: home}, {Name: "scratch"}}
	mounts := []workload.Mount{{Volume: "home", Path: home}, {Volume: "scratch", Path: cache, SubPath: "cache"}}
	s.Steps = []workload.Unit{sh("write", `echo at-home > "$1/note"; echo in-scratch > "$2/note"`, home, cache)}
	s.Steps[0].Mounts, s.Main.Mounts = mounts, mounts
	s.Main.Dir = home
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	// The shell prints the folder as the kernel names it.
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := printed(t, x, 3); got[0] != "at-home" || got[1] != "in-scratch" || got[2] != real {
		t.Fatalf("the main unit read %q", got)
	}
	if err := x.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(scratch); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the empty volume outlived the workload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "note")); err != nil {
		t.Fatalf("the home did not: %v", err)
	}

	for _, c := range []struct {
		why    string
		change func(*workload.Spec)
	}{
		{"cannot be shown anywhere else", func(s *workload.Spec) { s.Main.Mounts[0].Path = "/home/user" }},
		{"cannot be shown anywhere else", func(s *workload.Spec) { s.Main.Mounts[1].SubPath = "" }},
		{"not a folder inside it", func(s *workload.Spec) { s.Main.Mounts[1].SubPath = "../../other" }},
		{"which the workload does not have", func(s *workload.Spec) { s.Main.Mounts[0].Volume = "group-data" }},
		{"in none of its volumes", func(s *workload.Spec) { s.Main.Dir = "/home/user" }},
	} {
		bad := s
		bad.Main.Mounts = append([]workload.Mount{}, mounts...)
		c.change(&bad)
		refused(t, x, bad, workload.MainUnit, c.why)
	}
	bad := s
	bad.Volumes = []workload.Volume{{Name: "home", Source: "home/of/alice"}, {Name: "scratch"}}
	refused(t, x, bad, "", "not a folder of this machine")
}

// A workload runs as Javo: one that names another user is refused.
func TestRefusesAnotherUser(t *testing.T) {
	x, _ := build(t, process.Options{})
	s := app("sleep 300")
	s.User = workload.User{UID: int64(os.Getuid()), GID: int64(os.Getgid())}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatalf("as Javo's own user: %v", err)
	}
	_ = x.Stop(t.Context(), ref)
	s.User.UID++
	refused(t, x, s, "", "runs as Javo itself")
}

// The values of a bundle reach a unit as variables and as files only the
// user can read, and are written nowhere else under the state directory.
// The rest of Javo's environment does not reach a unit.
func TestSecretsReachTheUnitAndNothingElse(t *testing.T) {
	const value = "v3ry-s3cr3t-2206"
	t.Setenv("JAVO_DATABASE_URL", "postgres://example.com/"+value)
	dir := filepath.Join(t.TempDir(), "state")
	x, secrets := build(t, process.Options{Dir: dir})
	if err := secrets.Put(t.Context(), ref, "tokens", executor.Bundle{Data: map[string][]byte{"token": []byte(value), "other": []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	files := x.VolumeDir(ref, "tokens")
	s := app(`[ "$TOKEN" = "$(cat "$1/token")" ] && echo same; echo "db=[$JAVO_DATABASE_URL]"; ls "$1"; sleep 300`, files)
	s.Volumes = []workload.Volume{{Name: "tokens", Secret: &workload.SecretFiles{Bundle: "tokens", Keys: []string{"token"}, WorldReadable: true}}}
	s.Main.Mounts = []workload.Mount{{Volume: "tokens", Path: files, ReadOnly: true}}
	s.Main.Env = []workload.Env{{Name: "TOKEN", From: &workload.SecretRef{Bundle: "tokens", Key: "token"}}}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if got := printed(t, x, 3); got[0] != "same" || got[1] != "db=[]" || got[2] != "token" {
		t.Fatalf("the unit printed %q", got)
	}

	var holding []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if strings.Contains(string(b), value) {
			holding = append(holding, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(files, "token")
	if len(holding) != 1 || holding[0] != file {
		t.Fatalf("the value is in %q, want only %s", holding, file)
	}
	for p, want := range map[string]fs.FileMode{file: 0o600, files: 0o700, dir: 0o700} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s: %v, %v; want mode %o", p, info.Mode().Perm(), err, want)
		}
	}

	// A bundle that is not there yet: optional files are left out, a
	// variable cannot be.
	_ = x.Stop(t.Context(), ref)
	s.Volumes[0].Secret = &workload.SecretFiles{Bundle: "later", Optional: true}
	s.Main.Env[0].From.Bundle = "later"
	if err := x.Start(t.Context(), s); err == nil || strings.Contains(err.Error(), value) || !strings.Contains(err.Error(), "no bundle later") {
		t.Fatalf("a variable from a missing bundle: %v", err)
	}
}

// The state directory must be the user's alone, and root runs nothing.
func TestRefusesAnOpenStateDirectory(t *testing.T) {
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := process.New(process.Options{Dir: open}); err == nil {
		t.Fatal("a state directory others can read was accepted")
	}
	if _, err := process.New(process.Options{Dir: "state"}); err == nil {
		t.Fatal("a relative state directory was accepted")
	}
}

// A workload's port is its own, the same for a Javo started again on the
// same state directory, and a workload planned for another port is
// refused.
func TestPortIsKeptAcrossRestartsOfJavo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	x, _ := build(t, process.Options{Dir: dir})
	other := workload.Ref{Namespace: "alice", Name: "plots"}
	addr, err := x.Addr(t.Context(), ref, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if host, _, _ := net.SplitHostPort(addr); host != "127.0.0.1" {
		t.Fatalf("address %s is not loopback", addr)
	}
	if o, err := x.Addr(t.Context(), other, 8080); err != nil || o == addr {
		t.Fatalf("another workload's address: %s, %v", o, err)
	}
	x.Close()

	again, _ := build(t, process.Options{Dir: dir})
	if got, err := again.Addr(t.Context(), ref, 9090); err != nil || got != addr {
		t.Fatalf("after a restart: %s, %v; want %s", got, err, addr)
	}
	s := app("sleep 300")
	s.Port = 8080
	refused(t, again, s, "", "its port is the one Addr names")
}

// Logs can be cut to the lines since a time, shown with their times, and
// followed; the run before a restart stays readable.
func TestLogsSinceFollowAndPrevious(t *testing.T) {
	x, _ := build(t, process.Options{})
	gate := filepath.Join(t.TempDir(), "gate")
	// Prints, waits for the gate, prints again and crashes; the next run
	// finds the gate open.
	s := app(`echo early; until [ -e "$1" ]; do sleep 0.05; done; echo late; [ -e "$1.again" ] && sleep 300; echo x > "$1.again"; exit 1`, gate)
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	printed(t, x, 1)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	follow, err := x.Logs(ctx, ref, executor.LogOptions{Follow: true, Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer follow.Close()
	buf := make([]byte, 4096)
	line := func() (time.Time, string) {
		t.Helper()
		n, err := follow.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		stamp, text, _ := strings.Cut(string(buf[:n]), " ")
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			t.Fatalf("line %q does not start with a time: %v", buf[:n], err)
		}
		return at, text
	}
	if _, text := line(); text != "early\n" {
		t.Fatalf("followed %q, want the line printed so far", text)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	at, text := line()
	if text != "late\n" {
		t.Fatalf("followed %q, want the new line", text)
	}

	until(t, "restarted after the crash", func() bool {
		return output(t, x, executor.LogOptions{Previous: true}) == "early\nlate\n" &&
			output(t, x, executor.LogOptions{}) == "early\nlate\n"
	})
	if got := output(t, x, executor.LogOptions{Previous: true, Since: at}); got != "late\n" {
		t.Fatalf("the previous run since the second line: %q", got)
	}
	if got := output(t, x, executor.LogOptions{Tail: 1, Timestamps: true}); !strings.HasSuffix(got, " late\n") {
		t.Fatalf("the last line with its time: %q", got)
	}
}

// A crashed app fails with its exit code and last lines, as on any
// executor.
func TestCrashedAppFailsWithItsOutput(t *testing.T) {
	x, _ := build(t, process.Options{})
	if err := x.Start(t.Context(), app("echo boom >&2; exit 7")); err != nil {
		t.Fatal(err)
	}
	var st *workload.State
	until(t, "failed", func() bool {
		st, _ = x.Observe(t.Context(), ref)
		return st != nil && st.Phase == "Failed"
	})
	if st.Code != reasons.AppCrashed || len(st.Evidence) != 2 || st.Evidence[0] != "unit app exited with code 7" || st.Evidence[1] != "boom" {
		t.Fatalf("%s: %q", st.Code, st.Evidence)
	}
}

// Stop ends the whole process group: a unit that ignores the polite
// request is killed after the grace period, with what it started.
func TestStopEndsTheProcessGroup(t *testing.T) {
	x, _ := build(t, process.Options{Grace: 200 * time.Millisecond})
	if err := x.Start(t.Context(), app(`trap "" TERM; sleep 300 & echo $$; echo $!; wait`)); err != nil {
		t.Fatal(err)
	}
	lines := printed(t, x, 2)
	leader, child := pidOf(t, lines[0]), pidOf(t, lines[1])
	if err := x.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if alive(leader) {
		t.Fatal("the unit outlived Stop")
	}
	until(t, "rid of the process the unit started", func() bool { return !alive(child) })
}

// When a unit's first process exits, what it left running goes with it.
func TestProcessesAUnitLeavesBehindAreKilled(t *testing.T) {
	x, _ := build(t, process.Options{})
	s := app("sleep 300")
	s.Steps = []workload.Unit{sh("spawn", `sleep 300 & echo $!`)}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	var child int
	until(t, "the step printing", func() bool {
		out := output(t, x, executor.LogOptions{Unit: "spawn"})
		if out == "" {
			return false
		}
		child = pidOf(t, out)
		return true
	})
	until(t, "rid of the process the step left", func() bool { return !alive(child) })
}

// daemon is a script that starts this test binary as a daemon (daemonEnv)
// and then runs what follows.
func daemon(then string) (script string, args []string) {
	return daemonEnv + `=1 "$1" & ` + then, []string{os.Args[0]}
}

// A process a unit started that left its process group and session, as a
// daemon does, ends when the unit is stopped.
func TestStopEndsWhatLeftTheProcessGroup(t *testing.T) {
	x, _ := build(t, process.Options{})
	script, args := daemon("exec sleep 300")
	if err := x.Start(t.Context(), app(script, args...)); err != nil {
		t.Fatal(err)
	}
	left := pidOf(t, printed(t, x, 1)[0])
	if pgid, err := syscall.Getpgid(left); err != nil || pgid != left {
		t.Fatalf("the daemon is in the group %d (%v), want one of its own", pgid, err)
	}
	if err := x.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	until(t, "rid of the daemon the unit started", func() bool { return !alive(left) })
}

// What such a daemon started ends too, also when it was started without
// the daemon's environment: it is the daemon's while the daemon is its
// parent.
func TestStopEndsWhatADaemonStarted(t *testing.T) {
	x, _ := build(t, process.Options{})
	s := app(`"$1" & exec sleep 300`, os.Args[0])
	s.Main.Env = []workload.Env{{Name: daemonEnv, Value: daemonWithChild}}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	lines := printed(t, x, 2)
	left, child := pidOf(t, lines[0]), pidOf(t, lines[1])
	if err := x.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	until(t, "rid of the daemon the unit started", func() bool { return !alive(left) })
	until(t, "rid of the process the daemon started", func() bool { return !alive(child) })
}

// So does one whose unit exited by itself, and one of a step.
func TestADaemonEndsWithTheUnitThatStartedIt(t *testing.T) {
	x, _ := build(t, process.Options{})
	// The step ends as soon as its daemon is up.
	script := daemonUpEnv + `="$2" ` + daemonEnv + `=1 "$1" & until [ -e "$2" ]; do sleep 0.01; done`
	s := app("sleep 300")
	s.Steps = []workload.Unit{sh("spawn", script, os.Args[0], filepath.Join(t.TempDir(), "up"))}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	var left int
	until(t, "the step printing", func() bool {
		out := output(t, x, executor.LogOptions{Unit: "spawn"})
		if out == "" {
			return false
		}
		left = pidOf(t, out)
		return true
	})
	until(t, "rid of the daemon the step left", func() bool { return !alive(left) })
}

// A Javo that starts on a state directory kills what the Javo before it
// left running there, and leaves alone a recorded pid that is now another
// process.
func TestNewSweepsWhatAnEarlierJavoLeft(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	// The earlier Javo: it is never closed before the next one starts.
	x, _ := build(t, process.Options{Dir: dir})
	if err := x.Start(t.Context(), app(`echo $$; exec sleep 300`)); err != nil {
		t.Fatal(err)
	}
	left := pidOf(t, printed(t, x, 1)[0])
	// A unit of the earlier Javo whose first process is gone and whose
	// daemon is not.
	other := workload.Ref{Namespace: "alice", Name: "other", ID: "a-2", Owner: "alice"}
	script, args := daemon("sleep 300")
	orphaned := app(script, args...)
	orphaned.Ref = other
	if err := x.Start(t.Context(), orphaned); err != nil {
		t.Fatal(err)
	}
	var orphan int
	until(t, "the daemon printing", func() bool {
		rc, err := x.Logs(t.Context(), other, executor.LogOptions{})
		if err != nil {
			return false
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		if len(b) == 0 {
			return false
		}
		orphan = pidOf(t, string(b))
		return true
	})

	// A record of a pid that is this test's own, started at another time.
	rec := filepath.Join(dir, "w", "alice", "gone", "pids")
	if err := os.MkdirAll(rec, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rec, "app"), fmt.Appendf(nil, "%d 1.000001\n", os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}

	build(t, process.Options{Dir: dir})
	// Still here: the stale record was not acted on.
	until(t, "rid of the process left behind", func() bool { return !alive(left) })
	until(t, "rid of the daemon left behind", func() bool { return !alive(orphan) })
	if _, err := os.Stat(filepath.Join(rec, "app")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the stale record stayed: %v", err)
	}
}

// server is a workload of ref whose main unit serves HTTP on host and the
// workload's port.
func server(t *testing.T, x *process.Executor, host string) workload.Spec {
	t.Helper()
	addr, err := x.Addr(t.Context(), ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	return workload.Spec{Ref: ref, Port: int32(n), Main: workload.Unit{
		Name: workload.MainUnit, Command: []string{os.Args[0]},
		Env:   []workload.Env{{Name: listenEnv, Value: net.JoinHostPort(host, port)}},
		Ready: &workload.Check{HTTPPath: "/"},
	}}
}

// An app that listens on loopback runs and answers.
func TestAnAppOnLoopbackRuns(t *testing.T) {
	x, _ := build(t, process.Options{})
	if err := x.Start(t.Context(), server(t, x, "127.0.0.1")); err != nil {
		t.Fatal(err)
	}
	until(t, "running", func() bool {
		st, _ := x.Observe(t.Context(), ref)
		return st != nil && st.Phase == "Running"
	})
}

// An app that listens on every address is open to the network this
// machine is on: it is killed, and its log says why.
func TestAnAppOpenToTheNetworkIsKilled(t *testing.T) {
	addrs, _ := net.InterfaceAddrs()
	outside := false
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			outside = true
		}
	}
	if !outside {
		t.Skip("this machine has no address but loopback")
	}
	x, _ := build(t, process.Options{})
	if err := x.Start(t.Context(), server(t, x, "0.0.0.0")); err != nil {
		t.Fatal(err)
	}
	var st *workload.State
	until(t, "failed", func() bool {
		st, _ = x.Observe(t.Context(), ref)
		return st != nil && st.Phase == "Failed"
	})
	if !strings.Contains(strings.Join(st.Evidence, "\n"), "must listen on 127.0.0.1 only") {
		t.Fatalf("%s: %q", st.Code, st.Evidence)
	}
}

// What a plan for this machine is made with: the workload's own folders,
// the platform's files, and, for the step that installs an environment,
// the given pixi with sha256sum and flock ahead of the machine's PATH.
func TestHostNamesTheFoldersAndTheInstallTools(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	pixi := filepath.Join(t.TempDir(), "pixi")
	if err := os.WriteFile(pixi, []byte("#!/bin/sh\necho pixi here\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := t.TempDir()
	x, _ := build(t, process.Options{Dir: dir, Pixi: pixi, Files: files})
	h := x.Host(ref)
	if h.Files != files || h.Volumes != filepath.Dir(x.VolumeDir(ref, "tmp")) || h.UID != int64(os.Getuid()) || h.GID != int64(os.Getgid()) {
		t.Fatalf("host = %+v", h)
	}
	bin := filepath.Join(dir, "bin")
	if h.InstallPath != bin+":"+os.Getenv("PATH") {
		t.Fatalf("install path = %q", h.InstallPath)
	}

	// A step with that PATH finds the three tools; the two that are Javo's
	// own run this binary under their name.
	t.Setenv(selfEnv, "1")
	s := app(`pixi; sha256sum a b; flock l true`)
	s.Main.Env = []workload.Env{{Name: "PATH", Value: h.InstallPath}, {Name: selfEnv, Value: "1"}}
	if err := x.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	want := []string{"pixi here", selfWords + " sha256sum a b", selfWords + " flock l true"}
	if got := printed(t, x, 3); !slices.Equal(got[:3], want) {
		t.Fatalf("the tools printed %q, want %q", got, want)
	}
	for _, name := range []string{"sha256sum", "flock"} {
		if info, err := os.Stat(filepath.Join(bin, name)); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v, %v", name, info, err)
		}
	}
}

func TestNewRefusesAPixiOrFilesThatAreNotThere(t *testing.T) {
	for name, o := range map[string]process.Options{
		"a relative pixi":  {Pixi: "pixi"},
		"a missing pixi":   {Pixi: filepath.Join(t.TempDir(), "pixi")},
		"a missing folder": {Files: filepath.Join(t.TempDir(), "files")},
	} {
		o.Dir = filepath.Join(t.TempDir(), "state")
		if x, err := process.New(o); err == nil {
			x.Close()
			t.Errorf("%s: no error", name)
		}
	}
}
