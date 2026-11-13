//go:build !local

package wiring

// LocalBuild says whether this binary was built with the tag local: not
// this one, which carries everything Javo runs on.
const LocalBuild = false
