package status

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func ws(paused bool) *model.App {
	return &model.App{Owner: "alice", Template: "t", Paused: paused}
}

func find(cs []metav1.Condition, t string) *metav1.Condition {
	for i := range cs {
		if cs[i].Type == t {
			return &cs[i]
		}
	}
	return nil
}

func TestDeriveNoPodYet(t *testing.T) {
	phase, _, _ := Derive(ws(false), Observed{Now: now})
	if phase != "Pending" {
		t.Errorf("phase = %q, want Pending", phase)
	}
}

func TestDerivePausedBeatsTemplateProblems(t *testing.T) {
	phase, _, _ := Derive(ws(true), Observed{TemplateMissing: true, Now: now})
	if phase != "Stopped" {
		t.Fatalf("phase = %q, want Stopped", phase)
	}
}

func TestIdentityPendingBlocksHomeBeforeAnyPod(t *testing.T) {
	ws := &model.App{}
	phase, conds, _ := Derive(ws, Observed{Now: time.Now(), IdentityPending: true})
	if phase != "Pending" {
		t.Fatalf("phase = %q", phase)
	}
	for _, c := range conds {
		if c.Type == model.ConditionHomeReady && (c.Status != metav1.ConditionFalse || c.Reason != string(reasons.IdentityPending)) {
			t.Fatalf("HomeReady = %+v", c)
		}
	}
}

// An app the idle check stopped says so, with its timeout as evidence.
func TestDeriveStoppedForBeingIdle(t *testing.T) {
	app := ws(true)
	app.StoppedIdleAfter = 60
	phase, conds, _ := Derive(app, Observed{Now: now})
	if phase != "Stopped" {
		t.Errorf("phase = %q", phase)
	}
	c := find(conds, model.ConditionReady)
	if c == nil || c.Reason != string(reasons.StoppedIdle) || !strings.Contains(c.Message, "Stopped after 60 minutes without use") {
		t.Errorf("Ready = %+v", c)
	}
}

// A home that is not ready blocks the app before any pod exists, with the
// home's own reason and what it found.
func TestHomeNotReadyBlocksHomeBeforeAnyPod(t *testing.T) {
	for code, wantPhase := range map[reasons.Code]string{
		reasons.HomePreparing:   "Pending",
		reasons.HomePending:     "Pending",
		reasons.HomeTooLarge:    "Failed",
		reasons.TeamStorageFull: "Failed",
		reasons.HomeMountFailed: "Failed",
	} {
		phase, conds, ev := Derive(&model.App{}, Observed{Now: now, HomeReason: code, HomeEvidence: "quota used"})
		if phase != wantPhase {
			t.Errorf("%s: phase = %q, want %q", code, phase, wantPhase)
		}
		for _, c := range conds {
			if c.Type == model.ConditionHomeReady && (c.Status != metav1.ConditionFalse || c.Reason != string(code)) {
				t.Errorf("%s: HomeReady = %+v", code, c)
			}
		}
		if len(ev) != 1 || ev[0] != "quota used" {
			t.Errorf("%s: evidence = %v", code, ev)
		}
	}
}

// A Home that is being copied to another mode holds its apps back: Stopped
// (the platform's doing, nothing failed) with HomeMigrating on HomeReady.
func TestHomeMigratingStopsTheAppWithoutFailingIt(t *testing.T) {
	phase, conds, _ := Derive(&model.App{}, Observed{Now: now, HomeMigrating: true})
	if phase != "Stopped" {
		t.Fatalf("phase = %q, want Stopped", phase)
	}
	for _, c := range conds {
		if c.Type == model.ConditionHomeReady && (c.Status != metav1.ConditionFalse || c.Reason != string(reasons.HomeMigrating)) {
			t.Errorf("HomeReady = %+v", c)
		}
	}
}

// A restored app waiting for an admin says so on every step, whether it was
// running or stopped, and is never reported as starting.
func TestDeriveHeldAfterRestore(t *testing.T) {
	for _, paused := range []bool{false, true} {
		app := ws(paused)
		app.RestoreHold = "javo-20261101T020000Z"
		phase, conds, _ := Derive(app, Observed{Now: now})
		if phase != "Pending" {
			t.Errorf("paused %v: phase = %q", paused, phase)
		}
		for _, c := range conds {
			if c.Reason != string(reasons.RestoreHeld) || c.Status != metav1.ConditionFalse {
				t.Errorf("paused %v: %s = %s %s", paused, c.Type, c.Status, c.Reason)
			}
		}
	}
}

// An app a platform admin stopped says so, so its owner knows why.
func TestDeriveStoppedByAnAdmin(t *testing.T) {
	app := ws(true)
	app.StoppedByAdmin = true
	phase, conds, _ := Derive(app, Observed{Now: now})
	if phase != "Stopped" {
		t.Errorf("phase = %q", phase)
	}
	if c := find(conds, model.ConditionReady); c == nil || c.Reason != string(reasons.StoppedByAdmin) {
		t.Errorf("Ready = %+v", c)
	}
}
