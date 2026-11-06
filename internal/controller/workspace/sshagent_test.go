package workspace

import (
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/sizes"
)

func initContainer(pod *corev1.Pod, name string) (int, *corev1.Container) {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == name {
			return i, &pod.Spec.InitContainers[i]
		}
	}
	return -1, nil
}

func mountAtPath(c *corev1.Container, p string) *corev1.VolumeMount {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].MountPath == p {
			return &c.VolumeMounts[i]
		}
	}
	return nil
}

func volumeNamed(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

// agentPod is the pod of an app from a shipped template, with its
// environment's install step, as the reconciler builds it.
func agentPod(t *testing.T, name string) *corev1.Pod {
	t.Helper()
	tpl := shippedTemplate(t, name)
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name}
	pod, err := BuildPod(ws, tpl, "/u/a/agent/", "", testLayout(FixedUID), sizes.Size{}, testImages)
	if err != nil {
		t.Fatal(err)
	}
	if err := pixienv.Apply(&pod.Spec, testPixiImage, testImages.Runner, pixienv.Source{Default: tpl.Spec.Environment}); err != nil {
		t.Fatal(err)
	}
	WithSSHAgent(pod, tpl)
	return pod
}

// A coding agent never reads the owner's private keys: its ~/.ssh is an
// empty folder of the pod, and SSH reaches the keys through the socket of
// an ssh-agent sidecar, which alone sees the real ~/.ssh, read-only.
func TestCodingAgentsUseSSHThroughAnAgentSidecar(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"pi", "claude-code", "herdr"} {
		t.Run(name, func(t *testing.T) {
			pod := agentPod(t, name)
			app := &pod.Spec.Containers[0]

			dotSSH := mountAtPath(app, homeDir+"/.ssh")
			if dotSSH == nil || dotSSH.SubPath != "" {
				t.Fatalf("app mounts at ~/.ssh: %+v, want an emptyDir", dotSSH)
			}
			if v := volumeNamed(pod, dotSSH.Name); v == nil || v.EmptyDir == nil {
				t.Fatalf("volume %q is %+v, want an emptyDir", dotSSH.Name, v)
			}
			sock := mountAtPath(app, sshAgentDir)
			if sock == nil {
				t.Fatalf("app does not mount the socket folder %s", sshAgentDir)
			}
			if v := volumeNamed(pod, sock.Name); v == nil || v.EmptyDir == nil || sock.Name == dotSSH.Name {
				t.Fatalf("socket volume %q is %+v, want an emptyDir of its own", sock.Name, v)
			}
			if got, ok := plainEnv(pod, "SSH_AUTH_SOCK"); !ok || got != sshAgentDir+"/agent.sock" {
				t.Fatalf("SSH_AUTH_SOCK = %q, want %s/agent.sock", got, sshAgentDir)
			}

			i, side := initContainer(pod, SSHAgentContainer)
			if side == nil {
				t.Fatalf("no %s init container in %v", SSHAgentContainer, pod.Spec.InitContainers)
			}
			if side.RestartPolicy == nil || *side.RestartPolicy != corev1.ContainerRestartPolicyAlways {
				t.Fatalf("sidecar restartPolicy = %v, want Always (a native sidecar)", side.RestartPolicy)
			}
			// It starts once the environment it runs ssh-agent from is installed.
			if j, _ := initContainer(pod, pixienv.InstallContainer); j < 0 || j > i {
				t.Fatalf("sidecar at %d, environment install at %d: want the install first", i, j)
			}
			if side.Image != app.Image {
				t.Fatalf("sidecar image %q, want the app's %q", side.Image, app.Image)
			}
			if argv := slices.Concat(side.Command, side.Args); len(argv) < 2 || argv[0] != "/opt/javo/pod-user.sh" || argv[1] != "/opt/javo/agents/ssh-agent.sh" {
				t.Fatalf("sidecar argv = %q, want pod-user.sh running ssh-agent.sh", argv)
			}
			home := mountAtPath(side, homeDir)
			appHome := mountAtPath(app, homeDir)
			if home == nil || !home.ReadOnly || home.Name != appHome.Name || home.SubPath != appHome.SubPath {
				t.Fatalf("sidecar home mount %+v, want the app's home (%+v) read-only", home, appHome)
			}
			for _, m := range side.VolumeMounts {
				if m.MountPath != sshPublicDir && m.MountPath != sshAgentDir && !m.ReadOnly {
					t.Fatalf("sidecar mount %+v is writable; only its two emptyDirs may be", m)
				}
				if m.MountPath == homeDir+"/.ssh" {
					t.Fatalf("sidecar mounts the agent's ~/.ssh: %+v", m)
				}
			}
			if pub := mountAtPath(side, sshPublicDir); pub == nil || pub.Name != dotSSH.Name {
				t.Fatalf("sidecar copies public files into %+v, want the app's ~/.ssh volume %q", pub, dotSSH.Name)
			}
			if s := mountAtPath(side, sshAgentDir); s == nil || s.Name != sock.Name {
				t.Fatalf("sidecar socket mount %+v, want volume %q", s, sock.Name)
			}
			for _, e := range side.Env {
				if e.ValueFrom != nil || (e.Name != "HOME" && e.Name != "JAVO_USER") {
					t.Fatalf("sidecar env %+v: it gets none of the app's variables or secrets", e)
				}
			}
			sc := side.SecurityContext
			if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
				sc.Capabilities == nil || !slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) ||
				sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
				t.Fatalf("sidecar security context %+v, want restricted with a read-only root", sc)
			}
			if p := side.StartupProbe; p == nil || p.Exec == nil {
				t.Fatalf("sidecar startup probe %+v, want an exec probe for the ready marker", p)
			}
		})
	}
}

// Templates outside the coding agents category get no sidecar and keep
// their pod as it was.
func TestOtherTemplatesGetNoSSHAgent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"jupyterlab", "terminal", "vscode", "streamlit"} {
		t.Run(name, func(t *testing.T) {
			tpl := shippedTemplate(t, name)
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "x", Owner: "alice", Template: tpl.Name}
			pod, err := BuildPod(ws, tpl, "/u/a/x/", "", testLayout(FixedUID), sizes.Size{}, testImages)
			if err != nil {
				t.Fatal(err)
			}
			before := pod.DeepCopy()
			WithSSHAgent(pod, tpl)
			if !reflect.DeepEqual(before, pod) {
				t.Fatalf("pod changed for %s", name)
			}
		})
	}
}

// The reconciler adds the sidecar after the environment's install step, so
// the pod it creates starts ssh-agent from the installed environment.
func TestAgentAppPodHasTheSSHAgentSidecar(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "agent-ssh1", func(s *model.TemplateSpec) {
		s.Category, s.Environment = model.CategoryAgents, "agents"
		s.Kind, s.Port = "claude-code", 7681
		s.Command, s.Args = []string{"/opt/javo/pod-user.sh"}, []string{"ttyd"}
		s.PodFiles = true
	})
	app := fx.newApp(t, "ws-ssh1", "alice", "agent-ssh1")
	var pod *corev1.Pod
	eventually(t, "an agent pod", func() bool {
		pod = fx.podOf(app)
		return pod != nil
	})
	i, side := initContainer(pod, SSHAgentContainer)
	j, _ := initContainer(pod, pixienv.InstallContainer)
	if side == nil || j < 0 || j > i {
		t.Fatalf("sidecar at %d, install at %d in %v", i, j, pod.Spec.InitContainers)
	}
	if got, ok := plainEnv(pod, "SSH_AUTH_SOCK"); !ok || got != sshAgentDir+"/agent.sock" {
		t.Fatalf("SSH_AUTH_SOCK = %q", got)
	}
}
