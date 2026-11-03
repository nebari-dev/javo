package apitest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/bash"
	"github.com/nebari-dev/javo/internal/jobs/engines/command"
	"github.com/nebari-dev/javo/internal/jobs/engines/marimo"
	"github.com/nebari-dev/javo/internal/jobs/engines/papermill"
	"github.com/nebari-dev/javo/internal/model"
)

func TestEngines() *engine.Registry {
	r := registry.New[engine.Engine]()
	r.Register(papermill.New())
	r.Register(marimo.New())
	r.Register(command.New())
	r.Register(bash.New())
	return r
}

// JobTemplateObj is a template that allows jobs with the three test engines.
func JobTemplateObj() *model.Template {
	t := gatewaytest.Template("lab", "jupyterlab")
	t.Spec.Jobs = &model.TemplateJobs{
		Engines:    []string{"papermill", "marimo", "command"},
		Kernels:    []string{"python3"},
		MaxTimeout: &metav1.Duration{Duration: 6 * time.Hour},
	}
	return t
}

// JobObj is a stored job; short is the short name.
func JobObj(owner, short string) *model.Job {
	return &model.Job{
		Namespace: gatewaytest.Namespace, Name: short, Owner: owner,
		Spec: model.JobSpec{
			DisplayName: short, Template: "lab", Engine: "papermill",
			Source: model.JobSource{Path: "reports/sales.ipynb"},
		},
	}
}

func JobFixture(t *testing.T, o Options, objs ...any) *Fixture {
	t.Helper()
	o.Engines = TestEngines()
	return New(t, o, append([]any{JobTemplateObj()}, objs...)...)
}

// Jobs are the stored jobs, deleted ones left out.
func (f *Fixture) Jobs() []model.Job {
	f.T.Helper()
	all, err := f.Db.Jobs().ListAll(context.Background())
	if err != nil {
		f.T.Fatal(err)
	}
	var out []model.Job
	for _, j := range all {
		if j.DeletedAt.IsZero() {
			out = append(out, j)
		}
	}
	return out
}

// RunsOf are the recorded and requested runs of owner's job, newest first.
func (f *Fixture) RunsOf(owner, name string) []model.Run {
	f.T.Helper()
	job, err := f.Db.Jobs().GetByOwnerName(context.Background(), owner, name)
	if err != nil {
		f.T.Fatal(err)
	}
	runs, err := f.Db.Jobs().Runs(context.Background(), job.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	return runs
}

const NewJob = `{"displayName":"Nightly sales","template":"lab","engine":"papermill","source":"reports/sales.ipynb"%s}`

// Sales29 is a finished run of nightly-sales whose pod is gone; its runner
// saved the log as jobs/nightly-sales/nightly-sales-29/run.log.
func Sales29() gatewaytest.WithRuns {
	return gatewaytest.WithRuns{Job: JobObj(AliceID, "nightly-sales"), Runs: []model.Run{
		{K8sJob: "nightly-sales-29", Trigger: "schedule", Outcome: "Succeeded"}}}
}

// ShellTemplate is a job template that runs only bash scripts.
func ShellTemplate() *model.Template {
	t := JobTemplateObj()
	t.Name = "shell"
	t.Spec.Jobs = &model.TemplateJobs{Engines: []string{"bash"}}
	return t
}

func StrconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

// SampleJobTemplate is a job template that ships the "Print the time" sample.
func SampleJobTemplate() *model.Template {
	t := JobTemplateObj()
	t.Name = "jobs-lab"
	t.Spec.Jobs.Sample = &model.TemplateJobSample{
		Title: "Print the time", Command: "bash /opt/javo/samples/jobs/print-time.sh",
		Parameters: []model.JobParameter{{Name: "greeting", Type: "string", Default: "hello"}},
	}
	return t
}

// AddRunPod puts a run's pod in the fake cluster, labelled with the id the
// store gave the job (a seeded pod cannot know it).
func (f *Fixture) AddRunPod(owner, short string, pod *corev1.Pod) {
	f.T.Helper()
	j, err := f.Db.Jobs().GetByOwnerName(context.Background(), owner, short)
	if err != nil {
		f.T.Fatal(err)
	}
	pod.Labels[model.LabelJob] = j.ID
	if err := f.Client.Create(context.Background(), pod); err != nil {
		f.T.Fatal(err)
	}
}
