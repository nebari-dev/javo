package workload

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
