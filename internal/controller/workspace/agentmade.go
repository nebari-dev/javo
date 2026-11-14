package workspace

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"

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

// AgentLockError is why an app or job a coding agent made cannot start in
// that agent's lock (reasons.AgentLockUnavailable).
type AgentLockError struct{ Why string }

func (e *AgentLockError) Error() string { return e.Why }

// madeError is the refusal of an app or job (what) that the coding agent
// by made and that cannot start in its lock because of why; nil for no why.
func madeError(what, by, why string) error {
	if why == "" {
		return nil
	}
	return &AgentLockError{Why: fmt.Sprintf("a coding agent made this %s or changed what it runs (%s), so it starts locked in as that agent is (workloads.landlock), and it cannot: %s. "+
		"To run it as your own, outside the lock, change what it runs in the browser", what, by, why)}
}

// MakerOf is the coding agent app that made an app or job of owner
// (madeBy, its MadeBy): the owner's app of that name, while it is there
// and its template is a coding agent's. Nil for what no agent made and for
// what was made by an agent that is gone.
func MakerOf(ctx context.Context, db *store.Store, owner, madeBy string) (*model.App, error) {
	if madeBy == "" {
		return nil, nil
	}
	maker, err := db.Apps().GetByOwnerName(ctx, owner, madeBy)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && !maker.DeletedAt.IsZero():
		return nil, nil
	case err != nil:
		return nil, err
	}
	switch t, err := db.Templates().Get(ctx, maker.Template); {
	case errors.Is(err, store.ErrNotFound), err == nil && t.Spec.Category != model.CategoryAgents:
		return nil, nil
	case err != nil:
		return nil, err
	}
	return &maker, nil
}

// MadeRun holds a run of a job that a coding agent made to that agent's
// limits, the way Plan holds an app, for the job planner
// (internal/controller/jobs), so the two never differ.
type MadeRun struct {
	// By is the job's MadeBy; a job no agent made is left as it is. Maker
	// is that agent (MakerOf), nil when it is gone.
	By    string
	Maker *model.App
	// Template is the job's, and Picked says the job brings an
	// environment of its own.
	Template *model.Template
	Picked   bool
	// Lock says the platform locks coding agents in (workloads.landlock).
	Lock bool
	// Folder is the folder of the job's own runs, relative to the home:
	// the runner keeps each run's log and results there.
	Folder string
	// Host is where the run is planned as processes; nil for a container.
	Host *RunOnHost
}

// Problem is why the run cannot start in the agent's lock; nil when it
// can, when no agent made the job, and while the platform locks no agent
// in. Such a run is not started without the lock instead.
func (m MadeRun) Problem() error {
	if m.By == "" || !m.Lock {
		return nil
	}
	if err := madeError("job", m.By, cannotLock(m.Template, m.Maker, m.Picked)); err != nil {
		return err
	}
	if m.Host != nil {
		return needsLandlock(m.Host.at.host)
	}
	return nil
}

// Confine plans s, the run, within the agent's limits. In a container no
// unit sees the SSH keys (hideSSH). Where agents are locked in, the run's
// whole command (the runner, and so the engine and everything it starts)
// runs under agent-lock.sh, locked to the folder of the job's runs and let
// into what the agent opens: its folder, to read and change, and the
// folders its owner added to it. The lock is told where the runner is, to
// run it (envRunnerDir). It comes last: after every step s gets.
func (m MadeRun) Confine(s *workload.Spec) error {
	if m.By == "" {
		return nil
	}
	if err := m.Problem(); err != nil {
		return err
	}
	at := containerPlaces
	if m.Host != nil {
		at = m.Host.at
	}
	if m.Lock {
		s.Main.SetEnv(workload.Env{Name: envRunnerDir, Value: path.Dir(s.Main.Command[0])})
		opens := slices.Concat([]string{"--rw", at.lockFolder(m.Maker)}, at.lockGrants(m.Maker))
		at.lockMain(s, slices.Concat([]string{at.agentLock(), path.Join(at.home, m.Folder)}, opens, []string{"--"}, s.Main.Command))
	}
	if m.Host == nil {
		hideSSH(s, at.home)
	}
	return nil
}

// envRunnerDir tells agent-lock.sh the folder a job's run has its runner
// in, a volume of the run's own, to grant it to read and run.
const envRunnerDir = "JAVO_RUNNER_DIR"
