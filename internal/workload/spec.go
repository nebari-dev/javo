package workload

import (
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/sizes"
)

// MainUnit is the name of the unit a workload exists to run.
const MainUnit = "app"

// Spec is one workload to run: what runs, as whom, with which files. It
// says nothing about how a platform runs it. No field can ask for more
// than a platform gives every workload: there is none for privileges,
// capabilities, the host's network or a path of the host.
type Spec struct {
	Ref Ref
	// Fingerprint is recorded on the workload, to tell later whether it
	// must be built again.
	Fingerprint Fingerprint
	User        User

	// Main is what the workload exists to run.
	Main Unit
	// Steps run before Main, in order. One that Stays keeps running beside
	// Main; the others run to completion.
	Steps []Unit

	// Volumes are the files the units mount, by name.
	Volumes []Volume
	// Port is the port Main serves on; 0 when it serves nothing.
	Port int32

	// Resources are the template's own, and Size the size picked on top of
	// them (the zero Size keeps Resources). DiskMax limits what Main writes
	// outside its volumes; zero is no limit.
	Resources corev1.ResourceRequirements
	Size      sizes.Size
	DiskMax   resource.Quantity
	// CoLocate keeps all of the owner's workloads on one machine, for
	// storage that only one machine can mount.
	CoLocate bool
	// PullSecrets name the registry credentials the platform's own images
	// are pulled with. They are never offered for an image a person named.
	PullSecrets []string

	// Batch makes the workload a run: Main runs to completion once. Nil is
	// an app, which is kept running.
	Batch *Batch
}

// Batch is how a run ends.
type Batch struct {
	// Retries is how many times a failed run is started again.
	Retries int32
	// Timeout ends the run after that long; zero is no limit.
	Timeout time.Duration
	// FailNow are the exit codes of Main that a retry cannot fix.
	FailNow []int32
	// Slot is the time a schedule starts the run for; zero for a run a
	// person started.
	Slot time.Time
}

// User is who a workload runs as.
type User struct {
	UID, GID int64
	// Groups are the extra groups, one per group folder.
	Groups []int64
}

// Unit is one program of a workload.
type Unit struct {
	Name  string
	Image string
	// PullAlways fetches the image every time, so a registry checks the
	// credentials for it each time.
	PullAlways bool
	// Command and Args replace the image's entrypoint and arguments.
	Command, Args []string
	// Dir is where it runs; empty is the image's own folder.
	Dir    string
	Env    []Env
	Mounts []Mount
	// ReadOnlyRoot is for a unit that runs only the platform's own code
	// and writes nothing but its volumes.
	ReadOnlyRoot bool
	// Stays is for a step that keeps running beside Main, which starts
	// once the step's Ready check passes.
	Stays bool
	// Ready says when the unit can be used; nil when running is enough.
	Ready *Check
}

// Check is how to tell that a unit can be used. One of its three kinds is
// set.
type Check struct {
	// HTTPPath is fetched from the workload's port.
	HTTPPath string
	// TCP connects to the workload's port.
	TCP bool
	// Every is how often the port is checked, for a workload something
	// waits on; zero is the platform's own pace.
	Every time.Duration
	// Exec is run in the unit, once a second, up to Tries times.
	Exec  []string
	Tries int32
}

// Env is one variable of a unit: a plain Value, a value read from a bundle
// (From), which is then never part of the Spec, or the id the executor
// gives this run (RunID).
type Env struct {
	Name  string
	Value string
	From  *SecretRef
	RunID bool
}

// SecretRef names one value of a bundle (executor.Secrets).
type SecretRef struct {
	// Bundle is the bundle's name: the app's own, or its parent's for a
	// preview's repository token.
	Bundle string
	Key    string
}

// Mount shows a volume, or the folder SubPath of it, at Path in a unit.
type Mount struct {
	Volume, Path, SubPath string
	ReadOnly              bool
}

// Volume is files units can mount. Of its three kinds, storage has a
// Source, values of a bundle have Secret, and the rest is an empty folder
// that lasts as long as the workload.
type Volume struct {
	Name string
	// Source is what the storage provider calls a person's home or a group
	// folder. ReadOnly makes it read-only for every unit.
	Source   string
	ReadOnly bool
	// Limit is how much an empty folder may hold; nil is no limit.
	Limit *resource.Quantity
	// Secret makes the volume files holding values of a bundle.
	Secret *SecretFiles
}

// SecretFiles are values of one bundle as files, each named after its key.
type SecretFiles struct {
	Bundle string
	Keys   []string
	// WorldReadable lets a unit that does not run as the files' owner read
	// them.
	WorldReadable bool
	// Optional starts the workload without the files while the bundle does
	// not exist yet.
	Optional bool
}

// SetEnv adds vars to u, each replacing an earlier variable of the same
// name.
func (u *Unit) SetEnv(vars ...Env) {
	for _, v := range vars {
		u.Env = slices.DeleteFunc(u.Env, func(e Env) bool { return e.Name == v.Name })
		u.Env = append(u.Env, v)
	}
}

// EnvValue is the plain value of u's variable name, or "".
func (u *Unit) EnvValue(name string) string {
	for _, e := range u.Env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// MountAt is u's mount that shows the path p: the one at p or the deepest
// above it.
func (u *Unit) MountAt(p string) (Mount, bool) {
	var best Mount
	found := false
	for _, m := range u.Mounts {
		if (p == m.Path || strings.HasPrefix(p, strings.TrimSuffix(m.Path, "/")+"/")) && len(m.Path) > len(best.Path) {
			best, found = m, true
		}
	}
	return best, found
}

// Pull records that the workload runs one of the platform's own images,
// pulled with the credentials names, each once.
func (s *Spec) Pull(names []string) {
	for _, n := range names {
		if !slices.Contains(s.PullSecrets, n) {
			s.PullSecrets = append(s.PullSecrets, n)
		}
	}
}

// Step is the step called name, or nil.
func (s *Spec) Step(name string) *Unit {
	for i := range s.Steps {
		if s.Steps[i].Name == name {
			return &s.Steps[i]
		}
	}
	return nil
}

// Host is the machine a workload runs on as plain processes, when nothing
// puts it in a container: its folders take the place of the fixed paths a
// container has. A workload is planned with it, since a process sees the
// machine's own paths and none can be shown to it under another name.
type Host struct {
	// Volumes holds the workload's own volumes, each a folder named after
	// the volume.
	Volumes string
	// Files holds the platform's files (pod/files, the terminal page and
	// the tools), laid out as the platform's image holds them.
	Files string
	// InstallPath is the PATH of the step that installs an environment:
	// the folder with pixi, sha256sum and flock, then the machine's own.
	InstallPath string
	// UID and GID are who everything on the machine runs as: Javo itself.
	UID, GID int64
}
