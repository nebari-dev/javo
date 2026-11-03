package authz_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

func secretOf(owner string) *model.Secret { return &model.Secret{Owner: owner, Name: "API_KEY"} }

func appOf(owner string) *model.App { return &model.App{Owner: owner, Template: "t"} }

func TestOnlyTheOwnerManagesASecret(t *testing.T) {
	roles := func(p authz.Principal) []authz.Role { return authz.RolesOf(p, roleCfg) }
	for _, tc := range []struct {
		name string
		p    authz.Principal
		want bool
	}{
		{"owner", alice, true},
		{"another member", bob, false},
		// A platform admin manages every app, but never another person's secrets.
		{"platform admin", admin, false},
		{"anonymous", anon, false},
		{"zero principal", authz.Principal{}, false},
	} {
		d := authz.Decide(tc.p, roles(tc.p), authz.ActManageSecret, authz.Object{Secret: secretOf(alice.ID)})
		if d.Allowed != tc.want {
			t.Errorf("%s: allowed %v", tc.name, d.Allowed)
		}
		if !d.Allowed && d.Code != reasons.MsgSecretNotFound {
			t.Errorf("%s: code %s; a refusal must not reveal the secret exists", tc.name, d.Code)
		}
	}
	if d := authz.Decide(alice, nil, authz.ActManageSecret, authz.Object{}); d.Allowed {
		t.Fatal("allowed with no secret")
	}
}

func TestUseSecretNeedsOwnerOfBothAndNoPreview(t *testing.T) {
	preview := appOf(alice.ID)
	preview.ParentID = "parent-1"
	fork := appOf(alice.ID)
	fork.Source = &model.Source{URL: "https://github.com/example-org/site", Fork: true}
	for _, tc := range []struct {
		name string
		p    authz.Principal
		obj  authz.Object
		want bool
	}{
		{"own secret into own app", alice, authz.Object{App: appOf(alice.ID), Secret: secretOf(alice.ID)}, true},
		{"own secret into another's app", alice, authz.Object{App: appOf(bob.ID), Secret: secretOf(alice.ID)}, false},
		{"another's secret into own app", alice, authz.Object{App: appOf(alice.ID), Secret: secretOf(bob.ID)}, false},
		{"admin binds into alice's app", admin, authz.Object{App: appOf(alice.ID), Secret: secretOf(alice.ID)}, false},
		{"preview", alice, authz.Object{App: preview, Secret: secretOf(alice.ID)}, false},
		{"fork pull request", alice, authz.Object{App: fork, Secret: secretOf(alice.ID)}, false},
		{"no app", alice, authz.Object{Secret: secretOf(alice.ID)}, false},
	} {
		if d := authz.Decide(tc.p, authz.RolesOf(tc.p, roleCfg), authz.ActUseSecret, tc.obj); d.Allowed != tc.want {
			t.Errorf("%s: allowed %v", tc.name, d.Allowed)
		}
	}
}

// The controller acts for nobody: a secret goes into an app only when both
// have one owner and the app is not a preview.
func TestInjectSecret(t *testing.T) {
	preview := appOf(alice.ID)
	preview.ParentID = "parent-1"
	for _, tc := range []struct {
		name string
		obj  authz.Object
		want bool
	}{
		{"same owner", authz.Object{App: appOf(alice.ID), Secret: secretOf(alice.ID)}, true},
		{"other owner", authz.Object{App: appOf(bob.ID), Secret: secretOf(alice.ID)}, false},
		{"preview", authz.Object{App: preview, Secret: secretOf(alice.ID)}, false},
		{"empty owners", authz.Object{App: appOf(""), Secret: secretOf("")}, false},
		{"no secret", authz.Object{App: appOf(alice.ID)}, false},
	} {
		if d := authz.Decide(authz.Principal{}, nil, authz.ActInjectSecret, tc.obj); d.Allowed != tc.want {
			t.Errorf("%s: allowed %v", tc.name, d.Allowed)
		}
	}
}

func jobOf(owner string) *model.Job { return &model.Job{Owner: owner, Name: "nightly"} }

// Jobs get secrets on the same terms as apps: only the owner, only their own
// secret, only into their own job. A job has no previews.
func TestUseSecretInAJob(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    authz.Principal
		obj  authz.Object
		want bool
	}{
		{"own secret into own job", alice, authz.Object{Job: jobOf(alice.ID), Secret: secretOf(alice.ID)}, true},
		{"own secret into another's job", alice, authz.Object{Job: jobOf(bob.ID), Secret: secretOf(alice.ID)}, false},
		{"another's secret into own job", alice, authz.Object{Job: jobOf(alice.ID), Secret: secretOf(bob.ID)}, false},
		{"admin binds into alice's job", admin, authz.Object{Job: jobOf(alice.ID), Secret: secretOf(alice.ID)}, false},
		{"anonymous", anon, authz.Object{Job: jobOf(""), Secret: secretOf("")}, false},
		{"app and job both named", alice, authz.Object{App: appOf(alice.ID), Job: jobOf(alice.ID), Secret: secretOf(alice.ID)}, false},
	} {
		if d := authz.Decide(tc.p, authz.RolesOf(tc.p, roleCfg), authz.ActUseSecret, tc.obj); d.Allowed != tc.want {
			t.Errorf("%s: allowed %v", tc.name, d.Allowed)
		}
	}
}

func TestInjectSecretIntoAJob(t *testing.T) {
	for _, tc := range []struct {
		name string
		obj  authz.Object
		want bool
	}{
		{"same owner", authz.Object{Job: jobOf(alice.ID), Secret: secretOf(alice.ID)}, true},
		{"other owner", authz.Object{Job: jobOf(bob.ID), Secret: secretOf(alice.ID)}, false},
		{"empty owners", authz.Object{Job: jobOf(""), Secret: secretOf("")}, false},
		{"no secret", authz.Object{Job: jobOf(alice.ID)}, false},
	} {
		if d := authz.Decide(authz.Principal{}, nil, authz.ActInjectSecret, tc.obj); d.Allowed != tc.want {
			t.Errorf("%s: allowed %v", tc.name, d.Allowed)
		}
	}
}
