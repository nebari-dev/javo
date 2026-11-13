//go:build linux || darwin

package process

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podfiles"
	"github.com/nebari-dev/javo/internal/workload"
)

// javo is how a unit of the platform's own image names its program.
const javo = "/" + model.ProductName

// plain reports whether name can be one file or folder name.
func plain(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// folder is the workload's folder inside the state directory.
func folder(ref workload.Ref) (string, error) {
	if !plain(ref.Namespace) || !plain(ref.Name) {
		return "", &UnsupportedError{Why: fmt.Sprintf("the workload %q in %q cannot be named as a folder", ref.Name, ref.Namespace)}
	}
	return path.Join("w", ref.Namespace, ref.Name), nil
}

// withoutIdentity is s without the step that writes a passwd and group
// file for the container's user, and without the mounts of what it writes.
func withoutIdentity(s workload.Spec) workload.Spec {
	id := s.Step(podfiles.IdentityContainer)
	if id == nil {
		return s
	}
	written := func(volume string) bool {
		return slices.ContainsFunc(id.Mounts, func(m workload.Mount) bool { return m.Volume == volume })
	}
	strip := func(u workload.Unit) workload.Unit {
		u.Mounts = slices.DeleteFunc(slices.Clone(u.Mounts), func(m workload.Mount) bool { return written(m.Volume) })
		return u
	}
	var steps []workload.Unit
	for _, u := range s.Steps {
		if u.Name != podfiles.IdentityContainer {
			steps = append(steps, strip(u))
		}
	}
	s.Main, s.Steps = strip(s.Main), steps
	s.Volumes = slices.DeleteFunc(slices.Clone(s.Volumes), func(v workload.Volume) bool { return written(v.Name) })
	return s
}

// prepare checks that every part of s can run here, and makes what its
// units find when they start: the workload's folders and its secret files.
func (e *engine) prepare(ctx context.Context, s workload.Spec) error {
	wd, err := folder(s.Ref)
	if err != nil {
		return err
	}
	if err := checkUser(s.User); err != nil {
		return err
	}
	if s.Port != 0 {
		port, err := e.port(s.Ref)
		if err != nil {
			return err
		}
		if s.Port != int32(port) {
			return &UnsupportedError{Why: fmt.Sprintf("it serves on port %d; without containers its port is the one Addr names, %d", s.Port, port)}
		}
	}
	for _, sub := range []string{"logs", "pids", "work", "volumes"} {
		if err := e.root.MkdirAll(path.Join(wd, sub), 0o700); err != nil {
			return err
		}
	}
	for _, v := range s.Volumes {
		if err := e.volume(ctx, s.Ref, wd, v); err != nil {
			return err
		}
	}
	for _, u := range append(slices.Clone(s.Steps), s.Main) {
		if !plain(u.Name) {
			return &UnsupportedError{Unit: u.Name, Why: "its name cannot be a file name"}
		}
		if _, err := e.command(ctx, s, u); err != nil {
			return err
		}
	}
	return nil
}

// checkUser refuses a workload that is to run as anyone but Javo.
func checkUser(u workload.User) error {
	if (u.UID != 0 && u.UID != int64(os.Getuid())) || (u.GID != 0 && u.GID != int64(os.Getgid())) {
		return &UnsupportedError{Why: fmt.Sprintf("it runs as uid %d and gid %d; without containers everything runs as Javo itself, uid %d and gid %d",
			u.UID, u.GID, os.Getuid(), os.Getgid())}
	}
	mine, err := os.Getgroups()
	if err != nil {
		return err
	}
	for _, g := range u.Groups {
		if !slices.Contains(mine, int(g)) {
			return &UnsupportedError{Why: fmt.Sprintf("it runs in group %d, which Javo is not in", g)}
		}
	}
	return nil
}

// volume makes the folder of v, in the workload's folder wd, and fills
// the folder of a secret volume. A volume with a Source is only looked at.
func (e *engine) volume(ctx context.Context, ref workload.Ref, wd string, v workload.Volume) error {
	if !plain(v.Name) {
		return &UnsupportedError{Why: fmt.Sprintf("the volume name %q cannot be a folder name", v.Name)}
	}
	dir := path.Join(wd, "volumes", v.Name)
	switch {
	case v.Source != "":
		if !filepath.IsAbs(v.Source) || filepath.Clean(v.Source) != v.Source {
			return &UnsupportedError{Why: fmt.Sprintf("the volume %s is kept in %q, which is not a folder of this machine", v.Name, v.Source)}
		}
		if info, err := os.Stat(v.Source); err != nil || !info.IsDir() {
			return fmt.Errorf("process executor: the volume %s: %s is not a folder", v.Name, v.Source)
		}
		return nil
	case v.Secret == nil:
		return e.root.MkdirAll(dir, 0o700)
	}
	// Written anew each time: a key the bundle lost must not stay.
	if err := e.root.RemoveAll(dir); err != nil {
		return err
	}
	if err := e.root.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := e.secrets.Get(ctx, ref, v.Secret.Bundle)
	switch {
	case err != nil:
		return err
	case b == nil && v.Secret.Optional:
		return nil
	case b == nil:
		return fmt.Errorf("process executor: the volume %s: there is no bundle %s yet", v.Name, v.Secret.Bundle)
	}
	keys := v.Secret.Keys
	if len(keys) == 0 {
		keys = slices.Sorted(maps.Keys(b.Data))
	}
	for _, k := range keys {
		data, ok := b.Data[k]
		if !ok || !plain(k) {
			return fmt.Errorf("process executor: the volume %s: the bundle %s has no usable key %q", v.Name, v.Secret.Bundle, k)
		}
		if err := e.root.WriteFile(path.Join(dir, k), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// hostDir is the folder of this machine that is the workload's volume v.
func (e *engine) hostDir(ref workload.Ref, v workload.Volume) string {
	if v.Source != "" {
		return v.Source
	}
	return filepath.Join(e.dir, "w", ref.Namespace, ref.Name, "volumes", v.Name)
}

// command is the process that runs u, a unit of s, not yet started: the
// program this machine has for it, in the folder and with the variables
// the Spec names. It is where a unit that cannot run here is refused.
func (e *engine) command(ctx context.Context, s workload.Spec, u workload.Unit) (*exec.Cmd, error) {
	no := func(format string, args ...any) (*exec.Cmd, error) {
		return nil, &UnsupportedError{Unit: u.Name, Why: fmt.Sprintf(format, args...)}
	}
	argv := slices.Concat(u.Command, u.Args)
	switch {
	case len(argv) == 0:
		return no("it names no command, and there is no image to take one from")
	case u.Image == "":
	case u.Image == e.runner && argv[0] == javo:
		argv[0] = e.self
	default:
		return no("it needs the image %s, and nothing here runs images", u.Image)
	}

	wd, err := folder(s.Ref)
	if err != nil {
		return nil, err
	}
	for _, m := range u.Mounts {
		i := slices.IndexFunc(s.Volumes, func(v workload.Volume) bool { return v.Name == m.Volume })
		if i < 0 {
			return no("it mounts the volume %s, which the workload does not have", m.Volume)
		}
		if m.SubPath != "" && !filepath.IsLocal(m.SubPath) {
			return no("it mounts %q of the volume %s, which is not a folder inside it", m.SubPath, m.Volume)
		}
		v := s.Volumes[i]
		if at := filepath.Join(e.hostDir(s.Ref, v), m.SubPath); filepath.Clean(m.Path) != at {
			return no("it mounts the volume %s at %s; without containers that volume is the folder %s and cannot be shown anywhere else", m.Volume, m.Path, at)
		}
		if v.Source == "" && v.Secret == nil && m.SubPath != "" {
			if err := e.root.MkdirAll(path.Join(wd, "volumes", v.Name, m.SubPath), 0o700); err != nil {
				return nil, err
			}
		}
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = filepath.Join(e.dir, wd, "work")
	if u.Dir != "" {
		if _, ok := u.MountAt(u.Dir); !ok {
			return no("it runs in %s, which is in none of its volumes", u.Dir)
		}
		cmd.Dir = u.Dir
	}

	// Nothing of Javo's own environment but PATH: it may hold Javo's keys.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	bundles := map[string]*executor.Bundle{}
	for _, v := range u.Env {
		value := v.Value
		switch {
		case v.RunID:
			value = strings.ToLower(rand.Text())
		case v.From != nil:
			b, ok := bundles[v.From.Bundle]
			if !ok {
				if b, err = e.secrets.Get(ctx, s.Ref, v.From.Bundle); err != nil {
					return nil, err
				}
				bundles[v.From.Bundle] = b
			}
			if b == nil {
				return nil, fmt.Errorf("process executor: unit %s: variable %s: there is no bundle %s yet", u.Name, v.Name, v.From.Bundle)
			}
			data, ok := b.Data[v.From.Key]
			if !ok {
				return nil, fmt.Errorf("process executor: unit %s: variable %s: the bundle %s has no key %s", u.Name, v.Name, v.From.Bundle, v.From.Key)
			}
			value = string(data)
		}
		cmd.Env = append(cmd.Env, v.Name+"="+value)
	}
	return cmd, nil
}
