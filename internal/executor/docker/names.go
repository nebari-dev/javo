package docker

import (
	"fmt"
	"regexp"

	"github.com/nebari-dev/javo/internal/workload"
)

// Sandbox and Setup are the unit names of the two containers of a
// workload that run none of its units: the one that holds its network and
// secret files, and the one, never started, its volumes are prepared
// through.
const (
	Sandbox = "sandbox"
	Setup   = "setup"
)

// part is what a namespace, a workload, a unit and a volume may be called:
// lower-case letters, digits and dashes. Names of containers, networks and
// volumes join parts with dots, so two workloads never share one.
var part = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// base is the name of a workload's network and of its sandbox.
func base(ref workload.Ref) (string, error) {
	if !part.MatchString(ref.Namespace) || !part.MatchString(ref.Name) {
		return "", fmt.Errorf("workload %q in %q: both names must be lower-case letters, digits and dashes", ref.Name, ref.Namespace)
	}
	return "javo-" + ref.Namespace + "." + ref.Name, nil
}

// NetworkName is the name of the workload's own network, and SandboxName
// the name of the container that holds its place on it. The gateway
// reaches the workload's port at the sandbox's name or address.
func NetworkName(ref workload.Ref) (string, error) { return base(ref) }
func SandboxName(ref workload.Ref) (string, error) { return base(ref) }

// ContainerName is the name of the container that runs the workload's unit.
func ContainerName(ref workload.Ref, unit string) (string, error) {
	return sub(ref, "unit", unit)
}

// VolumeName is the name of the Docker volume behind the workload's own
// volume (an empty folder or secret files).
func VolumeName(ref workload.Ref, volume string) (string, error) {
	return sub(ref, "volume", volume)
}

func sub(ref workload.Ref, kind, name string) (string, error) {
	b, err := base(ref)
	if err != nil {
		return "", err
	}
	if !part.MatchString(name) {
		return "", fmt.Errorf("%s %q: the name must be lower-case letters, digits and dashes", kind, name)
	}
	return b + "." + name, nil
}
