package jobs

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Access decides whether a job's owner may still use a template. owner is the
// person's name, the value in the job's owner column.
type Access interface {
	MayUse(ctx context.Context, owner string, t *model.Template) (bool, error)
}

// PeopleAccess decides from the owner's person row, since a scheduled run has
// no login to read groups from: the groups the provider reported at the
// owner's last login and the owner's name, by the same rule the API applies
// (authz UseTemplate). An owner without a row may use nothing.
type PeopleAccess struct {
	People identity.PersonByName
	Roles  config.Roles
}

func (a PeopleAccess) MayUse(ctx context.Context, owner string, t *model.Template) (bool, error) {
	person, err := a.People.ByName(ctx, owner)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	p := identity.PrincipalOf(person)
	return authz.Decide(p, authz.RolesOf(p, a.Roles), authz.ActUseTemplate, authz.Object{Template: t}).Allowed, nil
}
