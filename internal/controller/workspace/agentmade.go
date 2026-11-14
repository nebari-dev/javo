package workspace

import (
	"fmt"
	"path"
	"slices"

	"github.com/nebari-dev/javo/internal/model"

	"github.com/nebari-dev/javo/internal/workload"
)

// What a coding agent's token made, or changed in what it runs (an app's or
// a job's MadeBy), reaches no more than that agent does. The agent's own
// pod sees no private SSH key (addSSHAgent) and, where the platform locks
// agents in, opens only its folders (addAgentLock); without the two steps
// here the agent would get both back by asking the platform to run a
// command for it.

// sshHiddenVolume is the empty folder a workload a coding agent made has
// at ~/.ssh.
const sshHiddenVolume = "ssh-hidden"

// hideSSH puts an empty folder of the workload over ~/.ssh in every unit
// of s that has the owner's home at home, steps included: a step may run
// or copy what the agent chose (an environment's install, the one file a
// static site serves). No ssh-agent is run for it. Nothing is hidden on a
// machine that runs it as processes, where an agent's own keys are not
// hidden either (Inputs.Host).
func hideSSH(s *workload.Spec, home string) {
	over := workload.Mount{Volume: sshHiddenVolume, Path: path.Join(home, ".ssh")}
	hidden := false
	hide := func(u *workload.Unit) {
		if slices.ContainsFunc(u.Mounts, func(m workload.Mount) bool { return m.Volume == homeVolume && m.Path == home }) {
			u.Mounts = append(u.Mounts, over)
			hidden = true
		}
	}
	hide(&s.Main)
	for i := range s.Steps {
		hide(&s.Steps[i])
	}
	if hidden {
		s.Volumes = append(s.Volumes, workload.Volume{Name: sshHiddenVolume})
	}
}

// madeProblem is why the app of in, which a coding agent made, cannot
// start in that agent's lock; nil when it can, when no agent made it, and
// while the platform locks no agent in. Such an app is not started without
// the lock instead.
func madeProblem(in Inputs) error {
	ws, t := in.App, in.Template
	if ws.MadeBy == "" || !in.Images.LockAgents {
		return nil
	}
	why := cannotLock(t, in.Maker, ws.Environment != nil)
	switch {
	case why != "":
	case ws.Source != nil:
		why = "it runs from a repository, whose steps run outside the lock"
	case t.Spec.Build != nil:
		why = "its site is built and copied outside the lock"
	}
	return madeError("app", ws.MadeBy, why)
}

// cannotLock is why nothing on t that the coding agent maker made can
// start in maker's lock, in words that follow "it cannot start, because";
// empty when it can. picked says it brings an environment of its own.
// The lock is the platform's files' (agent-lock.sh, javo-sandbox), and an
// environment's install step runs outside it.
func cannotLock(t *model.Template, maker *model.App, picked bool) string {
	switch {
	case maker == nil:
		return "that agent is gone"
	case !t.Spec.UsesPodFiles():
		return fmt.Sprintf("template %q has none of the platform's files, which carry the lock", t.Name)
	case picked:
		return "it brings an environment of its own, which is installed outside the lock"
	}
	return ""
}

// madeError is the refusal of an app or job (what) that the coding agent
// by made and that cannot start in its lock because of why; nil for no why.
func madeError(what, by, why string) error {
	if why == "" {
		return nil
	}
	return fmt.Errorf("a coding agent made this %s or changed what it runs (%s), so it starts locked in as that agent is (workloads.landlock), and it cannot: %s. "+
		"To run it as your own, outside the lock, change what it runs in the browser", what, by, why)
}
