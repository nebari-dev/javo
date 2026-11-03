package jobs

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

var envFiles = model.EnvironmentFiles{Manifest: "[workspace]\nname = \"sales\"\n", Lock: "version: 6\n"}

// inEnvironment stores a scheduled job that runs in the sales environment.
func (h *harness) inEnvironment(name string) model.Job {
	h.t.Helper()
	j := &model.Job{
		Namespace: h.ns, Name: name, Owner: owner,
		Spec: model.JobSpec{
			Template: h.tplName(), Engine: "papermill", Kernel: "python3",
			Source:      model.JobSource{Path: "reports/nightly.ipynb"},
			Schedule:    &model.JobSchedule{Cron: "0 6 * * 1-5", TimeZone: "Europe/Berlin", Concurrency: "Forbid"},
			Environment: &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3", Digest: envFiles.Digest()},
		},
	}
	got, err := h.db.Jobs().CreateWith(bg, model.Actor{Person: owner}, j, store.JobExtras{Environment: &envFiles})
	if err != nil {
		h.t.Fatal(err)
	}
	return got
}

func installs(p corev1.PodSpec) bool {
	for _, c := range p.InitContainers {
		if c.Name == pixienv.InstallContainer && c.Image == "example.com/pixi:1" {
			return true
		}
	}
	return false
}

func TestRunsOfAJobInAnEnvironmentInstallIt(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.PixiImage = "example.com/pixi:1" })
	h.template(nil)
	d := h.inEnvironment("in-env")
	h.waitPhase(d, model.JobPhaseReady)
	cj := h.cronJob(d)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		t.Fatal(err)
	}
	if !installs(cj.Spec.JobTemplate.Spec.Template.Spec) {
		t.Fatal("scheduled runs do not install the environment")
	}
	_, k := h.requestRun(d, nil)
	if !installs(k.Spec.Template.Spec) {
		t.Fatal("a run by hand does not install the environment")
	}
}

// Without an image to install with, the job waits and says why rather than
// run without its environment.
func TestAJobInAnEnvironmentWithNoInstallImageIsBlocked(t *testing.T) {
	h := newHarnessWith(t, nil)
	h.template(nil)
	got := h.waitPhase(h.inEnvironment("no-image"), model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.EnvironmentUnavailable) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
}

// A job on a template with an environment of its own runs in it, with the
// platform's pod files (pod-user.sh, the environments), when nothing is
// picked; without an image to install with it waits like a picked one.
func TestRunsOfAJobOnATemplateWithAnEnvironmentInstallIt(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.PixiImage = "example.com/pixi:1" })
	h.template(func(s *model.TemplateSpec) { s.Environment = "jobs-python" })
	d := h.definition("tpl-env", nil)
	h.waitPhase(d, model.JobPhaseReady)
	cj := h.cronJob(d)
	if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
		t.Fatal(err)
	}
	spec := cj.Spec.JobTemplate.Spec.Template.Spec
	if !installs(spec) {
		t.Fatal("scheduled runs do not install the template's environment")
	}
	copied := false
	for _, c := range spec.InitContainers {
		copied = copied || (c.Name == podfiles.CopyContainer && c.Image == "example.com/javo:9")
	}
	if !copied {
		t.Fatalf("runs do not get the pod files: %+v", spec.InitContainers)
	}

	b := newHarnessWith(t, nil)
	b.template(func(s *model.TemplateSpec) { s.Environment = "jobs-python" })
	got := b.waitPhase(b.definition("tpl-env-no-image", nil), model.JobPhaseBlocked)
	if condReason(got, model.ConditionValid) != string(reasons.EnvironmentUnavailable) {
		t.Fatalf("conditions = %+v", got.Status.Conditions)
	}
}
