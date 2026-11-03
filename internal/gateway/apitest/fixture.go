// Package apitest runs the whole gateway against a test database and a fake
// cluster, for tests that call the HTTP API: New builds the server with the
// Options given, and Fixture signs people in and makes requests.
package apitest

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/ext/envsource"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/features/authlocal"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/gateway"
	adminapi "github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/branding"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/gateway/podlogs"
	"github.com/nebari-dev/javo/internal/gateway/storage"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/sizes"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// Fixture is a running gateway: its fake cluster, database, people,
// sessions, tokens and server.
type Fixture struct {
	T      *testing.T
	Client client.Client
	Db     *store.Store
	People *FailablePeople
	Store  *session.DBStore
	Tokens *token.DBStore
	Srv    *gateway.Server
}

// Options change how New builds the gateway; the zero value is a gateway
// with local sign-in, the shared namespace and default roles.
type Options struct {
	Secure  bool
	Trusted []netip.Prefix
	Funcs   *interceptor.Funcs
	UiDir   string
	// Branding is the install's look; nil is the built-in one.
	Branding *branding.Branding
	Files    *StubFilesLister
	Logs     podlogs.Source
	Sizes    sizes.Catalog
	// NoLocal leaves the local provider out, as when auth.local is disabled.
	NoLocal bool
	// Extra providers are registered next to local.
	Extra []authprovider.Provider
	// Now is the gateway's clock; nil means the real one.
	Now func() time.Time
	// Urls and staleAfter are the platform settings sharing reads.
	Urls         config.URLs
	AllowOneHost bool
	StaleAfter   time.Duration
	// Placement says where apps live; nil is the shared namespace wsNS.
	Placement placement.Placement
	// Roles replaces the default (javo-admins are platform admins, everyone a member).
	Roles *config.Roles
	// Policy, when set, is the stored NetworkPolicy probe result.
	Policy netprobe.State
	// Engines configures the jobs API.
	Engines *engine.Registry
	// Images governs apps that run an image their creator names.
	Images config.ContainerImagesCfg
	// People are signed in before the test starts, each with the login time
	// given (the zero time keeps the real one).
	People []TestPerson
	// Uids is identity.uidRange.
	Uids store.IDRange
	// GroupsMaxAge and maxLifetime are api.tokens; apiDocs is api.docs.enabled.
	GroupsMaxAge time.Duration
	MaxLifetime  time.Duration
	ApiDocs      bool
	// Previews is Config.PreviewsEnabled.
	Previews bool
	// Thumbnails is Config.Thumbnails.
	Thumbnails bool
	// PreviewsMax is Config.PreviewsMaxPerApp.
	PreviewsMax int32
	// CodeHosts is Config.CodeHosts.
	CodeHosts *registry.Registry[codehost.Host]
	// Ready is Config.Ready: nil leaves the gateway always ready.
	Ready func(context.Context) error
	// Idle is the "Stop when idle" policy; the zero value is the chart's
	// (Off by default, 10 minutes to 7 days).
	Idle config.IdleStopCfg
	// Ai, when set, turns the AI features on with this service (all features).
	Ai *ai.Service
	// Mcp serves /mcp.
	Mcp bool
	// Usage reads a home's usage and groups tells a person's groups; nil
	// leaves each out of the gateway.
	Usage  storage.HomeUsages
	Groups podstorage.Memberships
	// Storage is the config's storage section; the zero value is defaults.
	Storage model.ClusterStorageSpec
	// Backups is where the database backups are (Config.Backups).
	Backups adminapi.BackupConfig
	// Environments is Config.Environments: nil offers none.
	Environments envsource.Source
	// Nebi, when set, gives the gateway a nebi.Tokens over it (Nebi on).
	Nebi nebi.TokenSource
}

// New builds a gateway over a test database and a fake cluster.
// objs are seeded first: apps, templates and jobs (*model.App, *model.Template,
// *model.Job) go to the database, any other client.Object to the cluster.
func New(t *testing.T, o Options, objs ...any) *Fixture {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	var kobjs []client.Object
	for _, obj := range objs {
		if k, ok := obj.(client.Object); ok {
			kobjs = append(kobjs, k)
		}
	}
	b := fake.NewClientBuilder().WithScheme(s).WithObjects(kobjs...).
		WithIndex(&corev1.Event{}, "involvedObject.name", func(o client.Object) []string {
			return []string{o.(*corev1.Event).InvolvedObject.Name}
		}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(o client.Object) []string { return []string{o.(*corev1.Pod).Spec.NodeName} })
	if o.Funcs != nil {
		b = b.WithInterceptorFuncs(*o.Funcs)
	}
	c := b.Build()
	db := storetest.New(t)
	users := &local.DBUsers{Accounts: db.LocalAccounts()}
	hash, err := aliceHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := users.Add(context.Background(), "alice", hash, []string{"javo-admins"}, false); err != nil {
		t.Fatal(err)
	}
	// The store shares the fixture's clock, so expiry follows the test's time.
	sessions := &session.DBStore{Sessions: db.Sessions(), Now: o.Now}
	people := &FailablePeople{People: db.People(o.Uids)}
	for _, p := range o.People {
		p.SignIn(t, db, people)
	}
	gatewaytest.Seed(t, db, objs)
	providers := registry.New[authprovider.Provider]()
	if !o.NoLocal {
		providers.Register(authlocal.New(authlocal.Config{Users: users}))
	}
	for _, p := range o.Extra {
		providers.Register(p)
	}
	var roles config.Roles
	roles.PlatformAdmin.Groups = []string{"javo-admins"}
	if o.Roles != nil {
		roles = *o.Roles
	}
	pl := o.Placement
	if pl == nil {
		pl = SharedPlacement()
	}
	if o.Engines == nil {
		o.Engines = TestEngines()
	}
	policy := &netprobe.Store{Facts: db.Facts()}
	if o.Policy != "" {
		if err := policy.Put(context.Background(), netprobe.Result{State: o.Policy}); err != nil {
			t.Fatal(err)
		}
	}
	tokens := &token.DBStore{Tokens: db.Tokens(), Now: o.Now}
	var api config.APICfg
	api.Tokens.GroupsMaxAge.Duration, api.Tokens.MaxLifetime.Duration, api.Docs.Enabled = o.GroupsMaxAge, o.MaxLifetime, o.ApiDocs
	if api.Tokens.GroupsMaxAge.Duration == 0 {
		api.Tokens.GroupsMaxAge.Duration = 30 * 24 * time.Hour
	}
	if o.Idle.MaxMinutes == 0 {
		o.Idle.MinMinutes, o.Idle.MaxMinutes = 10, 7*24*60
	}
	o.Storage.ApplyDefaults()
	var nebiTokens *nebi.Tokens
	if o.Nebi != nil {
		nebiTokens = &nebi.Tokens{Store: db, Source: o.Nebi}
	}
	srv := gateway.NewServer(gateway.Config{
		NebiTokens: nebiTokens,
		Tokens:     tokens, API: api, IdleStop: o.Idle,
		Usage: o.Usage, Groups: o.Groups, Storage: o.Storage,

		Client: c, Store: db, Sessions: sessions, Providers: providers,
		People: people, Roles: roles, Now: o.Now,
		Placement: pl, SecureCookies: o.Secure, UIDir: o.UiDir, Branding: o.Branding,
		Homes: Homes(o.Files), TrustedProxies: o.Trusted, Logs: o.Logs, Sizes: o.Sizes, Machines: capacity.Nodes{Reader: c},
		URLs: o.Urls, Exposure: exposurepath.New(o.Urls), Sharing: config.SharingCfg{AllowOneHost: o.AllowOneHost}, StaleAfter: o.StaleAfter,
		Diagnose: diagnose.NewEngine(diagnose.DefaultRules()...),
		AI:       o.Ai, MCP: config.MCPCfg{Enabled: o.Mcp}, AIFeatures: config.AIFeatures{Explain: true, Draft: true, Palette: true, Admin: true},
		NetworkPolicy: policy, Engines: o.Engines, ContainerImages: o.Images, Ready: o.Ready, PreviewsEnabled: o.Previews, Thumbnails: o.Thumbnails, PreviewsMaxPerApp: o.PreviewsMax, CodeHosts: o.CodeHosts, Backups: o.Backups,
		Environments: o.Environments,
	})
	return &Fixture{T: t, Client: c, Db: db, People: people, Store: sessions, Tokens: tokens, Srv: srv}
}

// aliceHash is alice's password as an argon2id hash with the smallest cost
// argon2 allows, made once. The real cost is deliberately slow and almost
// every test signs alice in; local.VerifyPassword reads the cost from the
// hash, so sign-in still runs the real check.
var aliceHash = sync.OnceValues(func() (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const memory, time, threads = 8, 1, 1
	key := argon2.IDKey([]byte("alice-password"), salt, time, memory, threads, 32)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memory, time, threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
})

// Homes keeps a nil stub a nil interface, so the files route stays out.
func Homes(s *StubFilesLister) storage.Homes {
	if s == nil {
		return nil
	}
	return s
}

// Session is a signed-in browser session of the person with this identity.
func (f *Fixture) Session(id string, groups ...string) *http.Cookie {
	f.T.Helper()
	tok, err := f.Store.Create(context.Background(), session.Session{Principal: authz.Principal{ID: id, Username: strings.SplitN(id, "-", 2)[0], Groups: groups}, Provider: "local", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour)})
	if err != nil {
		f.T.Fatal(err)
	}
	return &http.Cookie{Name: model.SessionCookieName, Value: tok}
}

// Do sends one request to the gateway, with the session cookie c if any.
func (f *Fixture) Do(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	f.T.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set(authn.RequestHeader, "1")
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

// Decode reads the response body as JSON, failing the test if it is not.
func Decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

// Problem is the JSON of a problem answer.
type Problem struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Errors []struct {
		Location string `json:"location"`
		Message  string `json:"message"`
	} `json:"errors"`
}

func (p Problem) FieldMessage(loc string) (string, bool) {
	for _, e := range p.Errors {
		if e.Location == loc {
			return e.Message, true
		}
	}
	return "", false
}

type StepJSON struct {
	Type, Status, Reason, Title, What, Todo, Blame, Who string
	Evidence                                            []string
}

type WorkspaceJSON struct {
	Name, DisplayName, Template, Kind, File, FilePath, Phase, URL, Size string
	Steps                                                               []StepJSON
	CanShare                                                            bool
}

func (f *Fixture) LoginFrom(ip, user, pw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/auth/local/login", strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(authn.RequestHeader, "1")
	r.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

func (f *Fixture) LoginVia(proxy, client, user, pw string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/auth/local/login", strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(authn.RequestHeader, "1")
	r.Header.Set("X-Forwarded-For", client)
	r.RemoteAddr = proxy + ":40000"
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

func SampleTemplateObj(name, kind string) *model.Template {
	t := gatewaytest.Template(name, kind)
	t.Spec.DisplayName = "Streamlit"
	t.Spec.Sample = &model.TemplateSample{Path: "/opt/javo/samples/" + name, Open: "app.py", Title: "Sales explorer"}
	return t
}

func LoginlessTemplate() *model.Template {
	t := gatewaytest.Template("code", "jupyterlab")
	t.Spec.AppLogin = "Disabled"
	return t
}

func JobOnlyTemplate() *model.Template {
	t := gatewaytest.Template("jobs-py", "custom")
	t.Spec.JobOnly = true
	t.Spec.Jobs = &model.TemplateJobs{Engines: []string{"papermill"}, Kernels: []string{"python3"}}
	return t
}

// Apps are the stored apps, deleted ones left out.
func (f *Fixture) Apps() []model.App {
	f.T.Helper()
	all, err := f.Db.Apps().ListAll(context.Background())
	if err != nil {
		f.T.Fatal(err)
	}
	var out []model.App
	for _, a := range all {
		if a.DeletedAt.IsZero() {
			out = append(out, a)
		}
	}
	return out
}

func (f *Fixture) WorkspaceCount() int { return len(f.Apps()) }

// GetWorkspace is owner's stored app with this short name.
func (f *Fixture) GetWorkspace(owner, short string) *model.App {
	f.T.Helper()
	app, err := f.Db.Apps().GetByOwnerName(context.Background(), owner, short)
	if err != nil {
		f.T.Fatalf("get %s: %v", short, err)
	}
	return &app
}
