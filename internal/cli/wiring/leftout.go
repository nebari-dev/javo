package wiring

import "fmt"

// LeftOut is what a binary built with the tag local says when it is
// asked for what, which it does not carry (LocalBuild).
func LeftOut(what string) error {
	return fmt.Errorf("%s is left out of a javo built with the tag local, which carries only what javo local runs: use the javo of a release or of the image", what)
}
