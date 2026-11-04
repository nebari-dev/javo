package workspace

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The run the API shows (Platform.RunOf) is what the controller's pod runs:
// the same image, and the container's command then args, for templates
// that fill in a file, the home, a typed command, the public host and a
// person's image.
func TestRunOfIsWhatThePodRuns(t *testing.T) {
	t.Parallel()
	const host = "javo.example.com"
	fx := &env{db: storetest.New(t), ns: newNamespace(t, "javo-apps", nil)}
	mgr, feed, _, err := newManager(fx.db, []string{fx.ns}, func(r *Reconciler) { r.PublicHost = host }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(run(fx.db, mgr, feed))
	platform := Platform{Exposure: pathExposure, PublicHost: host, PixiImage: testPixiImage}

	cases := []struct {
		template string
		app      func(*model.App)
		// want are words the command must hold, so a run that renders
		// nothing cannot pass.
		want []string
	}{
		{"streamlit", func(a *model.App) { a.File = "sales/app.py" }, []string{"/opt/javo/pod-user.sh", "streamlit", homeDir + "/sales/app.py"}},
		{"jupyterlab", func(*model.App) {}, []string{"/opt/javo/pod-user.sh", homeDir}},
		{"panel", func(a *model.App) { a.File = "dash.py" }, []string{homeDir + "/dash.py"}},
		{"custom", func(a *model.App) { a.Command = "python serve.py --port={{.Port}}" }, []string{"/opt/javo/pod-user.sh", "python", "serve.py", "--port=8080"}},
		{"container", func(a *model.App) { a.Image = "example.com/web:2"; a.Port = 9000 }, nil},
		{"container", func(a *model.App) { a.Image = "example.com/web:2"; a.Port = 9000; a.Command = "web --listen :9000" }, []string{"web", "--listen", ":9000"}},
	}
	for i, c := range cases {
		tpl := shippedTemplate(t, c.template)
		if _, err := fx.db.Templates().Get(bg, tpl.Name); err != nil {
			fx.newTemplate(t, tpl.Name, func(s *model.TemplateSpec) { *s = tpl.Spec })
		}
		app := fx.newApp(t, fmt.Sprintf("%s-%d", c.template, i), "alice", tpl.Name, c.app)
		got, err := platform.RunOf(&app, tpl)
		if err != nil {
			t.Fatalf("%s: %v", c.template, err)
		}
		for _, w := range c.want {
			if !slices.Contains(got.Command, w) {
				t.Errorf("%s: command %q has no %q", c.template, got.Command, w)
			}
		}
		if c.template == "panel" && !strings.Contains(strings.Join(got.Command, " "), host) {
			t.Errorf("panel: command %q does not name the public host", got.Command)
		}
		eventually(t, c.template+" pod", func() bool { return fx.podOf(app) != nil })
		ctr := fx.podOf(app).Spec.Containers[0]
		if ctr.Image != got.Image {
			t.Errorf("%s: API image %q, pod runs %q", c.template, got.Image, ctr.Image)
		}
		if pod := slices.Concat(ctr.Command, ctr.Args); !slices.Equal(pod, got.Command) {
			t.Errorf("%s: API command %q, pod runs %q", c.template, got.Command, pod)
		}
	}
}

// The environment is said the way withEnvironment picks it: the picked one,
// beside the template's on a template that serves kernels; else the
// repository's pixi.lock when there may be one, else the template's; none
// for a template without one.
func TestEnvironmentOfSaysWhichEnvironmentInstalls(t *testing.T) {
	t.Parallel()
	nebi := &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3"}
	lab := &model.Template{Name: "lab", Spec: model.TemplateSpec{Environment: "jupyterlab", ServesKernels: true}}
	st := &model.Template{Name: "st", Spec: model.TemplateSpec{Environment: "streamlit"}}
	img := &model.Template{Name: "img", Spec: model.TemplateSpec{Image: "example.com/r:1"}}
	cases := []struct {
		app  model.App
		tpl  *model.Template
		want string
	}{
		{model.App{}, st, "The template's environment streamlit"},
		{model.App{Environment: nebi}, st, "sales, version 3, from Nebi"},
		{model.App{Environment: nebi}, lab, "The template's environment jupyterlab, with sales, version 3, from Nebi beside it"},
		{model.App{Source: &model.Source{URL: "https://example.com/r.git"}}, st, "The repository's pixi.lock when it has one, else the template's environment streamlit"},
		{model.App{}, img, ""},
	}
	for _, c := range cases {
		if got := EnvironmentOf(&c.app, c.tpl); got != c.want {
			t.Errorf("EnvironmentOf(%+v, %s) = %q, want %q", c.app, c.tpl.Name, got, c.want)
		}
	}
}
