// Package sizing holds the size rules apps and jobs share: which sizes and
// custom amounts a person may use and which fit the cluster's machines.
package sizing

import (
	"context"
	"math"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
)

// Size is the API view of one admin-defined app size.
type Size struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"displayName"`
	CPU         float64 `json:"cpu" doc:"CPU cores, e.g. 0.5"`
	Memory      int64   `json:"memory" doc:"Memory in bytes"`
	GPU         int64   `json:"gpu,omitempty" doc:"GPUs an app at this size gets; absent for a size without"`
	Default     bool    `json:"default" doc:"The size new apps get when none is picked: the configured default when it fits, else the largest size that fits"`
	Fits        bool    `json:"fits" doc:"Some machine in the cluster is big enough for it (or the admin lets it wait for one)"`
	Reason      string  `json:"reason,omitempty" doc:"Why it does not fit, when it does not"`
	ReasonShort string  `json:"reasonShort,omitempty" doc:"Reason in a few words, for the size's card"`
}

// Amount is a custom CPU and memory, in the units of Size.
type Amount struct {
	CPU    float64 `json:"cpu" minimum:"0" doc:"CPU cores, e.g. 2.5 (whole thousandths)"`
	Memory int64   `json:"memory" minimum:"0" doc:"Memory in bytes"`
}

func (a Amount) quantities() (cpu, mem resource.Quantity) {
	return *resource.NewMilliQuantity(int64(math.Round(a.CPU*1000)), resource.DecimalSI),
		*resource.NewQuantity(a.Memory, resource.BinarySI)
}

// AmountOf is the Amount of these quantities.
func AmountOf(cpu, mem resource.Quantity) Amount {
	return Amount{CPU: float64(cpu.MilliValue()) / 1000, Memory: mem.Value()}
}

// CustomAllowance is how much custom CPU and memory the caller may ask for.
type CustomAllowance struct {
	Allowed    bool    `json:"allowed" doc:"The caller may ask for a custom amount"`
	MaxCPU     float64 `json:"maxCPU" doc:"Most CPU cores the caller may ask for (their limit or the largest machine, the lower); 0 when not known"`
	MaxMemory  int64   `json:"maxMemory" doc:"Most memory in bytes, as maxCPU; 0 when not known"`
	MinCPU     float64 `json:"minCPU"`
	MinMemory  int64   `json:"minMemory"`
	CPUStep    float64 `json:"cpuStep" doc:"The UI offers CPU in these steps"`
	MemoryStep int64   `json:"memoryStep" doc:"The UI offers memory in these steps (bytes)"`
	Limit      string  `json:"limit,omitempty" doc:"The catalog text for an amount over maxCPU or maxMemory"`
	Minimum    string  `json:"minimum,omitempty" doc:"The catalog text for an amount under minCPU or minMemory"`
}

type sizeListOutput struct {
	Body struct {
		Items  []Size          `json:"items" doc:"The sizes the caller may use"`
		Custom CustomAllowance `json:"custom"`
	}
}

// SizeInput changes the size of the app or job it names. (Its name is the
// OpenAPI schema name of its body.)
type SizeInput struct {
	Name  string `path:"name" maxLength:"63"`
	Owner string `query:"owner" maxLength:"253" doc:"Identity name of the app's owner; default the caller"`
	Body  struct {
		Size      string  `json:"size,omitempty" maxLength:"63" doc:"One of the names from GET /api/sizes"`
		Resources *Amount `json:"resources,omitempty" doc:"A custom amount instead of a size, for people the admin allows"`
	}
}

// Rules is the size rules apps and jobs share: the catalog, the machines
// sizes must fit, and the roles that say who may use which size and how much
// custom CPU and memory.
type Rules struct {
	Sizes sizes.Catalog
	// Machines reads the cluster's nodes (see gateway.Config.Machines).
	Machines capacity.Source
	Roles    config.Roles
}

// Same reports whether stored (a size name and a custom amount) is
// already what sz stores.
func Same(name string, custom *model.AppResources, sz sizes.Size) bool {
	n, c := sz.Stored()
	return n == name && c.Same(custom)
}

// Shown is what an app or a job runs at, for its view: its custom amount, or
// its size name, the default for one stored without (made before sizes).
func (s *Rules) Shown(name string, custom *model.AppResources) (string, *Amount) {
	if custom != nil {
		a := AmountOf(custom.CPU, custom.Memory)
		return name, &a
	}
	if name == "" {
		name = s.Sizes.Default()
	}
	return name, nil
}

// Usable is the catalog with only the sizes u may use (authz.ActUseSize).
func (s *Rules) Usable(u authz.Principal) sizes.Catalog {
	roles := authz.RolesOf(u, s.Roles)
	return s.Sizes.Only(func(sz sizes.Size) bool {
		return authz.Decide(u, roles, authz.ActUseSize, authz.Object{Size: &sz}).Allowed
	})
}

// allowance is u's custom amount allowance, capped at the largest machine.
func (s *Rules) allowance(u authz.Principal, cl capacity.Cluster) CustomAllowance {
	a, ok := authz.CustomAllowance(u, authz.RolesOf(u, s.Roles), s.Sizes.Custom())
	if !ok {
		return CustomAllowance{}
	}
	maxCPU, maxMem := a.MaxCPU, a.MaxMemory
	if m := cl.LargestMachine(); cl.Known() {
		if maxCPU.IsZero() || m.CPU.Cmp(maxCPU) < 0 {
			maxCPU = m.CPU
		}
		if maxMem.IsZero() || m.Memory.Cmp(maxMem) < 0 {
			maxMem = m.Memory
		}
	}
	lim, mins, steps := AmountOf(maxCPU, maxMem), AmountOf(sizes.MinCPU, sizes.MinMemory), AmountOf(sizes.CPUStep, sizes.MemoryStep)
	out := CustomAllowance{
		Allowed: true, MaxCPU: lim.CPU, MaxMemory: lim.Memory,
		MinCPU: mins.CPU, MinMemory: mins.Memory, CPUStep: steps.CPU, MemoryStep: steps.Memory,
		Minimum: minimumText(),
	}
	if words := (authz.Allowance{MaxCPU: maxCPU, MaxMemory: maxMem}).Limit(); words != "" {
		out.Limit = reasons.Msg(reasons.MsgCustomSizeOverLimit, words)
	}
	return out
}

// Allowed resolves what a new or changed app may run at: a named size
// ("" the caller's default) or a custom amount, which the caller may use
// (authz.Decide) and some machine can hold.
func (s *Rules) Allowed(ctx context.Context, name string, amount *Amount) (sizes.Size, error) {
	u := authn.User(ctx)
	cl, err := s.Machines.Cluster(ctx)
	if err != nil {
		return sizes.Size{}, problem.Internal("list nodes", err)
	}
	if amount != nil {
		return s.customSize(u, cl, name, *amount)
	}
	mine := s.Usable(u)
	if name == "" {
		name = mine.DefaultOn(cl)
	}
	sz, ok := mine.Resolve(name)
	switch {
	case !ok:
		return sz, problem.FieldError("size", reasons.MsgSizeUnavailable)
	case !sz.FitsOn(cl):
		return sz, problem.Fields(problem.Field{Name: "size", Msg: sizeTooBig(cl, sz)})
	}
	return sz, nil
}

// AllowedFor is Allowed for an app or a job from template t. With no size
// and no custom amount, t's DefaultResources (when set) decide: as a custom
// amount when the caller may ask for that one and a machine holds it, else
// the smallest size the caller may use that holds it (least memory, then
// least CPU; never a GPU size), else the caller's default size.
func (s *Rules) AllowedFor(ctx context.Context, t *model.Template, name string, amount *Amount) (sizes.Size, error) {
	def := t.Spec.DefaultResources
	if name != "" || amount != nil || def == nil {
		return s.Allowed(ctx, name, amount)
	}
	u := authn.User(ctx)
	cl, err := s.Machines.Cluster(ctx)
	if err != nil {
		return sizes.Size{}, problem.Internal("list nodes", err)
	}
	d := authz.Decide(u, authz.RolesOf(u, s.Roles), authz.ActCustomSize, authz.Object{Custom: &authz.CustomAsk{CPU: def.CPU, Memory: def.Memory, Policy: s.Sizes.Custom()}})
	if d.Allowed && cl.Fits(def.CPU, def.Memory) {
		return sizes.Custom(def.CPU, def.Memory), nil
	}
	var best *sizes.Size
	for _, sz := range s.Usable(u).List() {
		if sz.GPU > 0 || sz.CPU.Cmp(def.CPU) < 0 || sz.Memory.Cmp(def.Memory) < 0 || !sz.FitsOn(cl) {
			continue
		}
		if best == nil || sz.Memory.Cmp(best.Memory) < 0 || (sz.Memory.Cmp(best.Memory) == 0 && sz.CPU.Cmp(best.CPU) < 0) {
			best = &sz
		}
	}
	if best != nil {
		return *best, nil
	}
	return s.Allowed(ctx, "", nil)
}

func (s *Rules) customSize(u authz.Principal, cl capacity.Cluster, name string, amount Amount) (sizes.Size, error) {
	if name != "" {
		return sizes.Size{}, problem.FieldError("resources", reasons.MsgSizeOrCustom)
	}
	cpu, mem := amount.quantities()
	if cpu.Cmp(sizes.MinCPU) < 0 || mem.Cmp(sizes.MinMemory) < 0 {
		return sizes.Size{}, problem.Fields(problem.Field{Name: "resources", Msg: minimumText()})
	}
	d := authz.Decide(u, authz.RolesOf(u, s.Roles), authz.ActCustomSize, authz.Object{Custom: &authz.CustomAsk{CPU: cpu, Memory: mem, Policy: s.Sizes.Custom()}})
	switch {
	case d.Code == reasons.MsgCustomSizeOverLimit:
		return sizes.Size{}, problem.FieldError("resources", d.Code, d.Args...)
	case !d.Allowed:
		return sizes.Size{}, problem.New(http.StatusForbidden, reasons.Msg(d.Code, d.Args...))
	case !cl.Fits(cpu, mem):
		return sizes.Size{}, problem.Fields(problem.Field{Name: "resources", Msg: tooBig(cl)})
	}
	return sizes.Custom(cpu, mem), nil
}

// minimumText is the catalog text for a custom amount under the minimum.
func minimumText() string {
	return reasons.Msg(reasons.MsgCustomSizeTooSmall, capacity.CPU(sizes.MinCPU), capacity.Memory(sizes.MinMemory))
}

// sizeTooBig is the reason sz does not fit cl: tooBig for a size of only CPU
// and memory, else what it needs that no machine has all of.
func sizeTooBig(cl capacity.Cluster, sz sizes.Size) string {
	if need := sz.Need(); !need.Plain() {
		return reasons.Msg(reasons.MsgSizeNoMachine, need.Words())
	}
	return tooBig(cl)
}

// tooBig is the reason a CPU and memory amount does not fit cl.
func tooBig(cl capacity.Cluster) string {
	cpu, mem := cl.Largest()
	return reasons.Msg(reasons.MsgSizeTooBig, cpu, mem)
}

// RegisterList adds GET /api/sizes: the sizes the caller may use.
func (s *Rules) RegisterList(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "listSizes", Method: http.MethodGet, Path: "/api/sizes",
		Summary: "Sizes the caller can run an app at, and their custom amount allowance", Tags: []string{"workspaces"},
		Errors: []int{http.StatusUnauthorized},
	}, func(ctx context.Context, _ *struct{}) (*sizeListOutput, error) {
		u := authn.User(ctx)
		cl, err := s.Machines.Cluster(ctx)
		if err != nil {
			return nil, problem.Internal("list nodes", err)
		}
		mine := s.Usable(u)
		def := mine.DefaultOn(cl)
		out := &sizeListOutput{}
		out.Body.Items = []Size{}
		for _, sz := range mine.List() {
			item := Size{
				Name: sz.Name, DisplayName: sz.DisplayName,
				CPU: float64(sz.CPU.MilliValue()) / 1000, Memory: sz.Memory.Value(), GPU: sz.GPU,
				Default: sz.Name == def, Fits: sz.FitsOn(cl),
			}
			if !item.Fits {
				item.Reason, item.ReasonShort = sizeTooBig(cl, sz), reasons.Msg(reasons.MsgSizeTooBigShort)
			}
			out.Body.Items = append(out.Body.Items, item)
		}
		out.Body.Custom = s.allowance(u, cl)
		return out, nil
	})
}
