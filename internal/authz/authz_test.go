package authz_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

var roleCfg = config.Roles{
	PlatformAdmin: config.RoleBinding{Groups: []string{"/org/admins"}},
	TemplateAdmin: config.RoleBinding{Groups: []string{"/org/tpl"}},
	Member:        config.RoleBinding{Groups: []string{"/org/members"}},
}

var (
	alice = authz.Principal{ID: "alice-1a2b3c4d", Username: "alice", Groups: []string{"/org/members"}}
	bob   = authz.Principal{ID: "bob-5e6f7a8b", Username: "bob", Groups: []string{"/org/members", "/org/ds"}}
	carol = authz.Principal{ID: "carol-9c0d1e2f", Username: "carol"} // signed in, no groups
	admin = authz.Principal{ID: "root-0a0a0a0a", Groups: []string{"/org/admins"}}
	tplAd = authz.Principal{ID: "tina-3b3b3b3b", Groups: []string{"/org/tpl"}}
	anon  = authz.Principal{Anonymous: true}
)

func ws(access string, users, groups []string) *model.App {
	a := &model.App{Owner: alice.ID, Template: "t"}
	if access != "" {
		a.Sharing = model.Sharing{Access: access, Users: users, Groups: groups}
	}
	return a
}

func tpl(max string, users, groups []string) *model.Template {
	return &model.Template{Spec: model.TemplateSpec{MaxSharing: max, AllowedUsers: users, AllowedGroups: groups}}
}

func TestRolesOf(t *testing.T) {
	tests := []struct {
		name string
		p    authz.Principal
		cfg  config.Roles
		want []authz.Role
	}{
		{"member by group", alice, roleCfg, []authz.Role{authz.RoleMember}},
		{"no matching group", carol, roleCfg, nil},
		{"admin and member", authz.Principal{ID: "x", Groups: []string{"/org/admins", "/org/members"}}, roleCfg, []authz.Role{authz.RolePlatformAdmin, authz.RoleMember}},
		{"group match is case sensitive", authz.Principal{ID: "x", Groups: []string{"/org/Members"}}, roleCfg, nil},
		{"empty member groups: every signed-in person", carol, config.Roles{}, []authz.Role{authz.RoleMember}},
		{"empty member groups: not anonymous", anon, config.Roles{}, nil},
		{"empty member groups: zero principal is nobody", authz.Principal{}, config.Roles{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := authz.RolesOf(tc.p, tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("roles = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("roles = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestDecide(t *testing.T) {
	open := tpl("Public", nil, nil)
	tests := []struct {
		name string
		p    authz.Principal
		act  authz.Action
		obj  authz.Object
		want bool
		via  string
		code reasons.MessageCode
	}{
		// open app
		{"owner opens private", alice, authz.ActOpenApp, authz.Object{App: ws("", nil, nil), Template: open}, true, "owner", ""},
		{"stranger denied private", bob, authz.ActOpenApp, authz.Object{App: ws("Private", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"omitted sharing is owner only", bob, authz.ActOpenApp, authz.Object{App: ws("", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"admin opens private", admin, authz.ActOpenApp, authz.Object{App: ws("Private", nil, nil), Template: open}, true, "platform-admin", ""},
		{"restricted user opens", bob, authz.ActOpenApp, authz.Object{App: ws("Restricted", []string{bob.ID}, nil), Template: open}, true, "user", ""},
		{"restricted other user denied", carol, authz.ActOpenApp, authz.Object{App: ws("Restricted", []string{bob.ID}, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"restricted group opens", bob, authz.ActOpenApp, authz.Object{App: ws("Restricted", nil, []string{"/org/ds"}), Template: open}, true, "group:/org/ds", ""},
		{"group match is exact", bob, authz.ActOpenApp, authz.Object{App: ws("Restricted", nil, []string{"/org/DS"}), Template: open}, false, "", reasons.MsgAppNotFound},
		{"anonymous denied restricted", anon, authz.ActOpenApp, authz.Object{App: ws("Restricted", []string{""}, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"signed-in opens", carol, authz.ActOpenApp, authz.Object{App: ws("SignedIn", nil, nil), Template: open}, true, "signed-in", ""},
		{"anonymous denied signed-in", anon, authz.ActOpenApp, authz.Object{App: ws("SignedIn", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"anonymous opens public", anon, authz.ActOpenApp, authz.Object{App: ws("Public", nil, nil), Template: open}, true, "public", ""},
		{"public capped to signed-in: anonymous denied", anon, authz.ActOpenApp, authz.Object{App: ws("Public", nil, nil), Template: tpl("SignedIn", nil, nil)}, false, "", reasons.MsgAppNotFound},
		{"public capped to signed-in: bob allowed", bob, authz.ActOpenApp, authz.Object{App: ws("Public", nil, nil), Template: tpl("SignedIn", nil, nil)}, true, "signed-in", ""},
		{"cap Private ignores listed users", bob, authz.ActOpenApp, authz.Object{App: ws("Restricted", []string{bob.ID}, nil), Template: tpl("Private", nil, nil)}, false, "", reasons.MsgAppNotFound},
		{"template without maxSharing caps at Private", carol, authz.ActOpenApp, authz.Object{App: ws("SignedIn", nil, nil), Template: tpl("", nil, nil)}, false, "", reasons.MsgAppNotFound},
		{"missing template caps at Private", carol, authz.ActOpenApp, authz.Object{App: ws("Public", nil, nil)}, false, "", reasons.MsgAppNotFound},
		{"unknown access level grants nothing", bob, authz.ActOpenApp, authz.Object{App: ws("Everyone", []string{bob.ID}, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"owner opens even when template missing", alice, authz.ActOpenApp, authz.Object{App: ws("", nil, nil)}, true, "owner", ""},
		{"no workspace", bob, authz.ActOpenApp, authz.Object{}, false, "", reasons.MsgAppNotFound},
		{"anonymous with empty id never owns", anon, authz.ActOpenApp, authz.Object{App: &model.App{}, Template: open}, false, "", reasons.MsgAppNotFound},

		// manage and share
		{"owner manages", alice, authz.ActManageApp, authz.Object{App: ws("Public", nil, nil), Template: open}, true, "owner", ""},
		{"viewer cannot manage", bob, authz.ActManageApp, authz.Object{App: ws("Public", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"admin manages", admin, authz.ActManageApp, authz.Object{App: ws("", nil, nil)}, true, "platform-admin", ""},
		{"template admin cannot manage others' apps", tplAd, authz.ActManageApp, authz.Object{App: ws("", nil, nil)}, false, "", reasons.MsgAppNotFound},
		{"owner shares", alice, authz.ActShareApp, authz.Object{App: ws("", nil, nil)}, true, "owner", ""},
		{"admin shares", admin, authz.ActShareApp, authz.Object{App: ws("", nil, nil)}, true, "platform-admin", ""},
		{"viewer cannot share", bob, authz.ActShareApp, authz.Object{App: ws("Public", nil, nil), Template: open}, false, "", reasons.MsgShareNotAllowed},
		{"stranger shares private app: not found", bob, authz.ActShareApp, authz.Object{App: ws("Private", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"anonymous shares private app: not found", anon, authz.ActShareApp, authz.Object{App: ws("", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},
		{"share with no workspace: not found", alice, authz.ActShareApp, authz.Object{}, false, "", reasons.MsgAppNotFound},
		{"zero principal opens signed-in app", authz.Principal{}, authz.ActOpenApp, authz.Object{App: ws("SignedIn", nil, nil), Template: open}, false, "", reasons.MsgAppNotFound},

		// create
		{"member creates", alice, authz.ActCreateApp, authz.Object{}, true, "member", ""},
		{"admin creates", admin, authz.ActCreateApp, authz.Object{}, true, "platform-admin", ""},
		{"non-member denied create", carol, authz.ActCreateApp, authz.Object{}, false, "", reasons.MsgNotAMember},
		{"anonymous never creates", anon, authz.ActCreateApp, authz.Object{}, false, "", reasons.MsgSignInRequired},

		// use template
		{"member uses ungated template", alice, authz.ActUseTemplate, authz.Object{Template: tpl("", nil, nil)}, true, "member", ""},
		{"member uses template listing user", alice, authz.ActUseTemplate, authz.Object{Template: tpl("", []string{alice.ID}, nil)}, true, "user", ""},
		{"member in gated group", bob, authz.ActUseTemplate, authz.Object{Template: tpl("", nil, []string{"/org/ds"})}, true, "group:/org/ds", ""},
		{"member outside gate denied", alice, authz.ActUseTemplate, authz.Object{Template: tpl("", []string{bob.ID}, []string{"/org/ds"})}, false, "", reasons.MsgTemplateUnavailable},
		{"non-member denied template", carol, authz.ActUseTemplate, authz.Object{Template: tpl("", nil, nil)}, false, "", reasons.MsgNotAMember},
		{"template admin sees gated", tplAd, authz.ActUseTemplate, authz.Object{Template: tpl("", []string{bob.ID}, nil)}, true, "template-admin", ""},
		{"admin sees gated", admin, authz.ActUseTemplate, authz.Object{Template: tpl("", []string{bob.ID}, nil)}, true, "platform-admin", ""},
		{"anonymous denied template", anon, authz.ActUseTemplate, authz.Object{Template: tpl("", nil, nil)}, false, "", reasons.MsgSignInRequired},
		{"missing template denied", alice, authz.ActUseTemplate, authz.Object{}, false, "", reasons.MsgTemplateUnavailable},

		// jobs: only the owner and platform admins see or change one
		{"owner manages job", alice, authz.ActManageJob, authz.Object{Job: job(alice.ID)}, true, "owner", ""},
		{"admin manages job", admin, authz.ActManageJob, authz.Object{Job: job(alice.ID)}, true, "platform-admin", ""},
		{"other member cannot see job", bob, authz.ActManageJob, authz.Object{Job: job(alice.ID)}, false, "", reasons.MsgJobNotFound},
		{"template admin cannot see others' jobs", tplAd, authz.ActManageJob, authz.Object{Job: job(alice.ID)}, false, "", reasons.MsgJobNotFound},
		{"anonymous never owns a job", anon, authz.ActManageJob, authz.Object{Job: &model.Job{}}, false, "", reasons.MsgJobNotFound},
		{"no job", alice, authz.ActManageJob, authz.Object{}, false, "", reasons.MsgJobNotFound},
		{"member creates job", alice, authz.ActCreateJob, authz.Object{}, true, "member", ""},
		{"non-member denied job create", carol, authz.ActCreateJob, authz.Object{}, false, "", reasons.MsgNotAMember},

		// manage templates
		{"template admin manages templates", tplAd, authz.ActManageTemplates, authz.Object{}, true, "template-admin", ""},
		{"admin manages templates", admin, authz.ActManageTemplates, authz.Object{}, true, "platform-admin", ""},
		{"member cannot manage templates", alice, authz.ActManageTemplates, authz.Object{}, false, "", reasons.MsgTemplateAdminOnly},

		// unknown action
		{"unknown action denied", admin, authz.Action("Fly"), authz.Object{}, false, "", reasons.MsgInvalidRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := authz.Decide(tc.p, authz.RolesOf(tc.p, roleCfg), tc.act, tc.obj)
			if d.Allowed != tc.want || d.Via != tc.via || d.Code != tc.code {
				t.Fatalf("got %+v, want allowed=%v via=%q code=%q", d, tc.want, tc.via, tc.code)
			}
			if !d.Allowed {
				if _, ok := reasons.AllMessages()[d.Code]; !ok {
					t.Fatalf("denial code %q is not in the message catalog", d.Code)
				}
			}
		})
	}
}

// A Principal with no ID is nobody, even when Anonymous is false and the
// member binding is empty (which makes every signed-in person a member).
func TestZeroPrincipalFailsClosed(t *testing.T) {
	var zero authz.Principal
	roles := authz.RolesOf(zero, config.Roles{})
	for _, tc := range []struct {
		act authz.Action
		obj authz.Object
	}{
		{authz.ActCreateApp, authz.Object{}},
		{authz.ActUseTemplate, authz.Object{Template: tpl("", nil, nil)}},
		{authz.ActOpenApp, authz.Object{App: ws("SignedIn", nil, nil), Template: tpl("Public", nil, nil)}},
	} {
		if d := authz.Decide(zero, roles, tc.act, tc.obj); d.Allowed {
			t.Errorf("%s allowed zero principal: %+v", tc.act, d)
		}
	}
}

func TestNewMessageCodesHaveCopy(t *testing.T) {
	for _, c := range []reasons.MessageCode{reasons.MsgNotAMember, reasons.MsgShareNotAllowed, reasons.MsgShareTooOpen, reasons.MsgShareTargetUnknown, reasons.MsgTemplateAdminOnly} {
		if _, ok := reasons.AllMessages()[c]; !ok {
			t.Errorf("no copy for %q", c)
		}
	}
}

func job(owner string) *model.Job {
	return &model.Job{Owner: owner}
}
