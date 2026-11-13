package workload

// EnvToken is the variable a workload reads its owner's API token from.
const EnvToken = "JAVO_TOKEN"

// Fingerprint is what a running workload was built from, as far as a change
// to it means the workload must be built again. An executor records it on
// the workload and reads it back.
type Fingerprint struct {
	// Size is the size it runs at; empty on one built before sizes were
	// recorded.
	Size string
	// SizeUnpicked is true when the app names no size or amount of its own.
	// It describes the app, so it is never recorded.
	SizeUnpicked bool
	// Run covers the command, image, port, variables and Git source.
	Run string
	// Restart is the latest restart request.
	Restart string
	// APIAccess is the API access it was given.
	APIAccess string
	// GitToken is the generation of the repository token it reads.
	GitToken string
	// Secrets covers the secrets it reads.
	Secrets string
	// Layout covers what it mounts and as which user; empty when that
	// cannot be known now.
	Layout string
}

// Outdated reports whether a workload built from have must be built again
// to match want.
func (want Fingerprint) Outdated(have Fingerprint) bool {
	// A workload with no recorded size, of an app that never picked one,
	// keeps running: an upgrade must not restart every app.
	sizeChanged := have.Size != want.Size && (have.Size != "" || !want.SizeUnpicked)
	// A layout that cannot be known now is not compared.
	layoutChanged := want.Layout != "" && have.Layout != want.Layout
	return sizeChanged || layoutChanged ||
		have.Run != want.Run || have.Restart != want.Restart || have.APIAccess != want.APIAccess ||
		have.GitToken != want.GitToken || have.Secrets != want.Secrets
}
