package podspec

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestBaseCarriesTemplateLimitsAndHardening(t *testing.T) {
	limits := corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
	}
	spec := Base("alice-1a2b3c4d", "example.com/lab:1", limits)

	if len(spec.Containers) != 1 || spec.Containers[0].Name != ContainerName {
		t.Fatalf("containers = %+v", spec.Containers)
	}
	c := spec.Containers[0]
	if c.Image != "example.com/lab:1" {
		t.Fatalf("image = %q", c.Image)
	}
	if got := c.Resources.Limits.Memory().String(); got != "2Gi" {
		t.Fatalf("memory limit = %s", got)
	}
	if c.Command != nil || c.Args != nil || c.Ports != nil || c.ReadinessProbe != nil {
		t.Fatalf("Base must leave command, args, ports and probe to callers: %+v", c)
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("service account token must not be mounted")
	}
	// Service links put every Service of the namespace (other people's app
	// names and addresses, in a shared namespace) into the environment.
	if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
		t.Fatal("service-link variables must be off")
	}
	// The storage layout adds the ids and the volumes. No pod sets fsGroup:
	// on drivers with fsGroupPolicy File it would re-group every file of a
	// shared folder the pod mounts.
	sc := spec.SecurityContext
	if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.FSGroup != nil ||
		sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("pod security context = %+v", sc)
	}
	if len(spec.Volumes) != 0 || len(c.VolumeMounts) != 0 {
		t.Fatalf("volumes %v, mounts %v: storage comes from the layout", spec.Volumes, c.VolumeMounts)
	}
	if c.SecurityContext == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container security context = %+v", c.SecurityContext)
	}

	// Images set no HOME the pod can rely on (some run as a passwd user with
	// another home); "~" must be the mounted volume.
	var home string
	for _, e := range c.Env {
		if e.Name == "HOME" {
			home = e.Value
		}
	}
	if home != HomeDir {
		t.Fatalf("HOME = %q, want %q", home, HomeDir)
	}
}

// pod-user.sh names an allocated uid's passwd entry after JAVO_USER, so the
// shell prompt, whoami and git show the person, not "I have no name!".
func TestBaseNamesTheOwnerInJAVOUser(t *testing.T) {
	spec := Base("alice-1a2b3c4d", "example.com/lab:1", corev1.ResourceRequirements{})
	var got string
	for _, e := range spec.Containers[0].Env {
		if e.Name == "JAVO_USER" {
			got = e.Value
		}
	}
	if got != "alice" {
		t.Fatalf("JAVO_USER = %q, want alice", got)
	}
}

func TestScratchHomeKeepsTheOwnersHomeAwayFromTheAppContainer(t *testing.T) {
	spec := withHome(Base("alice", "example.com/app:1", corev1.ResourceRequirements{}))
	ScratchHome(&spec)

	for _, v := range spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			t.Fatalf("volume %s still claims the home", v.Name)
		}
	}
	var scratch string
	for _, m := range spec.Containers[0].VolumeMounts {
		if m.MountPath == HomeDir {
			scratch = m.Name
		}
	}
	if scratch == "" {
		t.Fatal("the app container has nothing at the home path")
	}
	found := false
	for _, v := range spec.Volumes {
		if v.Name == scratch {
			found = v.EmptyDir != nil
		}
	}
	if !found {
		t.Fatalf("mount %s is not an emptyDir", scratch)
	}
}

func TestScratchHomeKeepsTheClaimForAnInitContainerThatReadsIt(t *testing.T) {
	spec := withHome(Base("alice", "example.com/app:1", corev1.ResourceRequirements{}))
	spec.InitContainers = []corev1.Container{{Name: "site-copy",
		VolumeMounts: []corev1.VolumeMount{{Name: HomeVolume, MountPath: HomeDir, ReadOnly: true}}}}
	ScratchHome(&spec)

	if spec.InitContainers[0].VolumeMounts[0].Name != HomeVolume {
		t.Fatal("the init container lost its home mount")
	}
	claims := 0
	for _, v := range spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("claims = %d, want the one the init container reads", claims)
	}
	for _, m := range spec.Containers[0].VolumeMounts {
		if m.Name == HomeVolume {
			t.Fatal("the app container still mounts the home claim")
		}
	}
}

// withHome is spec with the home laid out as every app pod has it.
func withHome(spec corev1.PodSpec) corev1.PodSpec {
	spec.Volumes = append(spec.Volumes, corev1.Volume{Name: HomeVolume, VolumeSource: corev1.VolumeSource{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "home-alice"}}})
	spec.Containers[0].VolumeMounts = append(spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: HomeVolume, MountPath: HomeDir})
	return spec
}

// An admin may run people's pods in their own user namespace (a container
// escape then lands as an unprivileged uid on the node) and under a
// sandboxed runtime class. The zero value leaves the pod as it is.
func TestIsolationRunsPodsInAUserNamespaceAndARuntimeClass(t *testing.T) {
	spec := Base("alice-1a2b3c4d", "example.com/lab:1", corev1.ResourceRequirements{})
	Isolation{}.Apply(&spec)
	if spec.HostUsers != nil || spec.RuntimeClassName != nil {
		t.Fatalf("zero isolation changed the pod: hostUsers %v, runtimeClass %v", spec.HostUsers, spec.RuntimeClassName)
	}
	Isolation{UserNamespace: true, RuntimeClassName: "gvisor"}.Apply(&spec)
	if spec.HostUsers == nil || *spec.HostUsers || spec.RuntimeClassName == nil || *spec.RuntimeClassName != "gvisor" {
		t.Fatalf("hostUsers %v, runtimeClass %v", spec.HostUsers, spec.RuntimeClassName)
	}
}
