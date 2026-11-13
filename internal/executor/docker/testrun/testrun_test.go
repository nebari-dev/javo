//go:build e2e

package testrun

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// sweepEnv asks for what test processes that were killed left on the
// daemon to be removed: it holds how old a thing must be to go ("2h").
// Nothing else removes another test process's things, and a process that
// is still running against the same daemon loses the ones that old.
const sweepEnv = "JAVO_TEST_SWEEP_OLDER_THAN"

// Run on purpose, with sweepEnv set, it removes what earlier test
// processes left on the daemon and did not sweep themselves.
func TestSweepLeftovers(t *testing.T) {
	age, err := time.ParseDuration(os.Getenv(sweepEnv))
	if err != nil || age <= 0 {
		t.Skipf("set %s to an age (such as 2h) to remove what killed test runs left on the daemon", sweepEnv)
	}
	socket := Socket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	all, err := things(ctx, socket, Label)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("removed %d of %d things, those older than %s", remove(ctx, t, socket, all, time.Now().Add(-age)), len(all), age)
}

// A test process removes what it made, and leaves what another made.
func TestSweepRemovesOnlyWhatThisProcessMade(t *testing.T) {
	socket := Socket(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	other := map[string]string{Label: ID + "-another"}
	for name, labels := range map[string]map[string]string{"mine": Stamp(), "theirs": other} {
		body := map[string]any{"Name": "javo-testrun-" + ID + "-" + name, "Labels": labels}
		if err := Call(ctx, socket, http.MethodPost, "/volumes/create", nil, body, nil); err != nil {
			t.Fatal(err)
		}
	}
	theirs := "/volumes/javo-testrun-" + ID + "-theirs"
	defer func() { _ = Call(ctx, socket, http.MethodDelete, theirs, nil, nil, nil) }()

	Sweep(t, socket)
	if err := Call(ctx, socket, http.MethodGet, "/volumes/javo-testrun-"+ID+"-mine", nil, nil, nil); err == nil {
		t.Error("the volume this process made stayed")
	}
	if err := Call(ctx, socket, http.MethodGet, theirs, nil, nil, nil); err != nil {
		t.Errorf("the volume of another process: %v", err)
	}
}
