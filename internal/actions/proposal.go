// Package actions is the catalog of typed changes that diagnosis rules, a
// model, the command palette and agents may propose. A proposal changes
// nothing: it becomes a plan that a signed-in person approves (internal/plans).
package actions

// The ids of the app actions. A proposal names one of these.
const (
	Start   = "start"
	Stop    = "stop"
	Restart = "restart"
	Delete  = "delete"
	// Resize runs the app at another named size (param "size").
	Resize = "resize"
)

// Proposal names one catalog action, the object it targets (an app's short
// name) and its typed parameters.
type Proposal struct {
	Action string            `json:"action"`
	Target string            `json:"target"`
	Params map[string]string `json:"params,omitempty"`
}
