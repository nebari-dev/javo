//go:build local

package bundle

import "embed"

// assets holds bundle.zip once `make local` has written it; the folder is
// embedded whole so that a build with the tag and without the archive
// still compiles, and carries none.
//
//go:embed all:assets
var assets embed.FS

// archive is the archive this binary carries; nothing when it was built
// before `make local` wrote one.
func archive() []byte {
	data, _ := assets.ReadFile("assets/bundle.zip")
	return data
}
