// Package authz is the one place that decides whether a person may do
// something to an app or a template. Handlers and the proxy call Decide and
// never compare owners or groups themselves.
package authz

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

// Principal is who is making a request. Username and DisplayName are for
// display only and never enter a decision.
type Principal struct {
	ID          string // Identity name; empty for anonymous
	Username    string
	Email       string
	DisplayName string
	// Picture is an avatar image URL from the provider; empty for local accounts.
	Picture string
	Issuer  string
	Groups  []string // verbatim from the provider
	// ProviderRoles are the identity provider's roles (config rolesClaim),
	// verbatim. They gate templates and sizes; they are not platform roles.
	ProviderRoles []string
	Anonymous     bool
}

// Role is a platform role, computed per request from groups.
type Role string

const (
	RolePlatformAdmin Role = "platform-admin"
	RoleTemplateAdmin Role = "template-admin"
	RoleMember        Role = "member"
)

// RolesOf returns the roles p holds under cfg. Nothing is stored, so a
// config change applies at once. An empty member binding makes every
// signed-in person a member; empty admin bindings match nobody.
func RolesOf(p Principal, cfg config.Roles) []Role {
	if !signedIn(p) {
		return nil
	}
	var roles []Role
	if inAny(p.Groups, cfg.PlatformAdmin.Groups) {
		roles = append(roles, RolePlatformAdmin)
	}
	if inAny(p.Groups, cfg.TemplateAdmin.Groups) {
		roles = append(roles, RoleTemplateAdmin)
	}
	if len(cfg.Member.Groups) == 0 || inAny(p.Groups, cfg.Member.Groups) {
		roles = append(roles, RoleMember)
	}
	return roles
}

// Elevated is true when roles hold more than membership: an admin role of
// any kind.
func Elevated(roles []Role) bool {
	return slices.ContainsFunc(roles, func(r Role) bool { return r != RoleMember })
}

// signedIn is true only for an identified person. A Principal with no ID
// (including the zero value) is nobody, so a handler that forgets to fill it
// in is denied rather than treated as signed in.
func signedIn(p Principal) bool { return !p.Anonymous && p.ID != "" }

// Action is something a person may try to do.
type Action string

const (
	ActOpenApp         Action = "OpenApp"
	ActManageApp       Action = "ManageApp" // start, stop, delete, edit
	ActShareApp        Action = "ShareApp"
	ActCreateApp       Action = "CreateApp"
	ActUseTemplate     Action = "UseTemplate"
	ActManageTemplates Action = "ManageTemplates"
	ActManageJob       Action = "ManageJob" // see, run, change, delete
	ActCreateJob       Action = "CreateJob"
	ActUseSize         Action = "UseSize"      // run an app at a named size
	ActCustomSize      Action = "CustomSize"   // run an app at a custom CPU and memory
	ActIdleStop        Action = "IdleStop"     // give an app a "Stop when idle" timeout (0 is Off)
	ActAdminConsole    Action = "AdminConsole" // any /api/admin/ operation: every app, usage, backups
	// Secrets. A platform admin gets none of these for another
	// person: secrets are their owner's alone.
	ActManageSecret Action = "ManageSecret" // see, create, change, delete one's own secret
	ActUseSecret    Action = "UseSecret"    // give one's own secret to one's own app or job
	ActInjectSecret Action = "InjectSecret" // the controller hands a secret's value to an app's pod or a job's run
)

// Object is what the action is about. Which fields matter depends on the action.
type Object struct {
	App      *model.App
	Template *model.Template
	Job      *model.Job
	// Size is the named size for ActUseSize.
	Size *sizes.Size
	// Custom is the amount asked for, and the admin's policy, for ActCustomSize.
	Custom *CustomAsk
	// Idle is the timeout asked for, and the admin's policy, for ActIdleStop.
	Idle *IdleAsk
	// Secret is the secret for ActManageSecret, ActUseSecret and ActInjectSecret.
	Secret *model.Secret
}

// IdleAsk is an idle stop timeout in minutes (0 is Off) and the policy that
// governs it.
type IdleAsk struct {
	Minutes int32
	Policy  config.IdleStopCfg
}

// CustomAsk is a custom CPU and memory amount and the policy that governs it.
type CustomAsk struct {
	CPU, Memory resource.Quantity
	Policy      sizes.CustomPolicy
}

// Decision is the answer. When denied, Code is a catalog message. Via says
// what granted access: "owner", "user", "group:<value>", "signed-in",
// "public", "member", "template-admin" or "platform-admin".
type Decision struct {
	Allowed bool
	Code    reasons.MessageCode
	// Args fill in Code's text (reasons.Msg(Code, Args...)).
	Args []any
	Via  string
}

func allow(via string) Decision { return Decision{Allowed: true, Via: via} }
func deny(code reasons.MessageCode) Decision {
	return Decision{Code: code}
}

// Decide answers whether p, holding roles, may perform act on obj. It denies
// by default. Missing and not-allowed apps both answer MsgAppNotFound so
// the answer does not reveal that an app exists.
func Decide(p Principal, roles []Role, act Action, obj Object) Decision {
	admin := slices.Contains(roles, RolePlatformAdmin)
	switch act {
	case ActOpenApp:
		return openApp(p, admin, obj)
	case ActManageApp:
		if d := ownerOrAdmin(p, admin, obj.App); d.Allowed {
			return d
		}
		return deny(reasons.MsgAppNotFound)
	case ActShareApp:
		if d := ownerOrAdmin(p, admin, obj.App); d.Allowed {
			return d
		}
		// Someone who cannot open the app must not learn it exists.
		if !openApp(p, admin, obj).Allowed {
			return deny(reasons.MsgAppNotFound)
		}
		return deny(reasons.MsgShareNotAllowed)
	case ActCreateApp, ActCreateJob:
		return createApp(p, roles, admin)
	case ActManageJob:
		// Jobs are never shared: only the owner and platform admins see one.
		if j := obj.Job; j != nil && signedIn(p) && p.ID == j.Owner {
			return allow("owner")
		}
		if admin && obj.Job != nil {
			return allow("platform-admin")
		}
		return deny(reasons.MsgJobNotFound)
	case ActUseTemplate:
		return useTemplate(p, roles, admin, obj.Template)
	case ActUseSize:
		return useSize(p, roles, admin, obj.Size)
	case ActCustomSize:
		return customSize(p, roles, obj.Custom)
	case ActIdleStop:
		return idleStop(p, admin, obj.Idle)
	case ActAdminConsole:
		if admin && signedIn(p) {
			return allow("platform-admin")
		}
		return deny(reasons.MsgAdminOnly)
	case ActManageSecret:
		if s := obj.Secret; s != nil && signedIn(p) && p.ID == s.Owner {
			return allow("owner")
		}
		// Not found, so a refusal does not reveal that the secret exists.
		return deny(reasons.MsgSecretNotFound)
	case ActUseSecret:
		owner, ok := secretTarget(obj)
		if !ok || obj.Secret == nil || !signedIn(p) || p.ID != owner || p.ID != obj.Secret.Owner {
			return deny(reasons.MsgSecretsOwnerOnly)
		}
		return injectSecret(obj, "owner")
	case ActInjectSecret:
		// The controller acts for nobody: the app or job and the secret must
		// share an owner, whoever asks.
		owner, ok := secretTarget(obj)
		if !ok || obj.Secret == nil || owner == "" || owner != obj.Secret.Owner {
			return deny(reasons.MsgSecretsOwnerOnly)
		}
		return injectSecret(obj, "same-owner")
	case ActManageTemplates:
		if admin {
			return allow("platform-admin")
		}
		if slices.Contains(roles, RoleTemplateAdmin) {
			return allow("template-admin")
		}
		return deny(reasons.MsgTemplateAdminOnly)
	}
	return deny(reasons.MsgInvalidRequest)
}

// secretTarget is the owner of what a secret goes into: exactly one of the
// object's app and job.
func secretTarget(obj Object) (owner string, ok bool) {
	switch {
	case obj.App != nil && obj.Job == nil:
		return obj.App.Owner, true
	case obj.Job != nil && obj.App == nil:
		return obj.Job.Owner, true
	}
	return "", false
}

// injectSecret refuses the apps that never get secrets: previews of pull
// requests (they run code the owner has not reviewed) and fork pull requests.
// Jobs have neither.
func injectSecret(obj Object, via string) Decision {
	if obj.App == nil {
		return allow(via)
	}
	if obj.App.ParentID != "" || (obj.App.Source != nil && obj.App.Source.Fork) {
		return deny(reasons.MsgSecretsNotOnPreviews)
	}
	return allow(via)
}

func ownerOrAdmin(p Principal, admin bool, a *model.App) Decision {
	if a != nil && isOwner(p, a) {
		return allow("owner")
	}
	if admin && a != nil {
		return allow("platform-admin")
	}
	return Decision{}
}

func isOwner(p Principal, a *model.App) bool {
	return signedIn(p) && p.ID == a.Owner
}

func openApp(p Principal, admin bool, obj Object) Decision {
	a := obj.App
	if d := ownerOrAdmin(p, admin, a); d.Allowed {
		return d
	}
	if a == nil {
		return deny(reasons.MsgAppNotFound)
	}
	s := a.Sharing
	// The lower of the app's level and the template's cap wins. A missing
	// template or an unset or unknown level caps at Private.
	level := min(rank(s.Access), rank(templateMax(obj.Template)))
	if level >= model.AccessRank(model.AccessRestricted) && signedIn(p) {
		if slices.Contains(s.Users, p.ID) {
			return allow("user")
		}
		for _, g := range p.Groups {
			if slices.Contains(s.Groups, g) {
				return allow("group:" + g)
			}
		}
	}
	if level >= model.AccessRank(model.AccessSignedIn) && signedIn(p) {
		return allow("signed-in")
	}
	if level >= model.AccessRank(model.AccessPublic) {
		return allow("public")
	}
	return deny(reasons.MsgAppNotFound)
}

func templateMax(t *model.Template) string {
	if t == nil {
		return model.AccessPrivate
	}
	return t.Spec.MaxSharing
}

// rank is AccessRank floored at Private so unknown values grant nothing extra.
func rank(access string) int { return max(model.AccessRank(access), 0) }

func createApp(p Principal, roles []Role, admin bool) Decision {
	switch {
	case !signedIn(p):
		return deny(reasons.MsgSignInRequired)
	case admin:
		return allow("platform-admin")
	case slices.Contains(roles, RoleMember):
		return allow("member")
	}
	return deny(reasons.MsgNotAMember)
}

func useTemplate(p Principal, roles []Role, admin bool, t *model.Template) Decision {
	if !signedIn(p) {
		return deny(reasons.MsgSignInRequired)
	}
	if t == nil {
		return deny(reasons.MsgTemplateUnavailable)
	}
	if admin {
		return allow("platform-admin")
	}
	if slices.Contains(roles, RoleTemplateAdmin) {
		return allow("template-admin")
	}
	if !slices.Contains(roles, RoleMember) {
		return deny(reasons.MsgNotAMember)
	}
	spec := t.Spec
	if len(spec.AllowedUsers) == 0 && len(spec.AllowedGroups) == 0 && len(spec.AllowedProviderRoles) == 0 {
		return allow("member")
	}
	if slices.Contains(spec.AllowedUsers, p.ID) {
		return allow("user")
	}
	if via, ok := gate(p, spec.AllowedGroups, spec.AllowedProviderRoles); ok {
		return allow(via)
	}
	return deny(reasons.MsgTemplateUnavailable)
}

// gate is whether p is in one of groups or holds one of the provider roles,
// and what granted it ("group:<value>" or "provider-role:<value>").
func gate(p Principal, groups, providerRoles []string) (via string, ok bool) {
	for _, g := range p.Groups {
		if slices.Contains(groups, g) {
			return "group:" + g, true
		}
	}
	for _, r := range p.ProviderRoles {
		if slices.Contains(providerRoles, r) {
			return "provider-role:" + r, true
		}
	}
	return "", false
}

func inAny(have, want []string) bool {
	return slices.ContainsFunc(have, func(g string) bool { return slices.Contains(want, g) })
}

func useSize(p Principal, roles []Role, admin bool, s *sizes.Size) Decision {
	switch {
	case !signedIn(p):
		return deny(reasons.MsgSignInRequired)
	case s == nil:
		return deny(reasons.MsgSizeUnavailable)
	case admin:
		return allow("platform-admin")
	case !slices.Contains(roles, RoleMember):
		return deny(reasons.MsgNotAMember)
	case len(s.AllowedGroups) == 0 && len(s.AllowedProviderRoles) == 0:
		return allow("member")
	}
	if via, ok := gate(p, s.AllowedGroups, s.AllowedProviderRoles); ok {
		return allow(via)
	}
	return deny(reasons.MsgSizeUnavailable)
}

// Allowance is how much custom CPU and memory a person may ask for. A zero
// max is no limit of the rules' own (the largest machine still caps it).
type Allowance struct {
	MaxCPU, MaxMemory resource.Quantity
}

// CustomAllowance is what p may ask for under policy: for each amount the
// largest over the rules p matches (by role, group or provider role), no limit when any
// matching rule sets none. ok is false when custom amounts are off or no
// rule matches.
func CustomAllowance(p Principal, roles []Role, policy sizes.CustomPolicy) (a Allowance, ok bool) {
	if !signedIn(p) || !policy.Enabled {
		return Allowance{}, false
	}
	var cpuFree, memFree bool
	for _, r := range policy.Rules {
		if _, gated := gate(p, r.Groups, r.ProviderRoles); !gated && !slices.ContainsFunc(roles, func(role Role) bool { return slices.Contains(r.Roles, string(role)) }) {
			continue
		}
		ok = true
		cpuFree = cpuFree || r.MaxCPU.IsZero()
		memFree = memFree || r.MaxMemory.IsZero()
		if r.MaxCPU.Cmp(a.MaxCPU) > 0 {
			a.MaxCPU = r.MaxCPU.DeepCopy()
		}
		if r.MaxMemory.Cmp(a.MaxMemory) > 0 {
			a.MaxMemory = r.MaxMemory.DeepCopy()
		}
	}
	if cpuFree {
		a.MaxCPU = resource.Quantity{}
	}
	if memFree {
		a.MaxMemory = resource.Quantity{}
	}
	return a, ok
}

// Limit is the allowance in words, "4 CPU and 16 GB", leaving out an
// amount without a limit; empty when neither has one.
func (a Allowance) Limit() string {
	var parts []string
	if !a.MaxCPU.IsZero() {
		parts = append(parts, capacity.CPU(a.MaxCPU)+" CPU")
	}
	if !a.MaxMemory.IsZero() {
		parts = append(parts, capacity.Memory(a.MaxMemory))
	}
	return strings.Join(parts, " and ")
}

func customSize(p Principal, roles []Role, ask *CustomAsk) Decision {
	if !signedIn(p) {
		return deny(reasons.MsgSignInRequired)
	}
	if ask == nil {
		return deny(reasons.MsgCustomSizeNotAllowed)
	}
	a, ok := CustomAllowance(p, roles, ask.Policy)
	if !ok {
		return deny(reasons.MsgCustomSizeNotAllowed)
	}
	if !a.MaxCPU.IsZero() && ask.CPU.Cmp(a.MaxCPU) > 0 || !a.MaxMemory.IsZero() && ask.Memory.Cmp(a.MaxMemory) > 0 {
		d := deny(reasons.MsgCustomSizeOverLimit)
		d.Args = []any{a.Limit()}
		return d
	}
	return allow("custom-size")
}

// idleStop allows a timeout inside the admin's range, and Off when people
// may turn it off. Platform admins may always turn it off.
func idleStop(p Principal, admin bool, ask *IdleAsk) Decision {
	if !signedIn(p) {
		return deny(reasons.MsgSignInRequired)
	}
	if ask == nil {
		return deny(reasons.MsgInvalidRequest)
	}
	pol := ask.Policy
	switch m := ask.Minutes; {
	case m == 0 && admin:
		return allow("platform-admin")
	case m == 0 && pol.MayTurnOff():
		return allow("idle-off")
	case m == 0:
		return deny(reasons.MsgIdleStopOffNotAllowed)
	case m < pol.MinMinutes || m > pol.MaxMinutes:
		d := deny(reasons.MsgIdleStopOutOfRange)
		d.Args = []any{reasons.Minutes(pol.MinMinutes), reasons.Minutes(pol.MaxMinutes)}
		return d
	}
	return allow("idle-range")
}
