package identity

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/podspec"
	"github.com/nebari-dev/javo/internal/store"
)

// PersonByName finds a person by name; store.People implements it.
type PersonByName interface {
	ByName(ctx context.Context, name string) (model.Person, error)
}

// PodUIDOf is the uid owner's pods (apps and job runs) run as under uidMode:
// podspec.FixedUID unless uids are allocated, else the uid on owner's person
// row. ok is false while an allocated uid is not there yet (no row, or none
// allocated); nothing may run as the owner until it is.
func PodUIDOf(ctx context.Context, people PersonByName, uidMode, owner string) (uid int64, ok bool, err error) {
	if uidMode != config.UIDModeAllocated {
		return podspec.FixedUID, true, nil
	}
	switch p, err := people.ByName(ctx, owner); {
	case errors.Is(err, store.ErrNotFound):
		return 0, false, nil
	case err != nil:
		return 0, false, err
	case p.UID == 0:
		return 0, false, nil
	default:
		return p.UID, true, nil
	}
}
