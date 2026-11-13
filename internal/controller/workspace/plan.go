package workspace

import (
	"fmt"
	"slices"

	"github.com/nebari-dev/javo/internal/controller/secretenv"
	"github.com/nebari-dev/javo/internal/imageref"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/workload"
)

// Inputs is everything an app's workload is planned from. All of it is
// read before Plan runs, which itself reads nothing.
type Inputs struct {
	App      *model.App
	Template *model.Template
	// BaseURL is the path the app is served under and PublicHost the host
	// browsers load it from (empty when the deployment sets none).
	BaseURL, PublicHost string
	// Layout is the owner's storage and the ids the app runs as.
	Layout podstorage.Layout
	// Size is what the app runs at; the zero Size keeps the template's
	// resources.
	Size   sizes.Size
	Images Images
	// Fingerprint is recorded on the workload.
	Fingerprint workload.Fingerprint

	// NebiURL is Nebi's address for an app that gets Nebi access; empty
	// for one that does not.
	NebiURL string
	// APIAccess is the API access the app gets. HasToken says its bundle
	// holds a token of its owner. APIURL is the UI's public URL and
	// APIInternalURL, when set, the gateway where the app runs: the public
	// address may be one the app may not reach.
	APIAccess              string
	HasToken               bool
	APIURL, APIInternalURL string
	// GitHolder is the Name of the workload whose bundle holds the
	// repository token (the app's own, or its parent's for a preview) and
	// GitGen which paste of the token that is; empty when there is none.
	GitHolder, GitGen string
	// Secrets are the secrets the app is given.
	Secrets secretenv.Set
	// Agent is where a coding agent gets its model.
	Agent agentModel
	// EnvFiles are the files of the environment picked for the app; nil
	// when none is.
	EnvFiles *model.EnvironmentFiles
}

// EnvironmentError is why an app's environment cannot be installed: its
// files do not fit, or the platform has no image to install with.
type EnvironmentError struct{ Err error }

func (e *EnvironmentError) Error() string { return e.Err.Error() }
func (e *EnvironmentError) Unwrap() error { return e.Err }

// Plan is the workload that runs an app. The order of its steps is fixed
// here and nowhere else: later ones read what earlier ones set.
//
// A registry that is not allowed is an *imageref.NotAllowedError, an
// environment that cannot be installed an *EnvironmentError, and anything
// else is the template's to fix.
func Plan(in Inputs) (workload.Spec, error) {
	ws, t := in.App, in.Template
	s, err := planRun(in)
	if err != nil {
		return workload.Spec{}, err
	}
	if in.NebiURL != "" {
		addNebi(&s, in.NebiURL, in.Images.Runner)
	}
	if err := userImagePullSecrets(&s, t); err != nil {
		return workload.Spec{}, err
	}
	addAPIToken(&s, t, in)
	addGitToken(&s, ws, in.GitHolder, in.GitGen)
	AddEnv(&s.Main, ws.Env)
	addAppSecrets(&s, in.Secrets)
	addAgent(&s, ws, t, in.Agent, in.HasToken)
	if err := addEnvironment(&s, ws, t, in); err != nil {
		return workload.Spec{}, &EnvironmentError{Err: err}
	}
	addSSHAgent(&s, t)
	addAgentHelper(&s, t, in.Images.Runner)
	return s, nil
}

// planRun is the app's own part of its workload: what its template runs,
// from which code, on the owner's storage.
func planRun(in Inputs) (workload.Spec, error) {
	ws, t, img := in.App, in.Template, in.Images
	p, err := runOf(ws, t, in.BaseURL, in.PublicHost, img.Pixi)
	if err != nil {
		return workload.Spec{}, err
	}
	data := p.data
	base := appFolder(ws)
	// inits run first; site, the copy of a home page a site serves, runs
	// after the pod files and the sample, which may be that page.
	var inits, site []workload.Unit
	if ws.Source != nil {
		if inits, err = sourceSteps(ws, t, in.BaseURL, img); err != nil {
			return workload.Spec{}, err
		}
	} else if t.Spec.Build != nil && data.File != "" {
		if site, err = withSiteConfigStep([]workload.Unit{siteFromHomeStep(t, data.File)}, t); err != nil {
			return workload.Spec{}, err
		}
	}
	var samples []workload.Unit
	if ws.Sample {
		samples = sampleSteps(t, p.image)
	}
	if t.Spec.UsesPodFiles() && img.Runner.Ref == "" {
		return workload.Spec{}, fmt.Errorf("workspace template %q: its pods get the platform's files, and the platform has no image to copy them from (the controller's --runner-image)", t.Name)
	}
	if t.Spec.UserImage {
		// The gateway checked the image when the app was saved; the list may
		// have been tightened since.
		if err := imageref.Check(ws.Image, img.AllowedRegistries); err != nil {
			return workload.Spec{}, fmt.Errorf("spec.image: %w", err)
		}
	}
	ready, err := readyCheck(t, data)
	if err != nil {
		return workload.Spec{}, err
	}

	s := workload.Spec{
		Ref:         refOf(ws),
		Fingerprint: in.Fingerprint,
		Resources:   t.Spec.Resources,
		Size:        in.Size,
		Port:        p.port,
		Main:        MainUnit(ws.Owner, p.image),
		Steps:       inits,
	}
	app := &s.Main
	if ws.Source != nil {
		s.Pull(img.Runner.PullSecrets) // the source check
	}
	if t.Spec.UsesPodFiles() {
		podfiles.Add(&s, img.Runner)
	}
	s.Steps = append(append(s.Steps, samples...), site...)
	layout := in.Layout
	if ws.ParentID != "" {
		layout = layout.WithoutFolders()
	}
	if Shared(ws) {
		layout = layout.ReadOnlyFiles()
	}
	AddLayout(&s, layout)
	if t.Spec.UserImage {
		// The owner's uid and gid run it, as every other app: the image's
		// USER could be another person's uid or gid 0, which would open
		// their files in a shared folder. Always pull: the registry checks
		// the node's credentials for each pull, so an image cached on the
		// node for someone else is not reused without that check.
		app.Image = ws.Image
		app.PullAlways = true
		if err := userImagePullSecrets(&s, t); err != nil {
			return workload.Spec{}, err
		}
	}
	if ws.Command != "" || t.Spec.UserImage || ws.Source != nil {
		app.Env = append(app.Env, workload.Env{Name: "PORT", Value: fmt.Sprint(p.port)})
	}
	if ws.Source != nil {
		app.Env = append(app.Env, workload.Env{Name: "BASE_URL", Value: in.BaseURL})
	}
	if (ws.Command != "" && !t.Spec.UserImage) || ws.Source != nil {
		app.Dir = workDir(data.File, base)
	}
	app.Command, app.Args, app.Ready = p.command, p.args, ready
	addSourceVolumes(&s, ws, t)
	if ws.ParentID != "" || t.Spec.Build != nil {
		// A preview runs a stranger's pull request, and nginx serving a built
		// site never needs the home.
		scratchHome(&s)
	}
	return s, nil
}

// MainUnit is the unit every workload exists to run, for owner (a person's
// name) on image. Apps and job runs share it.
func MainUnit(owner, image string) workload.Unit {
	return workload.Unit{
		Name:  workload.MainUnit,
		Image: image,
		// Images name other homes (the passwd entry, a baked ENV), where
		// writes would be lost with the pod; "~" is the volume. JAVO_USER
		// is the name pod-user.sh gives a uid the image does not list.
		Env: append([]workload.Env{
			{Name: "HOME", Value: homeDir},
			{Name: "JAVO_USER", Value: naming.IdentityReadable(owner)},
		}, colorEnv()...),
	}
}

// colorEnv asks the programs in an app to print colours as they do in a
// terminal (podspec.ColorEnv); a variable of the same name the app sets
// replaces them.
func colorEnv() []workload.Env {
	var out []workload.Env
	for _, e := range podspec.ColorEnv() {
		out = append(out, workload.Env{Name: e.Name, Value: e.Value})
	}
	return out
}

// AddLayout gives s the storage l lays out: its volumes, the mounts and
// variables of the main unit, and who the workload runs as.
func AddLayout(s *workload.Spec, l podstorage.Layout) {
	s.Volumes = append(s.Volumes, l.Volumes...)
	s.Main.Mounts = append(s.Main.Mounts, l.Mounts...)
	s.Main.Env = append(s.Main.Env, l.Env...)
	s.User, s.DiskMax, s.CoLocate = l.User, l.DiskMax, l.CoLocate
}

// scratchHome gives the main unit an empty folder at the home path instead
// of the owner's home: nothing it writes lasts and nothing of the owner's
// is there. The home volume stays only while a step still mounts it (the
// copy of a single file out of the home).
func scratchHome(s *workload.Spec) {
	for i := range s.Main.Mounts {
		if m := &s.Main.Mounts[i]; m.Volume == homeVolume {
			m.Volume = podspec.HomeScratchVolume
		}
	}
	s.Volumes = append(s.Volumes, workload.Volume{Name: podspec.HomeScratchVolume})
	for _, u := range s.Steps {
		if slices.ContainsFunc(u.Mounts, func(m workload.Mount) bool { return m.Volume == homeVolume }) {
			return
		}
	}
	s.Volumes = slices.DeleteFunc(s.Volumes, func(v workload.Volume) bool { return v.Name == homeVolume })
}
