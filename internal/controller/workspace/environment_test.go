package workspace

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/store"
)

const testPixiImage = "example.com/pixi:1"

var envFiles = model.EnvironmentFiles{Manifest: "[workspace]\nname = \"sales\"\n", Lock: "version: 6\n"}

func installStep(pod *corev1.Pod) *corev1.Container {
	for i, c := range pod.Spec.InitContainers {
		if c.Name == pixienv.InstallContainer {
			return &pod.Spec.InitContainers[i]
		}
	}
	return nil
}

func TestAnAppInAnEnvironmentInstallsItBeforeItStarts(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-pixi1")
	in := &model.App{Namespace: fx.ns, Name: "ws-pixi1", Owner: "alice", Template: "lab-pixi1",
		Environment: &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3", Digest: envFiles.Digest()}}
	app, err := fx.db.Apps().CreateWith(bg, model.Actor{Person: "alice"}, in, store.AppExtras{Environment: &envFiles})
	if err != nil {
		t.Fatal(err)
	}
	var pod *corev1.Pod
	eventually(t, "a pod that installs the environment", func() bool {
		pod = fx.podOf(app)
		return pod != nil && installStep(pod) != nil
	})
	if img := installStep(pod).Image; img != testPixiImage {
		t.Fatalf("install image %q, want %q", img, testPixiImage)
	}
	if stepEnv(installStep(pod), "JAVO_PIXI_LOCK") == "" {
		t.Fatalf("the picked files do not reach the install step: %+v", installStep(pod).Env)
	}
}

func stepEnv(c *corev1.Container, name string) string {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// A template's own environment installs for an app with none picked; an app
// from Git may override it with the repository's lockfile, so the step sees
// the clone. A pick wins over both.
func TestATemplatesEnvironmentInstallsAndARepositoryMayReplaceIt(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-pixi4", func(s *model.TemplateSpec) { s.Environment = "streamlit"; s.FromRepo = model.FromRepoOptional })
	home := fx.newApp(t, "ws-pixi4", "alice", "lab-pixi4")
	repo := fx.newApp(t, "ws-pixi5", "alice", "lab-pixi4", func(a *model.App) {
		a.Source = &model.Source{URL: "https://github.com/example-org/site", Ref: "main"}
	})
	picked, err := fx.db.Apps().CreateWith(bg, model.Actor{Person: "alice"}, &model.App{Namespace: fx.ns, Name: "ws-pixi6", Owner: "alice", Template: "lab-pixi4",
		Environment: &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3", Digest: envFiles.Digest()}}, store.AppExtras{Environment: &envFiles})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[string]*corev1.Container{}
	eventually(t, "three pods that install", func() bool {
		for _, a := range []model.App{home, repo, picked} {
			if pod := fx.podOf(a); pod != nil && installStep(pod) != nil {
				steps[a.Name] = installStep(pod)
			}
		}
		return len(steps) == 3
	})
	if d := stepEnv(steps["ws-pixi4"], "JAVO_ENV_DEFAULT"); d != "/opt/javo/envs/streamlit" || stepEnv(steps["ws-pixi4"], "JAVO_ENV_REPO") != "" {
		t.Fatalf("an app from the home: default %q, repo %q", d, stepEnv(steps["ws-pixi4"], "JAVO_ENV_REPO"))
	}
	if r := stepEnv(steps["ws-pixi5"], "JAVO_ENV_REPO"); r != SourceDir+"/"+cloneLink {
		t.Fatalf("an app from Git: repo %q, want the clone", r)
	}
	if stepEnv(steps["ws-pixi6"], "JAVO_PIXI_LOCK") == "" || stepEnv(steps["ws-pixi6"], "JAVO_ENV_DEFAULT") != "" {
		t.Fatalf("a picked environment: %+v", steps["ws-pixi6"].Env)
	}
}

func TestAnAppWithoutAnEnvironmentInstallsNothing(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-pixi2")
	app := fx.newApp(t, "ws-pixi2", "alice", "lab-pixi2")
	var pod *corev1.Pod
	eventually(t, "a pod", func() bool { pod = fx.podOf(app); return pod != nil })
	if installStep(pod) != nil {
		t.Fatal("an app with no environment got an install step")
	}
}

// A preview of an app in an environment runs in the same environment: the
// controller makes it from the parent, and installs the parent's files.
func TestAPreviewInstallsItsParentsEnvironment(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-pixi3")
	in := &model.App{Namespace: fx.ns, Name: "ws-pixi3", Owner: "alice", Template: "lab-pixi3", File: "app.py",
		Source:      &model.Source{URL: "https://github.com/example-org/site", Ref: "main"},
		Environment: &model.Environment{Source: "nebi", ID: "ws-1", Name: "sales", Version: "3", Digest: envFiles.Digest()}}
	parent, err := fx.db.Apps().CreateWith(bg, model.Actor{Person: "alice"}, in, store.AppExtras{Environment: &envFiles})
	if err != nil {
		t.Fatal(err)
	}
	// As preview.Child makes it: the parent's spec, with no files of its own.
	child, err := fx.db.Previews().Put(bg, &model.App{Namespace: fx.ns, Name: "ws-pixi3-pr-1", Owner: "alice", Template: "lab-pixi3", File: "app.py",
		Source: parent.Source, Environment: parent.Environment, ParentID: parent.ID, PullNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the preview's pod installs the environment", func() bool {
		pod := fx.podOf(child)
		return pod != nil && installStep(pod) != nil
	})
}

// On a template that serves kernels, a picked environment goes beside the
// template's own; elsewhere it replaces it and must bring the program the
// template starts, which the step is told with the environment's name.
func TestAPickedEnvironmentGoesBesideAKernelServerAndIsCheckedElsewhere(t *testing.T) {
	t.Parallel()
	fx := newEnv(t)
	fx.newTemplate(t, "lab-pixi7", func(s *model.TemplateSpec) {
		s.Image, s.Environment, s.ServesKernels = "", "jupyterlab", true
		s.Command = []string{"/opt/javo/pod-user.sh", "sh", "-c", "exec jupyter-lab"}
	})
	fx.newTemplate(t, "lab-pixi8", func(s *model.TemplateSpec) {
		s.Image, s.Environment = "", "streamlit"
		s.Command, s.Args = []string{"/opt/javo/pod-user.sh", "streamlit"}, []string{"run", "app.py"}
	})
	picked := func(name, tpl string) model.App {
		a, err := fx.db.Apps().CreateWith(bg, model.Actor{Person: "alice"}, &model.App{Namespace: fx.ns, Name: name, Owner: "alice", Template: tpl,
			Environment: &model.Environment{Source: "nebi", ID: "ws-1", Name: "smoke-pushed", Version: "3", Digest: envFiles.Digest()}}, store.AppExtras{Environment: &envFiles})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	lab, st := picked("ws-pixi7", "lab-pixi7"), picked("ws-pixi8", "lab-pixi8")
	steps := map[string]*corev1.Container{}
	eventually(t, "two pods that install", func() bool {
		for _, a := range []model.App{lab, st} {
			if pod := fx.podOf(a); pod != nil && installStep(pod) != nil {
				steps[a.Name] = installStep(pod)
			}
		}
		return len(steps) == 2
	})
	l := steps["ws-pixi7"]
	if stepEnv(l, "JAVO_ENV_BESIDE") == "" || stepEnv(l, "JAVO_ENV_DEFAULT") != "/opt/javo/envs/jupyterlab" || stepEnv(l, "JAVO_PIXI_LOCK") == "" || stepEnv(l, "JAVO_ENV_NAME") != "smoke-pushed" {
		t.Fatalf("on a kernel server: %+v", l.Env)
	}
	s := steps["ws-pixi8"]
	if stepEnv(s, "JAVO_ENV_BESIDE") != "" || stepEnv(s, "JAVO_ENV_PROGRAM") != "streamlit" || stepEnv(s, "JAVO_ENV_NAME") != "smoke-pushed" || stepEnv(s, "JAVO_ENV_FROM") != "Nebi" {
		t.Fatalf("elsewhere: %+v", s.Env)
	}
}
