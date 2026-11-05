// Package buildinfo says which build of javo is running: the release tag
// and the commit it was built from.
package buildinfo

import "runtime/debug"

// Set at link time (scripts/build/cross-build-javo.sh, the Dockerfile):
//
//	-X github.com/nebari-dev/javo/internal/buildinfo.version=v0.2.22
//	-X github.com/nebari-dev/javo/internal/buildinfo.commit=<full sha>
var version, commit string

// Info is the running build. Version is the release tag, or "dev" for a
// build that is not a release; Commit is the full commit sha, empty when
// the build does not know it.
type Info struct {
	Version string
	Commit  string
}

// Get returns the running build.
func Get() Info { return resolve(version, commit, debug.ReadBuildInfo) }

// resolve fills what the linker did not set: the version is "dev" and the
// commit is the one Go stamps into a build made inside a git checkout.
func resolve(v, c string, read func() (*debug.BuildInfo, bool)) Info {
	if v == "" {
		v = "dev"
	}
	if c == "" {
		if bi, ok := read(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" {
					c = s.Value
				}
			}
		}
	}
	return Info{Version: v, Commit: c}
}
