package workspace

import (
	"slices"

	"github.com/nebari-dev/javo/internal/agenthelper"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// AgentHelperContainer is the native sidecar of a coding agent's pod
	// that holds its API token and model key (internal/agenthelper).
	AgentHelperContainer = "agent-helper"

	// agentHelperDir holds the helper's socket, in both containers.
	agentHelperDir    = "/run/javo-agent"
	agentHelperSocket = agentHelperDir + "/api.sock"
	agentHelperAddr   = "127.0.0.1:7690"
	// agentHelperModelURL is the model's base URL the agent is given.
	agentHelperModelURL = "http://" + agentHelperAddr + agenthelper.ModelPrefix
	agentHelperVolume   = "agent-helper"

	// envAPISocket tells javo-api to call the API through the helper.
	envAPISocket = "JAVO_API_SOCKET"
)

// addAgentHelper keeps a coding agent's credentials (a template of
// model.CategoryAgents) out of its main unit: the API token (JAVO_TOKEN)
// and the model key (JAVO_AGENT_KEY, the app's token on the platform's model
// or the owner's own key) move to a step that stays, running the platform's
// image (runner), which adds them to the calls it forwards. The agent gets
// JAVO_API_SOCKET, the helper's socket for javo-api, and the helper's local
// address as its model's base URL; the real one goes to the helper. It runs
// after the steps that set those variables. An agent with neither
// credential, and every other template, is left as it is.
func addAgentHelper(s *workload.Spec, t *model.Template, runner podspec.Image) {
	if t.Spec.Category != model.CategoryAgents {
		return
	}
	app := &s.Main
	held := func(e workload.Env) bool {
		return e.Name == envToken || e.Name == envAgentKey || e.Name == envAgentBaseURL
	}
	var moved, copied []workload.Env
	for _, e := range app.Env {
		switch {
		case held(e):
			moved = append(moved, e)
		case e.Name == envURL || e.Name == envInternalURL:
			copied = append(copied, e)
		}
	}
	isBaseURL := func(e workload.Env) bool { return e.Name == envAgentBaseURL }
	if !slices.ContainsFunc(moved, func(e workload.Env) bool { return !isBaseURL(e) }) {
		return
	}
	app.Env = slices.DeleteFunc(app.Env, held)
	app.SetEnv(workload.Env{Name: envAPISocket, Value: agentHelperSocket})
	if slices.ContainsFunc(moved, isBaseURL) {
		app.SetEnv(workload.Env{Name: envAgentBaseURL, Value: agentHelperModelURL})
	}

	s.Pull(runner.PullSecrets)
	s.Volumes = append(s.Volumes, workload.Volume{Name: agentHelperVolume})
	javo := "/" + model.ProductName
	s.Steps = append(s.Steps, workload.Unit{
		Name: AgentHelperContainer, Image: runner.Ref, ReadOnlyRoot: true, Stays: true,
		Command: []string{javo, "agent-helper", "--socket", agentHelperSocket, "--listen", agentHelperAddr},
		Env:     append(copied, moved...),
		Mounts:  []workload.Mount{{Volume: agentHelperVolume, Path: agentHelperDir}},
		// The agent starts once the helper answers, so its first call works.
		Ready: &workload.Check{Exec: []string{javo, "agent-helper", "--check", "--socket", agentHelperSocket}, Tries: 30},
	})
	app.Mounts = append(app.Mounts, workload.Mount{Volume: agentHelperVolume, Path: agentHelperDir, ReadOnly: true})
}
