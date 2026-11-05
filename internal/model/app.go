package model

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/naming"
)

// App condition types.
const (
	ConditionScheduled  = "Scheduled"
	ConditionHomeReady  = "HomeReady"
	ConditionImageReady = "ImageReady"
	ConditionBuilt      = "Built"
	// ConditionEnvReady is the step that installs the app's environment (and
	// fetches the code it may come from) before the app starts.
	ConditionEnvReady = "EnvReady"
	ConditionStarted  = "Started"
	ConditionReady    = "Ready"
)

// Access levels for a shared app, from least to most open.
const (
	AccessPrivate    = "Private"
	AccessRestricted = "Restricted"
	AccessSignedIn   = "SignedIn"
	AccessPublic     = "Public"
)

// AccessRank orders access levels (Private < Restricted < SignedIn < Public).
// It is the one ordering the API and the proxy use. An unknown value ranks
// below Private so it never grants more than the owner alone.
func AccessRank(access string) int {
	switch access {
	case AccessPrivate:
		return 0
	case AccessRestricted:
		return 1
	case AccessSignedIn:
		return 2
	case AccessPublic:
		return 3
	}
	return -1
}

// MaxAgentPrompt is the longest first prompt a coding-agent app takes.
const MaxAgentPrompt = 4000

// API access levels of an app, from none to most.
const (
	APIAccessNone      = "None"
	APIAccessRead      = "Read"
	APIAccessReadWrite = "ReadWrite"
)

var apiAccessLevels = []string{APIAccessNone, APIAccessRead, APIAccessReadWrite}

// APIAccessRank orders API access levels (None < Read < ReadWrite). An
// unknown value ranks as None so it never grants a token.
func APIAccessRank(access string) int {
	switch access {
	case APIAccessRead:
		return 1
	case APIAccessReadWrite:
		return 2
	}
	return 0
}

// APIAccessOf is the API access app a from template t has: its own APIAccess,
// or when empty the template's default, capped at the template's
// MaxAPIAccess. The gateway and the controller both ask it, so a lowered cap
// takes effect everywhere.
func APIAccessOf(a *App, t *Template) string {
	want := a.APIAccess
	if want == "" {
		want = DefaultAPIAccess(t)
	}
	return lowerAPIAccess(want, MaxAPIAccess(t))
}

// DefaultAPIAccess is the API access an app from t gets when its creator
// picks none: ReadWrite (capped) when t sets APIToken, else None.
func DefaultAPIAccess(t *Template) string {
	if t.Spec.APIToken {
		return lowerAPIAccess(APIAccessReadWrite, MaxAPIAccess(t))
	}
	return APIAccessNone
}

// MaxAPIAccess is the most API access an app from t may have; unset means
// ReadWrite and an unknown value None.
func MaxAPIAccess(t *Template) string {
	switch m := t.Spec.MaxAPIAccess; m {
	case "":
		return APIAccessReadWrite
	case APIAccessNone, APIAccessRead, APIAccessReadWrite:
		return m
	}
	return APIAccessNone
}

// NebiTokenFor reports whether app a, from template t, gets its owner's Nebi
// token (TemplateSpec.NebiToken): as with API tokens, a pull request preview
// never does, since it runs code its owner has not necessarily written. The
// controller and the gateway both ask it.
func NebiTokenFor(a *App, t *Template) bool {
	return t != nil && t.Spec.NebiToken && a.ParentID == ""
}

// lowerAPIAccess is the lesser of a and b; an unknown value is None.
func lowerAPIAccess(a, b string) string {
	return apiAccessLevels[min(APIAccessRank(a), APIAccessRank(b))]
}

// Sharing says who besides the owner may open the app. The zero value means
// owner only.
// +kubebuilder:object:generate=true
type Sharing struct {
	Access string `json:"access,omitempty"`
	// Users are person names.
	Users []string `json:"users,omitempty"`
	// Groups are group values kept verbatim from the provider.
	Groups []string `json:"groups,omitempty"`
}

// AppResources is a custom CPU and memory for the app container, set as
// both requests and limits, in place of a named size.
type AppResources struct {
	CPU    resource.Quantity
	Memory resource.Quantity
}

// Same reports whether r and o are the same amount; nil (a named size) is
// only the same as nil.
func (r *AppResources) Same(o *AppResources) bool {
	if r == nil || o == nil {
		return r == o
	}
	return r.CPU.Cmp(o.CPU) == 0 && r.Memory.Cmp(o.Memory) == 0
}

// size checks what an app or a job runs at: a size name, or a custom amount
// of at least the minimum, never both.
func (v *validator) size(name string, r *AppResources) {
	v.maxLen("spec.size", name, 63)
	if r == nil {
		return
	}
	if name != "" {
		v.add("spec", "set size or resources, not both")
	}
	if r.CPU.Cmp(MinCustomCPU) < 0 {
		v.add("spec.resources.cpu", "cpu must be at least 100m")
	}
	if r.Memory.Cmp(MinCustomMemory) < 0 {
		v.add("spec.resources.memory", "memory must be at least 256Mi")
	}
}

// The smallest custom amount an app or a job may ask for.
var (
	MinCustomCPU    = resource.MustParse("100m")
	MinCustomMemory = resource.MustParse("256Mi")
)

// MaxCommandLength is the longest command line an app may carry.
const MaxCommandLength = 2048

// AppStatus is the observed state of an app. The controller writes it; the
// gateway never does.
type AppStatus struct {
	// Phase is Pending, Starting, Running, Stopped or Failed.
	Phase string `json:"phase,omitempty"`
	URL   string `json:"url,omitempty"`
	// Commit is the commit of the repository the running pod cloned, for an
	// app with a Source.
	Commit string `json:"commit,omitempty"`
	// Conditions use the types Scheduled, HomeReady, ImageReady, Started and Ready.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// ObservedVersion is the Version of the spec the controller last acted on.
	// It has its own column, so it is not part of the stored JSON.
	ObservedVersion int64 `json:"-"`
	// Size is the resolved size name the app runs with. It is only read when a
	// write moves the phase to Running, to stamp the usage interval; it is not
	// stored on the row.
	Size string `json:"-"`
}

// App is one person's running JupyterLab or web app: one row of the apps
// table. The zero time means "not set".
type App struct {
	ID string
	// Namespace is where the app's Pod and Service live.
	Namespace string
	// Name is the short name: the one in the app's URL and in the API.
	Name string
	// Owner is the person's name. It goes into label values and claim names,
	// so it must be a DNS-1123 label, and it never changes.
	Owner       string
	DisplayName string
	// Template is the name of the template to run.
	Template string
	// File is the file to open, for templates that take a file.
	File string
	// Sample copies the template's sample into ~/samples/<template>/ before
	// the app starts; File then points into that folder.
	Sample bool
	// Size names one of the admin-defined sizes; empty means the default.
	// Changing it restarts a running app.
	Size string
	// Resources is a custom CPU and memory instead of a named size, for
	// people the admin allows; nil runs at Size. Changing it restarts a
	// running app.
	Resources *AppResources
	// Command is the command line typed for a template that takes one
	// (userCommand). Changing it restarts a running app.
	Command string
	// Image and Port are the image an app runs and the port it serves on,
	// for templates with userImage. Changing either restarts a running app.
	Image string
	Port  int32
	// APIAccess gives the app an API token of its owner (JAVO_URL and
	// JAVO_TOKEN), so it can call the API as them: None, Read (GET only) or
	// ReadWrite. Empty means the template's default (see APIAccessOf).
	// Changing it restarts a running app.
	APIAccess string
	// AgentPrompt is the request a coding-agent app (a template with
	// agentModel) starts working on: its pod gets it as JAVO_AGENT_PROMPT,
	// and the agent runs it on the app's first start only. Empty for none.
	AgentPrompt string
	// Env are the variables with a plain value the app's container gets
	// values from secrets are bindings beside the app.
	// Changing them restarts a running app.
	Env []EnvVar
	// IdleStopMinutes stops the app after that many minutes without traffic
	// through the gateway; 0 is Off. Changing it does not restart the app.
	IdleStopMinutes int32
	// StoppedIdleAfter is the timeout the idle check stopped the app at; 0
	// when it runs or a person stopped it. Starting the app clears it.
	StoppedIdleAfter int32
	// RestoreHold names the restore that brought the app back; while Held,
	// the controller starts nothing for it (not even its home claim, which
	// the volume restore may still bring back). Empty: never restored.
	RestoreHold string
	// ReleaseAt is when an admin let the held app go; zero: not yet.
	ReleaseAt time.Time
	// StoppedByAdmin is set when a platform admin, not the owner, stopped
	// the app, so the owner is told why. Starting the app clears it.
	StoppedByAdmin bool
	// LastActiveAt is when a gateway last saw traffic to the app, flushed at
	// most every minute; zero before any. Only Apps.NoteActivity writes it.
	LastActiveAt time.Time
	// Source is the Git repository the app runs from; nil for an app that
	// runs from the home folder. File is then relative to the repository.
	Source *Source
	// Environment is the pixi environment the app runs in; nil runs the
	// template image's own tools. It is set when the app is created.
	Environment *Environment
	Paused      bool
	// Sharing is who besides the owner may open the app.
	Sharing Sharing
	// RestartRequestedAt is set by the gateway to ask the controller to
	// restart the app.
	RestartRequestedAt time.Time
	// TokenRevoked is the id of the app's API token its owner revoked last. The
	// controller makes the app a new token when the one it holds has this id.
	TokenRevoked string
	// ParentID and PullNumber are set on a preview: the app it previews and
	// the pull request it follows. Empty and 0 on every ordinary app. They are
	// columns and never change.
	ParentID   string
	PullNumber int32
	// Version counts spec changes; the gateway saves with the version it read.
	Version int64
	// CreatedAt orders lists newest first.
	CreatedAt time.Time
	Status    AppStatus
	// DeletedAt is set when the owner deletes the app; the controller removes
	// the app's objects and the row is then purged.
	DeletedAt time.Time
}

// ObjectName is the name of the app's Pod and Service: unique per owner and
// short name (see naming.ObjectName), also in a namespace shared by everyone.
func (a *App) ObjectName() string { return naming.ObjectName(a.Owner, a.Name) }

// ApplyDefaults fills what an unset field means: sharing names owner only.
func (a *App) ApplyDefaults() {
	if a.Sharing.Access == "" {
		a.Sharing.Access = AccessPrivate
	}
	// Only a stopped app can have been stopped for being idle.
	if !a.Paused {
		a.StoppedIdleAfter, a.StoppedByAdmin = 0, false
	}
	if a.Source != nil && a.Source.Previews != nil && a.Source.Previews.Max == 0 {
		a.Source.Previews.Max = DefaultPreviewMax
	}
}

// Validate checks the rules every saved app satisfies. The errors are
// addressed like the old custom resource fields (spec.owner).
func (a *App) Validate() error {
	var v validator
	v.name(a.Name)
	v.owner("spec.owner", a.Owner)
	if a.Template == "" {
		v.add("spec.template", "template is required")
	}
	v.size(a.Size, a.Resources)
	v.maxLen("spec.command", a.Command, MaxCommandLength)
	v.maxLen("spec.image", a.Image, 512)
	if a.Port < 0 || a.Port > 65535 {
		v.add("spec.port", "port must be between 0 and 65535")
	}
	v.oneOf("spec.apiAccess", a.APIAccess, true, apiAccessLevels...)
	if err := a.ValidateAgentPrompt(); err != nil {
		v.add("spec.agentPrompt", err.Error())
	}
	if a.IdleStopMinutes < 0 {
		v.add("spec.idleStopMinutes", "idleStopMinutes must not be negative")
	}
	a.Sharing.validate(&v, "spec.sharing")
	if a.Source != nil {
		a.Source.validate(&v, "spec.source")
	}
	a.Environment.validate(&v, "spec.environment")
	return v.err()
}

// PrintableText is s with only what a person can see: letters, marks,
// numbers, punctuation, symbols, line breaks and tabs. Format characters
// (zero-width, bidirectional and tag characters), private-use, unassigned
// and control characters are taken out; other spaces become plain ones.
// Text that goes to a model (an agent's first prompt, a suggestion) passes
// through it, so it cannot carry instructions the person never saw.
func PrintableText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t', unicode.IsPrint(r):
			return r
		case r != '\r' && unicode.IsSpace(r):
			return ' '
		}
		return -1
	}, s)
}

// ValidateAgentPrompt checks AgentPrompt: text of at most MaxAgentPrompt
// characters, with line breaks and tabs but no other control characters.
func (a *App) ValidateAgentPrompt() error {
	if charLen(a.AgentPrompt) > MaxAgentPrompt || strings.ContainsFunc(a.AgentPrompt, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) {
		return fmt.Errorf("agentPrompt must be text of at most %d characters", MaxAgentPrompt)
	}
	return nil
}

// ValidateUpdate is Validate plus the rules for changing old into a: the
// owner, and for a preview its parent and pull request, never change.
func (a *App) ValidateUpdate(old *App) error {
	var v validator
	v.immutable("spec.owner", "owner is immutable", a.Owner, old.Owner)
	v.immutable("spec.parentId", "parentId is immutable", a.ParentID, old.ParentID)
	if a.PullNumber != old.PullNumber {
		v.add("spec.pullNumber", "pullNumber is immutable")
	}
	return v.merge(a.Validate())
}

func (s Sharing) validate(v *validator, field string) {
	v.oneOf(field+".access", s.Access, true, AccessPrivate, AccessRestricted, AccessSignedIn, AccessPublic)
	if (s.Access == AccessPrivate || s.Access == "") && (len(s.Users) > 0 || len(s.Groups) > 0) {
		v.add(field, "private apps cannot list people")
	}
	v.maxItems(field+".users", len(s.Users), 200)
	v.maxItems(field+".groups", len(s.Groups), 50)
}

// Held reports whether the app is waiting, after a restore, for an admin to
// release it: it has a restore hold and its release time has not come.
func (a *App) Held(now time.Time) bool {
	return a.RestoreHold != "" && (a.ReleaseAt.IsZero() || now.Before(a.ReleaseAt))
}

// ReleaseTimes spaces n starts so that at most perMinute begin in any
// minute: the first at now, then one every minute/perMinute. A rate below
// 1 is 1, so a release is never all at once by mistake.
func ReleaseTimes(n, perMinute int, now time.Time) []time.Time {
	perMinute = max(perMinute, 1)
	step := time.Minute / time.Duration(perMinute)
	out := make([]time.Time, n)
	for i := range out {
		out[i] = now.Add(time.Duration(i) * step)
	}
	return out
}
