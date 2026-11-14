package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// lockedImages builds pods with the coding agents locked in (config
// workloads.landlock).
func lockedImages() Images {
	img := testImages
	img.LockAgents = true
	return img
}

// argvOf is what the main container of the app's Pod runs: the whole plan
// (Plan), as Kubernetes renders it.
func argvOf(t *testing.T, ws *model.App, tpl *model.Template, img Images) []string {
	t.Helper()
	s, err := Plan(Inputs{App: ws, Template: tpl, BaseURL: "/u/a/" + ws.Name + "/", Layout: testLayout(FixedUID), Images: img})
	if err != nil {
		t.Fatal(err)
	}
	pod := podOf(s)
	return slices.Concat(pod.Spec.Containers[0].Command, pod.Spec.Containers[0].Args)
}

// With the lock on, the whole of a coding agent's container command (the
// agent, its terminal and everything they start) runs under agent-lock.sh,
// locked to the app's folder; with it off the command is as before.
func TestLockedAgentsStartThroughTheLockInTheirFolder(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"claude-code", "pi", "herdr"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tpl := shippedTemplate(t, name)
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name, File: "projects/api"}
			unlocked := argvOf(t, ws, tpl, testImages)
			if slices.Contains(unlocked, "/opt/javo/agents/agent-lock.sh") {
				t.Fatalf("lock off: argv = %q, want no lock", unlocked)
			}
			locked := argvOf(t, ws, tpl, lockedImages())
			want := slices.Concat([]string{"/opt/javo/agents/agent-lock.sh", "/home/user/projects/api", "--"}, unlocked)
			if !slices.Equal(locked, want) {
				t.Fatalf("lock on: argv =\n%q\nwant\n%q", locked, want)
			}
			// The API shows what the pod runs.
			run, err := (Platform{Exposure: pathExposure, PixiImage: testPixiImage, LockAgents: true}).RunOf(ws, tpl)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(run.Command, want) {
				t.Fatalf("RunOf = %q, want %q", run.Command, want)
			}
		})
	}
}

// The lock is for the coding agents only: other templates run as they did.
func TestLockLeavesOtherTemplatesAlone(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "jupyterlab")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "lab", Owner: "alice", Template: tpl.Name}
	if got, want := argvOf(t, ws, tpl, lockedImages()), argvOf(t, ws, tpl, testImages); !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// An agent app with no project folder would run in the whole home; with the
// lock on it gets a folder of its own instead, ~/projects/<app name>, and
// starts and is locked there. With the lock off it stays in the home.
func TestLockedAgentWithoutAFolderGetsItsOwnProject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, folderArg string }{
		{"claude-code", "%s"},
		{"pi", "%s"},
		{"herdr", "--cwd=%s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tpl := shippedTemplate(t, tc.name)
			ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "todo-app", Owner: "alice", Template: tpl.Name}
			if argv := argvOf(t, ws, tpl, testImages); !slices.Contains(argv, fmt.Sprintf(tc.folderArg, "/home/user")) {
				t.Fatalf("lock off: argv = %q, want the agent in the home", argv)
			}
			argv := argvOf(t, ws, tpl, lockedImages())
			const project = "/home/user/projects/todo-app"
			if len(argv) < 3 || argv[0] != agentLock || argv[1] != project || argv[2] != "--" {
				t.Fatalf("argv = %q, want it locked to %s", argv, project)
			}
			if !slices.Contains(argv, fmt.Sprintf(tc.folderArg, project)) || slices.Contains(argv, fmt.Sprintf(tc.folderArg, "/home/user")) {
				t.Fatalf("argv = %q, want the agent started in %s", argv, project)
			}
		})
	}
}

// Harf keeps what pi makes and what people upload in its workspace,
// ~/workspace when the app has no folder. Locked, that is the folder the app
// is locked to, the only one of the home pi may write.
func TestLockedHarfWorksInTheFolderItIsLockedTo(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "harf")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "chat", Owner: "alice", Template: tpl.Name}
	if argv := argvOf(t, ws, tpl, testImages); !slices.Contains(argv, "-workspace=/home/user/workspace") {
		t.Fatalf("lock off: argv = %q, want Harf's own workspace", argv)
	}
	argv := argvOf(t, ws, tpl, lockedImages())
	if argv[1] != "/home/user/projects/chat" || !slices.Contains(argv, "-workspace="+argv[1]) {
		t.Fatalf("lock on: argv = %q, want Harf locked to its workspace, ~/projects/chat", argv)
	}
}

// The lock changes an agent's Pod in two places and nowhere else: the main
// container's command gets the lock in front, and an app with no folder
// runs as the same app opening ~/projects/<app name> does. The step that
// installs the environment is told the lock is the program the app starts,
// as it reads the command. Every mount, volume, variable and other
// container is the one the unlocked Pod has.
func TestTheLockChangesOnlyTheCommandOfAnAgentsPod(t *testing.T) {
	t.Parallel()
	variants := map[string]func(*Inputs){
		"no folder": func(*Inputs) {},
		"a folder":  func(in *Inputs) { in.App.File = "projects/api" },
		"the sample": func(in *Inputs) {
			if in.Template.Spec.Sample != nil {
				in.App.Sample, in.App.File = true, SampleFileOf(in.Template)
			}
		},
		"secrets, API access and the platform's model": func(in *Inputs) {
			in.Secrets = secretenv.Set{Fingerprint: "abc", Bindings: []store.SealedBinding{{Env: "DB_PASSWORD"}}}
			in.APIAccess, in.HasToken, in.APIURL = model.APIAccessReadWrite, true, "https://javo.example.com"
			in.Agent = agentModel{mode: model.AgentModePlatform, model: "model-1", baseURL: "https://javo.example.com/agent"}
		},
		"a picked environment": func(in *Inputs) {
			in.App.Environment = &model.Environment{Source: "nebi", ID: "e1", Name: "data science", Digest: "d"}
			in.EnvFiles = &model.EnvironmentFiles{Manifest: "[workspace]\n", Lock: "version: 6\n"}
		},
	}
	for _, name := range []string{"claude-code", "pi", "herdr", "harf"} {
		for variant, edit := range variants {
			t.Run(name+"/"+variant, func(t *testing.T) {
				t.Parallel()
				inputs := func(img Images) Inputs {
					tpl := shippedTemplate(t, name)
					tpl.Spec.ApplyDefaults()
					in := Inputs{
						App:      &model.App{ID: "app-1", Namespace: "javo-apps", Name: "todo-app", Owner: "alice", Template: tpl.Name},
						Template: tpl, BaseURL: "/u/alice/todo-app/", Layout: testLayout(FixedUID), Images: img,
					}
					edit(&in)
					return in
				}
				in := inputs(lockedImages())
				s, err := Plan(in)
				if err != nil {
					t.Fatal(err)
				}
				locked := podOf(s)

				in = inputs(testImages)
				folder := "/home/user/projects/todo-app"
				if in.App.File == "" {
					in.App.File = "projects/todo-app"
				} else {
					folder = FilePath(in.App)
				}
				if s, err = Plan(in); err != nil {
					t.Fatal(err)
				}
				want := podOf(s)
				main := &want.Spec.Containers[0]
				main.Command = slices.Concat([]string{"/opt/javo/agents/agent-lock.sh", folder, "--"}, main.Command)
				if install := installStep(want); install != nil {
					for i, e := range install.Env {
						if e.Name == "JAVO_ENV_PROGRAM" && e.Value != "" {
							install.Env[i].Value = "/opt/javo/agents/agent-lock.sh"
						}
					}
				}
				if !reflect.DeepEqual(locked, want) {
					got, _ := json.MarshalIndent(locked, "", " ")
					exp, _ := json.MarshalIndent(want, "", " ")
					t.Fatalf("locked pod =\n%s\nwant\n%s", got, exp)
				}
			})
		}
	}
}

// Landlock is Linux's. An agent planned for a machine that is not Linux
// (apps as processes of a Mac) is refused rather than run without the lock;
// on a Linux machine it starts through the lock in that machine's folders,
// and every other app there runs as before.
func TestLockedAgentOnAHostNeedsLinux(t *testing.T) {
	t.Parallel()
	in := hostInputs(t, "claude-code")
	in.Images.LockAgents = true
	var unsupported *executor.UnsupportedError
	if _, err := Plan(in); !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "Landlock") || !strings.Contains(err.Error(), "osx-arm64") {
		t.Fatalf("on a Mac: err = %v, want it refused for Landlock", err)
	}
	lab := hostInputs(t, "jupyterlab")
	lab.Images.LockAgents = true
	if _, err := Plan(lab); err != nil {
		t.Fatalf("an app that is no agent, on a Mac: %v", err)
	}

	in.Host.Platform = "linux-64"
	s, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	home := hostHome
	want := []string{hostFiles + "/agents/agent-lock.sh", home + "/projects/demo", "--", hostFiles + "/pod-user.sh"}
	if !slices.Equal(s.Main.Command, want) {
		t.Fatalf("on Linux: command = %q, want %q", s.Main.Command, want)
	}
	if !slices.Contains(s.Main.Args, home+"/projects/demo") || slices.Contains(s.Main.Args, home) {
		t.Fatalf("on Linux: args = %q, want the agent started in its own project", s.Main.Args)
	}
	// The tools' caches and environments are in the workload's scratch
	// folder there, which the lock is told of to grant it; an agent that is
	// not locked is told nothing.
	env := unitEnv(s.Main)
	if got, want := env["JAVO_SCRATCH_DIR"], testHost.Volumes+"/scratch"; got != want || !strings.HasPrefix(env["XDG_CACHE_HOME"], want+"/") {
		t.Fatalf("on Linux: JAVO_SCRATCH_DIR = %q, want %q, where XDG_CACHE_HOME (%q) is", got, want, env["XDG_CACHE_HOME"])
	}
	in.Images.LockAgents = false
	if s, err = Plan(in); err != nil {
		t.Fatal(err)
	}
	if dir, told := unitEnv(s.Main)["JAVO_SCRATCH_DIR"]; told {
		t.Fatalf("lock off: JAVO_SCRATCH_DIR = %q", dir)
	}
}

// Whatever an app names as its folder, a locked agent is locked to a folder
// inside the home and never to the home itself: a path that is the home
// gets the app's own project, and one with ".." or from / stays under the
// home, so agent-lock.sh is never handed a folder outside it.
func TestLockedAgentIsLockedInsideTheHomeWhateverItsFolder(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "claude-code")
	for file, want := range map[string]string{
		"":                "/home/user/projects/todo-app",
		".":               "/home/user/projects/todo-app",
		"/":               "/home/user/projects/todo-app",
		"a/..":            "/home/user/projects/todo-app",
		"../..":           "/home/user/projects/todo-app",
		"../../etc":       "/home/user/etc",
		"/etc":            "/home/user/etc",
		"projects/api/..": "/home/user/projects",
	} {
		ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "todo-app", Owner: "alice", Template: tpl.Name, File: file}
		argv := argvOf(t, ws, tpl, lockedImages())
		if len(argv) < 3 || argv[0] != agentLock || argv[1] != want || argv[2] != "--" {
			t.Errorf("folder %q: argv = %q, want it locked to %s", file, argv, want)
		}
		if !slices.Contains(argv, want) || slices.Contains(argv, "/home/user") {
			t.Errorf("folder %q: argv = %q, want the agent started in %s", file, argv[3:], want)
		}
	}
}

// Turning the lock on or off changes what a coding agent's pod runs, so a
// running agent is out of date and restarts once, into the lock or out of
// it. No other app is, and with the lock off every app's fingerprint is the
// one it had before there was a lock.
func TestFlippingTheLockRestartsRunningAgentsAndNothingElse(t *testing.T) {
	t.Parallel()
	layout := testLayout(FixedUID)
	off, on := &Reconciler{}, &Reconciler{LockAgents: true}
	for _, name := range shippedNames(t) {
		tpl := shippedTemplate(t, name)
		app := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "demo", Owner: "alice", Template: name}
		was := LayoutFingerprint(app, tpl, layout)
		if got := off.layoutFingerprint(app, tpl, layout); got != was {
			t.Errorf("%s, lock off: fingerprint %s, want %s as before", name, got, was)
		}
		running := workload.Fingerprint{Layout: was}
		restarts := workload.Fingerprint{Layout: on.layoutFingerprint(app, tpl, layout)}.Outdated(running)
		if agent := tpl.Spec.Category == model.CategoryAgents; restarts != agent {
			t.Errorf("%s, lock turned on: restarts = %v, want %v", name, restarts, agent)
		}
	}
}

// The folders a person added to their agent go to agent-lock.sh in front of
// the command, each with how it may be opened and as a path of the home
// the app is planned for. The stored list is checked again here: a grant
// the API would refuse (written past it) opens nothing. With the lock off
// there is nothing to add to.
func TestLockedAgentIsToldTheFoldersItsOwnerAdded(t *testing.T) {
	t.Parallel()
	tpl := shippedTemplate(t, "pi")
	ws := &model.App{ID: "app-1", Namespace: "javo-apps", Name: "agent", Owner: "alice", Template: tpl.Name, File: "projects/api", Folders: []model.FolderGrant{
		{Path: "data/sales-2026", Mode: model.FolderRead},
		{Path: ".ssh", Mode: model.FolderRead},
		{Path: "../bob", Mode: model.FolderWrite},
		{Path: "notes.md", Mode: model.FolderWrite},
	}}
	unlocked := argvOf(t, ws, tpl, testImages)
	if slices.Contains(unlocked, "--ro") || slices.Contains(unlocked, "--rw") {
		t.Fatalf("lock off: argv = %q, want no folders", unlocked)
	}
	want := slices.Concat([]string{agentLock, "/home/user/projects/api", "--ro", "/home/user/data/sales-2026", "--rw", "/home/user/notes.md", "--"}, unlocked)
	if got := argvOf(t, ws, tpl, lockedImages()); !slices.Equal(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
	run, err := (Platform{Exposure: pathExposure, PixiImage: testPixiImage, LockAgents: true}).RunOf(ws, tpl)
	if err != nil || !slices.Equal(run.Command, want) {
		t.Fatalf("RunOf = %q, %v, want %q", run.Command, err, want)
	}

	// On a machine the home is a folder of that machine.
	in := hostInputs(t, "pi")
	in.Images.LockAgents, in.Host.Platform = true, "linux-64"
	in.App.Folders = ws.Folders
	s, err := Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	onHost := []string{hostFiles + "/agents/agent-lock.sh", hostHome + "/projects/demo", "--ro", hostHome + "/data/sales-2026", "--rw", hostHome + "/notes.md", "--"}
	if len(s.Main.Command) < len(onHost) || !slices.Equal(s.Main.Command[:len(onHost)], onHost) {
		t.Fatalf("on a host: command = %q, want it to start with %q", s.Main.Command, onHost)
	}
}

// Landlock is set when the agent starts, so a running agent cannot be let
// into more, or less: adding a folder, taking one away or changing how one
// may be opened leaves its pod out of date, and it restarts. A list that
// grants the same, and any list while the lock is off or on an app that is
// no agent, leaves the pod as it is.
func TestChangingAnAgentsFoldersRestartsIt(t *testing.T) {
	t.Parallel()
	layout := testLayout(FixedUID)
	on := &Reconciler{LockAgents: true}
	read := []model.FolderGrant{{Path: "data", Mode: model.FolderRead}}
	app := func(template string, folders []model.FolderGrant) *model.App {
		return &model.App{ID: "app-1", Namespace: "javo-apps", Name: "demo", Owner: "alice", Template: template, Folders: folders}
	}
	outdated := func(r *Reconciler, template string, was, now []model.FolderGrant) bool {
		tpl := shippedTemplate(t, template)
		running := workload.Fingerprint{Layout: r.layoutFingerprint(app(template, was), tpl, layout)}
		return workload.Fingerprint{Layout: r.layoutFingerprint(app(template, now), tpl, layout)}.Outdated(running)
	}
	for name, tc := range map[string]struct {
		was, now []model.FolderGrant
		want     bool
	}{
		"added":         {nil, read, true},
		"removed":       {read, nil, true},
		"another mode":  {read, []model.FolderGrant{{Path: "data", Mode: model.FolderWrite}}, true},
		"another path":  {read, []model.FolderGrant{{Path: "data2", Mode: model.FolderRead}}, true},
		"the same":      {read, []model.FolderGrant{{Path: "./data/", Mode: model.FolderRead}}, false},
		"none it keeps": {nil, []model.FolderGrant{{Path: ".ssh", Mode: model.FolderRead}}, false},
	} {
		if got := outdated(on, "pi", tc.was, tc.now); got != tc.want {
			t.Errorf("%s: restarts = %v, want %v", name, got, tc.want)
		}
	}
	if outdated(&Reconciler{}, "pi", nil, read) {
		t.Error("lock off: a folder restarts the agent")
	}
	if outdated(on, "jupyterlab", nil, read) {
		t.Error("an app that is no agent: a folder restarts it")
	}
}
