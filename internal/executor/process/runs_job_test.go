//go:build linux || darwin

package process

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/executortest"
	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// onHost turns the plan of a run, which the job planner makes for
// containers, into one for this machine: the folder the runner is copied
// to and the home are folders of the machine, named where the plan names
// a container's paths. The storage is planned for the machine already
// (podstorage.Local). The job planner has no way yet to plan for a
// workload.Host as the app planner does; until it has, this is what it
// would have to do.
func onHost(s workload.Spec, runnerDir, home string) workload.Spec {
	const planned = "/javo-run"
	move := func(u *workload.Unit) {
		for i, m := range u.Mounts {
			if m.Path == planned {
				u.Mounts[i].Path = runnerDir
			}
		}
		for _, words := range [][]string{u.Command, u.Args} {
			for i, w := range words {
				if rest, ok := strings.CutPrefix(w, planned+"/"); ok {
					words[i] = filepath.Join(runnerDir, rest)
				}
			}
		}
	}
	for i := range s.Steps {
		move(&s.Steps[i])
	}
	move(&s.Main)
	s.Main.Args = append(s.Main.Args, "--home", home)
	s.Main.SetEnv(workload.Env{Name: "HOME", Value: home})
	return s
}

// A job, for real: the job planner's plan of a run (jobs.PlanRun), moved
// to this machine's folders, runs as processes. The Javo binary installs
// the runner, the runner runs the job's command in the owner's home, and
// what the executor then shows of the run is recorded as a run that
// succeeded, with its folder and its parameters.
func TestRunsRunAPlannedJob(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the javo binary")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	javo, home := filepath.Join(tmp, "javo"), filepath.Join(tmp, "home")
	build := exec.Command("go", "build", "-o", javo, "./cmd/javo")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}

	const runner = "example.com/javo:1"
	x, err := New(Options{Dir: filepath.Join(tmp, "state"), Secrets: &executortest.Secrets{}, RunnerImage: runner})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(x.Close)
	x.eng.self = javo
	r, err := x.Runs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)

	job := &model.Job{ID: "job-1", Namespace: "alice", Name: "report", Owner: "alice", Spec: model.JobSpec{
		Template: "tools", Engine: "command",
		Command:    []string{"sh", "-c", `echo "hello $JAVO_PARAM_CITY" > greeting.txt; echo the job ran`},
		Parameters: []model.JobParameter{{Name: "city", Type: "string", Default: "nowhere"}},
	}}
	tpl := &model.Template{Name: "tools", Spec: model.TemplateSpec{Jobs: &model.TemplateJobs{Engines: []string{"command"}}}}
	ref := workload.Ref{Namespace: job.Namespace, Name: jobs.RunName(job, "run-1"), ID: job.ID, Owner: job.Owner}
	s, err := jobs.PlanRun(jobs.Inputs{
		Job: job, Template: tpl, RunnerImage: podspec.Image{Ref: runner},
		Layout: podstorage.Local(home, x.VolumeDir(ref, podstorage.TmpVolume), x.VolumeDir(ref, podstorage.ScratchVolume), workload.User{}),
	}, "run-1", map[string]string{"city": "lisbon"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Ref != ref {
		t.Fatalf("the plan is for %+v, want %+v", s.Ref, ref)
	}
	s = onHost(s, x.VolumeDir(ref, "javo-run"), home)

	if err := r.Start(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	logs := func() string {
		var all string
		for _, u := range append(s.Steps, s.Main) {
			if rc, err := r.Logs(t.Context(), ref, executor.LogOptions{Unit: u.Name}); err == nil && rc != nil {
				b, _ := io.ReadAll(rc)
				_ = rc.Close()
				all += "== " + u.Name + "\n" + string(b)
			}
		}
		return all
	}
	var st *workload.State
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
		if st, err = r.Observe(t.Context(), ref); err != nil {
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
	folder := filepath.Join("jobs", "report", ref.Name)
	if run.Outcome != model.RunOutcomeSucceeded || run.Reason != "Succeeded" || run.Trigger != "manual" || run.Handle != ref.Name {
		t.Fatalf("recorded %+v\nstate %+v\n%s", run, st.Run, logs())
	}
	if run.OutputPath != folder || run.Parameters["city"] != "lisbon" || run.StartTime.IsZero() || run.FinishTime.Before(run.StartTime) {
		t.Fatalf("recorded %+v, want the folder %s and the parameter", run, folder)
	}
	if out := logs(); !strings.Contains(out, "the job ran") {
		t.Fatalf("the run's log:\n%s", out)
	}
	// What the runner and the command left in the home.
	for file, want := range map[string]string{
		"greeting.txt":                       "hello lisbon",
		filepath.Join(folder, "result.json"): `"outcome": "Succeeded"`,
		filepath.Join(folder, "run.log"):     "the job ran",
		filepath.Join(folder, "params.yaml"): "city: lisbon",
	} {
		got, err := os.ReadFile(filepath.Join(home, file))
		if err != nil || !strings.Contains(string(got), want) {
			t.Errorf("%s in the home holds %q (%v), want %q", file, got, err, want)
		}
	}

	if err := r.Stop(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(x.eng.dir, "w", ref.Namespace, ref.Name)); !os.IsNotExist(err) {
		t.Errorf("the run's folder stayed: %v", err)
	}
}
