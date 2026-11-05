package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

const (
	helmKey = "sk-helm-PLACEHOLDER-0000"
	uiKey   = "sk-ui-PLACEHOLDER-4321"
	draft   = `{"displayName":"Sales","template":"streamlit","file":""}`
)

// providers records the config of every provider the gateway builds; each
// answers drafts.
type providers struct {
	mu   sync.Mutex
	cfgs []provider.Config
	made []*aitest.Scripted
}

func (p *providers) build(c provider.Config) (provider.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c.Provider == "anthropic" && c.APIKey == "" {
		return nil, errors.New("anthropic: no API key")
	}
	s := aitest.New(aitest.Reply{JSON: draft}, aitest.Reply{JSON: draft}, aitest.Reply{JSON: draft})
	p.cfgs, p.made = append(p.cfgs, c), append(p.made, s)
	return s, nil
}

func (p *providers) last() (provider.Config, *aitest.Scripted) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfgs[len(p.cfgs)-1], p.made[len(p.made)-1]
}

func helm(prov string) *config.AICfg {
	return &config.AICfg{Enabled: true, Provider: prov, Model: "helm/model", BaseURL: "https://helm.example.com/v1"}
}

func aiFixture(t *testing.T, c *config.AICfg) (*apitest.Fixture, *providers) {
	t.Helper()
	p := &providers{}
	f := apitest.New(t, apitest.Options{AIConfig: c, AIConfigKey: helmKey, AIBuild: p.build},
		gatewaytest.Template("streamlit", "streamlit"))
	return f, p
}

type aiView struct {
	Enabled, Overridden, KeyStored bool
	Provider, BaseURL, Model       string
	KeyLast4, KeyFrom              string
	Config                         struct {
		Provider, BaseURL, Model string
		Key                      bool
	}
}

// Only platform admins read or change the AI settings: a member is refused
// and nothing changes.
func TestAISettingsArePlatformAdminsOnly(t *testing.T) {
	t.Parallel()
	f, _ := aiFixture(t, helm("openai"))
	member := f.Session(apitest.AliceID)
	body := `{"baseURL":"https://llm.example.com/v1","model":"m","apiKey":"` + uiKey + `"}`
	for _, r := range []struct{ method, body string }{{"GET", ""}, {"PUT", body}, {"DELETE", ""}} {
		if w := f.Do(r.method, "/api/admin/ai", r.body, member); w.Code != http.StatusForbidden {
			t.Errorf("%s by a member: %d %s", r.method, w.Code, w.Body)
		}
	}
	if _, err := f.Db.AISettings().Get(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a member's save was stored: %v", err)
	}
}

// The key goes in and never comes out: the answers, the settings page and
// the audit log show its last four characters at most, and the audit log
// says who changed which fields.
func TestAISettingsKeyIsNeverReturned(t *testing.T) {
	t.Parallel()
	f, _ := aiFixture(t, helm("openai"))
	admin := f.Session(apitest.AdminID, "javo-admins")
	w := f.Do("PUT", "/api/admin/ai", `{"baseURL":"https://llm.example.com/v1","model":"ui/model","apiKey":"`+uiKey+`"}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	v := apitest.Decode[aiView](t, w)
	if !v.Overridden || !v.KeyStored || v.KeyLast4 != "4321" || v.KeyFrom != "settings" || v.Model != "ui/model" {
		t.Fatalf("saved view %+v", v)
	}
	for what, w := range map[string]string{
		"save":  w.Body.String(),
		"get":   f.Do("GET", "/api/admin/ai", "", admin).Body.String(),
		"audit": f.Do("GET", "/api/admin/audit", "", admin).Body.String(),
	} {
		if strings.Contains(w, uiKey) || strings.Contains(w, "PLACEHOLDER") || strings.Contains(w, helmKey) {
			t.Errorf("%s shows the key: %s", what, w)
		}
	}
	events, err := f.Db.Audit().List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var d struct{ Fields []string }
	if len(events) == 0 || json.Unmarshal(events[0].Detail, &d) != nil {
		t.Fatalf("events %+v", events)
	}
	if e := events[0]; e.Action != "ai.settings.update" || e.Actor != apitest.AdminID || e.ObjectName != "ai" || strings.Join(d.Fields, ",") != "baseURL,model,apiKey" {
		t.Fatalf("audit %+v fields %v", e, d.Fields)
	}
}

// Without settings the config applies with its key; settings saved apply to
// the next AI call without a restart, the key kept while other fields
// change; a reset goes back to the config.
func TestAISettingsSwitchTheModelAtRuntime(t *testing.T) {
	t.Parallel()
	f, p := aiFixture(t, helm("openai"))
	admin, member := f.Session(apitest.AdminID, "javo-admins"), f.Session(apitest.PlanOwner)
	ask := func(want string) provider.Config {
		t.Helper()
		d := apitest.Decode[apitest.DraftJSON](t, f.Do("POST", "/api/ai/draft", `{"text":"a sales app","kind":"app"}`, member))
		c, s := p.last()
		if d.Draft.DisplayName != "Sales" || s.Calls() == 0 || s.Requests[s.Calls()-1].Model != want {
			t.Fatalf("draft %+v; model asked %v, want %s", d, s.Requests, want)
		}
		return c
	}
	status := func() string {
		return apitest.Decode[struct{ Model string }](t, f.Do("GET", "/api/ai/status", "", member)).Model
	}
	if c := ask("helm/model"); c.APIKey != helmKey || c.BaseURL != "https://helm.example.com/v1" {
		t.Fatalf("config build %+v", c)
	}
	if v := apitest.Decode[aiView](t, f.Do("GET", "/api/admin/ai", "", admin)); v.Overridden || v.KeyFrom != "config" || !v.Config.Key || v.Config.Model != "helm/model" || !v.Enabled {
		t.Fatalf("view before %+v", v)
	}

	save := func(body string) {
		t.Helper()
		if w := f.Do("PUT", "/api/admin/ai", body, admin); w.Code != http.StatusOK {
			t.Fatalf("save %s: %d %s", body, w.Code, w.Body)
		}
	}
	save(`{"baseURL":"https://llm.example.com","model":"ui/model","apiKey":"` + uiKey + `"}`)
	if c := ask("ui/model"); c.Provider != "openai" || c.APIKey != uiKey || c.BaseURL != "https://llm.example.com" {
		t.Fatalf("settings build %+v", c)
	}
	if m := status(); m != "ui/model" {
		t.Fatalf("status model %q", m)
	}
	save(`{"baseURL":"https://llm.example.com","model":"ui/other"}`)
	if c := ask("ui/other"); c.APIKey != uiKey {
		t.Fatalf("key not kept: %+v", c)
	}
	if w := f.Do("DELETE", "/api/admin/ai", "", admin); w.Code != http.StatusNoContent {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if c := ask("helm/model"); c.APIKey != helmKey || c.Provider != "openai" {
		t.Fatalf("after reset %+v", c)
	}
}

// The coding agents' status follows the settings in force: an anthropic
// config gives them no platform model, settings for an OpenAI-compatible
// server do.
func TestAgentStatusFollowsTheSettings(t *testing.T) {
	t.Parallel()
	f, _ := aiFixture(t, helm("anthropic"))
	admin, member := f.Session(apitest.AdminID, "javo-admins"), f.Session(apitest.PlanOwner)
	type agent struct{ Mode, Model string }
	if a := apitest.Decode[agent](t, f.Do("GET", "/api/ai/agent", "", member)); a.Mode != "login" {
		t.Fatalf("before: %+v", a)
	}
	if w := f.Do("PUT", "/api/admin/ai", `{"baseURL":"https://router.example.com/api/v1","model":"ui/agent","apiKey":"`+uiKey+`"}`, admin); w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if a := apitest.Decode[agent](t, f.Do("GET", "/api/ai/agent", "", member)); a.Mode != "platform" || a.Model != "ui/agent" {
		t.Fatalf("after: %+v", a)
	}
}

func TestAISettingsRefuseBadInput(t *testing.T) {
	t.Parallel()
	f, _ := aiFixture(t, helm("openai"))
	admin := f.Session(apitest.AdminID, "javo-admins")
	for body, want := range map[string]reasons.MessageCode{
		`{"baseURL":"http://llm.example.com/v1","model":"m"}`:  reasons.MsgAISettingsBaseURL,
		`{"baseURL":"https://169.254.169.254/v1","model":"m"}`: reasons.MsgAISettingsBaseURLHost,
		`{"baseURL":"https://llm.example.com/v1","model":" "}`: reasons.MsgAISettingsModel,
	} {
		w := f.Do("PUT", "/api/admin/ai", body, admin)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), reasons.Msg(want)) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	long := `{"model":"m","apiKey":"` + strings.Repeat("k", 4097) + `"}`
	if w := f.Do("PUT", "/api/admin/ai", long, admin); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("a key over the bound: %d", w.Code)
	}
	if _, err := f.Db.AISettings().Get(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused save was stored: %v", err)
	}
}

// With ai.enabled false there is nothing to set.
func TestAISettingsWithAIOff(t *testing.T) {
	t.Parallel()
	f := apitest.New(t, apitest.Options{})
	admin := f.Session(apitest.AdminID, "javo-admins")
	if v := apitest.Decode[aiView](t, f.Do("GET", "/api/admin/ai", "", admin)); v.Enabled {
		t.Fatalf("view %+v", v)
	}
	if w := f.Do("PUT", "/api/admin/ai", `{"model":"m"}`, admin); w.Code != http.StatusConflict {
		t.Fatalf("save with AI off: %d %s", w.Code, w.Body)
	}
}
