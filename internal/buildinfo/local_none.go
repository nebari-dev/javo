//go:build !local

package buildinfo

// Local says whether this binary was built with the tag local: not
// this one, which carries everything Javo runs on.
const Local = false
