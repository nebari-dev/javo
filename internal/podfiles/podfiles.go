// Package podfiles gives a pod the platform's files and an identity without
// a purpose-built image. The files (pod-user.sh, the samples, the terminal
// page, the bundled pixi environments: pod/files in the repository) travel
// in the platform's own image; an init container copies them into an empty
// folder the app container sees at Dir. A second init container, in the
// app's own image, writes a passwd and group file that list the pod's uid
// (allocated uids are not in any image's /etc/passwd, and git, ssh, getpass
// and RStudio Server need a name) and a bash.bashrc that also reads the
// platform's shell defaults; the app container sees them over the image's
// files, read-only. Nothing needs a library such as nss_wrapper, and the
// image stays as it was published.
package podfiles

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
)

const (
	// Dir is where the app container (and the init containers that need
	// them) sees the files.
	Dir = model.PodFilesDir
	// ImageDir is where the platform's image keeps the files it copies.
	ImageDir = "/opt/javo-files"

	// CopyContainer copies the files out of the platform's image.
	CopyContainer = "javo-files"
	// IdentityContainer writes the passwd, group and bash.bashrc files.
	IdentityContainer = "pod-user"

	volume    = "javo-files"
	etcVolume = "javo-etc"
	etcDir    = "/opt/javo-etc"
)

// identityScript writes into $1 the image's passwd with an entry for the
// pod's uid (any entry the image has for that uid is replaced, so the home is
// the mounted one), its group with the pod's gid, and its bash.bashrc
// followed by a line that reads the platform's shell defaults. The entry is
// named after JAVO_USER (the owner, set by the controller) when that is a
// valid name no other account has, else "user". Plain sh and grep, which
// every image with a shell has.
const identityScript = `set -eu
out=$1
uid=$(id -u)
gid=$(id -g)
name=user
case "${JAVO_USER:-}" in
  "" | *[!a-z0-9_-]* | [!a-z_]*) ;;
  *) grep -q "^$JAVO_USER:" /etc/passwd || name=$JAVO_USER ;;
esac
while IFS= read -r line; do
  rest=${line#*:*:}
  [ "${rest%%:*}" = "$uid" ] || printf '%s\n' "$line"
done < /etc/passwd > "$out/passwd"
echo "$name:x:$uid:$gid:$name:${HOME:-/home/user}:/bin/bash" >> "$out/passwd"
cp /etc/group "$out/group"
grep -q "^[^:]*:[^:]*:$gid:" /etc/group || echo "$name:x:$gid:" >> "$out/group"
{ cat /etc/bash.bashrc 2>/dev/null || true; echo '[ -r ` + Dir + `/javo.bashrc ] && . ` + Dir + `/javo.bashrc'; } > "$out/bash.bashrc"`

// Apply gives spec's first container the files at Dir, copied by image (the
// platform's own, which has the javo binary; spec gets its pull secrets),
// and the identity files over /etc, written by the first container's own
// image.
func Apply(spec *corev1.PodSpec, image podspec.Image) {
	image.Pull(spec)
	empty := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	spec.Volumes = append(spec.Volumes,
		corev1.Volume{Name: volume, VolumeSource: empty},
		corev1.Volume{Name: etcVolume, VolumeSource: empty})
	app := &spec.Containers[0]
	step := func(name, image string, mount corev1.VolumeMount) corev1.Container {
		return corev1.Container{
			Name: name, Image: image, SecurityContext: podspec.ReadOnlyRootContext(),
			Env:                      []corev1.EnvVar{{Name: "HOME", Value: podspec.HomeDir}},
			VolumeMounts:             []corev1.VolumeMount{mount},
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		}
	}
	cp := step(CopyContainer, image.Ref, corev1.VolumeMount{Name: volume, MountPath: Dir})
	cp.Command = []string{"/" + model.ProductName, "pod-files", "--to", Dir}
	id := step(IdentityContainer, app.Image, corev1.VolumeMount{Name: etcVolume, MountPath: etcDir})
	id.Command = []string{"sh", "-c", identityScript, "sh", etcDir}
	id.Env = append(id.Env, corev1.EnvVar{Name: "JAVO_USER", Value: envValue(app, "JAVO_USER")})
	spec.InitContainers = append(spec.InitContainers, cp, id)

	// Not read-only: the API guide is mounted at a file inside it.
	app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: volume, MountPath: Dir})
	for _, f := range []string{"passwd", "group", "bash.bashrc"} {
		app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: etcVolume, MountPath: "/etc/" + f, SubPath: f, ReadOnly: true})
	}
}

// Mount shows the files at Dir, read-only, to another init container (the
// sample copy, the environment install).
func Mount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: volume, MountPath: Dir, ReadOnly: true}
}

func envValue(c *corev1.Container, name string) string {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// Copy copies the tree at from into to, keeping modes and symbolic links.
func Copy(from, to string) error {
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, dst)
		default:
			return copyFile(p, dst, info.Mode().Perm())
		}
	})
}

func copyFile(from, to string, mode fs.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(to, mode)
}
