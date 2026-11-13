package docker

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	"github.com/nebari-dev/javo/internal/workload"
)

// testRuns is an executor of runs on the daemon behind c, and the
// executor of apps it was made from, with the container gw as their
// gateway. Both leave what they made when the test ends.
func testRuns(t *testing.T, c *Client, gw string) (*Runs, *Executor) {
	t.Helper()
	x := testExecutor(t, c, gw, testPool)
	r, err := x.Runs(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r, x
}

// job is a run of the daemon tests called name, whose main unit runs
// script.
func job(name, script string, retries int32) workload.Spec {
	s := testSpec(name)
	s.Main = sh(workload.MainUnit, script)
	s.Batch = &workload.Batch{Retries: retries}
	return s
}

func finished(t *testing.T, r *Runs, ref workload.Ref) *workload.RunState {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(50 * time.Millisecond) {
		st, err := r.Observe(testContext(t), ref)
		if err != nil {
			t.Fatal(err)
		}
		if st != nil && st.Run.Done {
			return st.Run
		}
		if time.Now().After(deadline) {
			t.Fatalf("never done; last state %+v", st)
		}
	}
}

// The Docker executor of runs does what every executor of runs must.
func TestDaemonRunsConformance(t *testing.T) {
	c, _ := daemon(t)
	gw := javoGateway(t, c, "runs")
	executortest.Runs(t, func(t *testing.T) executortest.Target {
		r, x := testRuns(t, c, gw)
		return executortest.Target{Executor: r, Secrets: testSecrets, Image: testImage, Fit: fit, Namespace: testNamespace,
			Again: func() executor.Executor {
				r.Close()
				x.Close()
				r, x = testRuns(t, c, gw)
				return r
			}}
	})
}

// A run's containers are confined as an app's are, on a network of the
// run's own, and its values come from the bundles; the executor of apps
// neither lists nor adopts a run, and refuses to start one.
func TestDaemonRunsRunConfinedAndApartFromApps(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "confined")
	r, x := testRuns(t, c, gw)

	s := job("confined", `[ "$TOKEN" = t0p-s3cr3t-7781 ] || exit 9; echo going; sleep 300`, 0)
	s.Main.Env = []workload.Env{{Name: "TOKEN", From: &workload.SecretRef{Bundle: "confined-tokens", Key: "token"}}}
	if err := testSecrets.Put(ctx, s.Ref, "confined-tokens", executor.Bundle{Data: map[string][]byte{"token": []byte("t0p-s3cr3t-7781")}}); err != nil {
		t.Fatal(err)
	}
	if err := x.Start(ctx, s); !errors.Is(err, ErrBatch) {
		t.Fatalf("the executor of apps and a run: %v", err)
	}
	app := testSpec("confined")
	app.Main = sh(workload.MainUnit, "sleep 300")
	if err := r.Start(ctx, app); err == nil {
		t.Fatal("the executor of runs started an app")
	}
	root := s
	root.User.UID = 0
	if err := r.Start(ctx, root); err == nil || !strings.Contains(err.Error(), "uid") {
		t.Fatalf("a run as root: %v", err)
	}

	if err := r.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	var id string
	for deadline := time.Now().Add(time.Minute); id == ""; time.Sleep(50 * time.Millisecond) {
		if rc, err := r.Logs(ctx, s.Ref, executor.LogOptions{}); err == nil && rc != nil {
			out, _ := io.ReadAll(rc)
			_ = rc.Close()
			if string(out) == "going\n" {
				id = unitID(t, c, s.Ref, workload.MainUnit)
			}
		}
		if time.Now().After(deadline) {
			st, _ := r.Observe(ctx, s.Ref)
			t.Fatalf("the run never printed; state %+v", st)
		}
	}

	var have struct {
		HostConfig struct {
			NetworkMode, IpcMode string
			CapDrop, SecurityOpt []string
			Privileged           bool
			PortBindings         map[string]any
			Binds                []string
			Mounts               []Mount
		}
		Config struct{ User string }
	}
	if err := c.get(ctx, "/containers/"+id+"/json", nil, &have); err != nil {
		t.Fatal(err)
	}
	h := have.HostConfig
	sandbox, _ := SandboxName(s.Ref)
	if h.NetworkMode != "container:"+unitID(t, c, s.Ref, Sandbox) || h.Privileged || !slices.Equal(h.CapDrop, []string{"ALL"}) ||
		!slices.Contains(h.SecurityOpt, "no-new-privileges") || len(h.PortBindings) != 0 || len(h.Binds) != 0 || have.Config.User != "1000:100" {
		t.Fatalf("the run's container is not confined as an app's: %+v", have)
	}
	for _, m := range h.Mounts {
		if m.Type != "volume" {
			t.Fatalf("mount %+v is not a named volume", m)
		}
	}
	n, err := c.NetworkInspect(ctx, sandbox)
	if err != nil || !n.isolated() {
		t.Fatalf("the run's network: %+v, %v", n, err)
	}

	listed := func(e executor.Executor) bool {
		refs, err := e.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(refs, s.Ref)
	}
	if !listed(r) || listed(x) {
		t.Fatalf("listed by the executor of runs: %v; by the executor of apps: %v", listed(r), listed(x))
	}
	// A new executor of apps leaves the run alone.
	x2 := testExecutor(t, c, gw, testPool)
	if listed(x2) {
		t.Fatal("a new executor of apps lists the run")
	}
	if st, _ := x2.Observe(ctx, s.Ref); st != nil {
		t.Fatalf("a new executor of apps adopted the run: %+v", st)
	}
	if c, err := c.ContainerInspect(ctx, id); err != nil || !c.State.Running {
		t.Fatalf("the run's main unit after a new executor of apps: %+v, %v", c.State, err)
	}

	// Stopped and let go, nothing of it is left.
	if err := r.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	if got := finished(t, r, s.Ref); !got.Stopped {
		t.Fatalf("run %+v, want it stopped", got)
	}
	if err := r.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	if rest := left(t, c, s.Ref); len(rest) != 0 {
		t.Fatalf("left after the run was let go: %v", rest)
	}
}

// A run that is going when Javo restarts goes on: its containers outlive
// Javo, and the next executor waits for the same attempt.
func TestDaemonRunsARunGoingAtARestartGoesOn(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "goes-on")
	r, x := testRuns(t, c, gw)
	s := job("goes-on", `echo going; sleep 4; `+`printf done > "$JAVO_TERMINATION_LOG"`, 0)
	if err := r.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	var id string
	for deadline := time.Now().Add(time.Minute); id == ""; time.Sleep(50 * time.Millisecond) {
		all, err := c.ContainerList(ctx, append(Selector(s.Ref), LabelUnit+"="+workload.MainUnit)...)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) == 1 && all[0].State == "running" {
			id = all[0].ID
		}
		if time.Now().After(deadline) {
			t.Fatal("the main unit never ran")
		}
	}
	r.Close()
	x.Close()

	r, _ = testRuns(t, c, gw)
	got := finished(t, r, s.Ref)
	if !got.Succeeded || got.Attempts != 1 || got.Message != "done" {
		t.Fatalf("run %+v, want the first attempt to succeed", got)
	}
	if now := unitID(t, c, s.Ref, workload.MainUnit); now != id {
		t.Fatal("the main unit was started again")
	}
}
