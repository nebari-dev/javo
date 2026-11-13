package workspace

import (
	"cmp"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/pixienv"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// places is where an app finds what it is planned with: the fixed paths
// of a container, or the folders of the machine it runs on as processes.
// Everything Plan writes as a path comes from here.
type places struct {
	// home is the owner's home and files the platform's files.
	home, files string
	// host is the machine, for an app that runs as processes; nil in a
	// container.
	host *workload.Host
}

var containerPlaces = places{home: homeDir, files: model.PodFilesDir}

// hostPlaces are the places of an app that runs as processes of host,
// with the owner's home in the folder home.
func hostPlaces(host *workload.Host, home string) places {
	return places{home: home, files: host.Files, host: host}
}

// placesOf are the places in is planned with. An app for a host needs its
// owner's home to be a folder of that host.
func placesOf(in Inputs) (places, error) {
	if in.Host == nil {
		return containerPlaces, nil
	}
	return placesOn(in.Host, in.Layout)
}

// placesOn are the places of a workload on host whose owner's storage is
// layout: the home must be a folder of that host.
func placesOn(host *workload.Host, layout podstorage.Layout) (places, error) {
	for _, v := range layout.Volumes {
		if v.Name == homeVolume && filepath.IsAbs(v.Source) {
			return hostPlaces(host, v.Source), nil
		}
	}
	return places{}, &executor.UnsupportedError{Why: "the owner's home is not a folder of this machine"}
}

// volume is the folder of the workload's own volume called name.
func (p places) volume(name string) string {
	return filepath.Join(p.host.Volumes, name)
}

// allAddresses is the address a program listens on to be reached from
// everywhere, and loopback the one only this machine reaches.
const (
	allAddresses = "0.0.0.0"
	loopback     = "127.0.0.1"
)

// words are the words of a template's command or args, or of a typed
// command, before their placeholders are filled in. In a container they
// stay as written. On a host a word that is, as a whole or as the whole
// value after name=, a path in the platform's files names the host's
// folder of them, and one that is the address meaning every address (with
// or without a port) names loopback: what an app serves must not be open
// to the network the machine is on. Nothing else is touched, and no value
// that fills a placeholder ever is: a word is not known to be a path
// because it looks like one.
func (p places) words(in []string) []string {
	if p.host == nil {
		return in
	}
	out := make([]string, len(in))
	for i, w := range in {
		name, value, named := strings.Cut(w, "=")
		if !named {
			name, value = "", w
		} else {
			name += "="
		}
		out[i] = name + p.value(value)
	}
	return out
}

func (p places) value(v string) string {
	switch rest, ok := strings.CutPrefix(v, model.PodFilesDir); {
	case ok && (rest == "" || strings.HasPrefix(rest, "/")):
		return p.files + rest
	}
	switch rest, ok := strings.CutPrefix(v, allAddresses); {
	case ok && (rest == "" || strings.HasPrefix(rest, ":")):
		return loopback + rest
	}
	return v
}

// linuxOnly are the bundled environments (pod/files/envs) that are locked
// for Linux only, each with why: what they need has no build for macOS.
var linuxOnly = map[string]string{
	"jobs-julia": "conda-forge has no julia for macOS",
	"pluto":      "conda-forge has no julia for macOS",
}

// RunsAsProcesses says whether apps of t can run as plain processes of a
// machine pixi calls platform (workload.Host.Platform), without
// containers: nil, or an *executor.UnsupportedError that says why not.
// Such an app runs the program of the template's pixi environment; a
// template that needs an image cannot, and neither can one whose
// environment is not locked for the platform.
func RunsAsProcesses(t *model.Template, platform string) error {
	if err := runsWithoutImage(t); err != nil {
		return err
	}
	return lockedFor(t, platform)
}

// RunsWithoutContainers says whether anything of t, an app or a job's run,
// can run as plain processes of a machine pixi calls platform: nil, or an
// *executor.UnsupportedError that says why not. It is what decides which
// templates the binary of `javo local` carries (internal/bundle/pack).
func RunsWithoutContainers(t *model.Template, platform string) error {
	if err := needsImage(t); err != nil {
		return err
	}
	return lockedFor(t, platform)
}

func unsupported(t *model.Template, format string, args ...any) error {
	return &executor.UnsupportedError{Why: fmt.Sprintf("template %q ", t.Name) + fmt.Sprintf(format, args...)}
}

// lockedFor says whether the environment t bundles installs on platform.
func lockedFor(t *model.Template, platform string) error {
	if why, only := linuxOnly[t.Spec.Environment]; only && !strings.HasPrefix(platform, "linux-") {
		return unsupported(t, "runs in the environment %s, which is locked for Linux only (%s), and this machine is %s",
			t.Spec.Environment, why, cmp.Or(platform, "not one pixi has a name for"))
	}
	return nil
}

// runsWithoutImage says whether t takes its program from a pixi
// environment and from no image.
func runsWithoutImage(t *model.Template) error {
	no := func(format string, args ...any) error { return unsupported(t, format, args...) }
	switch s := t.Spec; {
	case s.JobOnly:
		return no("only runs jobs")
	case s.Environment == "" && needsImage(t) == nil:
		return no("has no pixi environment to take its program from")
	}
	return needsImage(t)
}

// needsImage is why nothing of t, app or job, runs without containers:
// it names an image. Nil when it names none.
func needsImage(t *model.Template) error {
	no := func(format string, args ...any) error { return unsupported(t, format, args...) }
	switch s := t.Spec; {
	case s.UserImage:
		return no("runs the image each app names, and nothing here runs images")
	case s.Build != nil:
		return no("builds and serves a site in images, and nothing here runs images")
	case s.Image != "":
		return no("needs its image %s, and nothing here runs images", s.Image)
	}
	return nil
}

// hostProblem is why the app of in cannot run as processes; nil when it
// can.
func hostProblem(in Inputs) error {
	if err := runsWithoutImage(in.Template); err != nil {
		return err
	}
	// A picked environment takes the template's place, unless the template
	// serves kernels and keeps its own beside it (addEnvironment).
	if in.App.Environment == nil || in.Template.Spec.ServesKernels {
		if err := lockedFor(in.Template, in.Host.Platform); err != nil {
			return err
		}
	}
	if err := lockProblem(in); err != nil {
		return err
	}
	switch {
	case in.App.Source != nil:
		return &executor.UnsupportedError{Why: "the app runs from a repository, which is cloned by an image, and nothing here runs images"}
	case in.NebiURL != "":
		return &executor.UnsupportedError{Why: "the app gets Nebi access, whose CLI comes from the platform's image, and nothing here runs images"}
	case in.Images.Runner.Ref == "":
		return errors.New("the platform's own image is not named (the controller's --runner-image): its steps run as this binary under that name")
	}
	return nil
}

// layout is the storage of an app on a host: the home, and a temporary
// and a scratch folder of the workload's own, as Javo itself.
func (p places) layout() podstorage.Layout {
	return podstorage.Local(p.home, p.volume(podspec.TmpVolume), p.volume(podstorage.ScratchVolume),
		workload.User{UID: p.host.UID, GID: p.host.GID})
}

// sampleStep copies the template's sample into the home on a host, where
// cp differs from one system to the next: the platform's binary copies
// it, making the folders above it and keeping every file that exists.
func (p places) sampleStep(t *model.Template, runner string) workload.Unit {
	return workload.Unit{
		Name: model.SampleCopyContainer, Image: runner,
		Command: []string{"/" + model.ProductName},
		Args:    []string{"pod-files", "--keep", "--from", p.value(strings.TrimSuffix(t.Spec.Sample.Path, "/")), "--to", p.sampleHome(t)},
		Mounts:  []workload.Mount{{Volume: homeVolume, Path: p.home}},
	}
}

// environment are the places an environment is installed with.
func (p places) environment() pixienv.Places {
	if p.host == nil {
		return pixienv.InContainer
	}
	return pixienv.Places{Home: p.home, Dir: p.volume(pixienv.Volume), Files: p.files, Tmp: p.volume(podspec.TmpVolume)}
}

// The variables that tell the platform's files where they are on a host
// (pod/files/pod-user.sh); a container has them at fixed paths.
const (
	envFilesDir = "JAVO_FILES_DIR"
	envEnvDir   = "JAVO_ENV_DIR"
	// envListenAddr tells a program that picks its own address to listen
	// on loopback: the samples (pod/files/samples) and Pluto's launcher
	// read it, and listen on every address without it, as in a container.
	envListenAddr = "JAVO_LISTEN_ADDR"
	// envTmuxDir is tmux's own variable for the folder it keeps its
	// servers' sockets in (TMUX_TMPDIR/tmux-UID). Without it that is
	// /tmp/tmux-UID, which on a host is the person's own: the session of
	// a coding agent (pod/files/agents/agent-serve.sh) would be made in
	// the tmux server the person runs. A container's /tmp is its own.
	envTmuxDir = "TMUX_TMPDIR"
)

// onHost finishes a plan for a host: the main unit starts in the home
// and learns where the platform's files and its environment's activation
// are, which address to listen on and where tmux keeps its sockets, and the install step runs with the host's tools, tells the
// environment's own install script (javo-post-install.sh) where the
// platform's files are, writes its temporary files in the workload's
// folder and its message for the person beside the activation.
func (p places) onHost(s *workload.Spec) {
	if p.host == nil {
		return
	}
	env := p.environment()
	if s.Main.Dir == "" {
		// A container starts in its image's folder; here that is the home.
		s.Main.Dir = p.home
	}
	s.Main.SetEnv(workload.Env{Name: envFilesDir, Value: p.files}, workload.Env{Name: envListenAddr, Value: loopback},
		workload.Env{Name: envTmuxDir, Value: env.Tmp})
	if install := s.Step(pixienv.InstallContainer); install != nil {
		s.Main.SetEnv(workload.Env{Name: envEnvDir, Value: env.Dir})
		install.SetEnv(
			workload.Env{Name: envFilesDir, Value: p.files},
			workload.Env{Name: "PATH", Value: p.host.InstallPath},
			workload.Env{Name: "TMPDIR", Value: env.Tmp},
			workload.Env{Name: "JAVO_TERMINATION_LOG", Value: path.Join(env.Dir, "termination-log")},
		)
	}
}

// RunOnHost is a job's run planned for a machine that runs it as plain
// processes: the places an app has there, for the job planner
// (internal/controller/jobs), so the two never differ.
type RunOnHost struct{ at places }

// PlanRunOnHost is where a run of a job of t is planned on host, whose
// owner's storage is layout. picked says the job brings an environment
// of its own, which then takes the template's place. The error is an
// *executor.UnsupportedError when such a run cannot be: the home is not
// a folder of the machine, the template needs an image, or its
// environment is not locked for the machine. A template with no
// environment runs the job with the machine's own programs.
func PlanRunOnHost(host *workload.Host, layout podstorage.Layout, t *model.Template, picked bool) (RunOnHost, error) {
	at, err := placesOn(host, layout)
	if err != nil {
		return RunOnHost{}, err
	}
	if err := needsImage(t); err != nil {
		return RunOnHost{}, err
	}
	if !picked {
		if err := lockedFor(t, host.Platform); err != nil {
			return RunOnHost{}, err
		}
	}
	return RunOnHost{at: at}, nil
}

// Home is the owner's home folder and Files the folder of the platform's
// files.
func (r RunOnHost) Home() string  { return r.at.home }
func (r RunOnHost) Files() string { return r.at.files }

// Volume is the folder of the run's own volume called name.
func (r RunOnHost) Volume(name string) string { return r.at.volume(name) }

// Layout is the run's storage: the home, and a temporary and a scratch
// folder of its own.
func (r RunOnHost) Layout() podstorage.Layout { return r.at.layout() }

// Environment are the places the run's environment is installed with.
func (r RunOnHost) Environment() pixienv.Places { return r.at.environment() }

// Finish is what an app's plan gets last on a host (onHost), for the
// run's plan s.
func (r RunOnHost) Finish(s *workload.Spec) { r.at.onHost(s) }
