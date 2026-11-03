package sizes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/capacity"
)

const shipped = `
default: small
sizes:
  - {name: small, displayName: Small, cpu: "1", memory: 2Gi}
  - {name: medium, displayName: Medium, cpu: "4", memory: 8Gi}
`

func TestResolveEmptyNameGivesTheDefault(t *testing.T) {
	c, err := Parse([]byte(shipped))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := c.Resolve("")
	if !ok || got.Name != "small" || got.DisplayName != "Small" {
		t.Fatalf("Resolve(\"\") = %+v, %v", got, ok)
	}
	if !got.CPU.Equal(resource.MustParse("1")) || !got.Memory.Equal(resource.MustParse("2Gi")) {
		t.Fatalf("quantities: cpu %s memory %s", got.CPU.String(), got.Memory.String())
	}
}

func TestResolveNamedAndUnknown(t *testing.T) {
	c, err := Parse([]byte(shipped))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Resolve("medium"); !ok || !got.Memory.Equal(resource.MustParse("8Gi")) {
		t.Fatalf("medium = %+v, %v", got, ok)
	}
	if _, ok := c.Resolve("huge"); ok {
		t.Fatal("unknown size resolved")
	}
}

func TestListKeepsOrderAndMarksDefault(t *testing.T) {
	c, err := Parse([]byte(shipped))
	if err != nil {
		t.Fatal(err)
	}
	l := c.List()
	if len(l) != 2 || l[0].Name != "small" || l[1].Name != "medium" {
		t.Fatalf("List = %+v", l)
	}
	if c.Default() != "small" {
		t.Fatalf("Default = %q", c.Default())
	}
}

// With no sizes configured every app keeps its template's resources: the
// empty name resolves to the zero Size and any other name is unknown.
func TestEmptyCatalog(t *testing.T) {
	c, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Resolve(""); !ok || got.Name != "" {
		t.Fatalf("Resolve(\"\") = %+v, %v", got, ok)
	}
	if _, ok := c.Resolve("small"); ok {
		t.Fatal("named size resolved in an empty catalog")
	}
}

func TestParseRejectsBadCatalogs(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"default not listed":    {`{default: big, sizes: [{name: small, displayName: Small, cpu: "1", memory: 2Gi}]}`, "default"},
		"default without sizes": {`{default: small}`, "default"},
		"no default":            {`{sizes: [{name: small, displayName: Small, cpu: "1", memory: 2Gi}]}`, "default"},
		"bad cpu":               {`{default: small, sizes: [{name: small, displayName: Small, cpu: lots, memory: 2Gi}]}`, "cpu"},
		"zero memory":           {`{default: small, sizes: [{name: small, displayName: Small, cpu: "1", memory: "0"}]}`, "memory"},
		"duplicate":             {`{default: small, sizes: [{name: small, displayName: A, cpu: "1", memory: 1Gi}, {name: small, displayName: B, cpu: "1", memory: 1Gi}]}`, "twice"},
		"bad name":              {`{default: Small!, sizes: [{name: Small!, displayName: Small, cpu: "1", memory: 1Gi}]}`, "name"},
		"no display name":       {`{default: small, sizes: [{name: small, cpu: "1", memory: 1Gi}]}`, "displayName"},
		"unknown field":         {`{default: small, sizes: [{name: small, displayName: Small, cpus: "1", memory: 1Gi}]}`, "cpus"},
		"negative gpu":          {`{default: small, sizes: [{name: small, displayName: Small, cpu: "1", memory: 1Gi, gpu: -1}]}`, "gpu"},
		"bad gpu resource":      {`{default: small, sizes: [{name: small, displayName: Small, cpu: "1", memory: 1Gi, gpu: 1, gpuResource: "gpu!"}]}`, "gpuResource"},
		"gpu resource is cpu":   {`{default: small, sizes: [{name: small, displayName: Small, cpu: "1", memory: 1Gi, gpu: 1, gpuResource: cpu}]}`, "gpuResource"},
		"bad selector label":    {`{default: small, sizes: [{name: small, displayName: Small, cpu: "1", memory: 1Gi, nodeSelector: {"bad key!": x}}]}`, "nodeSelector"},
		"bad toleration":        {`{default: small, sizes: [{name: small, displayName: Small, cpu: "1", memory: 1Gi, tolerations: [{key: k, operator: Sometimes}]}]}`, "tolerations"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	if c, err := Load(""); err != nil || len(c.List()) != 0 {
		t.Fatalf("Load(\"\") = %+v, %v", c, err)
	}
	p := filepath.Join(t.TempDir(), "sizes.yaml")
	if err := os.WriteFile(p, []byte(shipped), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.Default() != "small" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing file loaded")
	}
}

func machines(cpu, mem string) capacity.Cluster {
	return capacity.Cluster{{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem)}}
}

// A size bigger than every machine does not fit, unless the admin marked it
// allowUnschedulable (an autoscaler adds a big enough machine on demand).
func TestSizeFitsTheCluster(t *testing.T) {
	c, err := Parse([]byte(`{default: small, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: large, displayName: Large, cpu: "8", memory: 32Gi},
		{name: huge, displayName: Huge, cpu: "64", memory: 512Gi, allowUnschedulable: true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	singleNode := machines("8", "16365140Ki")
	for name, want := range map[string]bool{"small": true, "large": false, "huge": true} {
		s, _ := c.Resolve(name)
		if got := s.FitsOn(singleNode); got != want {
			t.Errorf("%s fits = %v, want %v", name, got, want)
		}
	}
	if !(Size{}).FitsOn(singleNode) {
		t.Error("the zero size (template resources) is left to the scheduler")
	}
}

// New apps get the configured default when it fits, else the largest size
// that fits; with none fitting, the configured default (refused later).
func TestDefaultOnFallsBackToTheLargestThatFits(t *testing.T) {
	c, err := Parse([]byte(`{default: large, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: medium, displayName: Medium, cpu: "4", memory: 8Gi},
		{name: large, displayName: Large, cpu: "8", memory: 32Gi},
		{name: burst, displayName: Burst, cpu: "64", memory: 512Gi, allowUnschedulable: true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// burst may wait for an autoscaler, but a fallback is one that fits now.
	if got := c.DefaultOn(machines("8", "16Gi")); got != "medium" {
		t.Errorf("single-node default = %q, want medium", got)
	}
	if got := c.DefaultOn(machines("16", "64Gi")); got != "large" {
		t.Errorf("big machine default = %q, want large", got)
	}
	if got := c.DefaultOn(machines("500m", "1Gi")); got != "large" {
		t.Errorf("nothing fits: default = %q, want the configured large", got)
	}
	if got := c.DefaultOn(nil); got != "large" {
		t.Errorf("unknown cluster: default = %q, want large", got)
	}
}

// Sizes can be limited to groups, and custom amounts are allowed per role or
// group up to a limit (no limit: the largest machine caps it).
func TestParseGroupsAndCustomRules(t *testing.T) {
	c, err := Parse([]byte(`{default: small,
		sizes: [
			{name: small, displayName: Small, cpu: "1", memory: 2Gi},
			{name: gpu, displayName: GPU, cpu: "8", memory: 32Gi, allowedGroups: [ml]}],
		custom: {enabled: true, rules: [
			{roles: [platform-admin]},
			{groups: [ml, research], maxCPU: "4", maxMemory: 16Gi}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Resolve("gpu"); len(s.AllowedGroups) != 1 || s.AllowedGroups[0] != "ml" {
		t.Fatalf("gpu allowedGroups = %v", s.AllowedGroups)
	}
	p := c.Custom()
	if !p.Enabled || len(p.Rules) != 2 {
		t.Fatalf("custom = %+v", p)
	}
	if r := p.Rules[0]; r.Roles[0] != "platform-admin" || !r.MaxCPU.IsZero() || !r.MaxMemory.IsZero() {
		t.Fatalf("admin rule = %+v", r)
	}
	if r := p.Rules[1]; len(r.Groups) != 2 || r.MaxCPU.Cmp(resource.MustParse("4")) != 0 || r.MaxMemory.Cmp(resource.MustParse("16Gi")) != 0 {
		t.Fatalf("group rule = %+v", r)
	}
}

// Sizes and custom rules can name the identity provider's roles; a rule
// that names only provider roles is a rule for someone.
func TestParseProviderRoles(t *testing.T) {
	c, err := Parse([]byte(`{default: small,
		sizes: [
			{name: small, displayName: Small, cpu: "1", memory: 2Gi},
			{name: gpu, displayName: GPU, cpu: "8", memory: 32Gi, allowedProviderRoles: [gpu-users]}],
		custom: {enabled: true, rules: [{providerRoles: [gpu-users], maxCPU: "8"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Resolve("gpu"); len(s.AllowedProviderRoles) != 1 || s.AllowedProviderRoles[0] != "gpu-users" {
		t.Fatalf("gpu allowedProviderRoles = %v", s.AllowedProviderRoles)
	}
	if r := c.Custom().Rules; len(r) != 1 || len(r[0].ProviderRoles) != 1 || r[0].ProviderRoles[0] != "gpu-users" {
		t.Fatalf("rules = %+v", r)
	}
}

func TestParseRejectsBadCustomRules(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"rule for nobody": {`{custom: {enabled: true, rules: [{maxCPU: "2"}]}}`, "groups or roles"},
		"bad max":         {`{custom: {enabled: true, rules: [{roles: [member], maxCPU: lots}]}}`, "maxCPU"},
		"negative max":    {`{custom: {enabled: true, rules: [{roles: [member], maxMemory: "-1Gi"}]}}`, "maxMemory"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A custom amount is a Size with no name, not offered to wait for an
// autoscaler, and known by its amounts.
func TestCustomSize(t *testing.T) {
	s := Custom(resource.MustParse("2500m"), resource.MustParse("6Gi"))
	if !s.IsCustom() || s.Name != "" || s.AllowUnschedulable {
		t.Fatalf("custom = %+v", s)
	}
	if s.Key() != Custom(resource.MustParse("2.5"), resource.MustParse("6144Mi")).Key() {
		t.Fatal("the same amounts written differently are different keys")
	}
	if s.Key() == Custom(resource.MustParse("2"), resource.MustParse("6Gi")).Key() {
		t.Fatal("different amounts share a key")
	}
	if named := (Size{Name: "small"}); named.IsCustom() || named.Key() != "small" {
		t.Fatalf("named key = %q", named.Key())
	}
}

// Only keeps the sizes a person may use; when that leaves out the default,
// their default is the largest of theirs that fits.
func TestOnlyKeepsUsableSizesAndMovesTheDefault(t *testing.T) {
	c, err := Parse([]byte(`{default: gpu, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: medium, displayName: Medium, cpu: "4", memory: 8Gi},
		{name: gpu, displayName: GPU, cpu: "2", memory: 4Gi, allowedGroups: [ml]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	mine := c.Only(func(s Size) bool { return len(s.AllowedGroups) == 0 })
	if l := mine.List(); len(l) != 2 || l[1].Name != "medium" {
		t.Fatalf("List = %+v", l)
	}
	if got := mine.DefaultOn(nil); got != "medium" {
		t.Fatalf("default = %q, want medium", got)
	}
	if _, ok := mine.Resolve("gpu"); ok {
		t.Fatal("a left-out size still resolves")
	}
}

const gpuCatalog = `{default: small, sizes: [
	{name: small, displayName: Small, cpu: "1", memory: 2Gi},
	{name: gpu, displayName: GPU, cpu: "4", memory: 16Gi, gpu: 1,
	 nodeSelector: {pool: gpu},
	 tolerations: [{key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}]},
	{name: amd, displayName: AMD, cpu: "4", memory: 16Gi, gpu: 2, gpuResource: amd.com/gpu}]}`

func gpuMachine(cpu, mem, resourceName, gpus string, labels map[string]string) capacity.Machine {
	return capacity.Machine{CPU: resource.MustParse(cpu), Memory: resource.MustParse(mem), Labels: labels,
		Devices: corev1.ResourceList{corev1.ResourceName(resourceName): resource.MustParse(gpus)}}
}

// A GPU size's pod asks for its GPUs (nvidia.com/gpu unless the size names
// another resource) as request and limit, lands on the size's nodes and
// tolerates their taints; other pods are left as they are.
func TestGPUSizeAppliesToThePod(t *testing.T) {
	c, err := Parse([]byte(gpuCatalog))
	if err != nil {
		t.Fatal(err)
	}
	gpu, _ := c.Resolve("gpu")
	if gpu.GPU != 1 {
		t.Fatalf("gpu size has %d GPUs", gpu.GPU)
	}
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}},
		NodeSelector: map[string]string{"kubernetes.io/os": "linux"}}
	gpu.Apply(&spec, &spec.Containers[0], corev1.ResourceRequirements{})
	r := spec.Containers[0].Resources
	for _, l := range []corev1.ResourceList{r.Requests, r.Limits} {
		if g := l["nvidia.com/gpu"]; g.Cmp(resource.MustParse("1")) != 0 {
			t.Fatalf("gpu resources %v", r)
		}
		if c := l[corev1.ResourceCPU]; c.Cmp(resource.MustParse("4")) != 0 {
			t.Fatalf("cpu resources %v", r)
		}
	}
	if spec.NodeSelector["pool"] != "gpu" || spec.NodeSelector["kubernetes.io/os"] != "linux" {
		t.Fatalf("node selector %v", spec.NodeSelector)
	}
	if len(spec.Tolerations) != 1 || spec.Tolerations[0].Key != "nvidia.com/gpu" || spec.Tolerations[0].Operator != corev1.TolerationOpExists {
		t.Fatalf("tolerations %+v", spec.Tolerations)
	}

	amd, _ := c.Resolve("amd")
	spec = corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	amd.Apply(&spec, &spec.Containers[0], corev1.ResourceRequirements{})
	if g := spec.Containers[0].Resources.Limits["amd.com/gpu"]; g.Cmp(resource.MustParse("2")) != 0 {
		t.Fatalf("amd resources %v", spec.Containers[0].Resources)
	}
	if _, ok := spec.Containers[0].Resources.Limits["nvidia.com/gpu"]; ok || spec.NodeSelector != nil || spec.Tolerations != nil {
		t.Fatalf("amd pod %+v", spec)
	}

	small, _ := c.Resolve("small")
	spec = corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	small.Apply(&spec, &spec.Containers[0], corev1.ResourceRequirements{})
	if len(spec.Containers[0].Resources.Limits) != 2 || spec.NodeSelector != nil || spec.Tolerations != nil {
		t.Fatalf("small pod %+v", spec)
	}
}

// A GPU size fits only a machine with its GPUs and its labels.
func TestGPUSizeFitsOnlyGPUMachines(t *testing.T) {
	c, err := Parse([]byte(gpuCatalog))
	if err != nil {
		t.Fatal(err)
	}
	gpu, _ := c.Resolve("gpu")
	cpuOnly := machines("64", "256Gi")
	if gpu.FitsOn(cpuOnly) {
		t.Error("a GPU size fits a cluster without GPUs")
	}
	unlabelled := capacity.Cluster{gpuMachine("8", "32Gi", "nvidia.com/gpu", "1", nil)}
	if gpu.FitsOn(unlabelled) {
		t.Error("a GPU size fits a GPU machine its selector does not match")
	}
	pool := capacity.Cluster{gpuMachine("8", "32Gi", "nvidia.com/gpu", "1", map[string]string{"pool": "gpu"})}
	if !gpu.FitsOn(pool) {
		t.Error("a GPU size does not fit its pool")
	}
	amd, _ := c.Resolve("amd")
	if amd.FitsOn(pool) {
		t.Error("2 amd.com/gpu fit a machine with nvidia.com/gpu")
	}
}

// Falling back from a default that does not fit never hands out a GPU size:
// GPUs are scarce, and an app gets one only when someone picks it.
func TestDefaultOnFallbackSkipsGPUSizes(t *testing.T) {
	c, err := Parse([]byte(`{default: large, sizes: [
		{name: small, displayName: Small, cpu: "1", memory: 2Gi},
		{name: large, displayName: Large, cpu: "64", memory: 512Gi},
		{name: gpu, displayName: GPU, cpu: "4", memory: 16Gi, gpu: 1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cl := capacity.Cluster{gpuMachine("8", "32Gi", "nvidia.com/gpu", "1", nil)}
	if got := c.DefaultOn(cl); got != "small" {
		t.Errorf("fallback = %q, want small", got)
	}
}
