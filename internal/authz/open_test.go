package authz_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// OpenOn is whether an app opens for p where apps are served: authz first,
// then what a shared origin refuses.
func TestOpenOn(t *testing.T) {
	oneHost := exposurepath.New(config.URLs{Mode: "path", UIHost: "javo.example.com"})
	appsHost := exposurepath.New(config.URLs{Mode: "path", UIHost: "javo.example.com", AppsHost: "apps.example.com"})
	subdomain := exposuresubdomain.New(config.URLs{Mode: "subdomain", UIHost: "javo.example.com", AppsDomain: "apps.example.com"})
	open := tpl("Public", nil, nil)
	shared := ws("Restricted", []string{bob.ID}, nil)
	preview := func(owner string) *model.App {
		a := ws("Restricted", []string{bob.ID}, nil)
		a.Owner, a.ParentID = owner, "parent-1"
		return a
	}
	parent := func(owner string) *model.App {
		a := ws("Restricted", []string{bob.ID}, nil)
		a.ID, a.Owner = "parent-1", owner
		return a
	}
	adminOwn := authz.Principal{ID: alice.ID, Groups: admin.Groups}
	roles := func(p authz.Principal) []authz.Role { return authz.RolesOf(p, roleCfg) }
	tests := []struct {
		name         string
		exp          exposure.Backend
		allowOneHost bool
		p            authz.Principal
		obj          authz.Object
		allowed      bool
		refusal      reasons.MessageCode
	}{
		{"one host: a share does not open", oneHost, false, bob, authz.Object{App: shared, Template: open}, true, reasons.MsgSharedNeedsAppsHost},
		{"one host with allowOneHost: a share opens", oneHost, true, bob, authz.Object{App: shared, Template: open}, true, ""},
		{"one host: the owner opens", oneHost, false, alice, authz.Object{App: shared, Template: open}, true, ""},
		{"one host: an admin does not open another's app", oneHost, true, admin, authz.Object{App: shared, Template: open}, true, reasons.MsgAdminOpenNeedsAppsHost},
		{"apps host: a share opens", appsHost, false, bob, authz.Object{App: shared, Template: open}, true, ""},
		{"apps host: nobody opens a preview, not even its admin owner", appsHost, false, adminOwn, authz.Object{App: preview(alice.ID), Parent: parent(alice.ID), Template: open}, true, reasons.MsgPreviewNeedsAppsHost},
		{"subdomain: an admin opens another's plain app", subdomain, false, admin, authz.Object{App: shared, Template: open}, true, ""},
		{"subdomain: an admin opens their own preview", subdomain, false, adminOwn, authz.Object{App: preview(alice.ID), Parent: parent(alice.ID), Template: open}, true, ""},
		{"subdomain: an admin does not open another's terminal", subdomain, false, admin, authz.Object{App: shared, Template: kindTpl("terminal")}, false, ""},
		{"subdomain: a stranger does not open", subdomain, false, carol, authz.Object{App: shared, Template: open}, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, refusal := authz.OpenOn(tc.p, roles(tc.p), tc.obj, authz.OriginOf(tc.exp, tc.allowOneHost))
			if d.Allowed != tc.allowed || refusal != tc.refusal {
				t.Fatalf("allowed=%v refusal=%q, want allowed=%v refusal=%q", d.Allowed, refusal, tc.allowed, tc.refusal)
			}
		})
	}
}
