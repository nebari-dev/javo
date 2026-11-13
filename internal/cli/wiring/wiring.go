// Package wiring is the only place features are wired together: registries
// from internal/core/registry are filled here, explicitly (no init()
// registration), and neither internal/gateway nor internal/controller imports
// a feature.
package wiring

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/ai"
	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/ai/aisettings"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/ai/redact"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/backup/restic"
	"github.com/nebari-dev/javo/internal/buildinfo"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/jobs"
	storagectl "github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/core/registry"
	"github.com/nebari-dev/javo/internal/diagnose"
	"github.com/nebari-dev/javo/internal/ext/authprovider"
	"github.com/nebari-dev/javo/internal/ext/codehost"
	"github.com/nebari-dev/javo/internal/ext/envsource"
	"github.com/nebari-dev/javo/internal/ext/exposure"
	"github.com/nebari-dev/javo/internal/ext/placement"
	"github.com/nebari-dev/javo/internal/ext/storage"
	"github.com/nebari-dev/javo/internal/features/authlocal"
	"github.com/nebari-dev/javo/internal/features/authoidc"
	"github.com/nebari-dev/javo/internal/features/codehostgithub"
	"github.com/nebari-dev/javo/internal/features/envnebi"
	"github.com/nebari-dev/javo/internal/features/exposurepath"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/features/placementperteam"
	"github.com/nebari-dev/javo/internal/features/placementperuser"
	"github.com/nebari-dev/javo/internal/features/placementshared"
	storageperuser "github.com/nebari-dev/javo/internal/features/storage/peruser"
	storageshared "github.com/nebari-dev/javo/internal/features/storage/shared"
	"github.com/nebari-dev/javo/internal/gateway/admin"
	"github.com/nebari-dev/javo/internal/gateway/nebi"
	"github.com/nebari-dev/javo/internal/identity"
	"github.com/nebari-dev/javo/internal/jobs/engine"
	"github.com/nebari-dev/javo/internal/jobs/engines/bash"
	"github.com/nebari-dev/javo/internal/jobs/engines/command"
	"github.com/nebari-dev/javo/internal/jobs/engines/marimo"
	"github.com/nebari-dev/javo/internal/jobs/engines/papermill"
	"github.com/nebari-dev/javo/internal/jobs/engines/quarto"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
)

// ConfigLoader reads and validates the platform config file.
type ConfigLoader func() (config.Platform, error)

// ConfigFlag registers --config on f and returns the loader for the
// file it names. Controller and gateway read the same file.
func ConfigFlag(f *pflag.FlagSet) ConfigLoader {
	var path string
	f.StringVar(&path, "config", config.DefaultPath, "platform config file (rendered by Helm into ConfigMap javo-config)")
	return func() (config.Platform, error) { return config.Load(path) }
}

// ClusterFlag registers --config on f and returns what says which
// executor the install that file describes runs apps with, and whether
// that is Kubernetes. The admin commands that read or change a cluster's
// objects (claims, Jobs, storage classes) ask it before they touch
// anything: with another executor there is no cluster, and a kubeconfig
// that happens to be there is some other cluster's. A config file that
// does not exist is taken for Kubernetes, where these commands are also
// run from a machine that has a kubeconfig and no config file.
func ClusterFlag(f *pflag.FlagSet) func() (executor string, onCluster bool, err error) {
	load := ConfigFlag(f)
	return func() (string, bool, error) {
		p, err := load()
		if errors.Is(err, fs.ErrNotExist) {
			return config.ExecutorKubernetes, true, nil
		}
		return p.Executor, p.OnKubernetes(), err
	}
}

// AuthProviders registers the sign-in providers the config enables. link,
// when not nil, also signs a local account in through a link (javo local).
func AuthProviders(p config.Platform, users local.Users, link *authlocal.Link) *registry.Registry[authprovider.Provider] {
	r := registry.New[authprovider.Provider]()
	if p.Auth.Local.Enabled {
		r.Register(authlocal.New(authlocal.Config{Users: users, Link: link}))
	}
	if p.Auth.OIDC != nil {
		// Discovery runs in the background for the life of the process.
		r.Register(authoidc.New(*p.Auth.OIDC, p.URLs, nil))
	}
	return r
}

// Exposure picks the exposure that urls.mode names; config validation
// has already limited the mode to a registered one.
func Exposure(p config.Platform) exposure.Backend {
	r := registry.New[exposure.Backend]()
	r.Register(exposurepath.New(p.URLs))
	r.Register(exposuresubdomain.New(p.URLs))
	b, ok := r.Get(p.URLs.Mode)
	if !ok {
		panic("urls.mode " + p.URLs.Mode + " has no exposure backend")
	}
	return b
}

// Placement picks the placement that namespaces.mode names; config
// validation has already limited the mode to a registered one.
func Placement(p config.Platform) placement.Placement {
	r := registry.New[placement.Placement]()
	r.Register(placementshared.New(p.Namespaces))
	r.Register(placementperuser.New(p.Namespaces))
	r.Register(placementperteam.New(p.Namespaces))
	b, ok := r.Get(p.Namespaces.Mode)
	if !ok {
		panic("namespaces.mode " + p.Namespaces.Mode + " has no placement")
	}
	return b
}

// CodeHosts registers the code hosts previews can follow pull requests on.
func CodeHosts(p config.Platform) *registry.Registry[codehost.Host] {
	r := registry.New[codehost.Host]()
	gh := p.Source.Previews.GitHub
	r.Register(codehostgithub.New(codehostgithub.Config{APIURL: gh.APIURL, TokenFile: gh.TokenFile}))
	return r
}

// EnvironmentSource is the environment manager apps and jobs may pick a
// pixi environment from: Nebi when nebi.enabled, else none (nil keeps the
// forms from offering environments).
func EnvironmentSource(p config.Platform) envsource.Source {
	if !p.Nebi.Enabled {
		return nil
	}
	return envnebi.New(envnebi.Config{URL: p.Nebi.URL})
}

// NebiTokens keeps the owner's Nebi token of apps that get one (gateway),
// nil while Nebi is off.
func NebiTokens(p config.Platform, db *store.Store) *nebi.Tokens {
	if !p.Nebi.Enabled {
		return nil
	}
	return &nebi.Tokens{Store: db, Source: envnebi.New(envnebi.Config{URL: p.Nebi.URL})}
}

// NebiURL is Nebi's address for apps with Nebi access, empty while the
// integration is off (then no app gets a Nebi token).
func NebiURL(p config.Platform) string {
	if !p.Nebi.Enabled {
		return ""
	}
	return p.Nebi.URL
}

// LockAgents says whether the coding agents start locked in with Landlock
// (workloads.landlock), for the controller that plans their apps and the
// gateway that shows what they run, on every executor. Where apps run as
// processes NewHost refuses a machine that cannot apply the lock.
func LockAgents(p config.Platform) bool { return p.Workloads.Landlock }

// Engines is the only place job engines are wired in.
func Engines() *engine.Registry {
	r := registry.New[engine.Engine]()
	r.Register(papermill.New())
	r.Register(marimo.New())
	// No Pluto engine: Pluto.jl was dropped (DEPENDENCIES.md).
	r.Register(quarto.New())
	r.Register(command.New())
	r.Register(bash.New())
	return r
}

// DiagnoseEngine is the only place diagnosis rules are wired in.
func DiagnoseEngine() *diagnose.Engine { return diagnose.NewEngine(diagnose.DefaultRules()...) }

// AISettings is the AI settings in force (the config's, with what a
// platform admin set in Settings > AI in their place), or nil when AI is
// off: then no factory ever runs and nothing connects to a model. The
// config's key comes only from the environment (JAVO_AI_API_KEY, a Secret in
// the chart); seal is the gateway's sessions key, which seals the admin's.
func AISettings(c config.AICfg, db *store.Store, seal tink.AEAD) *aisettings.Live {
	return aiSettings(c, os.Getenv("JAVO_AI_API_KEY"), db, seal, aiProviders())
}

func aiSettings(c config.AICfg, key string, db *store.Store, seal tink.AEAD, r *provider.Registry) *aisettings.Live {
	if !c.Enabled {
		return nil
	}
	return &aisettings.Live{
		Config: c, ConfigKey: key, DB: db, Seal: seal,
		// Each provider gets its own circuit breaker.
		Build: func(pc provider.Config) (provider.Provider, error) {
			p, err := r.New(pc)
			if err != nil {
				return nil, err
			}
			return provider.WithBreaker(p), nil
		},
	}
}

// AIService builds the AI features over the settings in force, or nil when
// AI is off (live nil). A config that does not build fails the start, as it
// always has; settings an admin saved only log it, so the admin can fix
// them in the UI. The provider self-test runs in the background now and
// again after each change.
//
// fp finds the owners' stored secret values in log lines before anything is
// sent; nil (no fingerprint key) withholds the log lines of every owner who
// has secrets.
func AIService(ctx context.Context, c config.AICfg, live *aisettings.Live, db *store.Store, fp *secrets.Fingerprinter) (*ai.Service, error) {
	if live == nil {
		return nil, nil
	}
	if buildinfo.Local {
		return nil, buildinfo.LeftOut("the AI assistant (ai.enabled)")
	}
	if _, err := live.Backend(ctx); err != nil {
		over, oerr := live.Overridden(ctx)
		if oerr != nil || !over {
			return nil, err
		}
		slog.Error("the AI settings saved in Settings > AI do not build a provider; AI calls fail until they are fixed", "err", err)
	}
	// Secrets in log lines are hidden before anything is sent (gitleaks rules).
	scan, err := secretScanner()
	if err != nil {
		return nil, fmt.Errorf("load the secret scanner: %w", err)
	}
	svc := &ai.Service{
		Backend: live.Backend, MaxInputRunes: c.MaxInputRunes, Redactor: scan,
		Budget: &ai.DBBudget{Usage: db.AIUsage(), Daily: c.DailyTokensPerUser},
		SecretsOf: func(ctx context.Context, owner string) (func(string) []redact.Span, bool, error) {
			find, unchecked, err := secrets.Lookout(ctx, db, fp, owner)
			if err != nil {
				return nil, false, err
			}
			return func(text string) []redact.Span {
				var out []redact.Span
				for _, m := range find(text) {
					out = append(out, redact.Span{Start: m.Start, End: m.End, Name: m.Name})
				}
				return out
			}, unchecked, nil
		},
	}
	live.Changed = func() { go svc.SelfTest(context.WithoutCancel(ctx)) }
	go svc.SelfTest(ctx)
	return svc, nil
}

// AgentModel is the coding agents' model endpoint, or nil while AI is off.
// It forwards with the key of the settings in force; spend is capped at the
// provider, not here. While those settings give no agent model
// (config.AICfg.AgentModel) it answers that there is none.
func AgentModel(ctx context.Context, c config.AICfg, live *aisettings.Live) *agentproxy.Proxy {
	if live == nil {
		return nil
	}
	if live.AgentModel(ctx) == "" {
		slog.Info("the coding agents' model endpoint has no model now", "why", "it needs provider openai (any OpenAI-compatible server) and ai.agent.enabled, in the config or in Settings > AI")
	}
	return &agentproxy.Proxy{Upstream: live.Upstream, MaxRequestBytes: c.Agent.MaxRequestBytes}
}

// JobAccess is the only place the job reconciler's access rule is chosen:
// the owner's person row, decided by authz like the API decides.
func JobAccess(people identity.PersonByName, roles config.Roles) jobs.Access {
	return jobs.PeopleAccess{People: people, Roles: roles}
}

// newStorageRegistry registers both storage backends; which one a home uses
// is decided by the cluster inventory and the admin's setting.
func newStorageRegistry() *registry.Registry[storage.Backend] {
	r := registry.New[storage.Backend]()
	r.Register(storageperuser.New())
	r.Register(storageshared.New())
	return r
}

// StorageBackendsByMode indexes the registered storage backends by the home
// mode each one serves.
func StorageBackendsByMode() map[model.HomeMode]storage.Backend {
	out := map[model.HomeMode]storage.Backend{}
	for _, b := range newStorageRegistry().All() {
		out[b.Mode()] = b
	}
	return out
}

// StorageResolver is what every pod of a person mounts (apps, job runs, file
// pods), read from the database: the home, the ready folders of the
// person's groups, the scratch limit and co-location.
func StorageResolver(db *store.Store, platform config.Platform) podstorage.Resolver {
	return podstorage.Resolver{
		Homes: db.Homes(), Folders: db.SharedFolders(),
		Cluster:  store.StorageView{Storage: db.Storage(), Config: platform.Storage.Spec()},
		Groups:   ids.PersonGroups{People: db.People(store.IDRange{})},
		Backends: StorageBackendsByMode(),
	}
}

// HomeBackups is the home folders' backups to the bucket, or nil when the
// install names no bucket.
func HomeBackups(p config.Platform) (*storagectl.HomeBackups, error) {
	c := p.Backup.S3
	if c == nil {
		return nil, nil
	}
	t, err := c.Target()
	if err != nil {
		return nil, err
	}
	hb := &storagectl.HomeBackups{Target: t, Keep: c.Homes.Keep(), Rekey: rekeyer(t)}
	if c.CACertFile != "" {
		if hb.CACert, err = os.ReadFile(c.CACertFile); err != nil {
			return nil, fmt.Errorf("backup.s3.caCertFile: %w", err)
		}
	}
	return hb, nil
}

// rekeyTimeout bounds one move of a repository to its namespace's password:
// restic retries a bucket that does not answer for minutes.
const rekeyTimeout = 2 * time.Minute

// rekeyer moves each repository of the bucket t to its namespace's password
// (backup.Rekey) once per process: a repository done, or made after, stays so.
func rekeyer(t restic.Target) func(ctx context.Context, ns, repo string) error {
	var done sync.Map
	return func(ctx context.Context, ns, repo string) error {
		if _, ok := done.Load(repo); ok {
			return nil
		}
		ctx, cancel := context.WithTimeout(ctx, rekeyTimeout)
		defer cancel()
		if err := backup.Rekey(ctx, t.Repo(ResticBin(), repo), backup.NamespacePassword(t.Password, ns)); err != nil {
			return err
		}
		done.Store(repo, true)
		return nil
	}
}

// Bucket is the gateway's view of the backup bucket, or nil when the install
// names none. dbBackups are the server's own SQLite backups, whose copies
// Back up now also makes; empty for a gateway alone (Postgres).
func Bucket(p config.Platform, db *store.Store, dbBackups store.BackupPlan) (*admin.Bucket, error) {
	c := p.Backup.S3
	if c == nil {
		return nil, nil
	}
	t, err := c.Target()
	if err != nil {
		return nil, err
	}
	b := &admin.Bucket{
		Endpoint: c.Endpoint, Name: c.Bucket, Prefix: c.Prefix, Keep: c.Homes.Keep(),
		HomeSnapshots: func(ctx context.Context, ns, owner string) ([]restic.Snapshot, error) {
			return backup.HomeSnapshots(ctx, t, ResticBin(), ns, owner)
		},
	}
	copier := DatabaseCopier(t, db, dbBackups.Keep)
	b.DatabaseCopies, b.FetchDatabase = copier.List, copier.Fetch
	if dbBackups.Dir != "" {
		b.CopyDatabase = copier.CopyRun
	}
	return b, nil
}

// DatabaseCopier copies the database backups to the bucket, keeping as many
// there as on disk.
func DatabaseCopier(t restic.Target, db *store.Store, keep int) backup.DatabaseCopier {
	return backup.DatabaseCopier{Repo: t.Repo(ResticBin(), backup.DatabaseRepo), Facts: db.Facts(), Keep: keep, Dest: t.Dest(backup.DatabaseRepo)}
}

// ResticBin is the restic the javo image carries (on PATH); JAVO_RESTIC
// names another.
func ResticBin() string {
	if b := os.Getenv("JAVO_RESTIC"); b != "" {
		return b
	}
	return "restic"
}

// StoreLimits are the per-person caps of the limits section, for
// store.Store.SetLimits.
func StoreLimits(c config.LimitsCfg) store.Limits {
	return store.Limits{
		Apps: int64(c.Apps), RunningApps: int64(c.RunningApps), Previews: int64(c.Previews), Jobs: int64(c.Jobs),
		RunsPerJob: int64(c.ActiveRunsPerJob), RunsPerPerson: int64(c.ActiveRunsPerPerson),
		APITokens: int64(c.APITokens), PendingPlans: int64(c.PendingPlans),
	}
}
