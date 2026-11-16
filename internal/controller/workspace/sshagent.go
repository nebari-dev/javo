package workspace

import (
	"path"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/workload"
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

// addSSHAgent keeps the owner's private SSH keys out of reach of a coding
// agent (a template of model.CategoryAgents) while SSH still works: the
// main unit's ~/.ssh is an empty folder of the workload, and SSH_AUTH_SOCK
// points at an ssh-agent run by a step that stays, the only unit that sees
// the real ~/.ssh (the home, read-only). That step copies known_hosts,
// config and the public keys into the app's ~/.ssh and loads the keys that
// need no passphrase. It runs in the app's image and environment, so it
// comes after the environment's install step. Other templates are left as
// they are.
//
// own leaves the main unit's ~/.ssh as the home has it: its owner let the
// agent into it (ownsSSH). The lock, which alone then keeps the agent to
// what was added, is told not to open the folder by itself (envSSHOwn).
// The step stays, so that SSH still finds the keys when only a part of the
// folder was added (its config, say).
func addSSHAgent(s *workload.Spec, t *model.Template, own bool) {
	if t.Spec.Category != model.CategoryAgents {
		return
	}
	app := &s.Main
	s.Volumes = append(s.Volumes, workload.Volume{Name: sshAgentVolume}, workload.Volume{Name: sshPublicVolume})

	// The step sees what pod-user.sh needs to run ssh-agent from the
	// environment (the pod files, the identity files, the activation and the
	// home holding the installed environment), all read-only.
	var mounts []workload.Mount
	for _, m := range app.Mounts {
		if m.Path == homeDir || m.Path == model.PodFilesDir || m.Path == pixienv.Dir || strings.HasPrefix(m.Path, "/etc/") {
			m.ReadOnly = true
			mounts = append(mounts, m)
		}
	}
	mounts = append(mounts,
		workload.Mount{Volume: sshAgentVolume, Path: sshAgentDir},
		workload.Mount{Volume: sshPublicVolume, Path: sshPublicDir})
	s.Steps = append(s.Steps, workload.Unit{
		Name: SSHAgentContainer, Image: app.Image, ReadOnlyRoot: true, Stays: true,
		Command: []string{path.Join(model.PodFilesDir, "pod-user.sh"), path.Join(model.PodFilesDir, "agents", "ssh-agent.sh")},
		Args:    []string{path.Join(homeDir, ".ssh"), sshPublicDir, path.Join(sshAgentDir, "agent.sock")},
		Env:     []workload.Env{{Name: "HOME", Value: homeDir}},
		Mounts:  mounts,
		// The app starts once the keys are loaded (or found missing), so
		// its first git command already has them.
		Ready: &workload.Check{Exec: []string{"test", "-e", path.Join(sshAgentDir, "ready")}, Tries: 60},
	})
	if own {
		app.SetEnv(workload.Env{Name: envSSHOwn, Value: "1"})
	} else {
		app.Mounts = append(app.Mounts, workload.Mount{Volume: sshPublicVolume, Path: path.Join(homeDir, ".ssh")})
	}
	app.Mounts = append(app.Mounts, workload.Mount{Volume: sshAgentVolume, Path: sshAgentDir, ReadOnly: true})
	app.SetEnv(workload.Env{Name: "SSH_AUTH_SOCK", Value: path.Join(sshAgentDir, "agent.sock")})
}

// envSSHOwn tells agent-lock.sh that the container's ~/.ssh is the
// person's own, private keys and all, not the folder of public files it
// otherwise grants to read: only what the owner added of it is opened.
const envSSHOwn = "JAVO_SSH_OWN"

// ownsSSH reports whether the coding agent app of in is to see its owner's
// real ~/.ssh: it starts in a lock of its own, and its owner added the
// folder, or something in it, to what it opens (LockFolders: after the
// warning). An app an agent made never does, whatever its agent was
// allowed.
func ownsSSH(in Inputs) bool {
	if in.App.MadeBy != "" || !locksAgent(in.Template, in.Images.LockAgents) {
		return false
	}
	added, _ := LockFolders(in.App, in.App.Folders)
	return slices.ContainsFunc(added, func(g model.FolderGrant) bool { return within(".ssh", g.Path) })
}
