package docker

import (
	"errors"
	"fmt"

	"github.com/nebari-dev/javo/internal/workload"
)

// The labels on everything this package makes. LabelManaged marks it as
// Javo's: nothing without it is ever removed. The others say which
// workload it is for, so a list, the events and the sweep find it.
const (
	LabelManaged   = "javo.managed"
	LabelNamespace = "javo.namespace"
	LabelName      = "javo.name"
	LabelID        = "javo.id"
	LabelOwner     = "javo.owner"
	// LabelUnit is the unit a container runs; the sandbox, the setup
	// container and a run's record carry Sandbox, Setup and Record.
	LabelUnit = "javo.unit"
	// LabelRun marks what belongs to a job run. The executor of runs puts
	// it on everything it makes and finds only that; the executor of apps
	// passes it over.
	LabelRun = "javo.run"

	labelFingerprint = "javo.fingerprint."
)

// Managed is the label filter that finds everything of Javo's.
const Managed = LabelManaged + "=true"

// ErrForeign means a container, network or volume does not carry Javo's
// label. It is never removed or written to.
var ErrForeign = errors.New("is not Javo's")

func foreign(kind, name string) error { return fmt.Errorf("%s %q %w", kind, name, ErrForeign) }

// isRun reports whether labels mark a thing as a job run's.
func isRun(labels map[string]string) bool { return labels[LabelRun] == "true" }

// Ours reports whether labels mark a thing as Javo's.
func Ours(labels map[string]string) bool { return labels[LabelManaged] == "true" }

// Labels mark a container, network or volume as part of the workload ref.
func Labels(ref workload.Ref) map[string]string {
	return map[string]string{
		LabelManaged: "true", LabelNamespace: ref.Namespace, LabelName: ref.Name,
		LabelID: ref.ID, LabelOwner: ref.Owner,
	}
}

// Selector is the label filter that finds what belongs to the workload
// ref.
func Selector(ref workload.Ref) []string {
	return []string{Managed, LabelNamespace + "=" + ref.Namespace, LabelName + "=" + ref.Name}
}

// RefOf reads the workload a thing belongs to from its labels.
func RefOf(labels map[string]string) workload.Ref {
	return workload.Ref{Namespace: labels[LabelNamespace], Name: labels[LabelName], ID: labels[LabelID], Owner: labels[LabelOwner]}
}

// fingerprintLabels record f, one label per value that is set.
func fingerprintLabels(f workload.Fingerprint) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{
		"size": f.Size, "run": f.Run, "restart": f.Restart, "api-access": f.APIAccess,
		"git-token": f.GitToken, "secrets": f.Secrets, "layout": f.Layout,
	} {
		if v != "" {
			out[labelFingerprint+k] = v
		}
	}
	return out
}

// FingerprintOf reads back what a container was built from.
func FingerprintOf(labels map[string]string) workload.Fingerprint {
	l := func(k string) string { return labels[labelFingerprint+k] }
	return workload.Fingerprint{
		Size: l("size"), Run: l("run"), Restart: l("restart"), APIAccess: l("api-access"),
		GitToken: l("git-token"), Secrets: l("secrets"), Layout: l("layout"),
	}
}
