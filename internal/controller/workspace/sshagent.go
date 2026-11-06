package workspace

import (
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podspec"
)

const (
	// SSHAgentContainer is the native sidecar of a coding agent's pod that
	// holds the owner's SSH keys (pod/files/agents/ssh-agent.sh).
	SSHAgentContainer = "ssh-agent"

	// sshAgentDir holds the agent's socket and its ready marker, in both
	// containers.
	sshAgentDir = "/run/javo-ssh"
	// sshPublicDir is where the sidecar sees the agent container's ~/.ssh,
	// which it fills with known_hosts, config and the public keys.
	sshPublicDir = "/run/javo-ssh-public"

	sshAgentVolume  = "ssh-agent"
	sshPublicVolume = "ssh-public"
)

// WithSSHAgent keeps the owner's private SSH keys out of reach of a coding
// agent (a template of model.CategoryAgents) while SSH still works: the app
// container's ~/.ssh is an empty folder of the pod, and SSH_AUTH_SOCK points
// at an ssh-agent run by a native sidecar, the only container that sees the
// real ~/.ssh (the home, read-only). The sidecar copies known_hosts, config
// and the public keys into the app's ~/.ssh and loads the keys that need no
// passphrase. It runs in the app's image and environment, so it comes after
// the environment's install step (withEnvironment). Other templates are left
// as they are.
func WithSSHAgent(pod *corev1.Pod, t *model.Template) {
	if t.Spec.Category != model.CategoryAgents {
		return
	}
	spec := &pod.Spec
	app := &spec.Containers[0]
	empty := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	spec.Volumes = append(spec.Volumes,
		corev1.Volume{Name: sshAgentVolume, VolumeSource: empty},
		corev1.Volume{Name: sshPublicVolume, VolumeSource: empty})

	// The sidecar sees what pod-user.sh needs to run ssh-agent from the
	// environment (the pod files, the identity files, the activation and the
	// home holding the installed environment), all read-only.
	var mounts []corev1.VolumeMount
	for _, m := range app.VolumeMounts {
		if m.MountPath == homeDir || m.MountPath == model.PodFilesDir || m.MountPath == pixienv.Dir || strings.HasPrefix(m.MountPath, "/etc/") {
			m.ReadOnly = true
			mounts = append(mounts, m)
		}
	}
	mounts = append(mounts,
		corev1.VolumeMount{Name: sshAgentVolume, MountPath: sshAgentDir},
		corev1.VolumeMount{Name: sshPublicVolume, MountPath: sshPublicDir})
	spec.InitContainers = append(spec.InitContainers, corev1.Container{
		Name:          SSHAgentContainer,
		Image:         app.Image,
		RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
		Command:       []string{path.Join(model.PodFilesDir, "pod-user.sh"), path.Join(model.PodFilesDir, "agents", "ssh-agent.sh")},
		Args:          []string{path.Join(homeDir, ".ssh"), sshPublicDir, path.Join(sshAgentDir, "agent.sock")},
		Env:           []corev1.EnvVar{{Name: "HOME", Value: homeDir}},
		VolumeMounts:  mounts,
		// The app starts once the keys are loaded (or found missing), so
		// its first git command already has them.
		StartupProbe: &corev1.Probe{
			ProbeHandler:     corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"test", "-e", path.Join(sshAgentDir, "ready")}}},
			PeriodSeconds:    1,
			FailureThreshold: 60,
		},
		SecurityContext:          podspec.ReadOnlyRootContext(),
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	})
	app.VolumeMounts = append(app.VolumeMounts,
		corev1.VolumeMount{Name: sshPublicVolume, MountPath: path.Join(homeDir, ".ssh")},
		corev1.VolumeMount{Name: sshAgentVolume, MountPath: sshAgentDir, ReadOnly: true})
	podspec.SetEnv(app, corev1.EnvVar{Name: "SSH_AUTH_SOCK", Value: path.Join(sshAgentDir, "agent.sock")})
}
