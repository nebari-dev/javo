package status

import (
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/workload"
)

func TestEmittedReasonsAreCataloged(t *testing.T) {
	for _, c := range EmittedReasons() {
		if _, ok := reasons.Lookup(reasons.Code(c)); !ok {
			t.Errorf("%s emitted but missing from the catalog", c)
		}
	}
}

// A run that ended without a result its runner wrote is Lost, and says
// what the executor saw of its last attempt when it saw something. On
// Kubernetes, which says nothing more, the words stay as they were.
func TestALostRunSaysWhatTheExecutorSaw(t *testing.T) {
	ended := workload.RunState{Done: true, Attempts: 1}
	plain := DeriveRun(&workload.State{Run: &ended})
	if plain.Outcome != "Lost" || plain.Message != "The run stopped without reporting a result." {
		t.Fatalf("with nothing seen: %+v", plain)
	}
	ended.Why = "unit runner-install did not start: no such image"
	seen := DeriveRun(&workload.State{Run: &ended})
	if seen.Outcome != "Lost" || seen.Reason != string(reasons.RunLost) || !strings.Contains(seen.Message, ended.Why) {
		t.Fatalf("with a failure seen: %+v", seen)
	}
}
