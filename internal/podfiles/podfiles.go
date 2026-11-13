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

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/workload"
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

// Add gives s's main unit the files at Dir, copied by image (the platform's
// own, which has the javo binary; s gets its pull secrets), and the
// identity files over /etc, written by the main unit's own image.
func Add(s *workload.Spec, image podspec.Image) {
	s.Pull(image.PullSecrets)
	s.Volumes = append(s.Volumes, workload.Volume{Name: volume}, workload.Volume{Name: etcVolume})
	app := &s.Main
	step := func(name, image string, m workload.Mount) workload.Unit {
		return workload.Unit{
			Name: name, Image: image, ReadOnlyRoot: true,
			Env:    []workload.Env{{Name: "HOME", Value: podspec.HomeDir}},
			Mounts: []workload.Mount{m},
		}
	}
	cp := step(CopyContainer, image.Ref, workload.Mount{Volume: volume, Path: Dir})
	cp.Command = []string{"/" + model.ProductName, "pod-files", "--to", Dir}
	id := step(IdentityContainer, app.Image, workload.Mount{Volume: etcVolume, Path: etcDir})
	id.Command = []string{"sh", "-c", identityScript, "sh", etcDir}
	id.Env = append(id.Env, workload.Env{Name: "JAVO_USER", Value: app.EnvValue("JAVO_USER")})
	s.Steps = append(s.Steps, cp, id)

	// Not read-only: the API guide is mounted at a file inside it.
	app.Mounts = append(app.Mounts, workload.Mount{Volume: volume, Path: Dir})
	for _, f := range []string{"passwd", "group", "bash.bashrc"} {
		app.Mounts = append(app.Mounts, workload.Mount{Volume: etcVolume, Path: "/etc/" + f, SubPath: f, ReadOnly: true})
	}
}

// ReadOnly shows the files at Dir, read-only, to another step (the sample
// copy, the environment install).
func ReadOnly() workload.Mount {
	return workload.Mount{Volume: volume, Path: Dir, ReadOnly: true}
}

// Copy copies the tree at from into to, keeping modes and symbolic links.
func Copy(from, to string) error { return copyTree(from, to, false) }

// CopyMissing is Copy that leaves alone every file and link to already
// has, so what a person changed in an earlier copy stays.
func CopyMissing(from, to string) error { return copyTree(from, to, true) }

func copyTree(from, to string, keep bool) error {
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
		if d.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm()|0o700)
		}
		if keep {
			if _, err := os.Lstat(dst); err == nil {
				return nil
			}
		}
		switch {
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
