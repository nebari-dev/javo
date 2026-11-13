package workspace

import (
	"cmp"
	"path"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/workload"
)

// agentLock is the pod file that starts a coding agent's container command
// locked in with Landlock: agent-lock.sh FOLDER -- COMMAND ARGS... It knows
// which folders the agents keep their state in and grants them, the folder
// and the system, through javo-sandbox (cmd/javo-sandbox), which the
// platform's image carries with the pod files (bin/javo-sandbox).
var agentLock = containerPlaces.agentLock()

// agentLock is agentLock at the places p: on a host the platform's files
// are a folder of that machine.
func (p places) agentLock() string {
	return path.Join(p.files, "agents", "agent-lock.sh")
}

// locksAgent reports whether an app of t starts locked in: the platform
// turned the lock on (lock: config workloads.landlock) and t is a coding
// agent.
func locksAgent(t *model.Template, lock bool) bool {
	return lock && t.Spec.Category == model.CategoryAgents
}

// ownProject is the folder a locked agent app with no folder of its own
// works in: ~/projects/<app name>, which agent-lock.sh makes if missing.
func (p places) ownProject(ws *model.App) string {
	return path.Join(p.home, "projects", path.Clean("/"+ws.Name))
}

// lockFolder is the folder a locked agent app works in and is locked to:
// the one it opens, else one of its own. Locked to the whole home the lock
// would keep nothing out.
func (p places) lockFolder(ws *model.App) string {
	return cmp.Or(p.filePath(ws), p.ownProject(ws))
}

// lockAgent is command run under the agent lock at lock (agentLock),
// locked to folder. The lock wraps the container's whole command, so the
// agent's terminal and every process the agent starts inherit it.
func lockAgent(lock, folder string, command []string) []string {
	return slices.Concat([]string{lock, folder, "--"}, command)
}

// addAgentLock starts the main unit of a coding agent locked in with
// Landlock when the platform turns the lock on (Images.LockAgents): its
// whole command runs under agent-lock.sh, locked to the folder the app
// works in (lockFolder, where runOf already started it). Nothing else of
// the workload changes: javo-sandbox comes with the platform's files, which
// every coding agent's template has, and agent-lock.sh grants by path what
// the other steps mount. Every other app, and every app while the lock is
// off, is left as it is. It runs before the steps that read the command
// (addEnvironment).
func addAgentLock(s *workload.Spec, ws *model.App, t *model.Template, lock bool, at places) {
	if !locksAgent(t, lock) {
		return
	}
	s.Main.Command = lockAgent(at.agentLock(), at.lockFolder(ws), s.Main.Command)
}

// lockProblem is why a coding agent cannot start locked in on the host
// in is planned for; nil when it can, or is not to be locked. Landlock is
// Linux's: on another system javo-sandbox refuses every command, and the
// agent is not run without the lock instead.
func lockProblem(in Inputs) error {
	if !locksAgent(in.Template, in.Images.LockAgents) || strings.HasPrefix(in.Host.Platform, "linux-") {
		return nil
	}
	return &executor.UnsupportedError{Why: "coding agents start locked in with Landlock (workloads.landlock), which needs Linux, and this machine is " +
		cmp.Or(in.Host.Platform, "not one pixi has a name for")}
}
