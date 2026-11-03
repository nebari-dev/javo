package placementperteam_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/features/placementperteam"
	"github.com/nebari-dev/javo/internal/reasons"
)

func teams() placement.Placement {
	return placementperteam.New(config.Namespaces{Prefix: "javo", Teams: []config.Team{
		{Group: "/org/ds", Slug: "ds"}, {Group: "/org/ml", Slug: "ml"},
	}})
}

func refusal(t *testing.T, err error) reasons.MessageCode {
	t.Helper()
	var r *placement.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a *placement.Refusal", err)
	}
	return r.Code
}

func TestOneTeamNeedsNoChoice(t *testing.T) {
	p := authz.Principal{ID: "alice-1a2b3c4d", Groups: []string{"/org/ds", "/other"}}
	ns, err := teams().NamespaceFor(p, "")
	if err != nil || ns != "javo-ds" {
		t.Fatalf("NamespaceFor = %q, %v", ns, err)
	}
}

func TestSeveralTeamsAskForAChoice(t *testing.T) {
	p := authz.Principal{ID: "alice-1a2b3c4d", Groups: []string{"/org/ds", "/org/ml"}}
	_, err := teams().NamespaceFor(p, "")
	if got := refusal(t, err); got != reasons.MsgPickTeam {
		t.Fatalf("code = %s, want %s", got, reasons.MsgPickTeam)
	}
	ns, err := teams().NamespaceFor(p, "ml")
	if err != nil || ns != "javo-ml" {
		t.Fatalf("NamespaceFor(ml) = %q, %v", ns, err)
	}
}

func TestNoTeamCannotCreate(t *testing.T) {
	_, err := teams().NamespaceFor(authz.Principal{ID: "bob-5e6f7a8b", Groups: []string{"/other"}}, "")
	if got := refusal(t, err); got != reasons.MsgNoTeam {
		t.Fatalf("code = %s, want %s", got, reasons.MsgNoTeam)
	}
}

func TestOtherPeoplesTeamIsRefused(t *testing.T) {
	p := authz.Principal{ID: "alice-1a2b3c4d", Groups: []string{"/org/ds"}}
	for _, team := range []string{"ml", "nonsense"} {
		_, err := teams().NamespaceFor(p, team)
		if got := refusal(t, err); got != reasons.MsgTeamNotYours {
			t.Errorf("team %q: code = %s, want %s", team, got, reasons.MsgTeamNotYours)
		}
	}
}

func TestNamespacesAreTheTeamsOfThePerson(t *testing.T) {
	both := authz.Principal{ID: "a", Groups: []string{"/org/ml", "/org/ds"}}
	if got := teams().Namespaces(both); !slices.Equal(got, []string{"javo-ds", "javo-ml"}) {
		t.Fatalf("Namespaces = %v", got)
	}
	if got := teams().Namespaces(authz.Principal{ID: "b"}); len(got) != 0 {
		t.Fatalf("Namespaces(no team) = %v", got)
	}
}

func TestEveryListedTeamIsKept(t *testing.T) {
	var names []string
	for _, d := range teams().Desired() {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"javo-ds", "javo-ml"}) {
		t.Fatalf("Desired = %v", names)
	}
}
