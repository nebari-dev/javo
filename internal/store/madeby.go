package store

import (
	"reflect"

	"github.com/nebari-dev/javo/internal/model"
)

// An app or job a coding agent's token made, or whose code that token
// changed, must reach no more than the agent does, so each row says which
// agent decides what it runs (model.App.MadeBy, model.Job.MadeBy). The rule
// is here and nowhere else: every create and every spec write of both
// tables passes through it (rowKind.madeBy), so no route can forget it and
// none can set the mark itself.

// madeBy is the agent that decides what a row of owner runs after actor's
// write, given the one that did before (was) and whether the write changed
// what the row runs. A write that changed nothing of it keeps the mark. One
// by a coding agent's token (actor.Agent) sets it to that agent. One by the
// owner in the browser, with no token, takes it off: what it runs is then
// their choice. Anyone else's (a personal token, a trusted agent, an admin,
// the platform) keeps it, so no token ever takes it off. A new row counts
// as changed and was marked as it is handed in: an app an admin brings back
// from a backup keeps its mark.
func madeBy(actor model.Actor, owner, was string, changed bool) string {
	switch {
	case !changed:
		return was
	case actor.Agent != "":
		return actor.Agent
	case actor.Person == owner && actor.Token == "" && actor.System == "":
		return ""
	}
	return was
}

// appMadeBy sets next.MadeBy. What an app runs is its template, file,
// sample, command, image, port, plain variables, repository and
// environment; its name, size, sharing, idle stop and whether it is
// stopped are not.
func appMadeBy(actor model.Actor, next, old *model.App) {
	runs := func(a *model.App) any {
		return []any{a.Template, a.File, a.Sample, a.Command, a.Image, a.Port, a.Env, a.Source, a.Environment}
	}
	was, changed := next.MadeBy, true
	if old != nil {
		was, changed = old.MadeBy, !reflect.DeepEqual(runs(next), runs(old))
	}
	next.MadeBy = madeBy(actor, next.Owner, was, changed)
}

// jobMadeBy sets next.MadeBy. What a job's runs execute is everything of
// its spec but its name, schedule, run limits, history and notifications.
func jobMadeBy(actor model.Actor, next, old *model.Job) {
	runs := func(j *model.Job) model.JobSpec {
		s := j.Spec
		s.DisplayName, s.Schedule, s.Run, s.History, s.Notify = "", nil, model.JobRunPolicy{}, model.JobHistory{}, model.JobNotify{}
		return s
	}
	was, changed := next.MadeBy, true
	if old != nil {
		was, changed = old.MadeBy, !reflect.DeepEqual(runs(next), runs(old))
	}
	next.MadeBy = madeBy(actor, next.Owner, was, changed)
}
