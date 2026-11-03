// Package choose picks the home mode from what the cluster offers and what
// the admin asked for. It is pure; it reads no cluster.
package choose

import (
	"cmp"
	"slices"

	"github.com/nebari-dev/javo/internal/model"
)

// Who made the choice.
const (
	SourceAuto     model.ChoiceSource = "auto"
	SourceOverride model.ChoiceSource = "override"
)

// Choose returns the mode and classes in effect.
//
// A class can hold shared homes only when it offers ReadWriteMany files and
// keeps POSIX ownership, and both facts are known, verified or set by an
// admin. A class whose facts are unknown, or whose probe failed, is never a
// shared candidate. SharedClass is the first candidate (default class first,
// then by name), or the class the admin named for homes when that one
// qualifies; it is reported even when the mode is perUser.
//
// Auto mode picks shared when a candidate exists, otherwise perUser on the
// class the admin named or the default class. Naming a class that cannot be
// shared keeps auto on perUser. An admin's mode always wins; asking for shared
// where no class qualifies still returns shared, and the reconciler reports
// that the cluster cannot do it. A class the admin named is always the home
// class, even when it cannot be shared.
func Choose(caps []model.ClassCapability, nodes int, spec model.ClusterStorageSpec) model.StorageChoice {
	sorted := slices.SortedFunc(slices.Values(caps), func(a, b model.ClassCapability) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	named := spec.Home.StorageClassName

	sharedClass := ""
	if i := slices.IndexFunc(sorted, func(c model.ClassCapability) bool { return c.Name == named && CanShare(c) }); i >= 0 {
		sharedClass = named
	} else if i := slices.IndexFunc(sorted, CanShare); i >= 0 {
		sharedClass = sorted[i].Name
	}

	perUserClass := named
	if perUserClass == "" {
		if i := slices.IndexFunc(sorted, func(c model.ClassCapability) bool { return c.IsDefault }); i >= 0 {
			perUserClass = sorted[i].Name
		}
	}

	out := model.StorageChoice{SingleNode: nodes == 1, SharedClass: sharedClass, Source: SourceAuto}
	switch spec.Home.Mode {
	case model.HomeModePerUser, model.HomeModeShared:
		out.Mode, out.Source = spec.Home.Mode, SourceOverride
	default:
		out.Mode = model.HomeModePerUser
		if sharedClass != "" && (named == "" || named == sharedClass) {
			out.Mode = model.HomeModeShared
		}
	}
	if out.Mode == model.HomeModeShared {
		out.HomeClass = cmp.Or(named, sharedClass)
	} else {
		out.HomeClass = perUserClass
	}
	return out
}

// CanShare says whether a class can hold every user's home on one shared
// drive.
func CanShare(c model.ClassCapability) bool {
	trusted := func(f model.Fact) bool {
		return f.Value && (f.Provenance == model.ProvenanceKnown ||
			f.Provenance == model.ProvenanceVerified || f.Provenance == model.ProvenanceOverride)
	}
	return trusted(c.RWXFile) && trusted(c.PosixOwnership)
}
