package diagnose

import (
	"regexp"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

// DefaultRules is the built-in rule list, most specific first. internal/cli/wiring passes
// it to NewEngine; removing a rule is removing its line here and its type.
func DefaultRules() []Rule {
	return []Rule{
		imagePull{},
		logRule{id: "missing-module", code: reasons.MissingModule, re: regexp.MustCompile(`(ModuleNotFoundError|ImportError): No module named`), propose: restart},
		logRule{id: "port-in-use", code: reasons.PortInUse, re: regexp.MustCompile(`(?i)address already in use`), propose: restart},
		logRule{id: "home-permission", code: reasons.HomePermissionDenied, re: regexp.MustCompile(`PermissionError: \[Errno 13\] Permission denied: '/home/`)},
		wrongBasePath{},
		outOfMemory{},
		fromCondition{},
	}
}

const ruleFromCondition = "from-condition"

// imagePull splits a failed image download by the registry's answer. Pull
// failures carry their cause only in the message (as the probe rules do).
type imagePull struct{}

func (imagePull) ID() string { return "image-pull" }

func (imagePull) Match(in *Input) (Finding, bool) {
	c := failingCondition(in.App)
	if c == nil || reasons.Code(c.Reason) != reasons.ImagePullFailed {
		return Finding{}, false
	}
	msg := lowered(c.Message)
	var code reasons.Code
	switch {
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "authentication required") || strings.Contains(msg, "denied"):
		code = reasons.ImageAuthFailed
	case strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "not found"):
		code = reasons.ImageNotFound
	default:
		return Finding{}, false
	}
	return Finding{Code: code, Evidence: []Evidence{conditionEvidence(*c)}}, true
}

// logRule matches a line of the app's log, newest first, for a crashed app.
type logRule struct {
	id      string
	code    reasons.Code
	re      *regexp.Regexp
	propose func(*Input) []actions.Proposal
}

func (r logRule) ID() string { return r.id }

func (r logRule) Match(in *Input) (Finding, bool) {
	c := failingCondition(in.App)
	if c == nil || reasons.Code(c.Reason) != reasons.AppCrashed {
		return Finding{}, false
	}
	for i := len(in.Logs) - 1; i >= 0; i-- {
		l := in.Logs[i]
		if !r.re.MatchString(l.Text) {
			continue
		}
		f := Finding{Code: r.code, Evidence: []Evidence{{Kind: KindLog, Ref: l.Ref(), Excerpt: cut(l.Text)}, conditionEvidence(*c)}}
		if r.propose != nil {
			f.Proposals = r.propose(in)
		}
		return f, true
	}
	return Finding{}, false
}

// wrongBasePath is an app that answers 404 to its readiness probe: it serves,
// but not under the path the platform gives it.
type wrongBasePath struct{}

func (wrongBasePath) ID() string { return "wrong-base-path" }

func (wrongBasePath) Match(in *Input) (Finding, bool) {
	c := failingCondition(in.App)
	if c == nil || reasons.Code(c.Reason) != reasons.AppNotAnswering {
		return Finding{}, false
	}
	for i := len(in.Events) - 1; i >= 0; i-- {
		e := &in.Events[i]
		if e.Reason == "Unhealthy" && strings.Contains(e.Message, "statuscode: 404") {
			return Finding{Code: reasons.WrongBasePath, Evidence: []Evidence{eventEvidence(e), conditionEvidence(*c)}}, true
		}
	}
	return Finding{}, false
}

// outOfMemory proposes the smallest size the owner may use with more memory
// than the app has now and the same GPUs (more memory is no reason to take
// or drop a GPU); none when there is no larger one.
type outOfMemory struct{}

func (outOfMemory) ID() string { return "out-of-memory" }

func (outOfMemory) Match(in *Input) (Finding, bool) {
	c := failingCondition(in.App)
	if c == nil || reasons.Code(c.Reason) != reasons.OutOfMemory {
		return Finding{}, false
	}
	f := Finding{Code: reasons.OutOfMemory, Evidence: []Evidence{conditionEvidence(*c)}}
	if next, ok := largerSize(in.Current, in.Sizes); ok {
		f.Proposals = []actions.Proposal{{Action: actions.Resize, Target: target(in), Params: map[string]string{"size": next.Name}}}
	}
	return f, true
}

func largerSize(cur sizes.Size, all []sizes.Size) (sizes.Size, bool) {
	if cur.Memory.IsZero() {
		return sizes.Size{}, false
	}
	bigger := slices.DeleteFunc(slices.Clone(all), func(s sizes.Size) bool {
		return s.Memory.Cmp(cur.Memory) <= 0 || s.GPU != cur.GPU || s.GPUResource != cur.GPUResource
	})
	if len(bigger) == 0 {
		return sizes.Size{}, false
	}
	return slices.MinFunc(bigger, func(a, b sizes.Size) int { return a.Memory.Cmp(b.Memory) }), true
}

// fromCondition reports the failing condition's own code with its evidence.
// For a crash or an app that does not answer, restarting is offered.
type fromCondition struct{}

func (fromCondition) ID() string { return ruleFromCondition }

func (fromCondition) Match(in *Input) (Finding, bool) {
	c := failingCondition(in.App)
	if c == nil {
		return Finding{}, false
	}
	code := reasons.Code(c.Reason)
	if _, ok := reasons.Lookup(code); !ok {
		return Finding{}, false
	}
	f := Finding{Code: code, Evidence: []Evidence{conditionEvidence(*c)}}
	if code == reasons.AppCrashed || code == reasons.AppNotAnswering {
		f.Proposals = restart(in)
	}
	return f, true
}
