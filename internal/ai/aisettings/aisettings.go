// Package aisettings is the AI settings in force: the platform config's
// ai.provider, ai.baseURL, ai.model and key (JAVO_AI_API_KEY), with what a
// platform admin set in Settings > AI in their place. The model features,
// the coding agents' endpoint and the controller read it per call, so a
// change applies without a restart, on every gateway replica.
//
// The admin's key is sealed with Tink AEAD under the gateway's sessions key
// (the one key all replicas share, kept in a Kubernetes Secret, never in the
// database) and bound to this use, as the webhook secrets are. People's
// secrets use the HPKE keyset instead, whose private half only the
// controller holds; that does not fit here, because the gateway itself
// calls the model with the key. The key is never logged and never returned:
// only its last four characters are shown.
package aisettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Bounds of what an admin may set.
const (
	MaxKeyBytes     = 4096
	MaxModelRunes   = 200
	MaxBaseURLBytes = 512
)

// Where the key in force comes from (View.KeyFrom).
const (
	KeyFromSettings = "settings"
	KeyFromConfig   = "config"
	KeyFromNone     = "none"
)

// keyAD binds a sealed key to this use of the sessions key.
var keyAD = []byte("javo ai settings api key v1")

// Live is the AI settings in force. Config, ConfigKey, DB, Seal and Build
// are required.
type Live struct {
	// Config is the platform config's ai section, ConfigKey its key.
	Config    config.AICfg
	ConfigKey string
	DB        *store.Store
	// Seal seals and opens the admin's key: the gateway's sessions key.
	Seal tink.AEAD
	// Build makes the provider of a config (the registered adapters, behind
	// a circuit breaker).
	Build func(provider.Config) (provider.Provider, error)
	// Changed, when set, runs after each save or reset (the gateway runs
	// the new provider's self-test).
	Changed func()

	mu  sync.Mutex
	cur *state
}

// state is what one revision of the settings resolves to.
type state struct {
	rev string
	row *store.AISetting
	// cfg is Config with the settings in place; key and keyFrom the key.
	cfg      config.AICfg
	key      string
	keyFrom  string
	backend  ai.Backend
	err      error
	upstream agentproxy.Upstream
	agent    bool
}

// Input is what an admin saves. The address is spoken to as an
// OpenAI-compatible server, whatever the platform config's provider. APIKey
// replaces the stored key; empty keeps it, unless RemoveKey.
type Input struct {
	BaseURL, Model string
	APIKey         string
	RemoveKey      bool
}

// FieldError is a refused field and why.
type FieldError struct {
	Field string
	Code  reasons.MessageCode
}

func (e *FieldError) Error() string { return e.Field + ": " + reasons.Msg(e.Code) }

// View is what an admin sees: the settings in force, where the key comes
// from, and the platform config's values.
type View struct {
	// Overridden is true while settings saved here are in force.
	Overridden               bool
	Provider, BaseURL, Model string
	// KeyStored is whether a key is stored here, KeyLast4 its end.
	KeyStored bool
	KeyLast4  string
	KeyFrom   string
	// ConfigProvider, ConfigBaseURL and ConfigModel are the platform
	// config's; ConfigKey whether it has a key.
	ConfigProvider, ConfigBaseURL, ConfigModel string
	ConfigKey                                  bool
	UpdatedAt                                  time.Time
}

// load returns the state of the stored revision, resolving it once per
// revision. The row is read on every call: another replica may have saved.
func (l *Live) load(ctx context.Context) (*state, error) {
	var row *store.AISetting
	r, err := l.DB.AISettings().Get(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return nil, fmt.Errorf("read the AI settings: %w", err)
	default:
		row = &r
	}
	rev := ""
	if row != nil {
		rev = row.Rev
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur == nil || l.cur.rev != rev {
		l.cur = l.resolve(row)
	}
	return l.cur, nil
}

// effective is c with the settings of row in place; nil leaves c alone.
func effective(c config.AICfg, row *store.AISetting) config.AICfg {
	if row != nil {
		c.Provider, c.BaseURL, c.Model = row.Provider, row.BaseURL, row.Model
	}
	return c
}

// ownAPI is each provider's address when the base URL is empty.
var ownAPI = map[string]string{"anthropic": "https://api.anthropic.com", provider.OpenAI: "https://api.openai.com"}

// sameEndpoint is whether settings for provider at baseURL send to the
// server the config sends to (same scheme, host and port), where the
// config's key may go.
func (l *Live) sameEndpoint(provider, baseURL string) bool {
	o := origin(provider, baseURL)
	return o != "" && o == origin(l.Config.Provider, l.Config.BaseURL)
}

// origin is the scheme, host and port that provider at baseURL connects
// to; "" when unknown.
func origin(provider, baseURL string) string {
	if baseURL == "" {
		baseURL = ownAPI[provider]
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
	}
	return u.Scheme + "://" + strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")) + ":" + port
}

func (l *Live) resolve(row *store.AISetting) *state {
	st := &state{row: row, cfg: effective(l.Config, row), backend: ai.Backend{Rev: "config"}}
	if row != nil {
		st.rev, st.backend.Rev = row.Rev, row.Rev
	}
	switch {
	case row != nil && row.KeySealed != nil:
		if k, err := l.Seal.Decrypt(row.KeySealed, keyAD); err == nil {
			st.key, st.keyFrom = string(k), KeyFromSettings
			break
		}
		slog.Warn("the AI key saved in Settings > AI does not open with this gateway's sessions key; save it again", "rev", row.Rev)
		fallthrough
	default:
		if l.ConfigKey != "" && (row == nil || l.sameEndpoint(row.Provider, row.BaseURL)) {
			st.key, st.keyFrom = l.ConfigKey, KeyFromConfig
		} else {
			st.keyFrom = KeyFromNone
		}
	}
	// An address an admin typed in the UI connects only where the guard
	// allows; the config's is used as it always was.
	var client *http.Client
	if row != nil {
		client = &http.Client{Transport: guardedTransport()}
	}
	st.backend.Model = st.cfg.Model
	st.backend.Provider, st.err = l.Build(provider.Config{
		Provider: st.cfg.Provider, Model: st.cfg.Model, BaseURL: st.cfg.BaseURL, APIKey: st.key,
		Timeout: st.cfg.Timeout.Duration, HTTPClient: client,
	})
	if m := st.cfg.AgentModel(); m != "" {
		st.agent = true
		st.upstream = agentproxy.Upstream{BaseURL: st.cfg.BaseURL, APIKey: st.key, Model: m, More: st.cfg.Agent.Models, Client: agentClient(st.cfg.Timeout.Duration, row != nil)}
	}
	return st
}

// Backend is the provider and model the AI features call now.
func (l *Live) Backend(ctx context.Context) (ai.Backend, error) {
	st, err := l.load(ctx)
	if err != nil {
		return ai.Backend{}, err
	}
	return st.backend, st.err
}

// Overridden reports whether settings saved in Settings > AI are in force.
func (l *Live) Overridden(ctx context.Context) (bool, error) {
	st, err := l.load(ctx)
	return err == nil && st.row != nil, err
}

// Upstream is where the coding agents' requests go now; false while there
// is no agent model (config.AICfg.AgentModel of the settings in force).
func (l *Live) Upstream(ctx context.Context) (agentproxy.Upstream, bool) {
	st, err := l.load(ctx)
	if err != nil {
		slog.Error("read the AI settings", "err", err)
		return agentproxy.Upstream{}, false
	}
	return st.upstream, st.agent
}

// AgentModel is the coding agents' model now, "" while there is none.
func (l *Live) AgentModel(ctx context.Context) string {
	up, ok := l.Upstream(ctx)
	if !ok {
		return ""
	}
	return up.Model
}

// AgentModelOf is the coding agents' model in force for one that reads the
// settings but holds no key (the controller): c with the saved settings in
// place.
func AgentModelOf(db *store.Store, c config.AICfg) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		r, err := db.AISettings().Get(ctx)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return c.AgentModel(), nil
		case err != nil:
			return "", err
		}
		return effective(c, &r).AgentModel(), nil
	}
}

// View is what the admin's settings page shows. It never holds the key.
func (l *Live) View(ctx context.Context) (View, error) {
	st, err := l.load(ctx)
	if err != nil {
		return View{}, err
	}
	v := View{
		Overridden: st.row != nil, Provider: st.cfg.Provider, BaseURL: st.cfg.BaseURL, Model: st.cfg.Model, KeyFrom: st.keyFrom,
		ConfigProvider: l.Config.Provider, ConfigBaseURL: l.Config.BaseURL, ConfigModel: l.Config.Model, ConfigKey: l.ConfigKey != "",
	}
	if st.row != nil {
		v.KeyStored, v.KeyLast4, v.UpdatedAt = st.row.KeySealed != nil, st.row.KeyLast4, st.row.UpdatedAt
	}
	return v, nil
}

// Save validates in and stores it as the settings in force, recording actor
// and the names of the fields that changed on the audit log. A refused
// field is a *FieldError.
func (l *Live) Save(ctx context.Context, actor model.Actor, in Input) error {
	if err := check(in); err != nil {
		return err
	}
	var prev *store.AISetting
	r, err := l.DB.AISettings().Get(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return fmt.Errorf("read the AI settings: %w", err)
	default:
		prev = &r
	}
	next := store.AISetting{Provider: provider.OpenAI, BaseURL: in.BaseURL, Model: in.Model}
	keyChanged := false
	switch {
	case in.APIKey != "":
		sealed, err := l.Seal.Encrypt([]byte(in.APIKey), keyAD)
		if err != nil {
			return fmt.Errorf("seal the AI key: %w", err)
		}
		next.KeySealed, next.KeyLast4, keyChanged = sealed, lastRunes(in.APIKey, 4), true
	case in.RemoveKey:
		keyChanged = prev != nil && prev.KeySealed != nil
	case prev != nil:
		next.KeySealed, next.KeyLast4 = prev.KeySealed, prev.KeyLast4
	}
	was := effective(l.Config, prev)
	var changed []string
	for _, f := range []struct {
		name     string
		old, new string
	}{{"provider", was.Provider, next.Provider}, {"baseURL", was.BaseURL, next.BaseURL}, {"model", was.Model, next.Model}} {
		if f.old != f.new {
			changed = append(changed, f.name)
		}
	}
	if keyChanged {
		changed = append(changed, "apiKey")
	}
	if err := l.DB.AISettings().Put(ctx, actor, next, changed); err != nil {
		return fmt.Errorf("save the AI settings: %w", err)
	}
	l.changed()
	return nil
}

// Reset removes the saved settings: the platform config applies again.
func (l *Live) Reset(ctx context.Context, actor model.Actor) error {
	if err := l.DB.AISettings().Reset(ctx, actor); err != nil {
		return fmt.Errorf("reset the AI settings: %w", err)
	}
	l.changed()
	return nil
}

func (l *Live) changed() {
	if l.Changed != nil {
		l.Changed()
	}
}

func check(in Input) error {
	if code := checkBaseURL(in.BaseURL); code != "" {
		return &FieldError{"baseURL", code}
	}
	m := in.Model
	if m == "" || strings.TrimSpace(m) != m || utf8.RuneCountInString(m) > MaxModelRunes || strings.ContainsFunc(m, unicode.IsControl) {
		return &FieldError{"model", reasons.MsgAISettingsModel}
	}
	if in.APIKey != "" && in.RemoveKey {
		return &FieldError{"apiKey", reasons.MsgAISettingsKeyOrRemove}
	}
	if len(in.APIKey) > MaxKeyBytes || strings.ContainsFunc(in.APIKey, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return &FieldError{"apiKey", reasons.MsgAISettingsKey}
	}
	return nil
}

// checkBaseURL refuses an address that is not https, carries credentials,
// a query or a fragment, or names a link-local or cloud metadata host;
// empty is the provider's own address.
func checkBaseURL(s string) reasons.MessageCode {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || len(s) > MaxBaseURLBytes || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return reasons.MsgAISettingsBaseURL
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if slices.Contains(metadataHosts, host) {
		return reasons.MsgAISettingsBaseURLHost
	}
	if ip, err := netip.ParseAddr(host); err == nil && blocked(ip) {
		return reasons.MsgAISettingsBaseURLHost
	}
	return ""
}

// metadataHosts are the cloud metadata services' names.
var metadataHosts = []string{"metadata", "metadata.google.internal", "metadata.goog", "instance-data", "instance-data.ec2.internal"}

// metadataAddrs are cloud metadata addresses outside the link-local ranges.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"),   // Azure platform
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
}

// blocked is a link-local, unspecified or metadata address.
func blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || slices.Contains(metadataAddrs, ip)
}

// ErrBlockedAddress is a connection refused because it would reach a
// link-local or metadata address.
var ErrBlockedAddress = errors.New("refused to connect to a link-local or cloud metadata address")

// guardedTransport is the default transport whose connections never reach a
// blocked address, checked on the address actually dialed, after name
// resolution.
func guardedTransport() *http.Transport {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrBlockedAddress
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || blocked(ip) {
			return ErrBlockedAddress
		}
		return nil
	}}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = d.DialContext
	return t
}

// agentClient waits at most the longer of timeout and five minutes for a
// provider's first answer byte; a streamed answer then takes as long as it
// takes, until the agent hangs up. guard keeps it off blocked addresses.
func agentClient(timeout time.Duration, guard bool) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if guard {
		t = guardedTransport()
	}
	t.ResponseHeaderTimeout = max(timeout, 5*time.Minute)
	return &http.Client{Transport: t}
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return ""
	}
	return string(r[len(r)-n:])
}
