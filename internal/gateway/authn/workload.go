package authn

import (
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
)

// WorkloadTokenWrites are the only operations, besides reads, that an app's
// or a job's own API token may call: starting, stopping and running what
// already exists, proposing a change for a person to approve, and the
// backup job's own calls. None writes a file or changes what an app or job
// runs.
//
// Everything else is refused, file writes and creating, importing or
// changing an app or a job included, because every app and job of a person
// runs code from their home: a token that could write there, or start
// something that does, could put code in front of the person's
// secret-holding jobs and apps, which is the way to the secrets that
// NoAppTokens keeps from it. A route added later is refused until it is
// added here.
var WorkloadTokenWrites = []string{
	"startWorkspace", "stopWorkspace", "restartWorkspace", "redeployWorkspace",
	"runJob", "pauseJob", "resumeJob", "cancelJobRun",
	"createPlan", "explainDiagnosis", "previewSchedule", "previewImportWorkspaces", "warmFiles",
	"adminStartBackupRun", "adminStartRestoreRun",
}

// WorkloadTokensReadOnly is API middleware that refuses a workload token on
// every operation that is not a read and not in WorkloadTokenWrites. It runs
// before the operation's input is read.
func WorkloadTokensReadOnly(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		switch m := hctx.Method(); {
		case m == http.MethodGet, m == http.MethodHead, m == http.MethodOptions,
			!viaAppToken(hctx.Context()), slices.Contains(WorkloadTokenWrites, hctx.Operation().OperationID):
			next(hctx)
			return
		}
		hctx.SetHeader("Content-Type", "application/problem+json")
		hctx.SetStatus(http.StatusForbidden)
		_ = api.Marshal(hctx.BodyWriter(), "application/problem+json", problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgWorkloadTokenReadOnly)))
	}
}
