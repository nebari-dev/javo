//go:build e2e

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/workload"
)

// A fresh volume belongs to root, so a workload cannot write it. Own
// gives it to the workload's user without running anything.
// The volumes the tests below make outside a workload, named after this
// test process.
var (
	ownHome       = "javo-" + testNamespace + ".own.home"
	prepareHome   = "javo-" + testNamespace + ".prepare.home"
	foreignVolume = testNamespace + "-foreign"
)

func TestDaemonAFreshVolumeIsRootsUntilOwned(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	s := testSpec("own")
	up(t, c, s)
	s.Volumes = []workload.Volume{{Name: "home", Source: ownHome}}
	if err := c.VolumeCreate(ctx, ownHome, Labels(s.Ref)); err != nil {
		t.Fatal(err)
	}
	look := sh("look", `stat -c '%u:%g %a' /data; touch /data/x 2>&1 && echo wrote`, workload.Mount{Volume: "home", Path: "/data"})
	if _, out := run(t, c, s, look); !strings.HasPrefix(out, "0:0 755\n") || !strings.Contains(out, "Permission denied") {
		t.Errorf("fresh volume: %q", out)
	}
	if err := Own(ctx, c, s, testImage, ownHome, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, out := run(t, c, s, look); out != "1000:100 750\nwrote\n" {
		t.Errorf("owned volume: %q", out)
	}
	// The setup container is gone: only the sandbox is left.
	left, err := c.ContainerList(ctx, Selector(s.Ref)...)
	if err != nil || len(left) != 1 || left[0].Labels[LabelUnit] != Sandbox {
		t.Errorf("containers left: %+v, %v", left, err)
	}
}

// Prepare makes the workload's own volumes and the folders its mounts
// name, so every unit can write what it mounts. A volume that is not
// Javo's is never mounted.
func TestDaemonPrepareMakesVolumesAndFolders(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	s := testSpec("prepare")
	gw := up(t, c, s)
	if err := c.VolumeCreate(ctx, prepareHome, Labels(s.Ref)); err != nil {
		t.Fatal(err)
	}
	if err := Own(ctx, c, s, testImage, prepareHome, 0o750); err != nil {
		t.Fatal(err)
	}
	s.Volumes = []workload.Volume{{Name: "home", Source: prepareHome}, {Name: "scratch"}}
	s.Main = sh("app", `stat -c '%u:%g %a %n' /scratch /work /home/alice; touch /scratch/a /work/b && echo wrote; ls /home/alice/projects`,
		workload.Mount{Volume: "scratch", Path: "/scratch"},
		workload.Mount{Volume: "home", Path: "/home/alice"},
		workload.Mount{Volume: "home", Path: "/work", SubPath: "projects/one"})
	if err := Prepare(ctx, c, s, testImage); err != nil {
		t.Fatal(err)
	}
	// Without Prepare the daemon refuses a mount of a folder that does
	// not exist; with it the folder is there and the user's.
	_, out := run(t, c, s, s.Main)
	want := "1000:100 700 /scratch\n1000:100 755 /work\n1000:100 750 /home/alice\nwrote\none\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	// A second Prepare keeps what is there, files and modes.
	if err := Prepare(ctx, c, s, testImage); err != nil {
		t.Fatal(err)
	}
	if _, out := run(t, c, s, sh("again", `ls /scratch /work`, s.Main.Mounts...)); out != "/scratch:\na\n\n/work:\nb\n" {
		t.Errorf("after a second Prepare: %q", out)
	}

	// A folder that leaves the volume through a symlink is not mounted.
	run(t, c, s, sh("link", `ln -s /etc /home/alice/out`, workload.Mount{Volume: "home", Path: "/home/alice"}))
	escape := sh("escape", `ls /x`, workload.Mount{Volume: "home", Path: "/x", SubPath: "out", ReadOnly: true})
	body, err := ContainerBody(s, escape, Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.ContainerCreate(ctx, "javo-"+testNamespace+".prepare.escape", body)
	if err == nil {
		err = c.ContainerStart(ctx, id)
	}
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("a subpath through a symlink: err = %v", err)
	}

	// A volume of someone else's: made here without Javo's label.
	if err := c.VolumeCreate(ctx, foreignVolume, nil); !errors.Is(err, ErrForeign) {
		t.Fatalf("creating an unlabelled volume: %v", err)
	}
	theirs := testSpec("prepare")
	theirs.Volumes = []workload.Volume{{Name: "home", Source: foreignVolume}}
	if err := Prepare(ctx, c, theirs, testImage); !errors.Is(err, ErrForeign) {
		t.Errorf("Prepare with a foreign volume: %v", err)
	}
	if err := c.VolumeRemove(ctx, foreignVolume); !errors.Is(err, ErrForeign) {
		t.Errorf("removing a foreign volume: %v", err)
	}

	// Teardown removes the scratch volume and keeps the home.
	if err := Teardown(ctx, c, s.Ref, gw); err != nil {
		t.Fatal(err)
	}
	if _, err := c.VolumeInspect(ctx, "javo-"+testNamespace+".prepare.scratch"); !errors.Is(err, ErrNotFound) {
		t.Errorf("scratch after Teardown: %v", err)
	}
	if _, err := c.VolumeInspect(ctx, prepareHome); err != nil {
		t.Errorf("home after Teardown: %v", err)
	}
}

// Secret files reach a unit read-only, from memory, and are in no
// container's description.
func TestDaemonSecretFilesLiveInMemory(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	s := testSpec("secret")
	vol := workload.Volume{Name: "nebi", Secret: &workload.SecretFiles{Bundle: "nebi-token"}}
	s.Volumes = []workload.Volume{vol}
	if err := Prepare(ctx, c, s, testImage); err != nil {
		t.Fatal(err)
	}
	gw := gateway(t, c, "secret-gw", "true")

	// Before the sandbox runs there is nowhere to keep them.
	if err := SecretFiles(ctx, c, s, vol, map[string][]byte{"token": []byte("v1")}); err == nil {
		t.Fatal("secret files were written with no sandbox")
	}
	sandboxID := sandbox(t, c, s, gw)
	if err := SecretFiles(ctx, c, s, vol, map[string][]byte{"token": []byte("s3cr3t-value-1")}); err != nil {
		t.Fatal(err)
	}
	read := sh("read", `cat /run/nebi/token; echo; stat -c '%u:%g %a' /run/nebi/token /run/nebi; df /run/nebi | tail -1 | cut -d' ' -f1; touch /run/nebi/x 2>&1; grep -c ' /run/nebi tmpfs ro,nosuid,nodev,noexec' /proc/mounts`,
		workload.Mount{Volume: "nebi", Path: "/run/nebi"})
	_, out := run(t, c, s, read)
	want := "s3cr3t-value-1\n1000:100 400\n1000:100 700\ntmpfs\ntouch: /run/nebi/x: Read-only file system\n1\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}

	// A unit that keeps running sees a renewed value in place.
	watcher := start(t, c, s, sh("watch", `until grep -q value-2 /run/nebi/token 2>/dev/null; do sleep 0.2; done; cat /run/nebi/token`,
		workload.Mount{Volume: "nebi", Path: "/run/nebi"}))
	if err := SecretFiles(ctx, c, s, vol, map[string][]byte{"token": []byte("s3cr3t-value-2")}); err != nil {
		t.Fatal(err)
	}
	if code, err := c.ContainerWait(ctx, watcher); err != nil || code != 0 {
		t.Fatalf("watcher: exit %d, %v", code, err)
	}
	if got := logs(t, c, watcher, LogOptions{}); got != "s3cr3t-value-2" {
		t.Errorf("renewed value: %q", got)
	}

	// The value is in no container's description.
	for _, id := range []string{sandboxID, watcher} {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: "/containers/" + id + "/json"})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !json.Valid(raw) || strings.Contains(string(raw), "s3cr3t") {
			t.Errorf("the description of %s holds the secret", id)
		}
	}

	// Someone else's uid cannot read them, even with the volume mounted.
	other := s
	other.User = workload.User{UID: 2000, GID: 2000}
	if _, out := run(t, c, other, sh("other", `cat /run/nebi/token 2>&1`, workload.Mount{Volume: "nebi", Path: "/run/nebi"})); !strings.Contains(out, "Permission denied") {
		t.Errorf("another uid read the secret: %q", out)
	}

	// Once nothing runs with the volume, the values are gone: they were
	// in memory only.
	if err := c.ContainerRemove(ctx, watcher); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerRemove(ctx, sandboxID); err != nil {
		t.Fatal(err)
	}
	sandbox(t, c, s, gw)
	if _, out := run(t, c, s, sh("after", `ls -A /run/nebi | wc -l`, workload.Mount{Volume: "nebi", Path: "/run/nebi"})); strings.TrimSpace(out) != "0" {
		t.Errorf("files left after the sandbox ended: %q", out)
	}
}

// Memory, swap, CPU and process limits reach the container's cgroup, and
// a container that takes more memory than it may is killed.
func TestDaemonLimitsApply(t *testing.T) {
	c, d := daemon(t)
	t.Logf("Docker %s, API %s, rootless %v, cgroup v%s, runtimes %v", d.Version, d.APIVersion, d.Rootless, d.CgroupVersion, d.Runtimes)
	if d.CgroupVersion != "2" {
		t.Skipf("the limits are read from cgroup v2 files; this daemon uses v%s", d.CgroupVersion)
	}
	s := testSpec("limits")
	up(t, c, s)
	_, out := run(t, c, s, sh("read", `cd /sys/fs/cgroup; cat memory.max memory.swap.max cpu.max pids.max; grep -c NoNewPrivs:.1 /proc/self/status; grep CapEff /proc/self/status; grep Seccomp: /proc/self/status; id`))
	want := "67108864\n0\n50000 100000\n4096\n1\nCapEff:\t0000000000000000\nSeccomp:\t2\nuid=1000 gid=100(users) groups=100(users)\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}

	ctx := testContext(t)
	id := start(t, c, s, workload.Unit{Name: "hog", Image: testImage, Command: []string{"tail", "/dev/zero"}})
	code, err := c.ContainerWait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	have, err := c.ContainerInspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("a container over its memory limit: exit %d, OOMKilled %v", code, have.State.OOMKilled)
	if code != 137 || have.State.Running || have.State.FinishedAt.IsZero() {
		t.Errorf("exit %d, state %+v", code, have.State)
	}
}

// Logs come back whole, with the options applied, and events name the
// container's changes.
func TestDaemonLogsAndEvents(t *testing.T) {
	c, _ := daemon(t)
	s := testSpec("logs")
	up(t, c, s)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- c.Events(ctx, Selector(s.Ref), func(e Event) { events <- e }) }()
	// The stream is open once the daemon answered; give it a moment.
	time.Sleep(300 * time.Millisecond)

	before := time.Now()
	id := start(t, c, s, sh("print", `echo one; echo two >&2; echo three; exit 3`))
	if code, err := c.ContainerWait(ctx, id); err != nil || code != 3 {
		t.Fatalf("exit %d, %v", code, err)
	}
	got := logs(t, c, id, LogOptions{})
	if !strings.Contains(got, "one\n") || !strings.Contains(got, "two\n") || !strings.Contains(got, "three\n") || len(got) != len("one\ntwo\nthree\n") {
		t.Errorf("logs = %q", got)
	}
	if got := logs(t, c, id, LogOptions{Tail: 1}); len(strings.Split(strings.TrimSpace(got), "\n")) != 1 {
		t.Errorf("tail 1 = %q", got)
	}
	stamped := logs(t, c, id, LogOptions{Timestamps: true, Follow: true})
	for _, line := range strings.Split(strings.TrimSpace(stamped), "\n") {
		stamp, _, _ := strings.Cut(line, " ")
		if at, err := time.Parse(time.RFC3339Nano, stamp); err != nil || at.Before(before.Add(-time.Minute)) {
			t.Errorf("line %q: stamp %v", line, err)
		}
	}
	if got := logs(t, c, id, LogOptions{Since: time.Now().Add(time.Hour)}); got != "" {
		t.Errorf("since an hour from now = %q", got)
	}

	var actions []string
	timeout := time.After(15 * time.Second)
	for !strings.Contains(strings.Join(actions, " "), "die") {
		select {
		case e := <-events:
			if e.Actor.ID != id || RefOf(e.Actor.Attributes) != s.Ref {
				t.Errorf("event for %s %v", e.Actor.ID, e.Actor.Attributes)
			}
			actions = append(actions, e.Action)
			if e.Action == "die" && e.Actor.Attributes["exitCode"] != "3" {
				t.Errorf("die event: %v", e.Actor.Attributes)
			}
		case <-timeout:
			t.Fatalf("events so far: %v", actions)
		}
	}
	if strings.Join(actions, " ") != "create start die" {
		t.Errorf("events = %v", actions)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Events ended with %v", err)
	}
}

// The test image is the one its digest names, and a forced pull of it
// succeeds without a login.
func TestDaemonPullsByDigest(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	img, err := c.ImageInspect(ctx, testImage)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range img.RepoDigests {
		found = found || strings.HasSuffix(testImage, d[strings.Index(d, "@"):])
	}
	if !found {
		t.Errorf("digests = %v", img.RepoDigests)
	}
	if err := EnsureImage(ctx, c, testImage, true, nil); err != nil {
		t.Errorf("a forced pull: %v", err)
	}
	if _, err := c.ImageInspect(ctx, "javotest/no-such-image:1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing image: %v", err)
	}
	if err := c.ImagePull(ctx, "javotest.invalid/no-such-image:1", nil); err == nil {
		t.Error("a pull from a registry that does not exist succeeded")
	}
}
