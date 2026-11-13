// Package pixienv runs an app or a job in a pixi environment. An init
// container installs exactly a pixi.lock (pixi install --frozen) into the
// owner's home, under ~/.javo/envs/<sha256 of the environment's files>, with
// pixi's package cache beside it, so the first start installs and every
// later start, of any app or job with the same files, reuses it. A folder
// counts as installed only once its marker is written, last; one left by a
// crash is cleared and installed again. The step then points the app
// container at the environment's activation (ActivateFile in Dir), which
// pod-user.sh (pod/files) sources.
//
// Which environment: a picked one (Nebi: model.Environment, its files in the
// pod spec), else the pixi.toml and pixi.lock at the root of the app's
// repository, else the template's own (model.TemplateSpec.Environment, a
// folder of the pod files). One step, one script, for all three.
package pixienv

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// InstallContainer is the init container that installs the environment.
	InstallContainer = "env-install"
	// ToolsContainer copies the platform's binary into Dir for the install
	// step, which runs it to clear executable-stack requests.
	ToolsContainer = "env-tools"
	// Dir is the folder, in both containers, holding ActivateFile.
	Dir = "/opt/javo-env"
	// ActivateFile, in Dir, sources the installed environment's activation.
	ActivateFile = "activate.sh"
	// HomeFolder is the folder of the home that holds the environments
	// (envs/<sha256>) and pixi's package cache (pixi-cache).
	HomeFolder = ".javo"
	// Volume is the volume shown at Dir.
	Volume = "javo-env"

	// MaxEncoded is the most the compressed files may take in the pod spec.
	// They travel in the init container's environment, so the pod (and a
	// job's CronJob) stays far below the API server's object size limit.
	MaxEncoded = 512 << 10
)

// Source is where the environment comes from, first match wins: Files (a
// picked environment), else RepoDir's pixi.toml and pixi.lock when it has
// both, else Default, the name of an environment of the pod files. With
// neither Files nor Default there is nothing to install: a repository's
// lockfile only replaces a template's environment.
type Source struct {
	Files   *model.EnvironmentFiles
	RepoDir string
	Default string
	// Name and From name the picked environment and the manager it came
	// from ("Nebi") in what the step tells the person.
	Name, From string
	// Beside installs Files next to the environment the app runs in (the
	// repository's or Default) instead of in its place, and offers its
	// Jupyter kernels to the app (KernelsDir) and its activation to the
	// app's terminals (EnvsDir/<name>): for templates whose program serves
	// kernels, such as JupyterLab.
	Beside bool
	// Program is the program the app starts. An environment that replaces
	// the template's (a picked one, or a repository's) must have it on its
	// PATH, or the step fails with ExitProgramMissing and says so in its
	// termination message.
	Program string
}

const (
	// KernelsDir, in Dir, is the Jupyter data folder holding the kernels of
	// an environment installed beside (Source.Beside); the activation adds
	// it to JUPYTER_PATH.
	KernelsDir = "jupyter"
	// EnvsDir, in Dir, holds one file per environment installed beside,
	// named after it, that sources its activation (pod/files/bin/javo-env).
	EnvsDir = "envs"
	// ExitProgramMissing is the install step's exit code when the
	// environment lacks Source.Program.
	ExitProgramMissing = 78
)

// installLog is pixi's log filter for the install: without a terminal pixi
// shows no progress, so this asks for a line per conda package downloaded
// (rattler_cache) and per PyPI wheel fetched and installed (uv), which the
// app's start-up log shows live. The lines name package URLs and cache
// paths, never credentials.
const installLog = "info,rattler_cache=debug,uv_installer=debug,uv_distribution=debug"

// script gathers the environment's files in a folder of /tmp, then installs
// them, unless installed already, under JAVO_ENV_ROOT/<hash of the files>:
// one install at a time per folder (flock), the marker .ready last. A
// bundled environment may carry javo-post-install.sh, run in the activated
// environment before the marker (packages from outside conda-forge, from
// lockfiles of their own). Shared libraries that ask for an executable stack
// lose that request first (JAVO_TOOL clear-execstack: internal/execstack),
// since glibc 2.41 and later refuse to load them. The activation is pixi's
// own shell hook with the environment put in front of the pod's PATH, not
// the install step's. A picked environment installed beside (JAVO_ENV_BESIDE)
// goes through the same install; JAVO_TOOL env-kernels then writes its
// kernels, and a missing kernel is a note in the termination message, not a
// failure. What the person must read goes to the termination message.
var script = `set -eu
log=${JAVO_TERMINATION_LOG:-/dev/termination-log}
# install SRC WHAT: install the files in folder SRC unless they are already;
# sets dir to their folder.
install() {
  key=$(cd "$1" && find . -type f -exec sha256sum {} + | LC_ALL=C sort -k 2 | sha256sum | cut -c1-64)
  dir=$JAVO_ENV_ROOT/$key
  mkdir -p "$JAVO_ENV_ROOT"
  if [ -e "$dir/.ready" ]; then
    echo "Using $2, installed before in $dir"
    return
  fi
  echo "Installing $2 into $dir (once for these files)"
  flock "$dir.lock" sh -c '
    set -eu
    dir=$1
    [ ! -e "$dir/.ready" ] || exit 0
    rm -rf "$dir"
    mkdir -p "$dir"
    cp -R "$2/." "$dir/"
    cd "$dir"
    RUST_LOG=` + installLog + ` pixi install --frozen
    prefix=$dir/.pixi/envs/default
    "$JAVO_TOOL" clear-execstack "$prefix"
    pixi shell-hook --as-is --shell bash --change-ps1 false --no-completions |
      sed "s|^export PATH=\"$prefix/bin:.*\"\$|export PATH=\"$prefix/bin:\$PATH\"|" > activate.sh
    if [ -f javo-post-install.sh ]; then
      bash -c ". ./activate.sh && exec bash ./javo-post-install.sh"
    fi
    touch .ready' sh "$dir" "$1"
}
picked=
if [ -n "${JAVO_PIXI_LOCK:-}" ]; then
  picked=$(mktemp -d)
  printf '%s' "$JAVO_PIXI_TOML" | base64 -d | gunzip > "$picked/pixi.toml"
  printf '%s' "$JAVO_PIXI_LOCK" | base64 -d | gunzip > "$picked/pixi.lock"
fi
src=$(mktemp -d)
missing=
if [ -n "$picked" ] && [ -z "${JAVO_ENV_BESIDE:-}" ]; then
  src=$picked
  what="the picked environment"
  missing="The environment ${JAVO_ENV_NAME:-} has no ${JAVO_ENV_PROGRAM:-}. Add ${JAVO_ENV_PROGRAM:-} to it in ${JAVO_ENV_FROM:-its environment manager}, or pick another environment."
elif [ -n "${JAVO_ENV_REPO:-}" ] && [ -f "$JAVO_ENV_REPO/pixi.toml" ] && [ -f "$JAVO_ENV_REPO/pixi.lock" ]; then
  cp "$JAVO_ENV_REPO/pixi.toml" "$JAVO_ENV_REPO/pixi.lock" "$src/"
  what="the repository's pixi.lock"
  missing="The repository's pixi.lock has no ${JAVO_ENV_PROGRAM:-}. Add ${JAVO_ENV_PROGRAM:-} to it, or remove pixi.toml and pixi.lock from the repository."
else
  cp -R "$JAVO_ENV_DEFAULT/." "$src/"
  what="the template's environment"
fi
install "$src" "$what"
if [ -n "${JAVO_ENV_PROGRAM:-}" ] && [ -n "$missing" ] &&
  ! (. "$dir/activate.sh" && command -v "$JAVO_ENV_PROGRAM" >/dev/null); then
  printf '%s' "$missing" > "$log"
  echo "$missing" >&2
  exit ` + strconv.Itoa(ExitProgramMissing) + `
fi
printf '. %s/activate.sh\n' "$dir" > "$JAVO_ENV_DIR/` + ActivateFile + `"
if [ -n "$picked" ] && [ -n "${JAVO_ENV_BESIDE:-}" ]; then
  install "$picked" "the environment $JAVO_ENV_NAME"
  mkdir -p "$JAVO_ENV_DIR/` + EnvsDir + `"
  printf '. %s/activate.sh\n' "$dir" > "$JAVO_ENV_DIR/` + EnvsDir + `/$JAVO_ENV_SLUG"
  "$JAVO_TOOL" env-kernels --prefix "$dir/.pixi/envs/default" --activate "$dir/activate.sh" --name "$JAVO_ENV_NAME" --to "$JAVO_ENV_DIR/` + KernelsDir + `" --notice "$log"
  printf 'export JUPYTER_PATH="%s${JUPYTER_PATH:+:$JUPYTER_PATH}"\n' "$JAVO_ENV_DIR/` + KernelsDir + `" >> "$JAVO_ENV_DIR/` + ActivateFile + `"
fi`

// Add makes s's main unit run in the environment src names, installed by a
// step running image (one with pixi, sh, flock, sha256sum, base64 and
// gunzip), after one running toolsImage (the platform's, with its binary at
// /<product>) copies that binary into Dir. The step sees the home exactly as
// the main unit does (a preview's scratch home stays scratch), the pod files
// when it installs one of theirs, and the repository when it may hold the
// lockfile. Files too large to carry, or no image, are an error.
func Add(s *workload.Spec, image string, toolsImage podspec.Image, src Source) error {
	if (src.Files != nil || src.Default != "") && image == "" {
		return errors.New("environment: the platform has no image to install environments with (environments.pixiImage)")
	}
	return AddAt(s, image, toolsImage, src, InContainer)
}

// Places is where an install finds things: Home is the home folder, Dir
// the folder holding ActivateFile, Files the platform's files and Tmp the
// temporary folder.
type Places struct{ Home, Dir, Files, Tmp string }

// InContainer are the fixed paths of a container.
var InContainer = Places{Home: podspec.HomeDir, Dir: Dir, Files: podfiles.Dir, Tmp: "/tmp"}

// AddAt is Add at the places at. With no image the install step runs on
// the machine itself, which then needs what the pixi image brings.
func AddAt(s *workload.Spec, image string, toolsImage podspec.Image, src Source, at Places) error {
	if src.Files == nil && src.Default == "" {
		return nil
	}
	if toolsImage.Ref == "" {
		return errors.New("environment: the platform's own image is not set (the controller's --runner-image)")
	}
	app := &s.Main
	home, ok := app.MountAt(at.Home)
	if !ok {
		return errors.New("environment: the pod has no home folder to install into")
	}
	home.ReadOnly = false
	root := path.Join(at.Home, HomeFolder)
	tool := path.Join(at.Dir, model.ProductName)
	mount := workload.Mount{Volume: Volume, Path: at.Dir}
	env := []workload.Env{
		{Name: "HOME", Value: at.Home},
		{Name: "PIXI_CACHE_DIR", Value: root + "/pixi-cache"},
		{Name: "JAVO_ENV_ROOT", Value: root + "/envs"},
		{Name: "JAVO_ENV_DIR", Value: at.Dir},
		{Name: "JAVO_TOOL", Value: tool},
	}
	mounts := []workload.Mount{mount, home}
	for _, v := range []struct{ name, value string }{
		{"JAVO_ENV_NAME", src.Name}, {"JAVO_ENV_FROM", src.From}, {"JAVO_ENV_PROGRAM", src.Program},
	} {
		if v.value != "" {
			env = append(env, workload.Env{Name: v.name, Value: v.value})
		}
	}
	beside := src.Beside && src.Files != nil && src.Default != ""
	if beside {
		env = append(env, workload.Env{Name: "JAVO_ENV_BESIDE", Value: "1"}, workload.Env{Name: "JAVO_ENV_SLUG", Value: Slug(src.Name)})
	}
	if src.Files != nil {
		toml, lock, err := check(src.Files)
		if err != nil {
			return err
		}
		env = append(env, workload.Env{Name: "JAVO_PIXI_TOML", Value: toml}, workload.Env{Name: "JAVO_PIXI_LOCK", Value: lock})
	}
	if src.Files == nil || beside {
		env = append(env, workload.Env{Name: "JAVO_ENV_DEFAULT", Value: path.Join(at.Files, "envs", src.Default)})
		if at.Files == podfiles.Dir {
			// A container sees them through a volume.
			mounts = append(mounts, podfiles.ReadOnly())
		}
		if src.RepoDir != "" {
			if m, ok := app.MountAt(src.RepoDir); ok {
				m.ReadOnly = true
				mounts = append(mounts, m)
				env = append(env, workload.Env{Name: "JAVO_ENV_REPO", Value: src.RepoDir})
			}
		}
	}
	for _, v := range s.Volumes {
		if v.Name == podspec.TmpVolume {
			mounts = append(mounts, workload.Mount{Volume: podspec.TmpVolume, Path: at.Tmp})
		}
	}
	s.Volumes = append(s.Volumes, workload.Volume{Name: Volume})
	s.Steps = append(s.Steps, workload.Unit{
		Name: ToolsContainer, Image: toolsImage.Ref, ReadOnlyRoot: true,
		Command: []string{"/" + model.ProductName},
		Args:    []string{"runner", "install", "--to", tool},
		Mounts:  []workload.Mount{mount},
	}, workload.Unit{
		Name: InstallContainer, Image: image,
		Command: []string{"sh", "-c", script}, Env: env, Mounts: mounts,
	})
	s.Pull(toolsImage.PullSecrets)
	mount.ReadOnly = true
	app.Mounts = append(app.Mounts, mount)
	return nil
}

// Slug is name as a file name of EnvsDir: letters, digits, dot, dash and
// underscore, anything else a dash; "environment" when nothing is left.
func Slug(name string) string {
	out := strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_') {
			return r
		}
		return '-'
	}, name)
	if strings.Trim(out, ".-") == "" {
		return "environment"
	}
	return out
}

// Fits is nil when files are small enough to hand to a pod.
func Fits(files model.EnvironmentFiles) error {
	_, _, err := check(&files)
	return err
}

func check(files *model.EnvironmentFiles) (toml, lock string, err error) {
	toml, lock = encode(files.Manifest), encode(files.Lock)
	if n := len(toml) + len(lock); n > MaxEncoded {
		return "", "", fmt.Errorf("its files take %d KiB compressed and at most %d KiB fit in a pod", n>>10, MaxEncoded>>10)
	}
	return toml, lock, nil
}

// encode is s gzipped, in standard base64.
func encode(s string) string {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
