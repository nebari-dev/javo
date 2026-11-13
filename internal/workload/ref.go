package workload

import (
	"fmt"
	"net"
	"strconv"
)

// Ref names one workload: where it runs and which row it is for.
type Ref struct {
	// Namespace and Name are where an executor keeps it. Name is unique in
	// the namespace.
	Namespace, Name string
	// ID is the id of the row it runs for (an app or a job), the only link
	// back to it.
	ID string
	// Owner is the name of the person it runs for.
	Owner string
}

// ServiceAddr is where a workload kept as a Service of a cluster listens
// on port: the Service by its name in its namespace.
func (r Ref) ServiceAddr(port int32) string {
	return net.JoinHostPort(fmt.Sprintf("%s.%s.svc", r.Name, r.Namespace), strconv.Itoa(int(port)))
}
