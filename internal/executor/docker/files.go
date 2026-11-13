package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/workload"
)

// LabelEnds marks a volume that ends with its workload (an empty folder,
// secret files): Teardown removes those and never a person's storage.
const LabelEnds = "javo.ends-with-workload"

// secretMegabytes is how much the secret files of one volume may hold.
const secretMegabytes = 1

// Own makes the root of a Docker volume belong to s's user, with mode. A
// fresh volume belongs to root, and a workload never runs as root.
//
// Nothing runs for it, as root or otherwise: the volume is mounted in a
// container that is created from image and never started, and the daemon
// unpacks one folder entry onto the mount.
func Own(ctx context.Context, c *Client, s workload.Spec, image, volume string, mode fs.FileMode) error {
	return setup(ctx, c, s, image, volume, &mode, nil)
}

// Prepare makes the volumes workload s mounts ready, before any of its
// containers is created: it creates the workload's own (an empty folder
// on the host's disk, owned by s's user; secret files in memory), and in
// each volume a unit may write, the folders its mounts name (SubPath)
// that do not exist yet, as a Pod's would be. Storage (a volume with a
// Source) must exist and be Javo's: ErrForeign otherwise. image is any
// image the daemon holds; nothing of it runs.
//
// Docker's local volumes have no size limit on most storage drivers, so
// Volume.Limit and Spec.DiskMax are not enforced here.
func Prepare(ctx context.Context, c *Client, s workload.Spec, image string) error {
	labels := Labels(s.Ref)
	labels[LabelEnds] = "true"
	for _, v := range s.Volumes {
		name, _, err := dockerVolume(s, v)
		if err != nil {
			return err
		}
		var root *fs.FileMode
		switch {
		case v.Secret != nil:
			if err := c.MemoryVolumeCreate(ctx, name, s.User.UID, s.User.GID, secretMegabytes, labels); err != nil {
				return err
			}
			continue
		case v.Source == "":
			if err := c.VolumeCreate(ctx, name, labels); err != nil {
				return err
			}
			root = new(fs.FileMode(0o700))
		default:
			have, err := c.VolumeInspect(ctx, name)
			if err != nil {
				return fmt.Errorf("volume %q: %w", v.Name, err)
			}
			if !Ours(have.Labels) {
				return foreign("volume", name)
			}
		}
		var folders []string
		for _, u := range append([]workload.Unit{s.Main}, s.Steps...) {
			for _, m := range u.Mounts {
				if m.Volume == v.Name && m.SubPath != "" && !m.ReadOnly && !v.ReadOnly {
					folders = append(folders, m.SubPath)
				}
			}
		}
		if root == nil && len(folders) == 0 {
			continue
		}
		if err := setup(ctx, c, s, image, name, root, folders); err != nil {
			return fmt.Errorf("volume %q: %w", v.Name, err)
		}
	}
	return nil
}

// setupMount is where the setup container sees the volume.
const setupMount = "v"

// setup gives the root of volume to s's user with mode root (unless nil)
// and makes the folders in it that do not exist, owned by s's user.
func setup(ctx context.Context, c *Client, s workload.Spec, image, volume string, root *fs.FileMode, folders []string) error {
	name, err := sub(s.Ref, "unit", Setup)
	if err != nil {
		return err
	}
	b, err := confined(s, Setup, "")
	if err != nil {
		return err
	}
	b.Image = image
	b.HostConfig.NetworkMode = "none"
	b.HostConfig.Memory, b.HostConfig.MemorySwap = sandboxMemory, sandboxMemory
	b.HostConfig.Mounts = []Mount{{Type: "volume", Source: volume, Target: "/" + setupMount, VolumeOptions: VolumeOptions{NoCopy: true}}}
	// One left behind by a call that was cut short is in the way.
	if err := c.ContainerRemove(ctx, name); err != nil {
		return err
	}
	id, err := c.ContainerCreate(ctx, name, b)
	if err != nil {
		return err
	}
	defer c.ContainerRemove(context.WithoutCancel(ctx), id)

	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	dir := func(name string, mode fs.FileMode) error {
		bits := int64(mode.Perm())
		if mode&fs.ModeSetgid != 0 {
			bits |= 0o2000
		}
		return w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: bits, Uid: int(s.User.UID), Gid: int(s.User.GID)})
	}
	if root != nil {
		if err := dir(setupMount, *root); err != nil {
			return err
		}
	}
	var made []string
	for _, folder := range folders {
		if !insideVolume(folder) {
			return fmt.Errorf("%q is not a folder inside the volume", folder)
		}
		// Each folder on the way, the top one first.
		parts := strings.Split(folder, "/")
		for i := range parts {
			p := path.Join(setupMount, path.Join(parts[:i+1]...))
			if slices.Contains(made, p) {
				continue
			}
			made = append(made, p)
			// One that exists keeps its owner and mode.
			exists, err := c.PathExists(ctx, id, "/"+p)
			if err != nil {
				return err
			}
			if !exists {
				if err := dir(p, 0o755); err != nil {
					return err
				}
			}
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Extract(ctx, id, "/", &buf)
}

// insideVolume reports whether subPath names a folder of a volume and
// nothing above it.
func insideVolume(subPath string) bool {
	return subPath != "" && !path.IsAbs(subPath) && path.Clean(subPath) == subPath && subPath != "." && subPath != ".." && !strings.HasPrefix(subPath, "../")
}

// SecretFiles writes values of a bundle as the files of v, a secret
// volume of workload s: files is the bundle's values by key, nil when
// there is no such bundle. Calling it again renews the files in place for
// the units that already run; a reader may for an instant find a file
// missing or short, and a key that left the bundle keeps its file.
//
// The values go to the daemon over its socket and into a volume that
// lives in memory (MemoryVolumeCreate), mounted read-only in the units:
// they are never written to the host's disk and are in no container's
// description. The workload's sandbox must run: it keeps that memory
// mounted.
func SecretFiles(ctx context.Context, c *Client, s workload.Spec, v workload.Volume, files map[string][]byte) error {
	if v.Secret == nil {
		return fmt.Errorf("volume %q does not hold secret files", v.Name)
	}
	if files == nil {
		if v.Secret.Optional {
			return nil
		}
		return fmt.Errorf("volume %q: there is no bundle %q", v.Name, v.Secret.Bundle)
	}
	keys := v.Secret.Keys
	if len(keys) == 0 {
		for k := range files {
			keys = append(keys, k)
		}
		slices.Sort(keys)
	}
	mode := int64(0o400)
	if v.Secret.WorldReadable {
		mode = 0o444
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, k := range keys {
		value, ok := files[k]
		if !ok {
			return fmt.Errorf("volume %q: bundle %q has no value %q", v.Name, v.Secret.Bundle, k)
		}
		if k == "" || k == "." || k == ".." || strings.ContainsAny(k, "/\x00") {
			return fmt.Errorf("volume %q: %q is not a file name", v.Name, k)
		}
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: k, Mode: mode, Size: int64(len(value)), Uid: int(s.User.UID), Gid: int(s.User.GID)}); err != nil {
			return err
		}
		if _, err := w.Write(value); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	sandbox, err := SandboxName(s.Ref)
	if err != nil {
		return err
	}
	// Into a sandbox that does not run, the daemon would write to memory
	// it frees again at once, and report success.
	have, err := c.ContainerInspect(ctx, sandbox)
	if err != nil {
		return err
	}
	if !Ours(have.Config.Labels) {
		return foreign("container", sandbox)
	}
	if !have.State.Running {
		return fmt.Errorf("docker: the sandbox of workload %q does not run, so it cannot hold secret files", s.Ref.Name)
	}
	return c.Extract(ctx, have.ID, secretsDir+"/"+v.Name, &buf)
}

// Teardown removes everything Start made for the workload ref: its
// containers, its network (the gateway is taken off it first) and the
// volumes that end with it. A person's storage stays. Nothing that does
// not carry Javo's labels is touched, and tearing down what does not
// exist is not an error.
func Teardown(ctx context.Context, c *Client, ref workload.Ref, gateway string) error {
	if _, err := base(ref); err != nil {
		return err
	}
	containers, err := c.ContainerList(ctx, Selector(ref)...)
	if err != nil {
		return err
	}
	// The sandbox last: the units live in its network.
	slices.SortStableFunc(containers, func(a, b ContainerSummary) int {
		return strings.Compare(sandboxLast(a), sandboxLast(b))
	})
	var errs []error
	for _, have := range containers {
		errs = append(errs, c.ContainerRemove(ctx, have.ID))
	}
	errs = append(errs, Release(ctx, c, ref, gateway))
	volumes, err := c.VolumeList(ctx, append(Selector(ref), LabelEnds+"=true")...)
	errs = append(errs, err)
	for _, v := range volumes {
		errs = append(errs, c.VolumeRemove(ctx, v.Name))
	}
	return errors.Join(errs...)
}

func sandboxLast(c ContainerSummary) string {
	if c.Labels[LabelUnit] == Sandbox {
		return "1"
	}
	return "0"
}
