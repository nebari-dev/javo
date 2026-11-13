package docker

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/workload"
)

// spec is a workload as the planner writes one: an app with a home, a
// scratch folder, token files and a helper step.
func spec() workload.Spec {
	return workload.Spec{
		Ref:         workload.Ref{Namespace: "javo-alice", Name: "notebook", ID: "app-1", Owner: "alice"},
		Fingerprint: workload.Fingerprint{Run: "r1", Size: "small", Layout: "l1"},
		User:        workload.User{UID: 1000, GID: 100, Groups: []int64{2001, 2002}},
		Main: workload.Unit{
			Name: workload.MainUnit, Image: "registry.example.com/javo/lab:1", Command: []string{"jupyter"}, Args: []string{"lab"}, Dir: "/home/alice",
			Env: []workload.Env{
				{Name: "PLAIN", Value: "a=b"},
				{Name: workload.EnvToken, From: &workload.SecretRef{Bundle: "notebook-token", Key: "token"}},
				{Name: "RUN", RunID: true},
			},
			Mounts: []workload.Mount{
				{Volume: "home", Path: "/home/alice"},
				{Volume: "home", Path: "/data", SubPath: "data/in", ReadOnly: true},
				{Volume: "scratch", Path: "/scratch"},
				{Volume: "nebi", Path: "/run/nebi"},
			},
		},
		Volumes: []workload.Volume{
			{Name: "home", Source: "home-alice"},
			{Name: "shared", Source: "group-team", ReadOnly: true},
			{Name: "scratch"},
			{Name: "nebi", Secret: &workload.SecretFiles{Bundle: "notebook-nebi"}},
		},
		Port: 8888,
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}
}

func inputs() Inputs {
	return Inputs{RunID: "run-7", Secret: func(r workload.SecretRef) (string, bool) {
		return "s3cr3t", r.Bundle == "notebook-token" && r.Key == "token"
	}}
}

func mustBody(t *testing.T, s workload.Spec, u workload.Unit) Body {
	t.Helper()
	b, err := ContainerBody(s, u, inputs())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAUnitRunsAsItsUserWithNothingExtra(t *testing.T) {
	s := spec()
	b := mustBody(t, s, s.Main)
	h := b.HostConfig
	if b.User != "1000:100" || !slices.Equal(h.GroupAdd, []string{"2001", "2002"}) {
		t.Errorf("user %q groups %v", b.User, h.GroupAdd)
	}
	if !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) {
		t.Errorf("caps %v security %v", h.CapDrop, h.SecurityOpt)
	}
	if h.NetworkMode != "container:javo-javo-alice.notebook" || h.IpcMode != "private" || h.CgroupnsMode != "private" {
		t.Errorf("namespaces: net %q ipc %q cgroup %q", h.NetworkMode, h.IpcMode, h.CgroupnsMode)
	}
	if h.RestartPolicy.Name != "no" || h.PidsLimit <= 0 || h.ReadonlyRootfs {
		t.Errorf("restart %q pids %d readonly %v", h.RestartPolicy.Name, h.PidsLimit, h.ReadonlyRootfs)
	}
	if b.Image != s.Main.Image || !slices.Equal(b.Entrypoint, []string{"jupyter"}) || !slices.Equal(b.Cmd, []string{"lab"}) || b.WorkingDir != "/home/alice" {
		t.Errorf("image %q entrypoint %v cmd %v dir %q", b.Image, b.Entrypoint, b.Cmd, b.WorkingDir)
	}
}

// The body is the hardening boundary: whatever a Spec holds, the JSON sent
// to the daemon has no key that loosens a container.
func TestNoSpecCanAskForMore(t *testing.T) {
	s := spec()
	s.Main.ReadOnlyRoot = true
	raw, err := json.Marshal(mustBody(t, s, s.Main))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	host := body["HostConfig"].(map[string]any)
	for _, k := range []string{
		"Privileged", "CapAdd", "Binds", "Devices", "DeviceRequests", "DeviceCgroupRules", "PortBindings", "PublishAllPorts",
		"PidMode", "UTSMode", "UsernsMode", "VolumesFrom", "Sysctls", "Tmpfs", "ExtraHosts", "Links", "OomScoreAdj", "StorageOpt",
	} {
		if _, ok := host[k]; ok {
			t.Errorf("HostConfig has %s", k)
		}
	}
	for _, k := range []string{"ExposedPorts", "Volumes", "Tty", "OpenStdin", "NetworkingConfig", "Hostname"} {
		if _, ok := body[k]; ok {
			t.Errorf("body has %s", k)
		}
	}
	if strings.Contains(string(raw), "unconfined") || strings.Contains(string(raw), `"host"`) {
		t.Errorf("body mentions unconfined or host: %s", raw)
	}
	if host["ReadonlyRootfs"] != true {
		t.Error("ReadOnlyRoot unit has a writable root")
	}
}

func TestRootIsRefused(t *testing.T) {
	for name, u := range map[string]workload.User{
		"uid 0":          {UID: 0, GID: 100},
		"negative uid":   {UID: -1, GID: 100},
		"negative gid":   {UID: 1000, GID: -1},
		"negative group": {UID: 1000, GID: 100, Groups: []int64{-5}},
	} {
		s := spec()
		s.User = u
		if _, err := ContainerBody(s, s.Main, inputs()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLimitsComeFromTheSizeElseTheTemplate(t *testing.T) {
	s := spec()
	h := mustBody(t, s, s.Main).HostConfig
	if h.Memory != 1<<30 || h.MemorySwap != h.Memory || h.NanoCpus != 500_000_000 {
		t.Errorf("template: memory %d swap %d cpu %d", h.Memory, h.MemorySwap, h.NanoCpus)
	}
	s.Size = sizes.Size{Name: "medium", CPU: resource.MustParse("4"), Memory: resource.MustParse("8Gi")}
	h = mustBody(t, s, s.Main).HostConfig
	if h.Memory != 8<<30 || h.MemorySwap != h.Memory || h.NanoCpus != 4_000_000_000 {
		t.Errorf("size: memory %d swap %d cpu %d", h.Memory, h.MemorySwap, h.NanoCpus)
	}
	// A step can use no more than the app it prepares.
	step := workload.Unit{Name: "clone", Image: "registry.example.com/javo/git:1"}
	if got := mustBody(t, s, step).HostConfig; got.Memory != h.Memory || got.NanoCpus != h.NanoCpus {
		t.Errorf("step: memory %d cpu %d", got.Memory, got.NanoCpus)
	}
	// Without a memory limit one app could take the host's memory.
	s.Size, s.Resources = sizes.Size{}, corev1.ResourceRequirements{}
	if _, err := ContainerBody(s, s.Main, inputs()); err == nil {
		t.Error("a workload with no memory limit was accepted")
	}
}

func TestVolumesAreNamedDockerVolumesOnly(t *testing.T) {
	s := spec()
	got := mustBody(t, s, s.Main).HostConfig.Mounts
	nocopy := func(sub string) VolumeOptions { return VolumeOptions{NoCopy: true, Subpath: sub} }
	want := []Mount{
		{Type: "volume", Source: "home-alice", Target: "/home/alice", VolumeOptions: nocopy("")},
		{Type: "volume", Source: "home-alice", Target: "/data", ReadOnly: true, VolumeOptions: nocopy("data/in")},
		{Type: "volume", Source: "javo-javo-alice.notebook.scratch", Target: "/scratch", VolumeOptions: nocopy("")},
		{Type: "volume", Source: "javo-javo-alice.notebook.nebi", Target: "/run/nebi", ReadOnly: true, VolumeOptions: nocopy("")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts:\n got %+v\nwant %+v", got, want)
	}
	// A volume that is read-only is so in every unit.
	s.Main.Mounts = []workload.Mount{{Volume: "shared", Path: "/shared"}}
	if m := mustBody(t, s, s.Main).HostConfig.Mounts[0]; !m.ReadOnly {
		t.Error("read-only volume mounted for writing")
	}
}

func TestAHostPathIsNeverMounted(t *testing.T) {
	for name, change := range map[string]func(*workload.Spec){
		"absolute source":     func(s *workload.Spec) { s.Volumes[0].Source = "/etc" },
		"source with slash":   func(s *workload.Spec) { s.Volumes[0].Source = "a/b" },
		"source with dots":    func(s *workload.Spec) { s.Volumes[0].Source = ".." },
		"source dot dot in":   func(s *workload.Spec) { s.Volumes[0].Source = "a..b" },
		"source with colon":   func(s *workload.Spec) { s.Volumes[0].Source = "c:rw" },
		"docker socket":       func(s *workload.Spec) { s.Volumes[0].Source = "/var/run/docker.sock" },
		"subpath up":          func(s *workload.Spec) { s.Main.Mounts[1].SubPath = "../x" },
		"subpath up inside":   func(s *workload.Spec) { s.Main.Mounts[1].SubPath = "a/../../x" },
		"subpath absolute":    func(s *workload.Spec) { s.Main.Mounts[1].SubPath = "/etc" },
		"relative target":     func(s *workload.Spec) { s.Main.Mounts[0].Path = "home" },
		"target not clean":    func(s *workload.Spec) { s.Main.Mounts[0].Path = "/home/../proc" },
		"target is the root":  func(s *workload.Spec) { s.Main.Mounts[0].Path = "/" },
		"unknown volume":      func(s *workload.Spec) { s.Main.Mounts[0].Volume = "nope" },
		"volume name unsafe":  func(s *workload.Spec) { s.Volumes[2].Name = "a/b"; s.Main.Mounts[2].Volume = "a/b" },
		"unit name unsafe":    func(s *workload.Spec) { s.Main.Name = "a/b" },
		"namespace unsafe":    func(s *workload.Spec) { s.Ref.Namespace = "a.b" },
		"workload name empty": func(s *workload.Spec) { s.Ref.Name = "" },
	} {
		s := spec()
		change(&s)
		if _, err := ContainerBody(s, s.Main, inputs()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestEnvHoldsPlainSecretAndRunValues(t *testing.T) {
	s := spec()
	got := mustBody(t, s, s.Main).Env
	if !slices.Equal(got, []string{"PLAIN=a=b", "JAVO_TOKEN=s3cr3t", "RUN=run-7"}) {
		t.Fatalf("env = %v", got)
	}
	s.Main.Env = []workload.Env{{Name: "X", From: &workload.SecretRef{Bundle: "other", Key: "k"}}}
	if _, err := ContainerBody(s, s.Main, inputs()); err == nil {
		t.Error("a missing secret value was accepted")
	}
	for _, name := range []string{"", "A=B", "A\x00"} {
		s.Main.Env = []workload.Env{{Name: name, Value: "v"}}
		if _, err := ContainerBody(s, s.Main, inputs()); err == nil {
			t.Errorf("variable name %q was accepted", name)
		}
	}
}

func TestLabelsNameTheWorkloadAndWhatItWasBuiltFrom(t *testing.T) {
	s := spec()
	l := mustBody(t, s, s.Main).Labels
	if !Ours(l) || RefOf(l) != s.Ref || l[LabelUnit] != "app" {
		t.Errorf("labels = %v", l)
	}
	if got := FingerprintOf(l); got != s.Fingerprint {
		t.Errorf("fingerprint = %+v, want %+v", got, s.Fingerprint)
	}
	// A run is not built again, so it records nothing to compare.
	s.Batch = &workload.Batch{}
	if got := FingerprintOf(mustBody(t, s, s.Main).Labels); got != (workload.Fingerprint{}) {
		t.Errorf("run fingerprint = %+v", got)
	}
}

func TestTheOperatorsRuntimeIsUsed(t *testing.T) {
	s := spec()
	in := inputs()
	in.Runtime = "runsc"
	b, err := ContainerBody(s, s.Main, in)
	if err != nil || b.HostConfig.Runtime != "runsc" {
		t.Fatalf("runtime %q, err %v", b.HostConfig.Runtime, err)
	}
}

func TestTheSandboxHoldsTheNetworkAndTheSecretFiles(t *testing.T) {
	s := spec()
	b, err := SandboxBody(s, Tool{Image: "registry.example.com/javo/javo:1", Command: []string{"/javo", "pause"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	h := b.HostConfig
	if h.NetworkMode != "javo-javo-alice.notebook" || !h.ReadonlyRootfs || b.User != "1000:100" || h.Memory == 0 || h.MemorySwap != h.Memory || h.PidsLimit <= 0 {
		t.Errorf("sandbox = %+v", b)
	}
	if !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) {
		t.Errorf("caps %v security %v", h.CapDrop, h.SecurityOpt)
	}
	want := []Mount{{Type: "volume", Source: "javo-javo-alice.notebook.nebi", Target: "/run/javo/secrets/nebi", VolumeOptions: VolumeOptions{NoCopy: true}}}
	if !reflect.DeepEqual(h.Mounts, want) {
		t.Errorf("mounts = %+v", h.Mounts)
	}
	if b.Labels[LabelUnit] != Sandbox || !Ours(b.Labels) {
		t.Errorf("labels = %v", b.Labels)
	}
	// No unit may take the sandbox's name.
	s.Main.Name = Sandbox
	if _, err := ContainerBody(s, s.Main, inputs()); err == nil {
		t.Error("a unit called sandbox was accepted")
	}
}
