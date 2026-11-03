package wiring

import (
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
)

// With ai.enabled false, no provider is built, so nothing ever connects to a
// model, even with a provider and key configured.
func TestAIOffBuildsNoProvider(t *testing.T) {
	called := 0
	var r provider.Registry
	r.Register("spy", func(provider.Config) (provider.Provider, error) { called++; return aitest.New(), nil })
	cfg := config.AICfg{Enabled: false, Provider: "spy", Model: "m"}
	p, err := newAIProvider(cfg, "a-key", &r)
	if err != nil || p != nil || called != 0 {
		t.Fatalf("AI off built %v (%v), factory called %d times", p, err, called)
	}
	cfg.Enabled = true
	if _, err := newAIProvider(cfg, "a-key", &r); err != nil || called != 1 {
		t.Fatalf("AI on: %v, factory called %d times", err, called)
	}
}

func TestAuthProvidersFollowTheConfig(t *testing.T) {
	var on, off config.Platform
	on.Auth.Local.Enabled = true
	users := &local.DBUsers{}

	if _, ok := AuthProviders(on, users).Get("local"); !ok {
		t.Fatal("local provider missing with auth.local.enabled")
	}
	if n := len(AuthProviders(off, users).All()); n != 0 {
		t.Fatalf("%d providers with auth.local disabled", n)
	}
}

func TestOIDCProviderIsRegisteredOnlyWhenConfigured(t *testing.T) {
	var p config.Platform
	p.Auth.Local.Enabled = true
	if _, ok := AuthProviders(p, &local.DBUsers{}).Get("oidc"); ok {
		t.Fatal("oidc provider registered without auth.oidc")
	}
	p.Auth.OIDC = &config.OIDC{Issuer: "http://idp.test/realms/javo", ClientID: "javo"}
	r := AuthProviders(p, &local.DBUsers{})
	got, ok := r.Get("oidc")
	if !ok {
		t.Fatal("oidc provider missing with auth.oidc")
	}
	if c, ok := got.(interface{ Close() }); ok {
		c.Close()
	}
}

func TestExposureBackendFollowsURLsMode(t *testing.T) {
	for mode, want := range map[string]string{"path": "path", "subdomain": "subdomain"} {
		var p config.Platform
		p.URLs.Mode = mode
		if got := Exposure(p).ID(); got != want {
			t.Errorf("urls.mode %q: backend %q", mode, got)
		}
	}
}

func TestPlacementFollowsNamespacesMode(t *testing.T) {
	for _, mode := range []string{"shared", "per-user", "per-team"} {
		var p config.Platform
		p.Namespaces.Mode = mode
		if got := Placement(p).ID(); got != mode {
			t.Errorf("namespaces.mode %q: placement %q", mode, got)
		}
	}
}

func TestEngineRegistryHoldsTheVerifiedEngines(t *testing.T) {
	r := Engines()
	var ids []string
	for _, e := range r.All() {
		ids = append(ids, e.ID())
	}
	if want := []string{"bash", "command", "marimo", "papermill", "quarto"}; !slices.Equal(ids, want) {
		t.Errorf("engines = %v, want %v", ids, want)
	}
	byExt := func(ext string) []string {
		var out []string
		for _, e := range r.All() {
			if slices.Contains(e.Extensions(), ext) {
				out = append(out, e.ID())
			}
		}
		return out
	}
	if got := byExt(".ipynb"); !slices.Equal(got, []string{"papermill", "quarto"}) {
		t.Errorf(".ipynb = %v", got)
	}
	// No engine runs .jl files: Pluto.jl was dropped (DEPENDENCIES.md).
	if got := byExt(".jl"); len(got) != 0 {
		t.Errorf(".jl = %v, want none", got)
	}
}

func TestStorageRegistryHoldsBothBackends(t *testing.T) {
	r := newStorageRegistry()
	for id, mode := range map[string]model.HomeMode{
		"storage.per-user": model.HomeModePerUser,
		"storage.shared":   model.HomeModeShared,
	} {
		b, ok := r.Get(id)
		if !ok || b.Mode() != mode {
			t.Fatalf("Get(%q) = %v, %v; want mode %s", id, b, ok, mode)
		}
	}
}

func TestStorageBackendsAreIndexedByTheModeTheyServe(t *testing.T) {
	m := StorageBackendsByMode()
	for _, mode := range []model.HomeMode{model.HomeModePerUser, model.HomeModeShared} {
		if b, ok := m[mode]; !ok || b.Mode() != mode {
			t.Errorf("mode %s: %v, %v", mode, b, ok)
		}
	}
}

func TestCodeHostsServeGitHubRepositories(t *testing.T) {
	hosts := CodeHosts(config.Platform{}).All()
	if len(hosts) != 1 || hosts[0].ID() != "github" {
		t.Fatalf("code hosts = %v", hosts)
	}
	if !hosts[0].Owns("https://github.com/example-org/site") || hosts[0].Owns("https://gitlab.com/example-org/site") {
		t.Fatal("the GitHub host does not own exactly the github.com repositories")
	}
}

// With nebi.enabled false the gateway gets no environment source, so no form
// offers environments and nothing talks to Nebi, even with a URL set.
func TestNebiOffWiresNoEnvironmentSource(t *testing.T) {
	p := config.Platform{Nebi: config.NebiCfg{URL: "https://nebi.example.com"}}
	if src := EnvironmentSource(p); src != nil {
		t.Fatalf("nebi off wired %T", src)
	}
	p.Nebi.Enabled = true
	if src := EnvironmentSource(p); src == nil || src.ID() != "nebi" {
		t.Fatalf("nebi on wired %v", src)
	}
}

// Apps get their owner's Nebi token only with nebi.enabled: off, the gateway
// keeps none and the controller gives no app Nebi's address.
func TestNebiOffGivesNoAppANebiToken(t *testing.T) {
	p := config.Platform{Nebi: config.NebiCfg{URL: "https://nebi.example.com"}}
	if k := NebiTokens(p, nil); k != nil || NebiURL(p) != "" {
		t.Fatalf("nebi off: keeper %v, url %q", k, NebiURL(p))
	}
	p.Nebi.Enabled = true
	if k := NebiTokens(p, nil); k == nil || k.Source == nil || NebiURL(p) != "https://nebi.example.com" {
		t.Fatalf("nebi on: keeper %v, url %q", k, NebiURL(p))
	}
}
