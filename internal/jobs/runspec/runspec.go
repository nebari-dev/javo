// Package runspec is the JSON a run carries in env JAVO_RUN. The controller and
// gateway write it, the runner executes it and status reads its parameters
// back, so all three share this one definition.
package runspec

import "time"

// Param is one typed input; Value is text that must parse as Type.
type Param struct{ Name, Type, Value string }

// Spec is the JSON in env JAVO_RUN, rendered by the controller or gateway.
type Spec struct {
	Definition  string
	Engine      string
	Source      string // relative to home
	Kernel      string
	Command     []string
	Params      []Param
	CellTimeout time.Duration
	KeepRuns    int
}
