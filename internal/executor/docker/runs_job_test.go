package docker_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/docker/dockerlive"
	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// A job, for real: the job planner's plan of a run (jobs.PlanRun) runs
// on the daemon as it is. The platform's image installs the runner, the
// runner runs the job's command in the template's image and in the
// owner's home, and what the executor then shows of the run is recorded
// as a run that succeeded, with its folder and its parameters.
func TestDaemonRunsRunAPlannedJob(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the javo binary and an image of it")
	}
	d := dockerlive.New(t)
	c, ctx := d.Client, homesContext(t)
	// The platform's image, as far as a run uses it: the Javo binary of
	// this tree at /javo.
	runner := d.Image(t, map[string]string{"/javo": "./cmd/javo"})
	// What stands in for the container Javo's gateway runs in.
	var made struct {
		ID string `json:"Id"`
	}
	must(t, d.Call(ctx, http.MethodPost, "/containers/create", nil, map[string]any{
		"Image": dockerlive.Busybox, "Cmd": []string{"sleep", "86400"}, "Labels": dockerlive.Stamp(),
	}, &made))
	gw := made.ID
	must(t, c.ContainerStart(ctx, gw))
	x, err := docker.NewExecutor(ctx, docker.Options{
		Client: c, Secrets: &executortest.Secrets{}, Gateway: gw, RunnerImage: runner, Pool: docker.TestPool, Grace: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	r, err := x.Runs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)

	namespace := "javotest-" + testrun.ID[:10]
	job := &model.Job{ID: "job-1", Namespace: namespace, Name: "report", Owner: namespace, Spec: model.JobSpec{
		Template: "tools", Engine: "command",
		Command:    []string{"sh", "-c", `echo "hello $JAVO_PARAM_CITY" > greeting.txt; echo the job ran`},
		Parameters: []model.JobParameter{{Name: "city", Type: "string", Default: "nowhere"}},
	}}
	tpl := &model.Template{Name: "tools", Spec: model.TemplateSpec{
		Image: dockerlive.Busybox,
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
	// On Docker a home is a volume of Javo's, owned by the person.
	home := s.Volumes[0].Source
	must(t, c.VolumeCreate(ctx, home, docker.Labels(workload.Ref{Namespace: namespace, Owner: job.Owner})))
	must(t, docker.Own(ctx, c, s, dockerlive.Busybox, home, 0o700))

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
	name, err := docker.ContainerName(s.Ref, workload.MainUnit)
	must(t, err)
	main, err := c.ContainerInspect(ctx, name)
	must(t, err)
	for file, want := range map[string]string{
		"greeting.txt":          "hello lisbon",
		folder + "/result.json": `"outcome": "Succeeded"`,
		folder + "/run.log":     "the job ran",
		folder + "/params.yaml": "city: lisbon",
	} {
		got, err := c.ReadFile(ctx, main.ID, podstorage.HomeDir+"/"+file, 64<<10)
		if err != nil || !strings.Contains(string(got), want) {
			t.Errorf("%s in the home holds %q (%v), want %q", file, got, err, want)
		}
	}

	if err := r.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	rest, err := c.ContainerList(ctx, docker.Selector(s.Ref)...)
	must(t, err)
	nets, err := c.NetworkList(ctx, docker.Selector(s.Ref)...)
	must(t, err)
	if len(rest)+len(nets) != 0 {
		t.Fatalf("left after the run was let go: %d containers, %d networks", len(rest), len(nets))
	}
}
