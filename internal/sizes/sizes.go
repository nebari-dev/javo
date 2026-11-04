// Package sizes is the admin-defined list of app sizes (CPU, memory and
// optionally GPUs) users pick from. The controller and the gateway load the same file (the Helm
// chart renders it from the sizes values into one ConfigMap).
package sizes

import (
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/model"
)

// DefaultGPUResource is the extended resource a GPU size asks for unless it
// names another.
const DefaultGPUResource corev1.ResourceName = "nvidia.com/gpu"

// Size is one choice: its app container gets CPU and Memory (and GPU of
// GPUResource, when GPU is set) as both requests and limits, and its pod
// NodeSelector and Tolerations.
type Size struct {
	Name        string
	DisplayName string
	CPU         resource.Quantity
	Memory      resource.Quantity
	// GPU is how many of GPUResource the app gets; 0 is none.
	GPU         int64
	GPUResource corev1.ResourceName
	// NodeSelector and Tolerations place the pod, for example on GPU nodes
	// whose taint keeps other pods off.
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	// AllowUnschedulable offers the size even when no node is big enough
	// for it, for clusters whose autoscaler adds a bigger node on demand.
	// Off, such a size is refused and an app at it fails as SizeTooBig.
	AllowUnschedulable bool
	// AllowedGroups limits the size to members of these groups (platform
	// admins always may); empty offers it to everyone. authz.Decide reads it.
	AllowedGroups []string
	// AllowedProviderRoles limits the size to holders of these identity
	// provider roles; with AllowedGroups, either one is enough.
	AllowedProviderRoles []string
}

// Custom is the Size for a custom amount an app asked for: no name, never
// allowUnschedulable.
func Custom(cpu, mem resource.Quantity) Size { return Size{CPU: cpu, Memory: mem} }

// IsCustom reports whether s is a custom amount (not a named size, not the
// zero Size that keeps the template's resources).
func (s Size) IsCustom() bool { return s.Name == "" && !s.CPU.IsZero() }

// Key names what a pod was built at: the size's name, or for a custom
// amount "custom:<millicores>m,<bytes>", the same however it was written.
// The zero Size has the empty key.
func (s Size) Key() string {
	if !s.IsCustom() {
		return s.Name
	}
	return fmt.Sprintf("custom:%dm,%d", s.CPU.MilliValue(), s.Memory.Value())
}

// resources is base (a template's resources) with the CPU and memory
// requests and limits set to s's; other resources keep base's values. The
// zero Size returns base as it is.
func (s Size) resources(base corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := *base.DeepCopy()
	if s.Key() == "" {
		return out
	}
	if out.Requests == nil {
		out.Requests = corev1.ResourceList{}
	}
	if out.Limits == nil {
		out.Limits = corev1.ResourceList{}
	}
	for _, l := range []corev1.ResourceList{out.Requests, out.Limits} {
		l[corev1.ResourceCPU] = s.CPU
		l[corev1.ResourceMemory] = s.Memory
		if s.GPU > 0 {
			l[s.GPUResource] = *resource.NewQuantity(s.GPU, resource.DecimalSI)
		}
	}
	return out
}

// Apply runs a pod at s: c (its app container) gets resources(base), and
// spec gets the size's node selector (added to any it has) and
// tolerations. The zero Size leaves both as they are.
func (s Size) Apply(spec *corev1.PodSpec, c *corev1.Container, base corev1.ResourceRequirements) {
	if s.Key() == "" {
		return
	}
	disk := c.Resources
	c.Resources = s.resources(base)
	// A size is CPU, memory and GPUs: the disk limit the storage layout set
	// stays (podstorage).
	keep := func(to *corev1.ResourceList, from corev1.ResourceList) {
		if q, ok := from[corev1.ResourceEphemeralStorage]; ok {
			if *to == nil {
				*to = corev1.ResourceList{}
			}
			(*to)[corev1.ResourceEphemeralStorage] = q
		}
	}
	keep(&c.Resources.Requests, disk.Requests)
	keep(&c.Resources.Limits, disk.Limits)
	if len(s.NodeSelector) > 0 {
		if spec.NodeSelector == nil {
			spec.NodeSelector = map[string]string{}
		}
		for k, v := range s.NodeSelector {
			spec.NodeSelector[k] = v
		}
	}
	spec.Tolerations = append(spec.Tolerations, s.Tolerations...)
}

// For is what an app or a job asks to run at: its custom amount when it has
// one, else its named size ("" the default). ok is false for a size that is
// not configured.
func (c Catalog) For(name string, custom *model.AppResources) (Size, bool) {
	if custom != nil {
		return Custom(custom.CPU, custom.Memory), true
	}
	return c.Resolve(name)
}

// Stored is what an app or a job keeps for s: the size's name, or for a
// custom amount no name and the amount.
func (s Size) Stored() (name string, custom *model.AppResources) {
	if s.IsCustom() {
		return "", &model.AppResources{CPU: s.CPU, Memory: s.Memory}
	}
	return s.Name, nil
}

// CustomPolicy says who may ask for a custom amount and up to what. The
// gateway asks authz.Decide, which applies it.
type CustomPolicy struct {
	Enabled bool
	Rules   []CustomRule
}

// CustomRule allows custom amounts to holders of any of Roles (role names
// such as platform-admin; an unknown name matches nobody), members of any
// of Groups or holders of any of ProviderRoles (the identity provider's
// roles), up to MaxCPU and MaxMemory. A zero max is no limit of its own:
// the largest machine still caps it.
type CustomRule struct {
	Groups, Roles     []string
	ProviderRoles     []string
	MaxCPU, MaxMemory resource.Quantity
}

// The smallest custom amount (the rule every stored app follows), and the
// steps the UI snaps to.
var (
	MinCPU     = model.MinCustomCPU
	MinMemory  = model.MinCustomMemory
	CPUStep    = resource.MustParse("100m")
	MemoryStep = resource.MustParse("256Mi")
)

// FitsOn reports whether an app of this size can ever run on c.
func (s Size) FitsOn(c capacity.Cluster) bool {
	return s.AllowUnschedulable || c.Holds(s.Need())
}

// Need is what s asks of one machine.
func (s Size) Need() capacity.Need {
	n := capacity.Need{CPU: s.CPU, Memory: s.Memory, NodeSelector: s.NodeSelector}
	if s.GPU > 0 {
		n.Devices = corev1.ResourceList{s.GPUResource: *resource.NewQuantity(s.GPU, resource.DecimalSI)}
	}
	return n
}

// Catalog is the configured sizes in display order, plus the default and
// the custom amount policy.
type Catalog struct {
	sizes  []Size
	def    string
	custom CustomPolicy
}

// file is the on-disk form: {default: small, sizes: [{name, displayName, cpu,
// memory, gpu, gpuResource, nodeSelector, tolerations, allowUnschedulable,
// allowedGroups, allowedProviderRoles}], custom: {enabled, rules: [{groups,
// roles, providerRoles, maxCPU, maxMemory}]}}.
type file struct {
	Default string `json:"default"`
	Sizes   []struct {
		Name               string              `json:"name"`
		DisplayName        string              `json:"displayName"`
		CPU                string              `json:"cpu"`
		Memory             string              `json:"memory"`
		GPU                int64               `json:"gpu"`
		GPUResource        string              `json:"gpuResource"`
		NodeSelector       map[string]string   `json:"nodeSelector"`
		Tolerations        []corev1.Toleration `json:"tolerations"`
		AllowUnschedulable bool                `json:"allowUnschedulable"`
		AllowedGroups      []string            `json:"allowedGroups"`
		// AllowedProviderRoles gates by the identity provider's roles.
		AllowedProviderRoles []string `json:"allowedProviderRoles"`
	} `json:"sizes"`
	Custom struct {
		Enabled bool `json:"enabled"`
		Rules   []struct {
			Groups []string `json:"groups"`
			Roles  []string `json:"roles"`
			// ProviderRoles are identity provider roles, not platform roles.
			ProviderRoles []string `json:"providerRoles"`
			MaxCPU        string   `json:"maxCPU"`
			MaxMemory     string   `json:"maxMemory"`
		} `json:"rules"`
	} `json:"custom"`
}

// Parse reads a catalog. Empty input is the empty catalog.
func Parse(data []byte) (Catalog, error) {
	var f file
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return Catalog{}, fmt.Errorf("sizes: %w", err)
	}
	var c Catalog
	seen := map[string]bool{}
	for i, s := range f.Sizes {
		where := fmt.Sprintf("sizes[%d]", i)
		if errs := validation.IsDNS1123Label(s.Name); len(errs) > 0 {
			return Catalog{}, fmt.Errorf("sizes: %s.name %q: %s", where, s.Name, strings.Join(errs, "; "))
		}
		if seen[s.Name] {
			return Catalog{}, fmt.Errorf("sizes: %s.name %q appears twice", where, s.Name)
		}
		seen[s.Name] = true
		if s.DisplayName == "" {
			return Catalog{}, fmt.Errorf("sizes: %s.displayName is empty", where)
		}
		cpu, err := positive(where+".cpu", s.CPU)
		if err != nil {
			return Catalog{}, err
		}
		mem, err := positive(where+".memory", s.Memory)
		if err != nil {
			return Catalog{}, err
		}
		sz := Size{Name: s.Name, DisplayName: s.DisplayName, CPU: cpu, Memory: mem,
			NodeSelector: s.NodeSelector, Tolerations: s.Tolerations,
			AllowUnschedulable: s.AllowUnschedulable, AllowedGroups: s.AllowedGroups, AllowedProviderRoles: s.AllowedProviderRoles}
		if err := sz.placement(where, s.GPU, s.GPUResource); err != nil {
			return Catalog{}, err
		}
		c.sizes = append(c.sizes, sz)
	}
	if (len(c.sizes) > 0 || f.Default != "") && !seen[f.Default] {
		return Catalog{}, fmt.Errorf("sizes: default %q is not one of the sizes", f.Default)
	}
	c.def = f.Default
	c.custom.Enabled = f.Custom.Enabled
	for i, r := range f.Custom.Rules {
		where := fmt.Sprintf("custom.rules[%d]", i)
		if len(r.Groups) == 0 && len(r.Roles) == 0 && len(r.ProviderRoles) == 0 {
			return Catalog{}, fmt.Errorf("sizes: %s names no groups or roles", where)
		}
		rule := CustomRule{Groups: r.Groups, Roles: r.Roles, ProviderRoles: r.ProviderRoles}
		for _, m := range []struct {
			field, in string
			out       *resource.Quantity
		}{{"maxCPU", r.MaxCPU, &rule.MaxCPU}, {"maxMemory", r.MaxMemory, &rule.MaxMemory}} {
			if m.in == "" {
				continue
			}
			q, err := positive(where+"."+m.field, m.in)
			if err != nil {
				return Catalog{}, err
			}
			*m.out = q
		}
		c.custom.Rules = append(c.custom.Rules, rule)
	}
	return c, nil
}

// placement checks and sets s's GPUs (gpu of gpuResource, default
// DefaultGPUResource) and checks its node selector and tolerations.
func (s *Size) placement(where string, gpu int64, gpuResource string) error {
	if gpu < 0 {
		return fmt.Errorf("sizes: %s.gpu %d must not be negative", where, gpu)
	}
	if gpu > 0 {
		s.GPU, s.GPUResource = gpu, DefaultGPUResource
		if gpuResource != "" {
			// An extended resource is a qualified name with a domain, such as amd.com/gpu.
			errs := validation.IsQualifiedName(gpuResource)
			if !strings.Contains(gpuResource, "/") {
				errs = append(errs, "must be an extended resource with a domain, such as nvidia.com/gpu")
			}
			if len(errs) > 0 {
				return fmt.Errorf("sizes: %s.gpuResource %q: %s", where, gpuResource, strings.Join(errs, "; "))
			}
			s.GPUResource = corev1.ResourceName(gpuResource)
		}
	}
	for k, v := range s.NodeSelector {
		errs := append(validation.IsQualifiedName(k), validation.IsValidLabelValue(v)...)
		if len(errs) > 0 {
			return fmt.Errorf("sizes: %s.nodeSelector %q: %q: %s", where, k, v, strings.Join(errs, "; "))
		}
	}
	for i, t := range s.Tolerations {
		if err := checkToleration(t); err != nil {
			return fmt.Errorf("sizes: %s.tolerations[%d]: %w", where, i, err)
		}
	}
	return nil
}

func checkToleration(t corev1.Toleration) error {
	if t.Key != "" {
		if errs := validation.IsQualifiedName(t.Key); len(errs) > 0 {
			return fmt.Errorf("key %q: %s", t.Key, strings.Join(errs, "; "))
		}
	}
	switch t.Operator {
	case corev1.TolerationOpExists:
		if t.Value != "" {
			return fmt.Errorf("operator Exists takes no value")
		}
	case "", corev1.TolerationOpEqual:
		if t.Key == "" {
			return fmt.Errorf("operator Equal needs a key")
		}
		if errs := validation.IsValidLabelValue(t.Value); len(errs) > 0 {
			return fmt.Errorf("value %q: %s", t.Value, strings.Join(errs, "; "))
		}
	default:
		return fmt.Errorf("operator %q is not Exists or Equal", t.Operator)
	}
	switch t.Effect {
	case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule, corev1.TaintEffectNoExecute:
	default:
		return fmt.Errorf("effect %q is not NoSchedule, PreferNoSchedule or NoExecute", t.Effect)
	}
	return nil
}

func positive(where, s string) (resource.Quantity, error) {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return q, fmt.Errorf("sizes: %s %q: %w", where, s, err)
	}
	if q.Sign() <= 0 {
		return q, fmt.Errorf("sizes: %s %q must be more than zero", where, s)
	}
	return q, nil
}

// Load parses the file at path. An empty path is the empty catalog.
func Load(path string) (Catalog, error) {
	if path == "" {
		return Catalog{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("sizes: %w", err)
	}
	return Parse(data)
}

// Resolve returns the size called name; "" means the default. In the empty
// catalog "" resolves to the zero Size (keep the template's resources) and
// every other name is unknown.
func (c Catalog) Resolve(name string) (Size, bool) {
	if name == "" {
		name = c.def
		if name == "" {
			return Size{}, true
		}
	}
	for _, s := range c.sizes {
		if s.Name == name {
			return s, true
		}
	}
	return Size{}, false
}

// List returns the sizes in display order.
func (c Catalog) List() []Size { return append([]Size(nil), c.sizes...) }

// Only is c with just the sizes keep accepts, in the same order, with the
// same default name and policy. A left-out default resolves no more, so
// DefaultOn picks the largest kept size that fits.
func (c Catalog) Only(keep func(Size) bool) Catalog {
	out := Catalog{def: c.def, custom: c.custom}
	for _, s := range c.sizes {
		if keep(s) {
			out.sizes = append(out.sizes, s)
		}
	}
	return out
}

// Custom is the custom amount policy.
func (c Catalog) Custom() CustomPolicy { return c.custom }

// Default is the default size's name, "" in the empty catalog.
func (c Catalog) Default() string { return c.def }

// DefaultOn is the size new apps get on cl: the default when it is in c and fits, else
// the largest size that fits a machine there now (capacity.Machine.Bigger;
// an allowUnschedulable size is not picked to wait on an autoscaler, nor a
// GPU size, which an app gets only when someone picks it). When none fits
// it is still the default, which the caller refuses.
func (c Catalog) DefaultOn(cl capacity.Cluster) string {
	if d, ok := c.Resolve(""); ok && d.FitsOn(cl) {
		return c.def
	}
	best := -1
	for i, s := range c.sizes {
		if s.GPU > 0 || !cl.Holds(s.Need()) {
			continue
		}
		if best < 0 || s.machine().Bigger(c.sizes[best].machine()) {
			best = i
		}
	}
	if best < 0 {
		return c.def
	}
	return c.sizes[best].Name
}

func (s Size) machine() capacity.Machine { return capacity.Machine{CPU: s.CPU, Memory: s.Memory} }
