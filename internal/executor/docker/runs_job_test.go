package docker

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/executor"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// javoImage builds the Javo binary of this tree for the daemon's machine
// and an image that holds it at /javo, its entrypoint, and nothing else:
// what the steps and the sandbox use of the platform's own image. The image carries this
// test process's id in its name and goes when the test ends.
func javoImage(t *testing.T, c *Client) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the javo binary and an image of it")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH to build the javo binary with")
	}
	ctx := testContext(t)
	var machine struct{ Arch string }
	if err := c.get(ctx, "/version", nil, &machine); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "javo")
	build := exec.Command("go", "build", "-ldflags=-s -w", "-o", bin, "./cmd/javo")
	build.Dir, build.Env = repo, append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+machine.Arch)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build for linux/%s: %v\n%s", machine.Arch, err, out)
	}

	// The build's files, sent as they are written.
	files, w := io.Pipe()
	go func() {
		tw := tar.NewWriter(w)
		dockerfile := "FROM scratch\nCOPY javo /javo\nENTRYPOINT [\"/javo\"]\n"
		err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile))})
		if err == nil {
			_, err = io.WriteString(tw, dockerfile)
		}
		var f *os.File
		if err == nil {
			f, err = os.Open(bin)
		}
		if err == nil {
			defer f.Close()
			var info os.FileInfo
			if info, err = f.Stat(); err == nil {
				err = tw.WriteHeader(&tar.Header{Name: "javo", Mode: 0o755, Size: info.Size()})
			}
		}
		if err == nil {
			_, err = io.Copy(tw, f)
		}
		if err == nil {
			err = tw.Close()
		}
		_ = w.CloseWithError(err)
	}()
	tag := "javo-test-runner:" + testRun
	labels, _ := json.Marshal(map[string]string{testLabel: testRun})
	resp, err := c.do(ctx, request{
		method: http.MethodPost, path: "/build", body: io.Reader(files),
		query: url.Values{"t": {tag}, "labels": {string(labels)}, "rm": {"1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	t.Cleanup(func() {
		q := url.Values{"force": {"1"}}
		if err := gone(c.call(testContext(t), request{method: http.MethodDelete, path: "/images/" + tag, query: q}, nil)); err != nil {
			t.Errorf("cleanup of the image %s: %v", tag, err)
		}
	})
	// The daemon reports a failed build in the stream, not in the status.
	for dec := json.NewDecoder(resp.Body); ; {
		var line struct{ Error string }
		if err := dec.Decode(&line); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if line.Error != "" {
			t.Fatalf("building the image %s: %s", tag, line.Error)
		}
	}
	return tag
}

// A job, for real: the job planner's plan of a run (jobs.PlanRun) runs
// on the daemon as it is. The platform's image installs the runner, the
// runner runs the job's command in the template's image and in the
// owner's home, and what the executor then shows of the run is recorded
// as a run that succeeded, with its folder and its parameters.
func TestDaemonRunsRunAPlannedJob(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	runner := javoImage(t, c)
	gw := javoGateway(t, c, "job")
	x, err := NewExecutor(ctx, Options{Client: c, Secrets: testSecrets, Gateway: gw, RunnerImage: runner, Pool: testPool, Grace: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	r, err := x.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)

	job := &model.Job{ID: "job-1", Namespace: testNamespace, Name: "report", Owner: testNamespace, Spec: model.JobSpec{
		Template: "tools", Engine: "command",
		Command:    []string{"sh", "-c", `echo "hello $JAVO_PARAM_CITY" > greeting.txt; echo the job ran`},
		Parameters: []model.JobParameter{{Name: "city", Type: "string", Default: "nowhere"}},
	}}
	tpl := &model.Template{Name: "tools", Spec: model.TemplateSpec{
		Image: testImage,
		Jobs:  &model.TemplateJobs{Engines: []string{"command"}},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("512Mi"), corev1.ResourceCPU: resource.MustParse("1"),
		}},
	}}
	s, err := jobs.PlanRun(jobs.Inputs{
		Job: job, Template: tpl, RunnerImage: podspec.Image{Ref: runner},
		Layout: podstorage.Build(job.Owner, podstorage.Input{UID: 1000, GID: 100}),
	}, "run-1", map[string]string{"city": "lisbon"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	ownedHome(t, c, s)

	if err := r.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	logs := func() string {
		var all string
		for _, u := range append(s.Steps, s.Main) {
			if rc, err := r.Logs(ctx, s.Ref, executor.LogOptions{Unit: u.Name}); err == nil && rc != nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				all += "== " + u.Name + "\n" + string(b)
			}
		}
		return all
	}
	var st *workload.State
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(100 * time.Millisecond) {
		if st, err = r.Observe(ctx, s.Ref); err != nil {
			t.Fatal(err)
		}
		if st != nil && st.Run.Done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run is not done: %+v\n%s", st, logs())
		}
	}

	run := jobstatus.DeriveRun(st)
	folder := "jobs/report/" + s.Ref.Name
	if run.Outcome != model.RunOutcomeSucceeded || run.Reason != "Succeeded" || run.Trigger != "manual" || run.Handle != s.Ref.Name {
		t.Fatalf("recorded %+v\nstate %+v\n%s", run, st.Run, logs())
	}
	if run.OutputPath != folder || run.Parameters["city"] != "lisbon" || run.StartTime.IsZero() || run.FinishTime.Before(run.StartTime) {
		t.Fatalf("recorded %+v, want the folder %s and the parameter", run, folder)
	}
	if out := logs(); !strings.Contains(out, "the job ran") {
		t.Fatalf("the run's log:\n%s", out)
	}
	// What the runner and the command left in the home.
	main := unitID(t, c, s.Ref, workload.MainUnit)
	for file, want := range map[string]string{
		"greeting.txt":          "hello lisbon",
		folder + "/result.json": `"outcome": "Succeeded"`,
		folder + "/run.log":     "the job ran",
		folder + "/params.yaml": "city: lisbon",
	} {
		got, err := c.ReadFile(ctx, main, podstorage.HomeDir+"/"+file, 64<<10)
		if err != nil || !strings.Contains(string(got), want) {
			t.Errorf("%s in the home holds %q (%v), want %q", file, got, err, want)
		}
	}

	if err := r.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	if rest := left(t, c, s.Ref); len(rest) != 0 {
		t.Fatalf("left after the run was let go: %v", rest)
	}
}
