//go:build !local

package bundle

// archive is the archive this binary carries: none, without the tag local.
func archive() []byte { return nil }
