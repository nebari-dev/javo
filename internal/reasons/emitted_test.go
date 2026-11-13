package reasons_test

import (
	"slices"
	"strings"
	"testing"

	jobstatus "github.com/nebari-dev/javo/internal/jobs/status"
	. "github.com/nebari-dev/javo/internal/reasons"
)

// emittedByJobs are the codes the run derivation emits (its own list) plus the
// codes the runner, reconciler and app-kind packages emit, listed here
// because those packages have no EmittedReasons call of their own.
func emittedByJobs() []Code {
	codes := []Code{
		CellError, KernelDied, KernelMissing, EngineMissing, SourceMissing,
		OutputWriteFailed, RunSkipped, RunMissed,
		RunCancelled, RunInterrupted, ParametersInvalid, RunLimitReached,
		JobsNotAllowed, OwnerLostAccess, SchedulePaused, ScheduleInvalid, NotificationFailed, JobSizeUnknown, EnvironmentUnavailable,
		RunsUnsupported,
		TemplateMissing, NetworkPolicyNotEnforced, CommandFailed,
	}
	for _, c := range jobstatus.EmittedReasons() {
		// Pod codes shown on a stuck run are the workspace catalog's, and
		// their blame text is checked with the workspace codes.
		if slices.Contains([]Code{NoCapacity, HomeBusy, ImagePullFailed}, Code(c)) {
			continue
		}
		codes = append(codes, Code(c))
	}
	return codes
}

func TestJobAndKindCodesHaveCopyAndAgreeingBlame(t *testing.T) {
	opening := map[Party]string{PartyUser: "Your", PartyPlatform: "The platform", PartyTemplate: "The template"}
	for _, code := range emittedByJobs() {
		r, ok := Lookup(code)
		if !ok {
			t.Errorf("%s missing from catalog", code)
			continue
		}
		if r.Title == "" || r.What == "" || r.Todo == "" || r.Who == "" {
			t.Errorf("%s has empty copy: %+v", code, r)
		}
		if strings.HasPrefix(r.Blame, "Nothing is wrong") {
			continue
		}
		if !strings.HasPrefix(r.Blame, opening[r.Blamed]) {
			t.Errorf("%s blamed %s but Blame opens %q", code, r.Blamed, r.Blame)
		}
	}
}
