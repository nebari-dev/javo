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

// AgentDeployWrites are the operations a coding agent's token
// (ViaAgentToken) may call besides WorkloadTokenWrites: creating, changing
// and deleting its owner's apps and jobs. Each handler refuses it on an app
// or job that holds a secret, an API token or a Nebi token, and refuses to
// give one any (envvars.AgentTarget, envvars.AgentCreate); sharing, API
// access, secrets and tokens stay off this list.
var AgentDeployWrites = []string{
	"createWorkspace", "deleteWorkspace", "setWorkspaceLaunch", "setWorkspaceSize", "setWorkspaceEnv", "setWorkspaceIdleStop",
	"createJob", "updateJob", "deleteJob", "setJobSize", "setJobEnv",
}

// WorkloadTokensReadOnly is API middleware that refuses a workload token on
// every operation that is not a read and not in WorkloadTokenWrites. It runs
// before the operation's input is read. A Trusted agent's token passes.
func WorkloadTokensReadOnly(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(hctx huma.Context, next func(huma.Context)) {
		switch m := hctx.Method(); {
		case m == http.MethodGet, m == http.MethodHead, m == http.MethodOptions,
			!restricted(hctx.Context()), slices.Contains(WorkloadTokenWrites, hctx.Operation().OperationID),
			ViaAgentToken(hctx.Context()) && slices.Contains(AgentDeployWrites, hctx.Operation().OperationID):
			next(hctx)
			return
		}
		hctx.SetHeader("Content-Type", "application/problem+json")
		hctx.SetStatus(http.StatusForbidden)
		_ = api.Marshal(hctx.BodyWriter(), "application/problem+json", problem.New(http.StatusForbidden, reasons.Msg(reasons.MsgWorkloadTokenReadOnly)))
	}
}
