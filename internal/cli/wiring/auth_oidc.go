//go:build !local

package wiring

import (
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/features/authoidc"
)

// oidcProvider signs people in through the identity provider c names.
// Discovery runs in the background for the life of the process.
func oidcProvider(c config.OIDC, urls config.URLs) authprovider.Provider {
	return authoidc.New(c, urls, nil)
}
