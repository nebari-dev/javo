package workspace

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// A restored app waits: no Pod and no home claim (the volume restore may
// still be bringing the old one back), until an admin releases it; then it
// starts like any app.
func TestRestoredAppIsHeldAndCreatesNoClaim(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-hold")
	app := fx.newApp(t, "ws-hold", "dora", "lab-hold", func(a *model.App) { a.RestoreHold = "r1" })

	eventually(t, "held status", func() bool {
		got := fx.getApp(t, app.ID)
		c := condition(got, model.ConditionScheduled)
		return got.Status.Phase == "Pending" && c != nil && c.Reason == string(reasons.RestoreHeld)
	})
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("a held app has a pod")
	}
	if fx.exists(&corev1.PersistentVolumeClaim{}, naming.HomeClaimName("dora")) {
		t.Fatal("a held app made a home claim")
	}

	if _, err := fx.db.Apps().Release(bg, model.Actor{Person: "bob"}, "r1", store.ReleaseStart, 10, time.Now()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pod after release", func() bool { return fx.exists(&corev1.Pod{}, app.ObjectName()) })
	eventually(t, "phase Starting", fx.phaseIs(app.ID, "Starting"))
}

// An app released for later is requeued for its release time, not polled.
func TestHeldAppRequeuesAtItsReleaseTime(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-hold-later")
	app := fx.newApp(t, "ws-hold-later", "erin", "lab-hold-later", func(a *model.App) { a.RestoreHold = "r2" })
	if _, err := fx.db.Apps().Release(bg, model.Actor{Person: "bob"}, "r2", store.ReleaseStart, 1, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	res := reconcileNow(t, newRec(fx.db), app.ID)
	if res.RequeueAfter < 59*time.Minute || res.RequeueAfter > time.Hour {
		t.Errorf("RequeueAfter = %v, want about an hour", res.RequeueAfter)
	}
	if fx.exists(&corev1.Pod{}, app.ObjectName()) {
		t.Fatal("an app released for later already has a pod")
	}
}
