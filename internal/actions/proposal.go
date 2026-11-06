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
	// AttachSecret gives the app one of its owner's secrets as a variable
	// (params "secret" and "env", env defaulting to the secret's name).
	AttachSecret = "attach-secret"
	// DetachSecret takes the secret the app gets as variable "env" off it.
	DetachSecret = "detach-secret"
)

// Proposal names one catalog action, the object it targets (an app's short
// name and its owner) and its typed parameters. An empty Owner means the
// person who proposes it; a checked proposal (Planned) always names it, so a
// plan an admin makes for another person's app acts on that app.
type Proposal struct {
	Action string            `json:"action"`
	Target string            `json:"target"`
	Owner  string            `json:"owner,omitempty" maxLength:"253" doc:"Identity name of the app's owner; default the person proposing"`
	Params map[string]string `json:"params,omitempty"`
}
