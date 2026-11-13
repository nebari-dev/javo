package executor

import "fmt"

// UnsupportedError is a part of a workload that cannot be run without
// containers, as plain processes of a machine: whoever plans one for
// such a machine, or runs it there, says so with it. The template (or
// the plan made from it) has to change: it is shown as
// reasons.TemplateInvalid, not tried again.
type UnsupportedError struct {
	// Unit is the unit it is about; empty when it is the whole workload.
	Unit string
	Why  string
}

func (e *UnsupportedError) Error() string {
	if e.Unit == "" {
		return "cannot run without containers: " + e.Why
	}
	return fmt.Sprintf("cannot run without containers: unit %s: %s", e.Unit, e.Why)
}
