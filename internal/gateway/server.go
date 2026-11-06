// Package gateway is the single entrypoint: the authenticated REST API,
// the UI's static files and the app proxy.
package gateway

import (
	"context"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	ctrljobs "github.com/nebari-dev/javo/internal/controller/jobs"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/ext/envsource"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/branding"
	"github.com/nebari-dev/javo/internal/gateway/environments"
	"github.com/nebari-dev/javo/internal/gateway/hooks"
	"github.com/nebari-dev/javo/internal/gateway/jobs"
	"github.com/nebari-dev/javo/internal/gateway/namespaces"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/gateway/proxy"
	"github.com/nebari-dev/javo/internal/gateway/ratelimit"
	"github.com/nebari-dev/javo/internal/gateway/secrets"
	"github.com/nebari-dev/javo/internal/gateway/sizing"
	"github.com/nebari-dev/javo/internal/gateway/storage"
	"github.com/nebari-dev/javo/internal/gateway/streams"
	"github.com/nebari-dev/javo/internal/gateway/templates"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/mcp"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"sync"
	"time"
)

// Config wires the gateway's dependencies.
type Config struct {
	// Ready, when set, is asked by /readyz; an error means the gateway is not
	// ready to take traffic and its text is the answer.
	Ready func(context.Context) error
	// Client reads Pods, Pod events, home claims and namespaces, and keeps
	// the file picker pods and API token Secrets. The gateway never writes
	// apps, templates or jobs to Kubernetes: those are rows of Store.
	Client client.Client
	// Store holds apps, templates, sharing and jobs. Required: NewServer
	// panics without it.
	Store *store.Store
	// Tokens holds personal API tokens. Required: NewServer panics without it.
	Tokens *token.DBStore
	// API configures tokens (api.tokens) and the docs page (api.docs).
	API      config.APICfg
	Sessions session.Store
	// Providers are the ways to sign in, wired in internal/cli/wiring.
	Providers *registry.Registry[authprovider.Provider]
	// People finds or creates a person at sign-in.
	People authn.People
	// Roles binds groups to roles, for /api/me.
	Roles config.Roles
	// URLs and Sharing say where apps are served and whether one host may
	// carry public sharing.
	URLs    config.URLs
	Sharing config.SharingCfg
	// Exposure decides where apps are served; wired in internal/cli/wiring.
	Exposure exposure.Backend
	// PublicHost (--public-host) and PixiImage (environments.pixiImage) fill
	// in the command each app is shown to run, as the controller does.
	PublicHost, PixiImage string
	// StaleAfter is how long since last login before a person shared with
	// shows as no longer active; zero means never.
	StaleAfter time.Duration
	// OutageGrace is how long a session outlives an unreachable identity
	// provider; zero means 15 minutes. It is used only when Revalidator is nil.
	OutageGrace time.Duration
	// NebiTokens keeps the owner's Nebi token of apps that get one; nil while
	// Nebi is off. Share the one given to proxy.New.
	NebiTokens *nebi.Tokens
	// Revalidator asks providers whether a signed-in person is still valid.
	// Share the one given to proxy.New; nil builds one from Providers.
	Revalidator *authn.Revalidator
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// PreviewsEnabled says the platform lets apps from a GitHub repository
	// turn on previews (the app view reports it as previewsAvailable).
	PreviewsEnabled bool
	// Thumbnails lets an app's owner give its card a picture
	// (config thumbnails.enabled). Off, the picture routes are not served and
	// app views carry no thumbnail.
	Thumbnails bool
	// PreviewsMaxPerApp is the platform's cap on previews per app; 0 means
	// model.MaxPreviewMax.
	PreviewsMaxPerApp int32
	// CodeHosts verify the webhook deliveries of previews, served at
	// /hooks/<host id>/<hook id>. Nil serves no webhook.
	CodeHosts *registry.Registry[codehost.Host]
	// Placement decides the namespace of each app; wired in internal/cli/wiring.
	Placement placement.Placement
	// SecureCookies sets the session cookie's Secure flag even when
	// URLs.URLScheme is http; with https it is always set.
	SecureCookies bool
	// TrustedProxies are the peers whose X-Forwarded-For header is believed
	// when finding a client's address (for login rate limits). Empty
	// trusts none.
	TrustedProxies []netip.Prefix
	// UIDir holds the built UI. Empty serves no UI.
	UIDir string
	// Branding is the install's name, logo, favicon and accent, from
	// branding.Load; nil shows the built-in look.
	Branding *branding.Branding
	// Homes lists home folders for GET /api/files and frees a user's home
	// after an app is created. The gateway command always sets it; nil (only
	// when printing the OpenAPI document or in tests) answers GET /api/files
	// with a server error and frees nothing.
	Homes storage.Homes
	// Logs reads app logs. The gateway command always sets it; nil (only
	// when printing the OpenAPI document or in tests) answers the log
	// routes with a server error.
	Logs podlogs.Source
	// Sizes are the admin-defined app sizes; empty offers none and keeps
	// template resources.
	Sizes sizes.Catalog
	// Machines reads the cluster's nodes, so sizes no machine can hold are
	// marked and refused. Required: NewServer panics without it.
	Machines capacity.Source
	// Engines lists the ways a file can run as a job. Required: NewServer
	// panics without it.
	Engines *engine.Registry
	// NetworkPolicy is the stored NetworkPolicy probe result. Creating an
	// app from a template with no login of its own needs it to read
	// Enforced. Required: NewServer panics without it.
	NetworkPolicy netprobe.Reader
	// ContainerImages says whether apps may run an image their creator
	// names (templates with userImage) and from which registries.
	ContainerImages config.ContainerImagesCfg
	// IdleStop is the admin's "Stop when idle" policy.
	IdleStop config.IdleStopCfg
	// Diagnose finds the cause of a failed app; its rules are wired in
	// internal/cli/wiring. Required: NewServer panics without it.
	Diagnose *diagnose.Engine
	// Plans keeps proposed changes until they are approved; nil keeps them
	// in Store with the default lifetime.
	Plans *plans.Service
	// AI runs the model features; nil when AI is off (no provider exists).
	AI *ai.Service
	// AIFeatures turns single AI features off while AI is on.
	AIFeatures config.AIFeatures
	// InternalHost is the gateway's Service host inside the cluster (such as
	// javo-gateway.javo-system.svc): pods call the API there, since the
	// public address may not be reachable from them. It serves /api/ as the
	// UI host does; browsers cannot reach it.
	InternalHost string
	// Agent is the admin's agent model ("" while off) and template.
	Agent apps.AgentConfig
	// AgentModel is the coding agents' model endpoint; nil serves none. Only
	// the token of an app whose template has agentModel may call it.
	AgentModel *agentproxy.Proxy
	// AISettings is the AI settings in force, which platform admins change
	// in Settings > AI; nil while AI is off.
	AISettings *aisettings.Live
	// AIIncludeOwners names app owners in the admin report and its answers.
	AIIncludeOwners bool
	// MCP serves agents at /mcp when enabled.
	MCP config.MCPCfg
	// TrustedAgents is agents.allowTrusted: owners may let their coding
	// agents act without asking, unless a platform admin switched it off.
	TrustedAgents bool
	// Storage is the platform config's storage section, defaults applied,
	// for GET /api/storage and the admin storage report.
	Storage model.ClusterStorageSpec
	// Usage reads how full a home is for GET /api/storage?usage=true; nil
	// leaves usage out. Groups tells a person's groups for the group folders
	// listed there and in /api/files; nil means none.
	Usage  storage.HomeUsages
	Groups podstorage.Memberships
	// Backups is where the database backups are, for the admin's Backups page.
	Backups admin.BackupConfig
	// Environments is the environment manager apps and jobs may pick a pixi
	// environment from; nil offers none (wired in internal/cli/wiring).
	Environments envsource.Source
}

// Server routes /api/ to the API, /healthz, the UI at /, and any handler
// mounted later with Handle (for example the app proxy at /u/).
type Server struct {
	// API is the huma API so other features can register operations on
	// it. Every /api route needs a session unless it is in publicRoutes.
	API huma.API
	mux *http.ServeMux
	// handler is mux behind the request body deadline.
	handler http.Handler
	// exp and uiHost send requests for another host to apps.
	exp    exposure.Backend
	uiHost string
	// internalHost serves /api/ like uiHost (Config.InternalHost).
	internalHost string
	apps         http.Handler
	// ui is the built UI's files (nil without one): on one host they are
	// never an app's root request.
	ui http.FileSystem
	// https says the install is served over TLS, so its hosts get HSTS.
	https bool
}

// NewServer builds the gateway with the API, health check and UI.
// It panics when Store, Engines, NetworkPolicy, Tokens or Machines is missing: a gateway
// without them would refuse every request that needs them, so a wiring
// mistake must stop the start-up.
func NewServer(cfg Config) *Server {
	if cfg.Store == nil || cfg.Engines == nil || cfg.NetworkPolicy == nil || cfg.Tokens == nil || cfg.Machines == nil || cfg.Diagnose == nil {
		panic("gateway.NewServer: Config.Store, Config.Engines, Config.NetworkPolicy, Config.Tokens, Config.Machines and Config.Diagnose are required")
	}
	apiMux := http.NewServeMux()
	hc := huma.DefaultConfig(model.ProductName, "v1alpha1")
	hc.OpenAPIPath = "/api/openapi"
	hc.DocsPath = ""
	hc.SchemasPath = ""
	hc.CreateHooks = nil // no $schema links: no extra public routes
	hc.Security = authn.RequireSession
	hc.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		authn.SessionScheme: {Type: "apiKey", In: "cookie", Name: model.CookieName(model.SessionCookieName, true),
			Description: "Set at sign-in. Over plain http (urls.scheme http without --secure-cookies) it is " + model.SessionCookieName + "."},
		authn.BearerScheme: {Type: "http", Scheme: "bearer", Description: "A personal API token (" + token.Prefix + "...), " +
			"created under Settings. A read token may only GET; tokens cannot manage tokens."},
	}
	hc.Transformers = append(hc.Transformers, problem.Sanitize)
	api := humago.New(apiMux, hc)
	api.UseMiddleware(authn.WorkloadTokensReadOnly(api), authn.HomeReadsGuard(api))

	if cfg.Providers == nil {
		cfg.Providers = registry.New[authprovider.Provider]()
	}
	if cfg.Revalidator == nil {
		cfg.Revalidator = authn.NewRevalidator(cfg.Providers, cfg.Sessions, cfg.People, cfg.OutageGrace, cfg.Now)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	trust := apps.TrustPolicy{Config: cfg.TrustedAgents, DB: cfg.Store}
	a := &authn.Authenticator{
		Providers: cfg.Providers, People: cfg.People, Roles: cfg.Roles, Sessions: cfg.Sessions,
		Secure: cfg.URLs.SecureCookies(cfg.SecureCookies), Limiter: ratelimit.NewLogin(), Trusted: cfg.TrustedProxies,
		Clock: cfg.Now, Revalidator: cfg.Revalidator, Tokens: cfg.Tokens, GroupsMaxAge: cfg.API.Tokens.GroupsMaxAge.Duration,
		Apps: apps.AppTokenWanted(cfg.Store), Jobs: jobs.JobTokenWanted(cfg.Store), Agents: apps.AgentTokens(cfg.Store, ctrljobs.PeopleAccess{People: cfg.People, Roles: cfg.Roles}, trust, now),
	}
	mux := http.NewServeMux()
	a.Register(api, mux)
	if cfg.Branding == nil {
		cfg.Branding = branding.Default()
	}
	cfg.Branding.Register(api, mux)
	nss := namespaces.New(cfg.Placement, cfg.Client)
	var listings *storage.Listings
	if cfg.Homes != nil {
		listings = storage.NewListings(cfg.Homes, now)
		cfg.Homes = listings
		// A person using the UI may open the picker any moment: keep their home open.
		a.Active = cfg.Homes.Touch
	}
	sz := sizing.Rules{Sizes: cfg.Sizes, Machines: cfg.Machines, Roles: cfg.Roles}
	envs := &environments.API{Source: cfg.Environments, IDToken: a.IDToken}
	envs.Register(api)
	if cfg.Plans == nil {
		cfg.Plans = &plans.Service{DB: cfg.Store, Now: cfg.Now}
	}
	limits := cfg.API.Limits.WithDefaults()
	logStreams := streams.New(limits.StreamsPerPerson)
	run := workspace.Platform{Exposure: cfg.Exposure, PublicHost: cfg.PublicHost, PixiImage: cfg.PixiImage}
	ws := apps.New(apps.Config{
		Run:   run,
		Store: cfg.Store, Client: cfg.Client, Namespaces: nss, Homes: cfg.Homes, Sizing: sz, Exposure: cfg.Exposure,
		Logs: cfg.Logs, NetworkPolicy: cfg.NetworkPolicy, Images: cfg.ContainerImages, Tokens: cfg.Tokens,
		IdleStop: cfg.IdleStop, Diagnose: cfg.Diagnose, Environments: envs,
		NebiTokens: cfg.NebiTokens, IDToken: a.IDToken,
		PreviewsEnabled: cfg.PreviewsEnabled, Thumbnails: cfg.Thumbnails, PreviewsMaxPerApp: cfg.PreviewsMaxPerApp,
		Now: now, URLs: cfg.URLs, CodeHosts: cfg.CodeHosts, Trust: trust,
		Plans: cfg.Plans, AI: cfg.AI, AIFeatures: cfg.AIFeatures, AIIncludeOwners: cfg.AIIncludeOwners,
		People: cfg.People, Sharing: cfg.Sharing, StaleAfter: cfg.StaleAfter,
		Streams: logStreams, ThumbnailDecodes: limits.ThumbnailDecodes, Agent: cfg.Agent,
	})
	ws.Register(api)
	(&secrets.API{DB: cfg.Store, Roles: cfg.Roles}).Register(api)
	(&admin.API{Workspaces: ws, DB: cfg.Store, People: cfg.People, Roles: cfg.Roles, Now: now, Backups: cfg.Backups, Client: cfg.Client, Logs: cfg.Logs, AI: cfg.AISettings, Trust: trust}).Register(api)
	(&templates.API{DB: cfg.Store, Roles: cfg.Roles, Run: run, Idle: cfg.IdleStop}).Register(api)
	st := &storage.API{
		DB: cfg.Store, Spec: cfg.Storage, Client: cfg.Client, Sizes: cfg.Sizes, Namespaces: nss, Roles: cfg.Roles,
		Usage: cfg.Usage, Groups: cfg.Groups, Now: now,
	}
	st.Register(api)
	storage.RegisterFiles(api, listings, nss, st)

	(&authn.TokensAPI{Store: cfg.Tokens, Roles: cfg.Roles, Apps: cfg.Store.Apps(), MaxLifetime: cfg.API.Tokens.MaxLifetime.Duration, Now: now, Docs: cfg.API.Docs.Enabled}).Register(api)
	(&jobs.API{DB: cfg.Store, Client: cfg.Client, Namespaces: nss, Rules: sz, Logs: cfg.Logs, Homes: cfg.Homes, Engines: cfg.Engines, Environments: envs, Streams: logStreams}).Register(api)

	if cfg.CodeHosts != nil {
		mux.Handle("POST "+hooks.Route, &hooks.Receiver{
			DB: cfg.Store, Hosts: cfg.CodeHosts, Trusted: cfg.TrustedProxies, Limiter: ratelimit.NewBuckets(), Now: now,
		})
	}
	if cfg.Exposure != nil && cfg.Exposure.SeparateHost() {
		mux.Handle("GET "+proxy.TicketRoute, &proxy.Tickets{
			Apps: &proxy.AppLookup{DB: cfg.Store}, Sessions: cfg.Sessions,
			Exposure: cfg.Exposure, URLs: cfg.URLs, Roles: cfg.Roles, Now: now, Revalidator: cfg.Revalidator,
			Secure: cfg.URLs.SecureCookies(cfg.SecureCookies), Limiter: ratelimit.NewBuckets(),
		})
	}
	if p := cfg.AgentModel; p != nil {
		p.Caller = func(ctx context.Context) (string, bool) { return authn.User(ctx).ID, authn.ViaAgentModelToken(ctx) }
		p.Register(apiMux)
	}
	mux.Handle("/api/", noSniff(authn.SameOrigin(a.RequireUser(catalogFallback(apiMux)))))
	docs := apiDocs(cfg.API.Docs.Enabled, cfg.UIDir)
	mux.Handle("GET "+apiDocsRoute, docs)
	mux.Handle("GET /api/docs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cfg.API.Docs.Enabled {
			docs.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, apiDocsRoute, http.StatusMovedPermanently)
	}))
	if cfg.MCP.Enabled {
		mux.Handle(mcp.Route, mcp.NewHandler(mcp.Config{
			API: mux, Doc: api.OpenAPI(), Actions: ws.DescribeActions(),
			Verify: tokenVerifier(a.LiveToken), PublicURL: cfg.MCP.PublicURL, Version: "v1alpha1",
		}))
		mux.Handle("GET "+mcp.MetadataRoute, mcp.MetadataHandler(cfg.MCP.PublicURL))
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// /readyz is what the pod waits on before it gets traffic; /healthz only
	// says the process is alive.
	mux.Handle("GET /readyz", readiness(cfg.Ready, now))
	if cfg.UIDir != "" {
		mux.Handle("/", uiHandler(cfg.UIDir, cfg.Branding.ImageOrigins()))
	}
	// App requests on the UI host (/u/) carry the app's own uploads; only
	// the platform's routes get the body deadline.
	bodies := bodyDeadline(limits.BodyReadTimeout.Duration, mux)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, appsPath) {
			mux.ServeHTTP(w, r)
			return
		}
		bodies.ServeHTTP(w, r)
	})
	srv := &Server{API: api, mux: mux, handler: handler, exp: cfg.Exposure, uiHost: cfg.URLs.UIHost, internalHost: cfg.InternalHost, https: cfg.URLs.URLScheme() == "https"}
	if cfg.UIDir != "" {
		srv.ui = http.Dir(cfg.UIDir)
	}
	return srv
}

// readyFor is how long one answer of the readiness check is reused.
const readyFor = time.Second

// readiness serves /readyz: ready asked at most once per readyFor, however
// many probes come, and a fixed answer, since the route is public and the
// error may name the database's internals (they are logged instead).
func readiness(ready func(context.Context) error, now func() time.Time) http.Handler {
	var mu sync.Mutex
	var until time.Time
	var last error
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			mu.Lock()
			if t := now(); !t.Before(until) {
				if last = ready(r.Context()); last != nil {
					slog.Warn("not ready", "err", last)
				}
				until = t.Add(readyFor)
			}
			err := last
			mu.Unlock()
			if err != nil {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

// appsPath is where apps are served on the UI host when they have no host
// of their own.
const appsPath = "/u/"

// Handle mounts h at pattern (http.ServeMux syntax).
func (s *Server) Handle(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

// Apps mounts the app proxy. When apps have their own host it answers every
// request for that host and none for the UI's; otherwise it serves /u/ on the
// UI host.
func (s *Server) Apps(h http.Handler) {
	s.apps = h
	if !s.exp.SeparateHost() {
		s.mux.Handle(appsPath, h)
	}
}

// hsts pins a host to https for a year. It names no subdomains: other hosts
// of the organisation's domain are not the gateway's to pin.
const hsts = "max-age=31536000"

// gatewayHeaders are set on every answer of the gateway itself (not an
// app's): no sniffing, no referrer to other origins, a browsing context of
// its own, framing by the same origin only, and no powerful features.
var gatewayHeaders = map[string]string{
	"X-Content-Type-Options":     "nosniff",
	"Referrer-Policy":            "same-origin",
	"Cross-Origin-Opener-Policy": "same-origin",
	"X-Frame-Options":            "SAMEORIGIN",
	"Permissions-Policy":         "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.https {
		w.Header().Set("Strict-Transport-Security", hsts)
	}
	if s.apps != nil && (s.forApps(r) || s.appAtRoot(r)) {
		s.apps.ServeHTTP(w, r)
		return
	}
	if p := r.URL.Path; p != "/u" && !strings.HasPrefix(p, "/u/") {
		h := w.Header()
		for k, v := range gatewayHeaders {
			h.Set(k, v)
		}
		// API answers are personal: never kept in a browser or shared cache.
		// A route that needs another policy sets its own.
		if strings.HasPrefix(p, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
	}
	s.handler.ServeHTTP(w, r)
}

// forApps is true for a request that arrived on the apps' host. The UI and
// its API never answer there, so an app cannot be handed the UI's origin. The
// one exception is the health check: a probe sends the pod's address as Host,
// which is neither host, so /healthz and /readyz are the gateway's unless they name an app.
func (s *Server) forApps(r *http.Request) bool {
	if !s.exp.SeparateHost() {
		return false
	}
	_, isApp := s.exp.Match(r)
	if s.internalHost != "" && strings.HasPrefix(r.URL.Path, "/api/") {
		if h, _, err := net.SplitHostPort(r.Host); (err == nil && strings.EqualFold(h, s.internalHost)) || strings.EqualFold(r.Host, s.internalHost) {
			return false
		}
	}
	if s.uiHost != "" {
		if strings.EqualFold(r.Host, s.uiHost) {
			return false
		}
		return isApp || (r.URL.Path != "/healthz" && r.URL.Path != "/readyz")
	}
	return isApp
}

// rootClaimer is the app proxy (proxy.New): it says whether a request
// outside /u/ is a root request of an app, one its page sent for an absolute
// path.
type rootClaimer interface{ ClaimsRoot(r *http.Request) bool }

// appAtRoot is true, when apps share the UI host, for a root request of an
// app (rootClaimer) for a path the gateway does not serve itself: not a
// route of its own (any method), not /api or /u, not a file of the built UI
// and not a navigation, since the web UI routes its own pages in the browser
// and the server cannot tell which paths those are. Requests without Fetch
// Metadata are never an app's here, for the same reason.
func (s *Server) appAtRoot(r *http.Request) bool {
	c, ok := s.apps.(rootClaimer)
	if !ok || s.exp == nil || s.exp.SeparateHost() {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode == "" || mode == "navigate" {
		return false
	}
	p := path.Clean("/" + r.URL.Path)
	if p == "/" || p == "/index.html" || p == "/api" || p == "/u" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/u/") {
		return false
	}
	if s.ui != nil && isFile(s.ui, p) {
		return false
	}
	for _, m := range []string{r.Method, http.MethodGet, http.MethodPost} {
		q := *r
		q.Method = m
		if _, pattern := s.mux.Handler(&q); pattern != "" && pattern != "/" {
			return false
		}
	}
	return c.ClaimsRoot(r)
}

// catalogFallback answers requests that match no route (the mux's own 404
// and 405) with a problem whose text comes from the catalog, keeping the
// status and the Allow header the mux sets.
func catalogFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		rec := &statusOnly{header: http.Header{}}
		h.ServeHTTP(rec, r)
		code := reasons.MsgNoSuchAddress
		if rec.status == http.StatusMethodNotAllowed {
			code = reasons.MsgMethodNotAllowed
		}
		if allow := rec.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		problem.Write(w, rec.status, reasons.Msg(code))
	})
}

// statusOnly keeps a handler's status and headers and drops its body.
type statusOnly struct {
	header http.Header
	status int
}

func (s *statusOnly) Header() http.Header { return s.header }

func (s *statusOnly) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return len(b), nil
}

func (s *statusOnly) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
}

func noSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// bodyDeadline gives a request with a body d to send all of it; a client
// that sends it slower is cut off instead of holding the connection. The
// deadline ends once the body is read, so the answer may take as long as it
// needs, and requests without a body (log streams, websocket upgrades) get
// none: a read deadline left on the connection would end them.
func bodyDeadline(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(d)); err != nil {
			next.ServeHTTP(w, r)
			return
		}
		// A body left unread keeps the deadline: the server reads the rest
		// after the handler, and must not wait forever for it either. The
		// server sets its own deadlines again for the connection's next
		// request.
		r.Body = &deadlineBody{ReadCloser: r.Body, done: func() { _ = rc.SetReadDeadline(time.Time{}) }}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody calls done once its body has been read to the end.
type deadlineBody struct {
	io.ReadCloser
	done func()
	once sync.Once
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.once.Do(b.done)
	}
	return n, err
}
