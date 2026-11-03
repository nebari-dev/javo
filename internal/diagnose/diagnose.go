// Package diagnose finds the cause of a failed app from what the cluster
// reported: the app's conditions (with the evidence lines the controller
// stored), the Pod's events and the app's last log lines. Rules are plain
// code, registered explicitly and tried in order; no model is involved.
package diagnose

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/status"
)

// MaxExcerpt is the most runes one piece of evidence carries.
const MaxExcerpt = 300

// EvidenceKind says where a piece of evidence came from.
type EvidenceKind string

const (
	KindCondition EvidenceKind = "condition"
	KindEvent     EvidenceKind = "event"
	KindLog       EvidenceKind = "log"
)

// Evidence is one quoted line the cluster or the app wrote. Ref is stable for
// the same input, so a model's answer can cite it and the server can check
// the citation: "condition/<Type>", "event/<event name>" or
// "log/<container>/<cur|prev>/<line>".
type Evidence struct {
	Kind    EvidenceKind `json:"kind" enum:"condition,event,log"`
	Ref     string       `json:"ref"`
	Excerpt string       `json:"excerpt"`
	At      *time.Time   `json:"at,omitempty"`
}

// LogLine is one line of a container's log; N counts from 1 in the tail read.
type LogLine struct {
	Container string
	Previous  bool
	N         int
	Text      string
}

// Ref is the line's evidence ref.
func (l LogLine) Ref() string {
	run := "cur"
	if l.Previous {
		run = "prev"
	}
	return fmt.Sprintf("log/%s/%s/%d", l.Container, run, l.N)
}

// Input is everything a rule may read. Secret values and environment
// variables are not part of it: the collector never reads them.
type Input struct {
	App    *model.App
	Pod    *corev1.Pod
	Events []corev1.Event
	Logs   []LogLine
	// Current is the size the app runs at; Sizes are the sizes its owner may
	// pick (for a proposal to run bigger).
	Current sizes.Size
	Sizes   []sizes.Size
}

// Finding is what a rule concluded: a catalog code, the evidence behind it
// and the changes that may fix it.
type Finding struct {
	Rule      string             `json:"rule"`
	Code      reasons.Code       `json:"code"`
	Evidence  []Evidence         `json:"evidence"`
	Proposals []actions.Proposal `json:"proposals"`
}

// Diagnosis is the engine's answer.
type Diagnosis struct {
	Finding
	// Known is false when no rule pinned the cause down: only the generic
	// code of the failing step, or Unknown.
	Known bool `json:"known"`
	// Bundle is every piece of evidence the engine saw: the only refs a
	// model may cite.
	Bundle []Evidence `json:"bundle"`
}

// Rule finds one cause. Match is called only for a failing app.
type Rule interface {
	ID() string
	Match(in *Input) (Finding, bool)
}

// Engine tries its rules in order and returns the first match.
type Engine struct{ rules []Rule }

// NewEngine returns an engine over rules, tried in the order given.
func NewEngine(rules ...Rule) *Engine { return &Engine{rules: rules} }

// genericCodes name a failing step without a cause: a match on one of these
// by the condition rule alone leaves the diagnosis not Known.
var genericCodes = map[reasons.Code]bool{
	reasons.AppCrashed: true, reasons.AppNotAnswering: true, reasons.ImagePullFailed: true,
}

// Diagnose explains in.App. A healthy, starting or stopped app gets its own
// status code and no rules run.
func (e *Engine) Diagnose(in *Input) Diagnosis {
	d := Diagnosis{Bundle: bundle(in)}
	d.Evidence, d.Proposals = []Evidence{}, []actions.Proposal{}
	if !failing(in.App) {
		d.Code, d.Known = statusCode(in.App), true
		return d
	}
	for _, r := range e.rules {
		f, ok := r.Match(in)
		if !ok {
			continue
		}
		f.Rule = r.ID()
		if f.Evidence == nil {
			f.Evidence = []Evidence{}
		}
		if f.Proposals == nil {
			f.Proposals = []actions.Proposal{}
		}
		d.Finding, d.Known = f, !(r.ID() == ruleFromCondition && genericCodes[f.Code])
		return d
	}
	d.Code = reasons.Unknown
	return d
}

// failingCondition is the app's False condition that names a problem, or nil.
func failingCondition(app *model.App) *metav1.Condition {
	for i := range app.Status.Conditions {
		c := &app.Status.Conditions[i]
		if c.Status != metav1.ConditionFalse {
			continue
		}
		switch reasons.Code(c.Reason) {
		case reasons.Starting, reasons.Stopped, reasons.StoppedIdle, reasons.IdentityPending, reasons.HomeStarting, reasons.NamespacePending:
			continue
		}
		return c
	}
	return nil
}

// failing is true for a failed app and for one held back by a problem
// (no room, home folder busy) while it still counts as starting.
func failing(app *model.App) bool {
	if app.Paused {
		return false
	}
	return app.Status.Phase == "Failed" || failingCondition(app) != nil
}

func statusCode(app *model.App) reasons.Code {
	switch {
	case app.Paused && app.StoppedIdleAfter > 0:
		return reasons.StoppedIdle
	case app.Paused || app.Status.Phase == "Stopped":
		return reasons.Stopped
	case app.Status.Phase == "Running":
		return reasons.Ready
	}
	return reasons.Starting
}

// bundle is every condition message, event and log line, each once, cut to
// MaxExcerpt runes.
func bundle(in *Input) []Evidence {
	out := []Evidence{}
	seen := map[string]bool{}
	add := func(e Evidence) {
		if seen[e.Ref] {
			return
		}
		seen[e.Ref] = true
		e.Excerpt = cut(e.Excerpt)
		out = append(out, e)
	}
	for _, c := range in.App.Status.Conditions {
		if c.Message != "" || c.Status == metav1.ConditionFalse {
			add(conditionEvidence(c))
		}
	}
	for i := range in.Events {
		add(eventEvidence(&in.Events[i]))
	}
	for _, l := range in.Logs {
		add(Evidence{Kind: KindLog, Ref: l.Ref(), Excerpt: l.Text})
	}
	return out
}

func conditionEvidence(c metav1.Condition) Evidence {
	at := c.LastTransitionTime.Time
	msg := c.Message
	if msg == "" {
		msg = c.Reason
	}
	e := Evidence{Kind: KindCondition, Ref: "condition/" + c.Type, Excerpt: cut(msg)}
	if !at.IsZero() {
		e.At = &at
	}
	return e
}

func eventEvidence(ev *corev1.Event) Evidence {
	e := Evidence{Kind: KindEvent, Ref: "event/" + ev.Name, Excerpt: cut(ev.Reason + ": " + ev.Message)}
	if at := status.EventTime(ev); !at.IsZero() {
		e.At = &at
	}
	return e
}

// cut keeps at most MaxExcerpt runes of s.
func cut(s string) string {
	if utf8.RuneCountInString(s) <= MaxExcerpt {
		return s
	}
	r := []rune(s)
	return string(r[:MaxExcerpt])
}

// target is the short name proposals name for in's app.
func target(in *Input) string { return in.App.Name }

func restart(in *Input) []actions.Proposal {
	return []actions.Proposal{{Action: actions.Restart, Target: target(in)}}
}

// lowered is s in lower case, for message matching.
func lowered(s string) string { return strings.ToLower(s) }
