package workspace

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
)

// madeApp is alice's streamlit app that her coding agent pi made, and pi
// itself, working in ~/projects/api.
func madeApp(t *testing.T) (ws *model.App, tpl *model.Template, pi *model.App) {
	t.Helper()
	tpl = shippedTemplate(t, "streamlit")
	ws = &model.App{ID: "app-1", Namespace: "javo-apps", Name: "random", Owner: "alice", Template: tpl.Name, File: "projects/api/app.py", MadeBy: "pi"}
	pi = &model.App{ID: "app-2", Namespace: "javo-apps", Name: "pi", Owner: "alice", Template: "pi", File: "projects/api"}
	return ws, tpl, pi
}

// madePod is the pod of in's app, as Kubernetes runs it.
func madePod(t *testing.T, in Inputs) *corev1.Pod {
	t.Helper()
	in.BaseURL, in.Layout = "/u/a/"+in.App.Name+"/", testLayout(FixedUID)
	s, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	return podOf(s)
}

// An app a coding agent made gets no private SSH key, as the agent gets
// none: in every container of its pod that has the home, ~/.ssh is an
// empty folder of the pod. No ssh-agent is run for it either. The same app
// made by its owner sees the home as it is.
func TestAnAppAnAgentMadeSeesNoSSHKeys(t *testing.T) {
	t.Parallel()
	ws, tpl, pi := madeApp(t)
	pod := madePod(t, Inputs{App: ws, Template: tpl, Maker: pi, Images: testImages})
	homes := 0
	for _, c := range slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers) {
		if mountAtPath(&c, "/home/user") == nil {
			continue
		}
		homes++
		ssh := mountAtPath(&c, "/home/user/.ssh")
		if ssh == nil {
			t.Fatalf("container %s has the home and no mount over ~/.ssh: %+v", c.Name, c.VolumeMounts)
		}
		if v := volumeNamed(pod, ssh.Name); v == nil || v.EmptyDir == nil {
			t.Fatalf("container %s: ~/.ssh is the volume %+v, want an empty folder of the pod", c.Name, v)
		}
	}
	if homes < 2 {
		t.Fatalf("%d containers have the home, want the app and its environment's install step", homes)
	}
	if i, _ := initContainer(pod, SSHAgentContainer); i >= 0 {
		t.Fatal("the pod runs an ssh-agent")
	}

	ws.MadeBy = ""
	pod = madePod(t, Inputs{App: ws, Template: tpl, Images: testImages})
	if m := mountAtPath(&pod.Spec.Containers[0], "/home/user/.ssh"); m != nil {
		t.Fatalf("the owner's own app has %+v over ~/.ssh", m)
	}
}

// madeArgv is what the main container of in's app runs.
func madeArgv(t *testing.T, in Inputs) []string {
	t.Helper()
	c := madePod(t, in).Spec.Containers[0]
	return slices.Concat(c.Command, c.Args)
}

// Where the platform locks coding agents in, an app an agent made starts
// in that agent's lock: the agent's folder and what its owner added to it,
// whatever file the app itself names, so it opens nothing the agent could
// not. With the lock off it runs as any app.
func TestAnAppAnAgentMadeStartsInThatAgentsLock(t *testing.T) {
	t.Parallel()
	ws, tpl, pi := madeApp(t)
	// The agent names a file in a project that is not its own.
	ws.File = "projects/taxes/app.py"
	pi.Folders = []model.FolderGrant{{Path: "data", Mode: model.FolderRead}}
	unlocked := madeArgv(t, Inputs{App: ws, Template: tpl, Maker: pi, Images: testImages})
	if slices.Contains(unlocked, agentLock) {
		t.Fatalf("lock off: argv = %q, want no lock", unlocked)
	}
	got := madeArgv(t, Inputs{App: ws, Template: tpl, Maker: pi, Images: lockedImages()})
	want := slices.Concat([]string{agentLock, "/home/user/projects/api", "--ro", "/home/user/data", "--"}, unlocked)
	if !slices.Equal(got, want) {
		t.Fatalf("lock on: argv =\n%q\nwant\n%q", got, want)
	}
}

// What cannot start inside an agent's lock does not start outside it: an
// app whose agent is gone, one on a template without the platform's files
// (which carry the lock), and one with a step that would run or copy what
// the agent chose outside the lock (a repository, a picked environment, a
// built site). The reason says what its owner can do.
func TestAnAppAnAgentMadeThatCannotBeLockedDoesNotStart(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, template string
		change         func(ws *model.App, in *Inputs)
	}{
		{"its agent is gone", "streamlit", func(_ *model.App, in *Inputs) { in.Maker = nil }},
		{"an image of its own", "container", func(ws *model.App, _ *Inputs) { ws.Image, ws.File = "registry.example.com/a/b:1", "" }},
		{"a repository", "streamlit", func(ws *model.App, _ *Inputs) { ws.Source = &model.Source{URL: "https://git.example.com/a/b.git"} }},
		{"a picked environment", "streamlit", func(ws *model.App, in *Inputs) {
			ws.Environment, in.EnvFiles = &model.Environment{Name: "mine", Source: "nebi"}, &model.EnvironmentFiles{}
		}},
		{"a built site", "static-site", func(ws *model.App, _ *Inputs) { ws.File = "projects/api/index.html" }},
	} {
		ws, _, pi := madeApp(t)
		tpl := shippedTemplate(t, c.template)
		ws.Template = tpl.Name
		in := Inputs{App: ws, Template: tpl, Maker: pi, Images: lockedImages(), BaseURL: "/u/a/random/", Layout: testLayout(FixedUID)}
		c.change(ws, &in)
		if _, err := Plan(in); err == nil || !strings.Contains(err.Error(), "a coding agent made") || !strings.Contains(err.Error(), "in the browser") {
			t.Errorf("%s: Plan = %v, want it refused as made by a coding agent, with what its owner can do", c.name, err)
		}
		// Made by its owner, or with the lock off, it is planned as ever.
		in.Images = testImages
		if _, err := Plan(in); err != nil && strings.Contains(err.Error(), "a coding agent made") {
			t.Errorf("%s, lock off: %v", c.name, err)
		}
	}
}

// The reconciler holds an app to its agent's limits from the moment the
// agent changes what it runs: the running pod is replaced by one in the
// agent's lock without the SSH keys, replaced again when the owner changes
// what the agent may open, and stopped when the agent is gone.
func TestAnAgentsChangeReplacesThePodInsideTheAgentsLimits(t *testing.T) {
	t.Parallel()
	fx := newAccessFixture(t)
	fx.r.LockAgents = true
	putTemplateIn(t, fx.s, "agent-maker", func(s *model.TemplateSpec) { s.Category = model.CategoryAgents })
	putTemplateIn(t, fx.s, "lab-made", func(s *model.TemplateSpec) { s.PodFiles = true })
	pi := newAppIn(t, fx.s, fx.ns, "ws-maker", "frank", "agent-maker", func(a *model.App) { a.File = "projects/api" })
	app := newAppIn(t, fx.s, fx.ns, "ws-made", "frank", "lab-made")
	before := fx.settle(t, app)
	if c := before.Spec.Containers[0].Command; slices.Contains(c, agentLock) {
		t.Fatalf("frank's own app runs %q, want no lock", c)
	}
	if _, ok := podMount(before, "/home/user/.ssh"); ok {
		t.Fatal("frank's own app has a mount over ~/.ssh")
	}

	// The agent's token changes what the app runs.
	if _, err := fx.s.Apps().UpdateSpec(bg, model.Actor{Person: "frank", Token: "tok-1", Agent: pi.Name}, app.ID, app.Version, func(a *model.App) error {
		a.Env = []model.EnvVar{{Name: "MODE", Value: "demo"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	made := fx.settle(t, app)
	if made == nil || made.UID == before.UID {
		t.Fatal("the pod was not replaced")
	}
	if c := made.Spec.Containers[0].Command; len(c) < 3 || c[0] != agentLock || c[1] != "/home/user/projects/api" || c[2] != "--" {
		t.Fatalf("the app the agent changed runs %q, want it in the agent's lock", c)
	}
	if _, ok := podMount(made, "/home/user/.ssh"); !ok {
		t.Fatal("the app the agent changed sees the home's ~/.ssh")
	}

	// Frank lets the agent read one more folder: the app follows.
	editApp(t, fx.s, pi.ID, func(a *model.App) { a.Folders = []model.FolderGrant{{Path: "data", Mode: model.FolderRead}} })
	wider := fx.settle(t, app)
	if wider == nil || wider.UID == made.UID {
		t.Fatal("the pod was not replaced when the agent's folders changed")
	}
	if c := wider.Spec.Containers[0].Command; !slices.Equal(c[:5], []string{agentLock, "/home/user/projects/api", "--ro", "/home/user/data", "--"}) {
		t.Fatalf("the app runs %q, want the agent's added folder", c)
	}

	// Without its agent there is no lock to start it in.
	if err := fx.s.Apps().MarkDeleted(bg, actor, pi.ID); err != nil {
		t.Fatal(err)
	}
	if pod := fx.settle(t, app); pod != nil {
		t.Fatalf("the app still runs %q with its agent gone", pod.Spec.Containers[0].Command)
	}
}

// On a machine that runs apps as plain processes the lock is the same one:
// an app a coding agent made starts in that agent's lock, in the folders
// of that machine, and where there is no Landlock it does not start.
func TestAnAppAnAgentMadeIsLockedAsAProcessToo(t *testing.T) {
	t.Parallel()
	in := hostInputs(t, "streamlit")
	in.App.File, in.App.MadeBy = "projects/api/app.py", "pi"
	in.Maker = &model.App{Name: "pi", Owner: "alice", Template: "pi", File: "projects/api"}
	in.Images.LockAgents = true
	if _, err := Plan(in); err == nil || !strings.Contains(err.Error(), "Landlock") {
		t.Fatalf("on macOS: Plan = %v, want it refused for want of Landlock", err)
	}
	in.Host.Platform = "linux-64"
	s, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{hostFiles + "/agents/agent-lock.sh", hostHome + "/projects/api", "--", hostFiles + "/pod-user.sh", "streamlit"}
	if !slices.Equal(s.Main.Command, want) {
		t.Fatalf("on Linux: command = %q, want %q", s.Main.Command, want)
	}
	if got, want := unitEnv(s.Main)["JAVO_SCRATCH_DIR"], testHost.Volumes+"/scratch"; got != want {
		t.Fatalf("on Linux: JAVO_SCRATCH_DIR = %q, want %q", got, want)
	}
}

// An app on a coding agent's template that another agent made starts in
// that agent's lock, not one of its own: the API shows it no lock folder
// of its own, so its owner is offered no folders to add that would open
// nothing.
func TestAnAgentAppAnAgentMadeShowsNoLockOfItsOwn(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "herdr")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "second", Owner: "alice", Template: tpl.Name, File: "projects/taxes"}
	p := Platform{Exposure: pathExposure, PixiImage: testPixiImage, LockAgents: true}
	if folder, locked := p.LockedTo(ws, tpl); !locked || folder != "~/projects/taxes" {
		t.Fatalf("alice's own agent: locked to %q, %v", folder, locked)
	}
	ws.MadeBy = "pi"
	if folder, locked := p.LockedTo(ws, tpl); locked {
		t.Fatalf("the agent another agent made shows a lock of its own, to %q", folder)
	}
	run, err := p.RunOf(ws, tpl)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(run.Command, "/home/user/projects/taxes") && run.Command[0] == agentLock {
		t.Fatalf("RunOf = %q, want no lock to the folder the app names", run.Command)
	}
	pi := &model.App{Name: "pi", Owner: "alice", Template: "pi", File: "projects/api"}
	argv := madeArgv(t, Inputs{App: ws, Template: tpl, Maker: pi, Images: lockedImages()})
	if argv[0] != agentLock || argv[1] != "/home/user/projects/api" {
		t.Fatalf("argv = %q, want it in pi's lock", argv)
	}
}
