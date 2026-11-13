package docker

import "github.com/nebari-dev/javo/internal/workload"

// StorageName is the name of the Docker volume behind storage that
// outlives every workload (a person's home), called name in namespace:
// "javo-<namespace>.<name>". Both must be lower-case letters, digits and
// dashes, so the name has two parts; a workload's own volumes have three
// (VolumeName), and none is ever one of these.
func StorageName(namespace, name string) (string, error) {
	return base(workload.Ref{Namespace: namespace, Name: name})
}
