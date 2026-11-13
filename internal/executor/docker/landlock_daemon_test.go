//go:build e2e

package docker_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockerlive"
	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// A coding agent locked in with Landlock (workloads.landlock), for real:
// the planner's plan of an agent app runs on the daemon as it is, in a
// container confined as every container here is (no capabilities, no
// privilege gain, the daemon's seccomp profile). The agent's command starts
// through agent-lock.sh and javo-sandbox, both this tree's, writes and
// reads its own folder, and is refused a file of the same home outside it.
// The same app planned with the lock off reads that file, so the refusal
// is the lock's.
func TestDaemonLocksACodingAgentIn(t *testing.T) {
	d := dockerlive.New(t)
	c, ctx := d.Client, homesContext(t)
	// What the agents' own programs need and busybox lacks is stood in
	// for: pod-user.sh needs bash, and the ssh-agent sidecar ssh.
	standIn := func(script string) string {
		f := filepath.Join(t.TempDir(), "stand-in")
		must(t, os.WriteFile(f, []byte("#!/bin/sh\n"+script+"\n"), 0o755))
		return f
	}
	// The platform's image, as far as an agent uses it: the Javo binary
	// that copies the platform's files, and of those files the lock and
	// the sandbox it runs.
	runner := d.ImageWith(t, map[string]string{
		"/javo":                            "./cmd/javo",
		"/opt/javo-files/bin/javo-sandbox": "./cmd/javo-sandbox",
	}, map[string]string{
		"/opt/javo-files/agents/agent-lock.sh": "pod/files/agents/agent-lock.sh",
		"/opt/javo-files/pod-user.sh":          standIn(`exec "$@"`),
		"/opt/javo-files/agents/ssh-agent.sh":  standIn(`touch "${3%/*}/ready"; exec sleep 86400`),
	})
	var made struct {
		ID string `json:"Id"`
	}
	must(t, d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{
		"Image": dockerlive.Busybox, "Cmd": []string{"sleep", "86400"}, "Labels": dockerlive.Stamp(),
	}, &made))
	gw := made.ID
	must(t, c.ContainerStart(ctx, gw))
	x, err := docker.NewTestExecutor(ctx, docker.Options{
		Client: c, Secrets: &executortest.Secrets{}, Gateway: gw, RunnerImage: runner, Pool: docker.TestPool, Grace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)

	namespace := "javotest-" + testrun.ID[:10]
	// It makes the file outside when it can, then says in its own folder
	// what it could do, and serves that folder.
	tpl := &model.Template{Name: "agent", Spec: model.TemplateSpec{
		Kind: "agent", Category: model.CategoryAgents, Image: dockerlive.Busybox, PodFiles: true, Port: 8080,
		Command: []string{"/opt/javo/pod-user.sh"},
		Args: []string{"sh", "-c", `mkdir -p {{.File}} && cd {{.File}} || exit 1
[ -e {{.Home}}/secret.txt ] || echo s3cret > {{.Home}}/secret.txt
echo mine > inside.txt
cat inside.txt > read-inside
cat {{.Home}}/secret.txt > read-outside 2> read-outside-error
echo $? > read-outside-code
echo x 2>/dev/null > {{.Home}}/written-outside
echo $? > write-outside-code
echo ready > index.html
exec httpd -f -p {{.Port}} -h {{.File}}`},
		ReadinessPath: "/",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}}
	// run starts the app name, locked in or not, and returns what it left
	// in its folder.
	run := func(name string, lock bool) map[string]string {
		app := &model.App{ID: "app-" + name, Namespace: namespace, Name: name, Owner: namespace, Template: tpl.Name, File: "projects/" + name}
		s, err := workspace.Plan(workspace.Inputs{
			App: app, Template: tpl, BaseURL: "/u/" + namespace + "/" + name + "/",
			Layout: podstorage.Build(app.Owner, podstorage.Input{UID: 1000, GID: 100}),
			Images: workspace.Images{Runner: podspec.Image{Ref: runner}, LockAgents: lock},
		})
		must(t, err)
		if lock != (s.Main.Command[0] == "/opt/javo/agents/agent-lock.sh") {
			t.Fatalf("lock %v: the plan runs %q", lock, s.Main.Command)
		}
		// On Docker a home is a volume of Javo's, owned by the person.
		for _, v := range s.Volumes {
			if v.Source != "" {
				must(t, c.VolumeCreate(ctx, v.Source, docker.Labels(workload.Ref{Namespace: namespace, Owner: app.Owner})))
				must(t, docker.Own(ctx, c, s, dockerlive.Busybox, v.Source, 0o700))
			}
		}
		must(t, x.Start(ctx, s))
		logs := func() string {
			var all string
			for _, u := range append(s.Steps, s.Main) {
				if rc, err := x.Logs(ctx, s.Ref, executor.LogOptions{Unit: u.Name}); err == nil && rc != nil {
					b, _ := io.ReadAll(rc)
					_ = rc.Close()
					all += "== " + u.Name + "\n" + string(b)
				}
			}
			return all
		}
		for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(100 * time.Millisecond) {
			st, err := x.Observe(ctx, s.Ref)
			must(t, err)
			if st != nil && st.Phase == "Running" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the agent %s is not running: %+v\n%s", name, st, logs())
			}
		}
		cname, err := docker.ContainerName(s.Ref, workload.MainUnit)
		must(t, err)
		main, err := c.ContainerInspect(ctx, cname)
		must(t, err)
		got := map[string]string{}
		for _, f := range []string{"read-inside", "read-outside", "read-outside-error", "read-outside-code", "write-outside-code"} {
			b, err := c.ReadFile(ctx, main.ID, podstorage.HomeDir+"/"+app.File+"/"+f, 64<<10)
			if err != nil {
				t.Fatalf("%s of %s: %v\n%s", f, name, err, logs())
			}
			got[f] = strings.TrimSpace(string(b))
		}
		must(t, x.Stop(ctx, s.Ref))
		return got
	}

	// Without the lock the agent reaches the whole home.
	if got := run("open", false); got["read-inside"] != "mine" || got["read-outside"] != "s3cret" || got["read-outside-code"] != "0" || got["write-outside-code"] != "0" {
		t.Fatalf("lock off: %v, want the file outside read and written", got)
	}
	got := run("locked", true)
	if got["read-inside"] != "mine" {
		t.Errorf("lock on: %v, want its own folder written and read", got)
	}
	if got["read-outside"] != "" || got["read-outside-code"] == "0" || !strings.Contains(got["read-outside-error"], "Permission denied") {
		t.Errorf("lock on: %v, want the file outside its folder refused", got)
	}
	if got["write-outside-code"] == "0" {
		t.Errorf("lock on: %v, want a write outside its folder refused", got)
	}
}
