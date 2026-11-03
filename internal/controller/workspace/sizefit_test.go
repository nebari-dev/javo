package workspace

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

// fixedMachines stands in for the cluster's nodes (envtest has none).
type fixedMachines capacity.Cluster

func (m fixedMachines) Cluster(context.Context) (capacity.Cluster, error) {
	return capacity.Cluster(m), nil
}

// On a 2 CPU, 4Gi cluster an app at 4 CPU and 8Gi fails as SizeTooBig; one
// at a size the admin lets wait for an autoscaler is only starting. Either
// way the pod is built, since a bigger node may join.
func TestAppBiggerThanEveryMachineFailsAsSizeTooBig(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	alloc := newNamespace(t, "javo-alloc", nil)
	fx.newTemplate(t, "lab-fit")
	cat, err := sizes.Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: big, displayName: Big, cpu: "4", memory: 8Gi},
		{name: burst, displayName: Burst, cpu: "4", memory: 8Gi, allowUnschedulable: true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r := newRec(fx.db, func(r *Reconciler) {
		r.Sizes = cat
		r.Machines = fixedMachines{{CPU: resource.MustParse("2"), Memory: resource.MustParse("4Gi")}}
	})
	for size, wantPhase := range map[string]string{"big": "Failed", "burst": "Starting"} {
		app := newAppIn(t, fx.db, alloc, "ws-fit-"+size, "erin", "lab-fit", func(a *model.App) { a.Size = size })
		// The first pass builds the pod, the second sees it unscheduled.
		reconcileNow(t, r, app.ID)
		reconcileNow(t, r, app.ID)
		got := fx.getApp(t, app.ID)
		c := condition(got, model.ConditionScheduled)
		if tooBig := c != nil && c.Reason == string(reasons.SizeTooBig); got.Status.Phase != wantPhase || tooBig != (wantPhase == "Failed") {
			t.Errorf("%s: phase %q, Scheduled %+v", size, got.Status.Phase, c)
		}
		pod := &corev1.Pod{}
		if err := k8s.Get(bg, types.NamespacedName{Namespace: alloc, Name: app.ObjectName()}, pod); err != nil {
			t.Errorf("%s: the pod is still built (a bigger node may join): %v", size, err)
		}
	}
}

// A custom amount becomes the app container's requests and limits; changing
// it, or moving to a named size, replaces the pod.
func TestCustomAmountBuildsAndRestartsThePod(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-custom")
	app := fx.newApp(t, "ws-custom", "alice", "lab-custom")
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
	setCustom := func(cpu, mem string) {
		t.Helper()
		editApp(t, fx.db, app.ID, func(a *model.App) {
			a.Size = ""
			a.Resources = &model.AppResources{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}
		})
	}
	podAt := func(cpu, mem string) func() bool {
		return func() bool {
			p := &corev1.Pod{}
			if !fx.exists(p, app.ObjectName()) {
				return false
			}
			r := p.Spec.Containers[0].Resources
			return r.Limits.Cpu().Equal(resource.MustParse(cpu)) && r.Limits.Memory().Equal(resource.MustParse(mem)) &&
				r.Requests.Cpu().Equal(resource.MustParse(cpu)) && r.Requests.Memory().Equal(resource.MustParse(mem))
		}
	}
	setCustom("2500m", "6Gi")
	eventually(t, "pod at 2.5 CPU and 6Gi", podAt("2500m", "6Gi"))
	setCustom("3", "6Gi")
	eventually(t, "pod at 3 CPU", podAt("3", "6Gi"))

	editApp(t, fx.db, app.ID, func(a *model.App) { a.Resources, a.Size = nil, "medium" })
	eventually(t, "pod at medium", podAt("4", "8Gi"))
}
