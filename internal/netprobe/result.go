// Package netprobe finds out whether the platform keeps apps apart: whether
// the cluster enforces NetworkPolicy on Kubernetes, and whether a workload's
// network reaches nothing but the gateway on Docker. Apps whose template has
// no login of their own are safe only when it does, so the controller probes
// at start and every few hours, stores the answer (Store), and the controller
// and gateway read it.
//
// A probe is anything with the Probe method (Probe, in loop.go); the store,
// the loop and what reads the answer are the same for every platform.
//
// On Kubernetes (the Prober of internal/netprobe/kubeprobe) the probe starts a listener pod and a dialer pod in
// the apps namespace, where the allow-gateway-only policy covers the listener
// and the dialer has no gateway labels. (The release namespace holds the
// gateway's Secrets, so the controller may not create pods there.) It reports
// Enforced only when the dialer reaches the gateway (the control) and does not
// reach the listener.
//
// On Docker (the Prober of internal/netprobe/dockerprobe) the listener and
// the dialer are containers on two workload networks, and the dialer must
// reach the gateway and nothing else.
package netprobe

import "time"

// State is what the probe concluded.
type State string

const (
	// Enforced: the dialer reached the gateway but not the listener.
	Enforced State = "Enforced"
	// NotEnforced: the dialer reached the listener.
	NotEnforced State = "NotEnforced"
	// Unknown: the probe could not tell, or has not run. It counts as not
	// enforced wherever the answer gates something.
	Unknown State = "Unknown"
)

// Result is one probe's answer with the lines behind it.
type Result struct {
	State     State     `json:"state"`
	CheckedAt time.Time `json:"checkedAt"`
	Evidence  []string  `json:"evidence,omitempty"`
}
