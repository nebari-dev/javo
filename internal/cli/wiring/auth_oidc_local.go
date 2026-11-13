//go:build local

package wiring

import (
	"github.com/nebari-dev/javo/internal/buildinfo"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
)

// oidcProvider is the single sign-on of a binary built with the tag
// local: none. `javo local` signs its one person in through a link, so
// the OpenID Connect client is not carried, and the server refuses a
// config that names an identity provider before it gets here.
func oidcProvider(config.OIDC, config.URLs) authprovider.Provider {
	panic(buildinfo.LeftOut("single sign-on (auth.oidc)"))
}
