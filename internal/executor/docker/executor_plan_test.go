package docker

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor/docker/testrun"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// testImage is busybox 1.37.0, by the digest of its image index on Docker
// Hub: small, public, and never a different image under the same name.
const testImage = "busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec"

// testNamespace is where the workloads of the daemon tests live. It ends
// in the start of this test process's id, so no name is shared with
// another test process.
var testNamespace = "javotest-" + testrun.ID[:10]

// testSpec is a workload of the daemon tests, called name.
func testSpec(name string) workload.Spec {
	return workload.Spec{
		Ref:  workload.Ref{Namespace: testNamespace, Name: name, ID: "id-" + name, Owner: "alice"},
		User: workload.User{UID: 1000, GID: 100},
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}
}

// sh is a unit that runs a shell script in the test image.
func sh(name, script string, mounts ...workload.Mount) workload.Unit {
	return workload.Unit{Name: name, Image: testImage, Command: []string{"sh", "-c", script}, Mounts: mounts}
}

// planned is the workload the app reconciler's planner makes for an app
// of a small template of the tests' own: busybox serving one page, with
// the owner's home, as the shipped templates are planned.
func planned(t *testing.T, name string) workload.Spec {
	t.Helper()
	app := &model.App{ID: "app-" + name, Namespace: testNamespace, Name: name, Owner: testNamespace, Template: "page"}
	tpl := &model.Template{Name: "page", Spec: model.TemplateSpec{
		Kind: "page", Image: testImage, Port: 8080,
		Command:       []string{"sh", "-c"},
		Args:          []string{"mkdir -p /tmp/w && echo page-of-$JAVO_USER > /tmp/w/index.html && touch {{.Home}}/written && exec httpd -f -p {{.Port}} -h /tmp/w"},
		ReadinessPath: "/",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}}
	s, err := workspace.Plan(workspace.Inputs{
		App: app, Template: tpl, BaseURL: "/u/javotest/" + name + "/",
		Layout:      podstorage.Build(app.Owner, podstorage.Input{UID: 1000, GID: 100}),
		Fingerprint: workload.Fingerprint{Run: "r1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A planned app's Spec comes back from the label the sandbox keeps it in
// as the same containers: what an executor adopts is what was started.
func TestAPlannedSpecSurvivesItsLabel(t *testing.T) {
	s := planned(t, "label")
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back workload.Spec
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	units := func(s workload.Spec) []workload.Unit { return append(slices.Clone(s.Steps), s.Main) }
	for i, u := range units(s) {
		want, err := ContainerBody(s, u, Inputs{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := ContainerBody(back, units(back)[i], Inputs{})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("unit %s: %+v, %v, want %+v", u.Name, got, err, want)
		}
	}
	if back.Ref != s.Ref || back.Port != s.Port || !reflect.DeepEqual(back.Main.Ready, s.Main.Ready) || back.Fingerprint != s.Fingerprint {
		t.Fatalf("came back as %+v", back)
	}
}
