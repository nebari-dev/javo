package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/templates"
	"github.com/nebari-dev/javo/internal/workload"
)

// planned is the workload the app reconciler's planner makes for an app
// of a small template of the tests' own: busybox serving one page, with
// the owner's home, as the shipped templates are planned.
func planned(t *testing.T, name string) workload.Spec {
	t.Helper()
	app := &model.App{ID: "app-" + name, Namespace: "javotest", Name: name, Owner: "javotest", Template: "page"}
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

// ownedHome makes the Docker volume that is the home s mounts, owned by
// s's user: on Docker, a home is a volume of Javo's.
func ownedHome(t *testing.T, c *Client, s workload.Spec) string {
	t.Helper()
	ctx := testContext(t)
	for _, v := range s.Volumes {
		if v.Source == "" {
			continue
		}
		if err := c.VolumeCreate(ctx, v.Source, Labels(workload.Ref{Namespace: s.Ref.Namespace, Owner: s.Ref.Owner})); err != nil {
			t.Fatal(err)
		}
		if err := Own(ctx, c, s, testImage, v.Source, 0o700); err != nil {
			t.Fatal(err)
		}
		return v.Source
	}
	t.Fatal("the workload mounts no home")
	return ""
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

// An app planned as the reconciler plans it runs to Ready on Docker: the
// gateway reaches its page, and what it wrote is in the owner's home.
func TestDaemonExecutorRunsAPlannedApp(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	gw := javoGateway(t, c, "plan")
	x := testExecutor(t, c, gw, testPool)
	s := planned(t, "plan")

	home := ownedHome(t, c, s)

	if err := x.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	st := eventually(t, x, s.Ref, "running", isRunning)
	if st.Fingerprint.Run != "r1" {
		t.Fatalf("state %+v", st)
	}
	addr, err := x.Addr(ctx, s.Ref, s.Port)
	if err != nil {
		t.Fatal(err)
	}
	if code, err := c.Exec(ctx, gw, []string{"sh", "-c", "wget -q -T 5 -O - http://" + addr + "/ | grep -q page-of-javotest"}); err != nil || code != 0 {
		t.Fatalf("the gateway's fetch of http://%s/: exit %d, %v", addr, code, err)
	}
	if err := x.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	if got := left(t, c, s.Ref); len(got) != 0 {
		t.Fatalf("Stop left %v", got)
	}
	// The home outlives the app and holds what it wrote.
	reader := testSpec("plan-reader")
	reader.Volumes = []workload.Volume{{Name: "home", Source: home}}
	up(t, c, reader)
	if code, out := run(t, c, reader, sh("read", "ls /h/written", workload.Mount{Volume: "home", Path: "/h"})); code != 0 {
		t.Fatalf("the home lost what the app wrote: exit %d, %s", code, out)
	}
}

// The shipped "container" template, with the sample image it names, runs
// to Ready on Docker as the reconciler plans it: the image is pulled
// from its registry each start, as an image a person names always is.
func TestDaemonExecutorRunsTheShippedContainerTemplate(t *testing.T) {
	if testing.Short() {
		t.Skip("pulls the template's sample image")
	}
	c, _ := daemon(t)
	ctx := testContext(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "helm", "javo", "files", "templates", "container.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := templates.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	app := &model.App{ID: "app-shipped", Namespace: "javotest", Name: "shipped", Owner: "javotest", Template: tpl.Name, Image: tpl.Spec.Sample.Image, Port: tpl.Spec.Port}
	amount := tpl.Spec.DefaultResources
	s, err := workspace.Plan(workspace.Inputs{
		App: app, Template: &tpl, BaseURL: "/u/javotest/shipped/",
		Layout: podstorage.Build(app.Owner, podstorage.Input{UID: 1000, GID: 100}),
		Size:   sizes.Custom(amount.CPU, amount.Memory),
		Images: workspace.Images{AllowedRegistries: []string{"docker.io/nginxinc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownedHome(t, c, s)
	gw := javoGateway(t, c, "shipped")
	x := testExecutor(t, c, gw, testPool)
	if err := x.Start(ctx, s); err != nil {
		t.Fatal(err)
	}
	// The pull is part of the start.
	var st *workload.State
	for deadline := time.Now().Add(5 * time.Minute); ; time.Sleep(200 * time.Millisecond) {
		if st, err = x.Observe(ctx, s.Ref); err != nil {
			t.Fatal(err)
		}
		if isRunning(st) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never running; last state %+v", st)
		}
	}
	addr, _ := x.Addr(ctx, s.Ref, s.Port)
	if code, err := c.Exec(ctx, gw, []string{"sh", "-c", "wget -q -T 5 -O - http://" + addr + "/ | grep -qi 'welcome to nginx'"}); err != nil || code != 0 {
		t.Fatalf("the gateway's fetch of http://%s/: exit %d, %v", addr, code, err)
	}
	if err := x.Stop(ctx, s.Ref); err != nil {
		t.Fatal(err)
	}
	if got := left(t, c, s.Ref); len(got) != 0 {
		t.Fatalf("Stop left %v", got)
	}
}
