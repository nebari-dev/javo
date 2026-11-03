package authz_test

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

func decide(p authz.Principal, act authz.Action, obj authz.Object) authz.Decision {
	return authz.Decide(p, authz.RolesOf(p, roleCfg), act, obj)
}

// A size limited to groups is for their members and platform admins.
func TestUseSize(t *testing.T) {
	open := &sizes.Size{Name: "small"}
	gated := &sizes.Size{Name: "gpu", AllowedGroups: []string{"/org/ds"}}
	for _, tc := range []struct {
		name string
		p    authz.Principal
		size *sizes.Size
		want bool
		via  string
	}{
		{"member uses open size", alice, open, true, "member"},
		{"member of the group uses gated size", bob, gated, true, "group:/org/ds"},
		{"member outside the group denied", alice, gated, false, ""},
		{"admin uses gated size", admin, gated, true, "platform-admin"},
		{"non-member denied", carol, open, false, ""},
		{"anonymous denied", anon, open, false, ""},
		{"no size denied", alice, nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.p, authz.ActUseSize, authz.Object{Size: tc.size})
			if d.Allowed != tc.want || d.Via != tc.via {
				t.Fatalf("got %+v", d)
			}
			if !d.Allowed && d.Code == "" {
				t.Fatal("denied without a catalog message")
			}
		})
	}
}

var customPolicy = sizes.CustomPolicy{Enabled: true, Rules: []sizes.CustomRule{
	{Roles: []string{"platform-admin"}},
	{Groups: []string{"/org/ds"}, MaxCPU: resource.MustParse("4"), MaxMemory: resource.MustParse("16Gi")},
	{Groups: []string{"/org/ds", "/org/gpu"}, MaxCPU: resource.MustParse("2"), MaxMemory: resource.MustParse("32Gi")},
}}

func custom(cpu, mem string) *authz.CustomAsk {
	return &authz.CustomAsk{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem), Policy: customPolicy}
}

// Custom amounts follow the admin's rules: platform admins without a limit
// of their own, a group up to its limit (the larger of each amount over the
// rules it matches), everyone else not at all.
func TestCustomSize(t *testing.T) {
	limit := "You can ask for up to 4 CPU and 32 GB."
	for _, tc := range []struct {
		name string
		p    authz.Principal
		ask  *authz.CustomAsk
		want bool
		code reasons.MessageCode
		text string
	}{
		{"admin asks for a lot", admin, custom("64", "512Gi"), true, "", ""},
		{"group member within the limit", bob, custom("4", "32Gi"), true, "", ""},
		{"group member over the CPU limit", bob, custom("4500m", "8Gi"), false, reasons.MsgCustomSizeOverLimit, limit},
		{"group member over the memory limit", bob, custom("1", "33Gi"), false, reasons.MsgCustomSizeOverLimit, limit},
		{"member without a rule", alice, custom("1", "1Gi"), false, reasons.MsgCustomSizeNotAllowed, ""},
		{"anonymous", anon, custom("1", "1Gi"), false, reasons.MsgSignInRequired, ""},
		{"turned off", admin, &authz.CustomAsk{CPU: resource.MustParse("1"), Memory: resource.MustParse("1Gi"),
			Policy: sizes.CustomPolicy{Rules: customPolicy.Rules}}, false, reasons.MsgCustomSizeNotAllowed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.p, authz.ActCustomSize, authz.Object{Custom: tc.ask})
			if d.Allowed != tc.want || d.Code != tc.code {
				t.Fatalf("got %+v", d)
			}
			if tc.text != "" && reasons.Msg(d.Code, d.Args...) != tc.text {
				t.Fatalf("text %q, want %q", reasons.Msg(d.Code, d.Args...), tc.text)
			}
		})
	}
}

// The allowance the size picker shows is the same one Decide applies.
func TestCustomAllowance(t *testing.T) {
	if a, ok := authz.CustomAllowance(bob, authz.RolesOf(bob, roleCfg), customPolicy); !ok ||
		a.MaxCPU.Cmp(resource.MustParse("4")) != 0 || a.MaxMemory.Cmp(resource.MustParse("32Gi")) != 0 {
		t.Fatalf("bob = %+v, %v", a, ok)
	}
	if a, ok := authz.CustomAllowance(admin, authz.RolesOf(admin, roleCfg), customPolicy); !ok || !a.MaxCPU.IsZero() || !a.MaxMemory.IsZero() {
		t.Fatalf("admin = %+v, %v (no limit of its own)", a, ok)
	}
	if _, ok := authz.CustomAllowance(alice, authz.RolesOf(alice, roleCfg), customPolicy); ok {
		t.Fatal("alice has no rule")
	}
}

// dana holds the provider role gpu-users (a Keycloak client role, say) and
// is a member in no group that gates anything.
var dana = authz.Principal{ID: "dana-1", Username: "dana", Groups: []string{"/org/members"}, ProviderRoles: []string{"gpu-users"}}

// A template, a size or a custom-amount rule can be gated by the identity
// provider's roles as well as by groups: holding any listed role is enough,
// and a role the person does not hold grants nothing.
func TestProviderRolesGateTemplatesAndSizes(t *testing.T) {
	byRole := &model.Template{Name: "gpu-lab", Spec: model.TemplateSpec{AllowedProviderRoles: []string{"gpu-users"}}}
	byGroupOrRole := &model.Template{Name: "lab", Spec: model.TemplateSpec{AllowedGroups: []string{"/org/ds"}, AllowedProviderRoles: []string{"gpu-users"}}}
	sizeByRole := &sizes.Size{Name: "gpu", AllowedProviderRoles: []string{"gpu-users"}}
	for _, tc := range []struct {
		name string
		p    authz.Principal
		act  authz.Action
		obj  authz.Object
		want bool
		via  string
	}{
		{"role holder uses role-gated template", dana, authz.ActUseTemplate, authz.Object{Template: byRole}, true, "provider-role:gpu-users"},
		{"member without the role is refused the template", alice, authz.ActUseTemplate, authz.Object{Template: byRole}, false, ""},
		{"group member uses a template gated by group or role", bob, authz.ActUseTemplate, authz.Object{Template: byGroupOrRole}, true, "group:/org/ds"},
		{"role holder uses a template gated by group or role", dana, authz.ActUseTemplate, authz.Object{Template: byGroupOrRole}, true, "provider-role:gpu-users"},
		{"role holder uses role-gated size", dana, authz.ActUseSize, authz.Object{Size: sizeByRole}, true, "provider-role:gpu-users"},
		{"member without the role is refused the size", alice, authz.ActUseSize, authz.Object{Size: sizeByRole}, false, ""},
		{"a group named like the role does not count", authz.Principal{ID: "erin-1", Groups: []string{"/org/members", "gpu-users"}}, authz.ActUseSize, authz.Object{Size: sizeByRole}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.p, tc.act, tc.obj)
			if d.Allowed != tc.want || d.Via != tc.via {
				t.Fatalf("got %+v", d)
			}
		})
	}

	policy := sizes.CustomPolicy{Enabled: true, Rules: []sizes.CustomRule{
		{ProviderRoles: []string{"gpu-users"}, MaxCPU: resource.MustParse("8"), MaxMemory: resource.MustParse("64Gi")},
	}}
	if a, ok := authz.CustomAllowance(dana, authz.RolesOf(dana, roleCfg), policy); !ok || a.MaxCPU.Cmp(resource.MustParse("8")) != 0 {
		t.Fatalf("dana = %+v, %v", a, ok)
	}
	if _, ok := authz.CustomAllowance(alice, authz.RolesOf(alice, roleCfg), policy); ok {
		t.Fatal("alice holds no provider role")
	}
}
