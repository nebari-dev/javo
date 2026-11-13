package executor

import "fmt"

// UnsupportedError is a part of a workload that cannot be run without
// containers. The template (or the plan made from it) has to change: it
// is shown as reasons.TemplateInvalid, not tried again.
type UnsupportedError struct {
	// Unit is the unit it is about; empty when it is the whole workload.
	Unit string
	Why  string
}

func (e *UnsupportedError) Error() string {
	if e.Unit == "" {
		return "process executor: " + e.Why
	}
	return fmt.Sprintf("process executor: unit %s: %s", e.Unit, e.Why)
}
