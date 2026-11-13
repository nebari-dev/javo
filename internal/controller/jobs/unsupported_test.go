package jobs

import (
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// On a platform that cannot run jobs yet (a Reconciler without an Exec)
// a job is Blocked and says why, its schedule fires nothing, and a run a
// person asks for fails with the same reason instead of waiting for ever.
func TestAJobWhereNothingRunsJobsIsBlockedAndSaysSo(t *testing.T) {
	h := newHarnessWith(t, func(r *Reconciler) { r.Exec, r.Legacy = nil, nil })
	h.template(nil)
	d := h.definition("nightly", func(j *model.Job) {
		j.Spec.Schedule = &model.JobSchedule{Cron: "* * * * *", TimeZone: "UTC"}
	})
	got := h.waitPhase(d, model.JobPhaseBlocked)
	for _, typ := range []string{model.ConditionValid, model.ConditionScheduled} {
		if r := condReason(got, typ); r != string(reasons.RunsUnsupported) {
			t.Errorf("%s reason = %q, want RunsUnsupported", typ, r)
		}
	}
	run, err := h.db.Jobs().RequestRun(bg, actor, d.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	failed := h.waitRunWhere(d, run.ID, "failed", func(r model.Run) bool { return r.Outcome == model.RunOutcomeFailed })
	if failed.Reason != string(reasons.RunsUnsupported) || failed.Handle != "" {
		t.Fatalf("the run a person asked for: %+v", failed)
	}
	for _, r := range h.runs(d) {
		if r.Trigger == TriggerSchedule {
			t.Fatalf("the schedule fired %+v", r)
		}
	}
}
