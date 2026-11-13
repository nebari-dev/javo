//go:build e2e

package docker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

// execConn is a connection made by a program inside a container: what is
// written goes to its input, and what it prints is read.
type execConn struct {
	io.Reader
	rw io.ReadWriteCloser
}

func (c *execConn) Write(p []byte) (int, error)      { return c.rw.Write(p) }
func (c *execConn) Close() error                     { return c.rw.Close() }
func (c *execConn) LocalAddr() net.Addr              { return nil }
func (c *execConn) RemoteAddr() net.Addr             { return nil }
func (c *execConn) SetDeadline(time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(time.Time) error { return nil }

// dialThrough connects from inside the container gw, with its nc. Javo
// runs in the gateway container and connects from there itself; a test
// runs on the host, which no workload's network reaches.
func dialThrough(c *Client, gw string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		// A port nothing listens on is refused here: the nc below would
		// only end at once.
		if code, err := c.Exec(ctx, gw, []string{"nc", "-z", "-w", "1", host, port}); err != nil || code != 0 {
			return nil, fmt.Errorf("connect to %s: refused (nc exit %d, %v)", addr, code, err)
		}
		create := map[string]any{"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Cmd": []string{"nc", host, port}}
		var made struct {
			ID string `json:"Id"`
		}
		if err := c.post(ctx, "/containers/"+gw+"/exec", nil, create, &made); err != nil {
			return nil, err
		}
		resp, err := c.do(ctx, request{
			method: http.MethodPost, path: "/exec/" + made.ID + "/start", body: map[string]bool{"Detach": false, "Tty": false},
			header: http.Header{"Connection": {"Upgrade"}, "Upgrade": {"tcp"}},
		})
		if err != nil {
			return nil, err
		}
		rw, ok := resp.Body.(io.ReadWriteCloser)
		if !ok {
			resp.Body.Close()
			return nil, errors.New("the daemon did not hand over the connection")
		}
		return &execConn{Reader: &demux{src: bufio.NewReader(rw), body: rw}, rw: rw}, nil
	}
}

// javoGateway starts a container that stands in for the one Javo's
// gateway runs in: on the daemon's default network, and, unlike the
// workloads, not carrying Javo's labels.
func javoGateway(t *testing.T, c *Client, name string) string {
	t.Helper()
	ctx := testContext(t)
	body, err := ContainerBody(testSpec(name), sh("gateway", "sleep 86400"), Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	body.HostConfig.NetworkMode = "bridge"
	body.Labels = nil
	id, err := c.ContainerCreate(ctx, "javo-test-gateway."+testrun.ID[:10]+"."+name, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStart(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// testPause stands in for the runner image's "javo pause".
var testPause = Tool{Image: testImage, Command: []string{"sleep", "86400"}}

// testExecutor is an executor on the daemon behind c, with the container
// gw as its gateway and addresses from pool. It leaves its workloads
// running when the test ends: the daemon's sweep removes them.
func testExecutor(t *testing.T, c *Client, gw string, pool netip.Prefix) *Executor {
	t.Helper()
	x, err := build(testContext(t), Options{
		Client: c, Secrets: testSecrets, Gateway: gw, RunnerImage: testImage, Pool: pool, Grace: time.Second,
	}, internals{pause: testPause, dial: dialThrough(c, gw), every: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	return x
}

// testSecrets are the bundles of the daemon tests.
var testSecrets = &executortest.Secrets{}

// fit gives a workload the user and the memory limit every container
// needs.
func fit(s *workload.Spec) {
	limits := testSpec("x")
	s.User, s.Resources = limits.User, limits.Resources
}

// eventually waits for the workload's state to pass ok, and returns it.
func eventually(t *testing.T, x *Executor, ref workload.Ref, what string, ok func(*workload.State) bool) *workload.State {
	t.Helper()
	var st *workload.State
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		var err error
		if st, err = x.Observe(testContext(t), ref); err != nil {
			t.Fatal(err)
		}
		if ok(st) {
			return st
		}
	}
	t.Fatalf("never %s; last state %+v", what, st)
	return nil
}

func isRunning(st *workload.State) bool { return st != nil && st.Phase == "Running" }

// unitID is the id of the container that runs the unit now.
func unitID(t *testing.T, c *Client, ref workload.Ref, unit string) string {
	t.Helper()
	name, _ := ContainerName(ref, unit)
	if unit == Sandbox {
		name, _ = SandboxName(ref)
	}
	have, err := c.ContainerInspect(testContext(t), name)
	if err != nil {
		return ""
	}
	return have.ID
}

// left is what the daemon still holds of the workload: containers,
// networks and volumes that end with it.
func left(t *testing.T, c *Client, ref workload.Ref) []string {
	t.Helper()
	ctx := testContext(t)
	var out []string
	containers, err := c.ContainerList(ctx, Selector(ref)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range containers {
		out = append(out, x.Names...)
	}
	networks, err := c.NetworkList(ctx, Selector(ref)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range networks {
		out = append(out, "network "+x.Name)
	}
	volumes, err := c.VolumeList(ctx, append(Selector(ref), LabelEnds+"=true")...)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range volumes {
		out = append(out, "volume "+x.Name)
	}
	return out
}

// The Docker executor does what every executor must.
func TestDaemonExecutorConformance(t *testing.T) {
	c, _ := daemon(t)
	gw := javoGateway(t, c, "conformance")
	executortest.Run(t, func(t *testing.T) executortest.Target {
		return executortest.Target{Executor: testExecutor(t, c, gw, testPool), Secrets: testSecrets, Image: testImage, Fit: fit, Namespace: testNamespace}
	})
}

// web is a main unit that serves a page on port 8080.
func web(page string) workload.Unit {
	u := sh(workload.MainUnit, "mkdir /tmp/w; echo "+page+" > /tmp/w/index.html; exec httpd -f -p 8080 -h /tmp/w")
	u.Ready = &workload.Check{HTTPPath: "/"}
	return u
}

// A helper's check runs inside the helper, the main unit starts once it
// passes and gets values of a bundle as variables and as files, and Stop
// removes all of it but the person's storage.
func TestDaemonExecutorRunsAWorkload(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "run")
	x := testExecutor(t, c, gw, testPool)

	s := testSpec("run")
	s.Port = 8080
	if err := testSecrets.Put(ctx, s.Ref, "run-tokens", executor.Bundle{Data: map[string][]byte{"token": []byte("t0p-s3cr3t-5512")}}); err != nil {
		t.Fatal(err)
	}
	home := testNamespace + "-run-home"
	if err := c.VolumeCreate(ctx, home, Labels(s.Ref)); err != nil {
		t.Fatal(err)
	}
	if err := Own(ctx, c, s, testImage, home, 0o700); err != nil {
		t.Fatal(err)
	}
	s.Volumes = []workload.Volume{
		{Name: "home", Source: home},
		{Name: "scratch"},
		{Name: "tokens", Secret: &workload.SecretFiles{Bundle: "run-tokens"}},
	}
	// Only the helper itself has /tmp/ready.
	helper := sh("helper", "sleep 1; touch /tmp/ready; sleep 3600")
	helper.Stays, helper.Ready = true, &workload.Check{Exec: []string{"test", "-e", "/tmp/ready"}}
	s.Steps = []workload.Unit{
		sh("first", "echo from-first > /scratch/first", workload.Mount{Volume: "scratch", Path: "/scratch"}),
		helper,
	}
	s.Main = sh(workload.MainUnit, `mkdir /tmp/w
echo "env=$TOKEN file=$(cat /tokens/token) first=$(cat /scratch/first) uid=$(id -u)" > /tmp/w/index.html
echo kept > /home/user/kept
exec httpd -f -p 8080 -h /tmp/w`,
		workload.Mount{Volume: "home", Path: "/home/user"},
		workload.Mount{Volume: "scratch", Path: "/scratch"},
		workload.Mount{Volume: "tokens", Path: "/tokens", ReadOnly: true})
	s.Main.Env = []workload.Env{{Name: "TOKEN", From: &workload.SecretRef{Bundle: "run-tokens", Key: "token"}}}
	s.Main.Ready = &workload.Check{HTTPPath: "/"}
	s.Fingerprint = workload.Fingerprint{Run: "r1", Size: "small"}

	addr, err := x.Addr(ctx, s.Ref, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	st := eventually(t, x, s.Ref, "running", isRunning)
	if st.Fingerprint != s.Fingerprint || !st.Running {
		t.Fatalf("state %+v", st)
	}
	// The gateway reaches the app at the address named before it ran.
	code, err := c.Exec(ctx, gw, []string{"sh", "-c", "wget -q -T 5 -O - http://" + addr + "/ | grep -q 'env=t0p-s3cr3t-5512 file=t0p-s3cr3t-5512 first=from-first uid=1000'"})
	if err != nil || code != 0 {
		t.Fatalf("the gateway's fetch of http://%s/: exit %d, %v", addr, code, err)
	}
	// No container of the workload holds the value in its description
	// but as the variable that was asked for, and none publishes a port.
	for _, unit := range []string{Sandbox, "first", "helper", workload.MainUnit} {
		var raw struct {
			HostConfig struct {
				PortBindings map[string]any
				Binds        []string
				Privileged   bool
			}
		}
		if err := c.get(ctx, "/containers/"+unitID(t, c, s.Ref, unit)+"/json", nil, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw.HostConfig.PortBindings) != 0 || len(raw.HostConfig.Binds) != 0 || raw.HostConfig.Privileged {
			t.Errorf("unit %s: %+v", unit, raw.HostConfig)
		}
	}

	if err := x.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	if got := left(t, c, s.Ref); len(got) != 0 {
		t.Fatalf("Stop left %v", got)
	}
	if _, err := c.VolumeInspect(ctx, home); err != nil {
		t.Fatalf("Stop removed the person's storage: %v", err)
	}
	if refs, err := x.List(ctx); err != nil || len(refs) != 0 {
		t.Fatalf("List after Stop: %v, %v", refs, err)
	}
}

// An executor made after another went away finds what that one left
// running and keeps it: nothing is started again, and a unit that then
// ends is started again from the Spec that was found.
func TestDaemonExecutorAdoptsWhatRuns(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "adopt")
	s := testSpec("adopt")
	s.Port, s.Main = 8080, web("adopted")
	s.Steps = []workload.Unit{sh("first", "true")}
	s.Fingerprint = workload.Fingerprint{Run: "r1"}
	// Half of another workload: its network and its sandbox, no main unit.
	half := testSpec("adopt-half")
	sandbox(t, c, half, gw)

	first := testExecutor(t, c, gw, testPool)
	if err := first.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	eventually(t, first, s.Ref, "running", isRunning)
	main, box := unitID(t, c, s.Ref, workload.MainUnit), unitID(t, c, s.Ref, Sandbox)
	first.Close()

	second := testExecutor(t, c, gw, testPool)
	st := eventually(t, second, s.Ref, "running", isRunning)
	if st.Fingerprint.Run != "r1" {
		t.Fatalf("fingerprint %+v", st.Fingerprint)
	}
	if unitID(t, c, s.Ref, workload.MainUnit) != main || unitID(t, c, s.Ref, Sandbox) != box {
		t.Fatal("a container was replaced: the workload was started again")
	}
	if err := second.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := left(t, c, half.Ref); len(got) != 0 {
		t.Fatalf("half a workload was left: %v", got)
	}

	// The main unit is killed from outside: out of memory, as far as
	// anyone can tell, and started again.
	if err := c.ContainerKill(ctx, main, "SIGKILL"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
		if id := unitID(t, c, s.Ref, workload.MainUnit); id != "" && id != main {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the main unit was never started again")
		}
	}
	eventually(t, second, s.Ref, "running again", isRunning)
	if unitID(t, c, s.Ref, Sandbox) != box {
		t.Fatal("the sandbox was replaced with the main unit")
	}
}

// When the sandbox dies every unit is made again in a new one.
func TestDaemonExecutorStartsOverWhenTheSandboxDies(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "sandbox")
	x := testExecutor(t, c, gw, testPool)
	s := testSpec("sandbox-dies")
	s.Port, s.Main = 8080, web("again")
	helper := sh("helper", "sleep 3600")
	helper.Stays = true
	s.Steps = []workload.Unit{helper}
	if err := x.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	eventually(t, x, s.Ref, "running", isRunning)
	before := map[string]string{}
	for _, unit := range []string{Sandbox, "helper", workload.MainUnit} {
		before[unit] = unitID(t, c, s.Ref, unit)
	}
	if err := c.ContainerKill(ctx, before[Sandbox], "SIGKILL"); err != nil {
		t.Fatal(err)
	}
	eventually(t, x, s.Ref, "running in a new sandbox", func(st *workload.State) bool {
		if !isRunning(st) {
			return false
		}
		for unit, id := range before {
			if now := unitID(t, c, s.Ref, unit); now == "" || now == id {
				return false
			}
		}
		return true
	})
	addr, _ := x.Addr(ctx, s.Ref, 8080)
	if code, err := c.Exec(ctx, gw, []string{"sh", "-c", "wget -q -T 5 -O - http://" + addr + "/ | grep -q again"}); err != nil || code != 0 {
		t.Fatalf("the gateway does not reach the app in its new sandbox: exit %d, %v", code, err)
	}
}

// A pool with room for one network runs one workload: the next Start says
// so, and succeeds once the first is stopped.
func TestDaemonExecutorSaysWhenThePoolIsFull(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "pool")
	x := testExecutor(t, c, gw, onePool)
	a, b := testSpec("pool-a"), testSpec("pool-b")
	a.Main, b.Main = sh(workload.MainUnit, "sleep 3600"), sh(workload.MainUnit, "sleep 3600")
	if err := x.Start(ctx, a); err != nil {
		t.Fatal(err)
	}
	err := x.Start(ctx, b)
	if err == nil || !errors.Is(err, ErrPoolFull) || !strings.Contains(err.Error(), onePool.String()) {
		t.Fatalf("Start in a full pool: %v", err)
	}
	if st, _ := x.Observe(ctx, b.Ref); st != nil {
		t.Fatalf("a workload that did not start has a state: %+v", st)
	}
	if err := x.Stop(ctx, a.Ref); err != nil {
		t.Fatal(err)
	}
	if err := x.Start(ctx, b); err != nil {
		t.Fatalf("Start once the pool has room: %v", err)
	}
}

// What cannot run is refused by Start, or shows in the state with the
// reason the platform has for it.
func TestDaemonExecutorRefusesAndDiagnoses(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "refuse")
	x := testExecutor(t, c, gw, testPool)

	run := testSpec("refuse-run")
	run.Main, run.Batch = sh(workload.MainUnit, "true"), &workload.Batch{}
	if err := x.Start(ctx, run); !errors.Is(err, ErrBatch) {
		t.Fatalf("Start of a job run: %v", err)
	}
	root := testSpec("refuse-root")
	root.Main, root.User.UID = sh(workload.MainUnit, "true"), 0
	if err := x.Start(ctx, root); err == nil {
		t.Fatal("a workload that runs as root was started")
	}
	if got := left(t, c, root.Ref); len(got) != 0 {
		t.Fatalf("a refused workload left %v", got)
	}

	noImage := testSpec("refuse-image")
	noImage.Main = sh(workload.MainUnit, "true")
	noImage.Main.Image = "localhost:1/javo-test/none:1"
	if err := x.Start(ctx, noImage); err != nil {
		t.Fatal(err)
	}
	st := eventually(t, x, noImage.Ref, "failed", func(st *workload.State) bool { return st != nil && st.Phase == "Failed" })
	if st.Step != model.ConditionImageReady || st.Code != reasons.ImagePullFailed {
		t.Fatalf("failed at %q with %q", st.Step, st.Code)
	}

	// More memory than its limit: the kernel kills it.
	oom := testSpec("refuse-oom")
	oom.Main = sh(workload.MainUnit, "tail /dev/zero")
	if err := x.Start(ctx, oom); err != nil {
		t.Fatal(err)
	}
	st = eventually(t, x, oom.Ref, "out of memory", func(st *workload.State) bool { return st != nil && st.Phase == "Failed" })
	if st.Code != reasons.OutOfMemory {
		t.Fatalf("failed with %q: %q", st.Code, st.Evidence)
	}
}

// List names a workload that has nothing but its network, and Watch
// tells of a container that changed behind the executor's back.
func TestDaemonExecutorListsAndWatchesTheDaemon(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "list")
	x := testExecutor(t, c, gw, testPool)
	s := testSpec("list-net")
	if err := Isolate(ctx, c, s.Ref, gw, testPool); err != nil {
		t.Fatal(err)
	}
	refs, err := x.List(ctx)
	if err != nil || !slices.Contains(refs, s.Ref) {
		t.Fatalf("List: %v, %v", refs, err)
	}

	told := make(chan workload.Ref, 100)
	watching, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = x.Watch(watching, func(r workload.Ref) { told <- r }) }()
	// Watch listens from a moment the test cannot see: containers are
	// made until one is reported.
	other := testSpec("list-other")
	up(t, c, other)
	for deadline := time.After(time.Minute); ; {
		id := start(t, c, other, sh("app", "true"))
		select {
		case r := <-told:
			if r == other.Ref {
				return
			}
		case <-deadline:
			t.Fatal("never told of the container")
		case <-time.After(200 * time.Millisecond):
		}
		if err := c.ContainerRemove(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
}
