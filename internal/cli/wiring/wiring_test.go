package wiring

import (
	"context"
	"slices"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// With ai.enabled false, no AI settings exist and no provider is built, so
// nothing ever connects to a model, even with a provider and key configured.
// On, the provider is built from the config and its key on first use.
func TestAIOffBuildsNoProvider(t *testing.T) {
	var got []provider.Config
	var r provider.Registry
	r.Register("spy", func(c provider.Config) (provider.Provider, error) { got = append(got, c); return aitest.New(), nil })
	cfg := config.AICfg{Enabled: false, Provider: "spy", Model: "m"}
	db := storetest.New(t)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if l := aiSettings(cfg, "a-key", db, key, &r); l != nil || len(got) != 0 {
		t.Fatalf("AI off made settings %v, factory called %d times", l, len(got))
	}
	cfg.Enabled = true
	l := aiSettings(cfg, "a-key", db, key, &r)
	if _, err := l.Backend(context.Background()); err != nil || len(got) != 1 || got[0].APIKey != "a-key" || got[0].Model != "m" {
		t.Fatalf("AI on: %v, built %+v", err, got)
	}
}

func TestAuthProvidersFollowTheConfig(t *testing.T) {
	var on, off config.Platform
	on.Auth.Local.Enabled = true
	users := &local.DBUsers{}

	if _, ok := AuthProviders(on, users, nil).Get("local"); !ok {
		t.Fatal("local provider missing with auth.local.enabled")
	}
	if n := len(AuthProviders(off, users, nil).All()); n != 0 {
		t.Fatalf("%d providers with auth.local disabled", n)
	}
}

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

// Each limits key reaches the store's cap of the same name.
func TestStoreLimitsFollowTheConfig(t *testing.T) {
	got := StoreLimits(config.LimitsCfg{RunningApps: 1, Apps: 2, Previews: 3, Jobs: 4,
		ActiveRunsPerJob: 5, ActiveRunsPerPerson: 6, APITokens: 7, PendingPlans: 8})
	want := store.Limits{RunningApps: 1, Apps: 2, Previews: 3, Jobs: 4, RunsPerJob: 5, RunsPerPerson: 6, APITokens: 7, PendingPlans: 8}
	if got != want {
		t.Fatalf("StoreLimits = %+v, want %+v", got, want)
	}
}
