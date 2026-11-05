package aisettings_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

const (
	configKey = "sk-config-PLACEHOLDER-1111"
	uiKey     = "sk-ui-PLACEHOLDER-9876"
)

var admin = model.Actor{Person: "admin-1"}

// built records each provider config the live settings build from.
type built struct {
	mu   sync.Mutex
	cfgs []provider.Config
}

func (b *built) build(c provider.Config) (provider.Provider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cfgs = append(b.cfgs, c)
	if c.Provider == "anthropic" && c.APIKey == "" {
		return nil, errors.New("anthropic: no API key")
	}
	return aitest.New(), nil
}

func (b *built) last() provider.Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfgs[len(b.cfgs)-1]
}

func helm() config.AICfg {
	c := config.AICfg{Enabled: true, Provider: "openai", Model: "helm/model", BaseURL: "https://helm.example.com/v1"}
	c.Agent.Models = []string{"helm/extra"}
	return c
}

func newLive(t *testing.T, c config.AICfg) (*aisettings.Live, *built, *store.Store) {
	t.Helper()
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	db := storetest.New(t)
	b := &built{}
	return &aisettings.Live{Config: c, ConfigKey: configKey, DB: db, Seal: key, Build: b.build}, b, db
}

// With nothing set, the platform config applies with its key; settings saved
// apply to the next call, with the key pasted; a reset goes back.
func TestSettingsOverrideTheConfigAndResetFallsBack(t *testing.T) {
	ctx := context.Background()
	l, b, _ := newLive(t, helm())
	first, err := l.Backend(ctx)
	if err != nil || first.Model != "helm/model" {
		t.Fatalf("backend %+v, %v", first, err)
	}
	if c := b.last(); c.Provider != "openai" || c.BaseURL != "https://helm.example.com/v1" || c.APIKey != configKey || c.HTTPClient != nil {
		t.Fatalf("config build %+v", c)
	}
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "anthropic", BaseURL: "https://llm.example.com", Model: "ui/model", APIKey: uiKey}); err != nil {
		t.Fatal(err)
	}
	second, err := l.Backend(ctx)
	if err != nil || second.Model != "ui/model" || second.Rev == first.Rev {
		t.Fatalf("after save %+v, %v", second, err)
	}
	if c := b.last(); c.Provider != "anthropic" || c.BaseURL != "https://llm.example.com" || c.APIKey != uiKey || c.HTTPClient == nil {
		t.Fatalf("settings build %+v", c)
	}
	// Unchanged settings build nothing new.
	n := len(b.cfgs)
	if again, _ := l.Backend(ctx); again.Rev != second.Rev || len(b.cfgs) != n {
		t.Fatalf("rebuilt without a change: %d builds", len(b.cfgs))
	}
	if err := l.Reset(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if back, _ := l.Backend(ctx); back.Model != "helm/model" || b.last().APIKey != configKey {
		t.Fatalf("after reset %+v, key %q", back, b.last().APIKey)
	}
}

// The key is stored sealed, shown as its last four characters, kept when
// other fields change, and removed on request.
func TestKeyIsSealedKeptAndRemoved(t *testing.T) {
	ctx := context.Background()
	l, b, db := newLive(t, helm())
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", BaseURL: "https://llm.example.com/v1", Model: "m1", APIKey: uiKey}); err != nil {
		t.Fatal(err)
	}
	row, err := db.AISettings().Get(ctx)
	if err != nil || len(row.KeySealed) == 0 || bytes.Contains(row.KeySealed, []byte(uiKey)) || row.KeyLast4 != "9876" {
		t.Fatalf("stored %+v, %v", row, err)
	}
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", BaseURL: "https://llm.example.com/v1", Model: "m2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Backend(ctx); err != nil || b.last().APIKey != uiKey || b.last().Model != "m2" {
		t.Fatalf("key not kept: %+v %v", b.last(), err)
	}
	v, err := l.View(ctx)
	if err != nil || v.KeyLast4 != "9876" || !v.KeyStored || v.KeyFrom != aisettings.KeyFromSettings || v.Model != "m2" || !v.Overridden {
		t.Fatalf("view %+v, %v", v, err)
	}
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", BaseURL: "https://llm.example.com/v1", Model: "m2", RemoveKey: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Backend(ctx); err != nil || b.last().APIKey != "" {
		t.Fatalf("key not removed: %q %v", b.last().APIKey, err)
	}
	if v, _ := l.View(ctx); v.KeyStored || v.KeyLast4 != "" || v.KeyFrom != aisettings.KeyFromNone {
		t.Fatalf("view after removal %+v", v)
	}
}

// The config's key goes only to the config's provider and address: with
// another address and no key pasted, no key is sent, and a provider that
// needs one is refused.
func TestConfigKeyStaysWithTheConfigsEndpoint(t *testing.T) {
	ctx := context.Background()
	l, b, _ := newLive(t, helm())
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", BaseURL: "https://helm.example.com/v1", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Backend(ctx); err != nil || b.last().APIKey != configKey {
		t.Fatalf("same endpoint: key %q, %v", b.last().APIKey, err)
	}
	if v, _ := l.View(ctx); v.KeyFrom != aisettings.KeyFromConfig {
		t.Fatalf("key from %q", v.KeyFrom)
	}
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", BaseURL: "https://elsewhere.example.com/v1", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Backend(ctx); err != nil || b.last().APIKey != "" {
		t.Fatalf("another endpoint got key %q, %v", b.last().APIKey, err)
	}
	err := l.Save(ctx, admin, aisettings.Input{Provider: "anthropic", BaseURL: "", Model: "claude"})
	if fe := (*aisettings.FieldError)(nil); !errors.As(err, &fe) || fe.Field != "apiKey" || fe.Code != reasons.MsgAISettingsKeyNeeded {
		t.Fatalf("anthropic without a key: %v", err)
	}
}

func TestSaveRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	l, _, db := newLive(t, helm())
	ok := aisettings.Input{Provider: "openai", BaseURL: "https://llm.example.com/v1", Model: "m", APIKey: uiKey}
	for name, tc := range map[string]struct {
		edit  func(*aisettings.Input)
		field string
		code  reasons.MessageCode
	}{
		"plain http":         {func(i *aisettings.Input) { i.BaseURL = "http://llm.example.com/v1" }, "baseURL", reasons.MsgAISettingsBaseURL},
		"loopback over http": {func(i *aisettings.Input) { i.BaseURL = "http://127.0.0.1:8000/v1" }, "baseURL", reasons.MsgAISettingsBaseURL},
		"credentials in url": {func(i *aisettings.Input) { i.BaseURL = "https://u:p@llm.example.com/v1" }, "baseURL", reasons.MsgAISettingsBaseURL},
		"no host":            {func(i *aisettings.Input) { i.BaseURL = "https:///v1" }, "baseURL", reasons.MsgAISettingsBaseURL},
		"metadata ip":        {func(i *aisettings.Input) { i.BaseURL = "https://169.254.169.254/latest" }, "baseURL", reasons.MsgAISettingsBaseURLHost},
		"link-local v6":      {func(i *aisettings.Input) { i.BaseURL = "https://[fe80::1]/v1" }, "baseURL", reasons.MsgAISettingsBaseURLHost},
		"mapped metadata":    {func(i *aisettings.Input) { i.BaseURL = "https://[::ffff:169.254.169.254]/" }, "baseURL", reasons.MsgAISettingsBaseURLHost},
		"metadata name":      {func(i *aisettings.Input) { i.BaseURL = "https://Metadata.Google.Internal./v1" }, "baseURL", reasons.MsgAISettingsBaseURLHost},
		"empty model":        {func(i *aisettings.Input) { i.Model = "" }, "model", reasons.MsgAISettingsModel},
		"padded model":       {func(i *aisettings.Input) { i.Model = " m" }, "model", reasons.MsgAISettingsModel},
		"long model":         {func(i *aisettings.Input) { i.Model = strings.Repeat("m", 201) }, "model", reasons.MsgAISettingsModel},
		"key too long":       {func(i *aisettings.Input) { i.APIKey = strings.Repeat("k", aisettings.MaxKeyBytes+1) }, "apiKey", reasons.MsgAISettingsKey},
		"key with newline":   {func(i *aisettings.Input) { i.APIKey = "sk-a\nb" }, "apiKey", reasons.MsgAISettingsKey},
		"key and remove":     {func(i *aisettings.Input) { i.RemoveKey = true }, "apiKey", reasons.MsgAISettingsKeyOrRemove},
		"unknown provider":   {func(i *aisettings.Input) { i.Provider = "other" }, "provider", reasons.MsgAISettingsProvider},
	} {
		in := ok
		tc.edit(&in)
		err := l.Save(ctx, admin, in)
		if fe := (*aisettings.FieldError)(nil); !errors.As(err, &fe) || fe.Field != tc.field || fe.Code != tc.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := db.AISettings().Get(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused save was stored: %v", err)
	}
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", Model: "m"}); err != nil {
		t.Fatalf("an empty address is the provider's own: %v", err)
	}
}

// The coding agents' endpoint follows the settings: off for a provider that
// is not OpenAI-compatible, on with the settings' model and key otherwise.
// The controller reads the same model without the key.
func TestAgentEndpointFollowsTheSettings(t *testing.T) {
	ctx := context.Background()
	c := helm()
	c.Provider = "anthropic"
	l, _, db := newLive(t, c)
	agentModel := aisettings.AgentModelOf(db, c)
	if _, ok := l.Upstream(ctx); ok || l.AgentModel(ctx) != "" {
		t.Fatal("an anthropic config has no agent endpoint")
	}
	if m, err := agentModel(ctx); err != nil || m != "" {
		t.Fatalf("controller sees %q, %v", m, err)
	}
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", BaseURL: "https://router.example.com/api/v1", Model: "ui/agent", APIKey: uiKey}); err != nil {
		t.Fatal(err)
	}
	up, ok := l.Upstream(ctx)
	if !ok || up.BaseURL != "https://router.example.com/api/v1" || up.APIKey != uiKey || up.Model != "ui/agent" || len(up.More) != 1 || up.Client == nil {
		t.Fatalf("upstream %+v, %v", up, ok)
	}
	if l.AgentModel(ctx) != "ui/agent" {
		t.Fatalf("agent model %q", l.AgentModel(ctx))
	}
	if m, err := agentModel(ctx); err != nil || m != "ui/agent" {
		t.Fatalf("controller sees %q, %v", m, err)
	}
	// The endpoint's client never connects to a link-local address, whatever
	// a name resolves to.
	_, err := up.Client.Get("https://169.254.169.254/latest/meta-data/")
	if !errors.Is(err, aisettings.ErrBlockedAddress) {
		t.Fatalf("link-local connect: %v", err)
	}
}

// Every change calls Changed, so the gateway can run the new provider's
// self-test.
func TestChangesAreAnnounced(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLive(t, helm())
	n := 0
	l.Changed = func() { n++ }
	if err := l.Save(ctx, admin, aisettings.Input{Provider: "openai", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Reset(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("changed %d times", n)
	}
}

// Calls and saves may run at once (go test -race).
func TestConcurrentCallsAndSaves(t *testing.T) {
	ctx := context.Background()
	l, _, _ := newLive(t, helm())
	var wg sync.WaitGroup
	deadline := time.Now().Add(300 * time.Millisecond)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				if i == 0 {
					_ = l.Save(ctx, admin, aisettings.Input{Provider: "openai", Model: "m"})
					continue
				}
				if _, err := l.Backend(ctx); err != nil {
					t.Error(err)
					return
				}
				l.Upstream(ctx)
			}
		}()
	}
	wg.Wait()
}
