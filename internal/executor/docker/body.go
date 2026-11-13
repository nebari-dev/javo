package docker

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/nebari-dev/javo/internal/workload"
)

// Body is what a container is created from. It has a field for what Javo
// sets and for nothing else: there is none for privileges, added
// capabilities, a namespace of the host, a device, a published port, a
// bind mount or a sysctl, so no caller can send one.
//
// Its Env holds secret values in clear text. They are visible through
// "docker inspect" to whoever holds the Docker socket, who is root on the
// host anyway. Never log a Body.
type Body struct {
	Image      string
	User       string
	Entrypoint []string `json:",omitempty"`
	Cmd        []string `json:",omitempty"`
	WorkingDir string   `json:",omitempty"`
	Env        []string
	Labels     map[string]string
	// Healthcheck turns off the check an image may declare: the supervisor
	// decides when a unit is ready.
	Healthcheck struct{ Test []string }
	HostConfig  HostConfig
}

// HostConfig is how the daemon confines a container.
type HostConfig struct {
	// NetworkMode is the workload's own network for its sandbox, the
	// sandbox's network namespace for a unit, and "none".
	NetworkMode  string
	IpcMode      string
	CgroupnsMode string
	CapDrop      []string
	SecurityOpt  []string
	// ReadonlyRootfs lets a container write its volumes only.
	ReadonlyRootfs bool
	GroupAdd       []string `json:",omitempty"`
	// MemorySwap equals Memory: a container gets no swap on top.
	Memory, MemorySwap int64
	NanoCpus           int64 `json:",omitempty"`
	PidsLimit          int64
	RestartPolicy      struct{ Name string }
	Mounts             []Mount `json:",omitempty"`
	// Runtime is the operator's sandbox runtime, or empty for the
	// daemon's own.
	Runtime   string `json:",omitempty"`
	LogConfig LogConfig
}

// Mount shows a named Docker volume in a container. Type is always
// "volume", for which the daemon takes Source as a volume's name and
// never as a path of the host.
type Mount struct {
	Type, Source, Target string
	ReadOnly             bool `json:",omitempty"`
	VolumeOptions        VolumeOptions
}

// VolumeOptions are how a volume is mounted. NoCopy keeps the daemon from
// filling a new volume with the image's files at the mount path, which
// would also give it the image's owner; a Pod's volume starts empty too.
type VolumeOptions struct {
	NoCopy  bool
	Subpath string `json:",omitempty"`
}

// LogConfig is where the daemon keeps what a container prints.
type LogConfig struct {
	Type   string
	Config map[string]string
}

// pidsLimit is how many processes and threads one container may have.
// Kubernetes sets none by default; on one shared host a fork bomb must
// end in its own container.
const pidsLimit = 4096

// Inputs are what a unit's container needs that its Spec does not hold.
type Inputs struct {
	// Secret is the value an Env.From names, already resolved; false when
	// there is none.
	Secret func(workload.SecretRef) (string, bool)
	// RunID is the id of this run, for an Env with RunID.
	RunID string
	// Runtime is the container runtime the operator configured (a sandbox
	// such as runsc); empty is the daemon's default.
	Runtime string
	// Proxy is the URL of the gateway's forward proxy as the workload
	// reaches it; the unit is told to use it (withProxy). Empty tells it
	// nothing.
	Proxy string
}

// ContainerBody is the container that runs unit u of workload s. Every
// container gets the same confinement, whatever s asks for: its user and
// never root, no capabilities, no privilege gain, the daemon's seccomp
// and AppArmor profiles, namespaces of its own except the workload's
// network, limits on memory, swap and processes, no restart by the
// daemon, and only named volumes. A Spec that cannot be run that way is
// an error.
//
// Docker's init is not used: PID 1 is the unit's own command, as in a
// Pod, so an image behaves the same on both platforms.
func ContainerBody(s workload.Spec, u workload.Unit, in Inputs) (Body, error) {
	if u.Name == Sandbox || u.Name == Setup || u.Name == Record {
		return Body{}, fmt.Errorf("unit %q: the name is reserved", u.Name)
	}
	if _, err := ContainerName(s.Ref, u.Name); err != nil {
		return Body{}, err
	}
	sandbox, _ := SandboxName(s.Ref)
	b, err := confined(s, u.Name, in.Runtime)
	if err != nil {
		return Body{}, err
	}
	if s.Batch == nil {
		maps.Copy(b.Labels, fingerprintLabels(s.Fingerprint))
	}
	b.Image, b.Entrypoint, b.Cmd, b.WorkingDir = u.Image, u.Command, u.Args, u.Dir
	b.HostConfig.NetworkMode = "container:" + sandbox
	b.HostConfig.ReadonlyRootfs = u.ReadOnlyRoot
	if b.HostConfig.Memory, b.HostConfig.NanoCpus, err = limits(s); err != nil {
		return Body{}, err
	}
	b.HostConfig.MemorySwap = b.HostConfig.Memory
	if b.Env, err = environment(u.Env, in); err != nil {
		return Body{}, fmt.Errorf("unit %q: %w", u.Name, err)
	}
	if in.Proxy != "" {
		if b.Env, err = withProxy(b.Env, in.Proxy); err != nil {
			return Body{}, err
		}
	}
	for _, m := range u.Mounts {
		mount, err := mountOf(s, m)
		if err != nil {
			return Body{}, fmt.Errorf("unit %q: %w", u.Name, err)
		}
		b.HostConfig.Mounts = append(b.HostConfig.Mounts, mount)
	}
	return b, nil
}

// confined is the body every container of workload s starts from, with no
// image, no network and no memory yet.
func confined(s workload.Spec, unit, runtime string) (Body, error) {
	if _, err := base(s.Ref); err != nil {
		return Body{}, err
	}
	u := s.User
	if u.UID <= 0 || u.GID < 0 {
		return Body{}, fmt.Errorf("a workload runs as a uid above 0 and a gid of 0 or above, not as %d:%d", u.UID, u.GID)
	}
	b := Body{
		User:   fmt.Sprintf("%d:%d", u.UID, u.GID),
		Labels: Labels(s.Ref),
		HostConfig: HostConfig{
			IpcMode: "private", CgroupnsMode: "private",
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			PidsLimit:   pidsLimit,
			Runtime:     runtime,
			// What a container prints is kept on the host's disk: without
			// a limit it could fill it. The kubelet rotates logs as well.
			LogConfig: LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "5"}},
		},
	}
	b.Labels[LabelUnit] = unit
	b.Healthcheck.Test = []string{"NONE"}
	b.HostConfig.RestartPolicy.Name = "no"
	for _, g := range u.Groups {
		if g < 0 {
			return Body{}, fmt.Errorf("group %d: a gid is 0 or above", g)
		}
		b.HostConfig.GroupAdd = append(b.HostConfig.GroupAdd, strconv.FormatInt(g, 10))
	}
	return b, nil
}

// limits are the memory in bytes and the CPU in billionths of a core that
// each container of s may use: the picked size's, else the template's
// limits. Memory is required: without it one workload could take all of
// the host's. No CPU limit is no limit, as on Kubernetes.
func limits(s workload.Spec) (memory, nanoCPUs int64, err error) {
	if s.Size.Key() != "" {
		memory, nanoCPUs = s.Size.Memory.Value(), s.Size.CPU.MilliValue()*1_000_000
	} else {
		l := s.Resources.Limits
		memory, nanoCPUs = l.Memory().Value(), l.Cpu().MilliValue()*1_000_000
	}
	if memory <= 0 {
		return 0, 0, fmt.Errorf("workload %q has no memory limit: give its template one, or pick a size", s.Ref.Name)
	}
	return memory, nanoCPUs, nil
}

// envName is what a variable may be called: no "=", which would end the
// name early, and nothing a shell could not name.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

func environment(vars []workload.Env, in Inputs) ([]string, error) {
	out := make([]string, 0, len(vars))
	for _, e := range vars {
		if !envName.MatchString(e.Name) {
			return nil, fmt.Errorf("variable %q: not a variable name", e.Name)
		}
		value := e.Value
		switch {
		case e.RunID:
			value = in.RunID
		case e.From != nil:
			v, ok := "", false
			if in.Secret != nil {
				v, ok = in.Secret(*e.From)
			}
			if !ok {
				return nil, fmt.Errorf("variable %q: bundle %q has no value %q", e.Name, e.From.Bundle, e.From.Key)
			}
			value = v
		}
		if strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("variable %q: the value holds a zero byte", e.Name)
		}
		out = append(out, e.Name+"="+value)
	}
	return out, nil
}

// volumeSource is what a storage provider may call a volume: a Docker
// volume name. A path of the host is never one.
var volumeSource = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// dockerVolume is the Docker volume behind v of workload s, and whether
// units may only read it. Storage is the volume the provider named; an
// empty folder and secret files are volumes of the workload's own.
func dockerVolume(s workload.Spec, v workload.Volume) (name string, readOnly bool, err error) {
	if v.Source == "" {
		name, err = VolumeName(s.Ref, v.Name)
		return name, v.Secret != nil, err
	}
	if !volumeSource.MatchString(v.Source) || strings.Contains(v.Source, "..") {
		return "", false, fmt.Errorf("volume %q: %q is not the name of a Docker volume (a path of the host is never mounted)", v.Name, v.Source)
	}
	return v.Source, v.ReadOnly, nil
}

func mountOf(s workload.Spec, m workload.Mount) (Mount, error) {
	for _, v := range s.Volumes {
		if v.Name != m.Volume {
			continue
		}
		source, readOnly, err := dockerVolume(s, v)
		if err != nil {
			return Mount{}, err
		}
		if !path.IsAbs(m.Path) || path.Clean(m.Path) != m.Path || m.Path == "/" {
			return Mount{}, fmt.Errorf("mount of %q: %q is not a clean absolute path below the root", m.Volume, m.Path)
		}
		if m.SubPath != "" && !insideVolume(m.SubPath) {
			return Mount{}, fmt.Errorf("mount of %q: %q is not a folder inside the volume", m.Volume, m.SubPath)
		}
		return Mount{
			Type: "volume", Source: source, Target: m.Path, ReadOnly: readOnly || m.ReadOnly,
			VolumeOptions: VolumeOptions{NoCopy: true, Subpath: m.SubPath},
		}, nil
	}
	return Mount{}, fmt.Errorf("mount of %q: the workload has no such volume", m.Volume)
}

// Tool is an image of the platform's own and the command to run in it.
type Tool struct {
	Image   string
	Command []string
}

// secretsDir is where the sandbox sees the workload's secret volumes.
const secretsDir = "/run/javo/secrets"

// sandboxMemory is all a sandbox may use: it runs one idle program.
const sandboxMemory = 64 << 20

// SandboxBody is the container that holds workload s's place on its
// network, started before any unit and kept until the workload stops.
// Units join its network namespace, so they reach each other on loopback
// as the containers of a Pod do, and a unit that restarts keeps its
// address. It also keeps the secret volumes mounted, which live in memory
// and would be emptied the moment no container used them.
//
// pause is one of the platform's own images with a command that does
// nothing until it is told to end; runtime is the operator's, as in
// Inputs.
func SandboxBody(s workload.Spec, pause Tool, runtime string) (Body, error) {
	b, err := confined(s, Sandbox, runtime)
	if err != nil {
		return Body{}, err
	}
	b.Image, b.Entrypoint = pause.Image, pause.Command
	b.HostConfig.NetworkMode, _ = NetworkName(s.Ref)
	b.HostConfig.ReadonlyRootfs = true
	b.HostConfig.Memory, b.HostConfig.MemorySwap = sandboxMemory, sandboxMemory
	b.HostConfig.PidsLimit = 64
	for _, v := range s.Volumes {
		if v.Secret == nil {
			continue
		}
		name, err := VolumeName(s.Ref, v.Name)
		if err != nil {
			return Body{}, err
		}
		b.HostConfig.Mounts = append(b.HostConfig.Mounts, Mount{
			Type: "volume", Source: name, Target: secretsDir + "/" + v.Name, VolumeOptions: VolumeOptions{NoCopy: true},
		})
	}
	return b, nil
}
