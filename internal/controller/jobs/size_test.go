package jobs

import (
	"cmp"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

func jobSizes(t *testing.T) sizes.Catalog {
	t.Helper()
	c, err := sizes.Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: large, displayName: Large, cpu: "4", memory: 16Gi}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The CronJob's runs and a run started by hand both get the job's size; a
// job that names none gets the default.
func TestRunsGetTheJobsSizeFromTheCatalog(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.Sizes = jobSizes(t) })
	h.template(nil)
	limits := func(p corev1.PodSpec) (cpu, mem string) {
		l := p.Containers[0].Resources.Limits
		return l.Cpu().String(), l.Memory().String()
	}
	for _, tc := range []struct{ size, cpu, mem string }{{"large", "4", "16Gi"}, {"", "1", "2Gi"}} {
		d := h.definition("at-"+cmp.Or(tc.size, "default"), func(j *model.Job) { j.Size = tc.size })
		h.waitPhase(d, model.JobPhaseReady)
		cj := h.cronJob(d)
		if err := k8s.Get(bg, client.ObjectKeyFromObject(cj), cj); err != nil {
			t.Fatal(err)
		}
		if cpu, mem := limits(cj.Spec.JobTemplate.Spec.Template.Spec); cpu != tc.cpu || mem != tc.mem {
			t.Errorf("size %q: scheduled runs get %s CPU and %s, want %s and %s", tc.size, cpu, mem, tc.cpu, tc.mem)
		}
		_, k := h.requestRun(d, nil)
		if cpu, mem := limits(k.Spec.Template.Spec); cpu != tc.cpu || mem != tc.mem {
			t.Errorf("size %q: a run by hand gets %s CPU and %s, want %s and %s", tc.size, cpu, mem, tc.cpu, tc.mem)
		}
	}
}

// A job set to a size the admin has since removed is Invalid, for the owner
// to pick another.
func TestJobAtAnUnknownSizeIsInvalid(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.Sizes = jobSizes(t) })
	h.template(nil)
	d := h.definition("nightly", func(j *model.Job) { j.Size = "gone" })
	got := h.waitPhase(d, model.JobPhaseInvalid)
	if condReason(got, model.ConditionValid) != string(reasons.JobSizeUnknown) {
		t.Fatalf("conditions = %+v, want reason %s", got.Status.Conditions, reasons.JobSizeUnknown)
	}
}
