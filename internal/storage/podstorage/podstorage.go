// Package podstorage is the one place that turns a user's Home, their group
// folders and the scratch settings into the volumes, mounts, environment and
// ids of what runs for them. Apps, job runs and file pods all use its
// Layout, so they always agree on what is mounted where.
package podstorage

import (
	"path"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/naming"
	"github.com/nebari-dev/javo/internal/workload"
)

const (
	// HomeDir is where the user's home is mounted.
	HomeDir = "/home/user"
	// SharedRoot holds one folder per group the user belongs to.
	SharedRoot = "/shared"
	// ScratchDir is node-local space for environments and caches.
	ScratchDir = "/scratch"

	// HomeVolume, TmpVolume and ScratchVolume are the volume names.
	HomeVolume    = "home"
	TmpVolume     = "tmp"
	ScratchVolume = "scratch"

	tmpDir = "/tmp"
	// FolderSubPath is the folder's own directory inside its claim; the
	// prepare Job makes it and owns the claim root.
	FolderSubPath = "data"
	cacheSubPath  = "cache"
)

// Input is everything a Layout depends on.
type Input struct {
	// Home is the owner's Home; nil or without a claim name falls back to the
	// first-generation claim name.
	Home *model.Home
	// Folders are the ready folders of the owner's groups.
	Folders []model.SharedFolder
	// ScratchMax limits the scratch volume, and /tmp when TmpMax is zero;
	// zero means no limit.
	ScratchMax resource.Quantity
	// TmpMax limits /tmp; zero means ScratchMax's limit.
	TmpMax resource.Quantity
	// DiskMax is the app container's ephemeral-storage limit: all the pod
	// may write to the node's disk (scratch, /tmp, its image layer, logs).
	// Zero means no limit.
	DiskMax resource.Quantity
	// CoLocate keeps all of the owner's pods on one node (Backend.CoLocate).
	CoLocate bool
	// UID and GID are the ids the pod runs as.
	UID, GID int64
	// ReadOnly mounts the home and folders read-only (file pods).
	ReadOnly bool
}

// Layout is the storage part of a workload: its volumes, where its main
// unit mounts them, the variables that point tools at scratch, and who it
// runs as.
type Layout struct {
	Volumes []workload.Volume
	Mounts  []workload.Mount
	Env     []workload.Env
	// User is the ids it runs as; its Groups are the gids of the group
	// folders. Nothing built from a Layout sets a group on the files
	// themselves: on some storage that would re-group every file of every
	// folder mounted.
	User workload.User
	// DiskMax is Input.DiskMax and CoLocate Input.CoLocate.
	DiskMax  resource.Quantity
	CoLocate bool
}

// Build returns the layout of owner's workload.
func Build(owner string, in Input) Layout {
	claim := naming.HomeClaimName(owner)
	if in.Home != nil && in.Home.Status.ClaimName != "" {
		claim = in.Home.Status.ClaimName
	}
	// /tmp is the machine's disk like scratch: going over its limit ends
	// this workload, not the machine's others. Without a /tmp limit of its
	// own it gets the scratch limit.
	var scratch, tmp *resource.Quantity
	if !in.ScratchMax.IsZero() {
		scratch, tmp = ptr.To(in.ScratchMax), ptr.To(in.ScratchMax)
	}
	if !in.TmpMax.IsZero() {
		tmp = ptr.To(in.TmpMax)
	}
	l := Layout{
		DiskMax:  in.DiskMax,
		CoLocate: in.CoLocate,
		Volumes: []workload.Volume{
			{Name: HomeVolume, Source: claim, ReadOnly: in.ReadOnly},
			{Name: TmpVolume, Limit: tmp},
			{Name: ScratchVolume, Limit: scratch},
		},
		Mounts: []workload.Mount{
			{Volume: HomeVolume, Path: HomeDir, ReadOnly: in.ReadOnly},
			{Volume: TmpVolume, Path: tmpDir},
			{Volume: ScratchVolume, Path: ScratchDir},
		},
		Env:  scratchEnv(ScratchDir),
		User: workload.User{UID: in.UID, GID: in.GID},
	}
	if !in.ReadOnly {
		// Caches the tools write under ~/.cache go to scratch, not the home.
		l.Mounts = append(l.Mounts, workload.Mount{Volume: ScratchVolume, Path: path.Join(HomeDir, ".cache"), SubPath: cacheSubPath})
	}
	for _, f := range in.Folders {
		name := GroupVolume(naming.GroupDir(f.Spec.Group))
		l.Volumes = append(l.Volumes, workload.Volume{Name: name, Source: f.Status.ClaimName, ReadOnly: in.ReadOnly})
		l.Mounts = append(l.Mounts, workload.Mount{
			Volume: name, Path: path.Join(SharedRoot, naming.GroupDir(f.Spec.Group)), SubPath: FolderSubPath, ReadOnly: in.ReadOnly,
		})
		l.User.Groups = append(l.User.Groups, f.Status.GID)
	}
	return l
}

// Local is the layout of a workload that runs as plain processes of this
// machine, as user: home is the owner's home folder, tmp and scratch the
// folders of the workload's own temporary and scratch volumes. Each volume
// is mounted at its own folder, since nothing can show a folder to a
// process at another path: there is no cache folder inside the home (the
// variables below point the tools at scratch) and no group folder.
// Programs write their temporary files in tmp (TMPDIR), not in the
// machine's.
func Local(home, tmp, scratch string, user workload.User) Layout {
	return Layout{
		Volumes: []workload.Volume{{Name: HomeVolume, Source: home}, {Name: TmpVolume}, {Name: ScratchVolume}},
		Mounts: []workload.Mount{
			{Volume: HomeVolume, Path: home},
			{Volume: TmpVolume, Path: tmp},
			{Volume: ScratchVolume, Path: scratch},
		},
		Env:  append(scratchEnv(scratch), workload.Env{Name: "TMPDIR", Value: tmp}),
		User: user,
	}
}

// AddTo gives s the storage l lays out: its volumes, the mounts and
// variables of the main unit, and who the workload runs as.
func (l Layout) AddTo(s *workload.Spec) {
	s.Volumes = append(s.Volumes, l.Volumes...)
	s.Main.Mounts = append(s.Main.Mounts, l.Mounts...)
	s.Main.Env = append(s.Main.Env, l.Env...)
	s.User, s.DiskMax, s.CoLocate = l.User, l.DiskMax, l.CoLocate
}

// GroupVolume is the name of the volume of the group folder at /shared/<dir>
// (dir is naming.GroupDir of the group).
func GroupVolume(dir string) string { return groupVolumePrefix + dir }

const groupVolumePrefix = "group-"

// IsGroupVolume reports whether name is a group folder's volume.
func IsGroupVolume(name string) bool { return strings.HasPrefix(name, groupVolumePrefix) }

// WithoutFolders is l without the group folders and their gids, for a pod
// that runs code its owner has not reviewed (a pull request's preview).
func (l Layout) WithoutFolders() Layout {
	folder := IsGroupVolume
	l.Volumes = slices.DeleteFunc(slices.Clone(l.Volumes), func(v workload.Volume) bool { return folder(v.Name) })
	l.Mounts = slices.DeleteFunc(slices.Clone(l.Mounts), func(m workload.Mount) bool { return folder(m.Volume) })
	l.User.Groups = nil
	return l
}

// ReadOnlyFiles is l with the home and the group folders mounted read-only
// in the app container, for a pod that people other than its owner drive (a
// shared app): what they make it do cannot change the code the owner's other
// apps and jobs run. The volumes stay writable, so the platform's own init
// steps (an environment install) still write; caches and /tmp stay
// writable.
func (l Layout) ReadOnlyFiles() Layout {
	l.Mounts = slices.Clone(l.Mounts)
	for i, m := range l.Mounts {
		if (m.Volume == HomeVolume && m.Path == HomeDir) || strings.HasPrefix(m.Volume, groupVolumePrefix) {
			l.Mounts[i].ReadOnly = true
		}
	}
	return l
}

// scratchEnv points every tool cache and the conda and pixi environments at
// scratch, the folder dir, so a large install does not fill the home.
func scratchEnv(dir string) []workload.Env {
	c := dir + "/" + cacheSubPath
	return []workload.Env{
		{Name: "XDG_CACHE_HOME", Value: c},
		{Name: "PIP_CACHE_DIR", Value: c + "/pip"},
		{Name: "UV_CACHE_DIR", Value: c + "/uv"},
		{Name: "PIXI_CACHE_DIR", Value: c + "/pixi"},
		{Name: "npm_config_cache", Value: c + "/npm"},
		{Name: "CONDA_PKGS_DIRS", Value: dir + "/pkgs"},
		{Name: "CONDA_ENVS_PATH", Value: dir + "/envs"},
	}
}

// ScratchPaths are the places an app's files are not saved: the cache folder
// in the home and the scratch volume (environments live there).
func ScratchPaths() []string { return []string{"~/.cache", ScratchDir} }
