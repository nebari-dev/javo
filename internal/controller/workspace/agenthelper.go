package workspace

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/agenthelper"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
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

// WithAgentHelper keeps a coding agent's credentials (a template of
// model.CategoryAgents) out of its container: the API token (JAVO_TOKEN)
// and the model key (JAVO_AGENT_KEY, the app's token on the platform's model
// or the owner's own key) move to a native sidecar running the platform's
// image (runner), which adds them to the calls it forwards. The agent gets
// JAVO_API_SOCKET, the helper's socket for javo-api, and the helper's local
// address as its model's base URL; the real one goes to the helper. It runs
// after the With* steps that set those variables. An agent with neither
// credential, and every other template, is left as it is.
func WithAgentHelper(pod *corev1.Pod, t *model.Template, runner podspec.Image) {
	if t.Spec.Category != model.CategoryAgents {
		return
	}
	spec := &pod.Spec
	app := &spec.Containers[0]
	var moved, copied []corev1.EnvVar
	for _, e := range app.Env {
		switch e.Name {
		case envToken, envAgentKey, envAgentBaseURL:
			moved = append(moved, e)
		case envURL, envInternalURL:
			copied = append(copied, e)
		}
	}
	if !slices.ContainsFunc(moved, func(e corev1.EnvVar) bool { return e.Name != envAgentBaseURL }) {
		return
	}
	app.Env = slices.DeleteFunc(app.Env, func(e corev1.EnvVar) bool {
		return e.Name == envToken || e.Name == envAgentKey || e.Name == envAgentBaseURL
	})
	podspec.SetEnv(app, corev1.EnvVar{Name: envAPISocket, Value: agentHelperSocket})
	if slices.ContainsFunc(moved, func(e corev1.EnvVar) bool { return e.Name == envAgentBaseURL }) {
		podspec.SetEnv(app, corev1.EnvVar{Name: envAgentBaseURL, Value: agentHelperModelURL})
	}

	runner.Pull(spec)
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: agentHelperVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	javo := "/" + model.ProductName
	spec.InitContainers = append(spec.InitContainers, corev1.Container{
		Name:          AgentHelperContainer,
		Image:         runner.Ref,
		RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
		Command:       []string{javo, "agent-helper", "--socket", agentHelperSocket, "--listen", agentHelperAddr},
		Env:           append(copied, moved...),
		VolumeMounts:  []corev1.VolumeMount{{Name: agentHelperVolume, MountPath: agentHelperDir}},
		// The agent starts once the helper answers, so its first call works.
		StartupProbe: &corev1.Probe{
			ProbeHandler:     corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{javo, "agent-helper", "--check", "--socket", agentHelperSocket}}},
			PeriodSeconds:    1,
			FailureThreshold: 30,
		},
		SecurityContext:          podspec.ReadOnlyRootContext(),
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	})
	app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: agentHelperVolume, MountPath: agentHelperDir, ReadOnly: true})
}
