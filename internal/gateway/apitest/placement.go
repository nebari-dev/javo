package apitest

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/features/placementperteam"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/features/placementshared"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

func PerUserFixture(t *testing.T) *Fixture {
	t.Helper()
	pl := placementperuser.New(config.Namespaces{Prefix: "javo"})
	// The controller labels each person's namespace when their Identity appears.
	return New(t, Options{Placement: pl}, gatewaytest.Template("lab", "jupyterlab"),
		ManagedNS("javo-"+AliceID), ManagedNS("javo-"+BobID))
}

func PerTeamFixture(t *testing.T) *Fixture {
	t.Helper()
	pl := placementperteam.New(config.Namespaces{Prefix: "javo", Teams: []config.Team{
		{Group: "/org/ds", Slug: "ds"}, {Group: "/org/ml", Slug: "ml"},
	}})
	return New(t, Options{Placement: pl}, gatewaytest.Template("lab", "jupyterlab"))
}

const NewApp = `{"displayName":"Sales","template":"lab"}`

// WorkspaceIn is owner's stored app, which must live in namespace ns.
func (f *Fixture) WorkspaceIn(ns, owner, name string) *model.App {
	f.T.Helper()
	app := f.GetWorkspace(owner, name)
	if app.Namespace != ns {
		f.T.Fatalf("app %s is in namespace %q, want %q", name, app.Namespace, ns)
	}
	return app
}

func (f *Fixture) WorkspaceCountAll() int { return f.WorkspaceCount() }

// SharedPlacement is every test's default: all apps in wsNS.
func SharedPlacement() placement.Placement {
	return placementshared.New(config.Namespaces{Shared: gatewaytest.Namespace})
}

func ManagedNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{model.LabelManaged: "true"}}}
}
