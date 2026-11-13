//go:build !local

package wiring

import (
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/config"
	"testing"
)

func TestOIDCProviderIsRegisteredOnlyWhenConfigured(t *testing.T) {
	var p config.Platform
	p.Auth.Local.Enabled = true
	if _, ok := AuthProviders(p, &local.DBUsers{}, nil).Get("oidc"); ok {
		t.Fatal("oidc provider registered without auth.oidc")
	}
	p.Auth.OIDC = &config.OIDC{Issuer: "http://idp.test/realms/javo", ClientID: "javo"}
	r := AuthProviders(p, &local.DBUsers{}, nil)
	got, ok := r.Get("oidc")
	if !ok {
		t.Fatal("oidc provider missing with auth.oidc")
	}
	if c, ok := got.(interface{ Close() }); ok {
		c.Close()
	}
}
