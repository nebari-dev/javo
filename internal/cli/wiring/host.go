//go:build linux || darwin

package wiring

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	storagectl "github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/process"
	"github.com/nebari-dev/javo/internal/executor/vault"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/hosthome"
	"github.com/nebari-dev/javo/internal/storage/podstorage"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/workload"
)

// Host is where apps run when there is no cluster: one Docker daemon, or
// the machine Javo itself runs on. It is built once per process, by
// NewHost, and the gateway and the controller share it: the executor that
// starts an app is the one asked where it listens and what it printed.
type Host struct {
	// Executor is the config's name for it (docker, process), for messages.
	Executor string
	// Apps runs the apps, Secrets keeps the values they read (sealed, in
	// the database) and Place is what an app has beside its workload.
	Apps    executor.Executor
	Secrets executor.Secrets
	Place   workspace.Place
	// Runs runs the jobs' runs. Nil: no executor here runs jobs yet, so
	// every job is Blocked and says so. This is the one place to plug a
	// runs executor in.
	Runs executor.Executor
	// PlanFor says what an app is planned with to run as processes of this
	// machine; nil where apps run in containers.
	PlanFor func(workload.Ref) workload.Host
	// Homes sets up what makes each person's home. Nil on Docker, where
	// homes (named volumes) are not built yet: this is the one place for
	// their provider, and until then an app there waits for its home.
	Homes func(d loop.Driver, feed *dbsource.Feed) error
	// Files reads and writes people's homes for the gateway. Nil on
	// Docker, with Homes: the file routes then answer that they are not
	// available.
	Files *files.Lister
	// Keys opens people's secrets and the apps' bundles; SessionsKey seals
	// the sessions' provider tokens; Fingerprinter marks secret values so
	// they are found in logs. All three come from files of keysDir, made
	// on the first start.
	Keys          *secrets.Keys
	SessionsKey   tink.AEAD
	Fingerprinter *secrets.Fingerprinter
	// APIURL is where an app reaches the gateway.
	APIURL string
	// Close ends the executor: apps that are processes stop with it, apps
	// that are containers run on.
	Close func()
}

// Machines is what is known of the machines apps run on: nothing, so
// every size fits.
func (*Host) Machines() capacity.Source { return unknownMachines{} }

// NetworkPolicy is whether apps are proven to be reachable by the gateway
// only: never, so an app with no login of its own does not start. Nothing
// here probes it yet.
func (*Host) NetworkPolicy() netprobe.Reader { return unprobed{} }

// People is the people the gateway signs in. Where apps run as processes
// the first person to sign in stays the only one: a second would run
// their apps as the same user, with the first one's files.
func (h *Host) People(p *store.People) authn.People {
	if h.Executor != config.ExecutorProcess {
		return p
	}
	return onePerson{p}
}

type onePerson struct{ *store.People }

func (o onePerson) Ensure(ctx context.Context, c store.Claims) (model.Person, error) {
	switch _, err := o.BySubject(ctx, c.Issuer, c.Subject); {
	case errors.Is(err, store.ErrNotFound):
		ids, err := o.IDs(ctx)
		if err != nil {
			return model.Person{}, err
		}
		if len(ids) > 0 {
			return model.Person{}, errors.New("executor process serves one person, and someone else has signed in here before")
		}
	case err != nil:
		return model.Person{}, err
	}
	return o.People.Ensure(ctx, c)
}

type unknownMachines struct{}

func (unknownMachines) Cluster(context.Context) (capacity.Cluster, error) { return nil, nil }

type unprobed struct{}

func (unprobed) Get(context.Context) (netprobe.Result, error) { return netprobe.Result{}, nil }

// HostInputs is what NewHost needs besides the config.
type HostInputs struct {
	DB *store.Store
	// Namespace is the name apps and homes are kept under in the database.
	Namespace string
	// Listen is the address the gateway listens on (--listen).
	Listen string
	// RunnerImage is the platform's own image (--runner-image).
	RunnerImage string
}

// The files of keysDir.
const (
	secretsKeyFile     = "secrets-key"
	sessionsKeyFile    = "sessions-key"
	fingerprintKeyFile = "secret-fingerprint-key"
)

// gatewayLabel marks what one Javo made on a Docker daemon with the name
// of its own container, so two of them on one daemon never adopt, list or
// sweep each other's apps.
const gatewayLabel = "javo.gateway"

// euid is the user Javo runs as.
var euid = os.Geteuid

// NewHost builds what runs apps for the config's executor, or nil on
// Kubernetes, where the cluster does. It is the only place that asks which
// executor is configured. It refuses to start what it cannot keep safe:
// keys or folders that others can open, a Docker daemon that does not
// answer, is too old, lacks the configured runtime or Javo's own
// container, and, for apps as processes, root, a listener beyond loopback
// and more than one person.
func NewHost(ctx context.Context, cfg config.Platform, in HostInputs) (*Host, error) {
	if cfg.OnKubernetes() {
		return nil, nil
	}
	h := &Host{Executor: cfg.Executor}
	_, port, err := net.SplitHostPort(in.Listen)
	if err != nil {
		return nil, fmt.Errorf("--listen %q: %w", in.Listen, err)
	}
	if cfg.Executor == config.ExecutorProcess {
		if err := onePersonsMachine(ctx, in); err != nil {
			return nil, fmt.Errorf("executor process: %w", err)
		}
	}
	if err := h.loadKeys(ctx, cfg.KeysDir, in.DB); err != nil {
		return nil, err
	}
	h.Secrets = vault.Secrets{Keys: h.Keys}
	switch cfg.Executor {
	case config.ExecutorDocker:
		c := docker.New(cfg.Docker.Socket)
		c.Labels = map[string]string{gatewayLabel: cfg.Docker.Gateway}
		x, err := docker.NewExecutor(ctx, docker.Options{
			Client: c, Secrets: h.Secrets, Gateway: cfg.Docker.Gateway, RunnerImage: in.RunnerImage,
			Pool: cfg.Docker.AddressPool(), Runtime: cfg.Docker.Runtime,
		})
		if err != nil {
			return nil, err
		}
		h.Apps, h.Place, h.Close = x, x, x.Close
		// Apps reach the gateway's container by its name, on their network.
		h.APIURL = "http://" + net.JoinHostPort(cfg.Docker.Gateway, port)
	case config.ExecutorProcess:
		root, err := hosthome.Open(cfg.Process.HomesDir)
		if err != nil {
			return nil, err
		}
		x, err := process.New(process.Options{
			Dir: cfg.Process.StateDir, Secrets: h.Secrets, RunnerImage: in.RunnerImage,
			Pixi: cfg.Process.Pixi, Files: cfg.Process.FilesDir,
		})
		if err != nil {
			return nil, err
		}
		h.Apps, h.Place, h.PlanFor, h.Close = x, noPlace{}, x.Host, x.Close
		h.APIURL = "http://" + in.Listen
		h.Homes = func(d loop.Driver, feed *dbsource.Feed) error {
			return (&storagectl.HostHomeReconciler{
				Rows: storagectl.Rows{Store: in.DB, Config: cfg.Storage.Spec()}, Root: root,
			}).Setup(d, feed)
		}
		h.Files = files.NewLister(files.Folders{
			Homes:     podstorage.Provisioner{Resolver: StorageResolver(in.DB, cfg), Maker: in.DB.Homes()},
			Namespace: in.Namespace, Root: root.Dir(),
		}, files.Direct{})
	default:
		return nil, fmt.Errorf("executor %q has nothing that runs apps", cfg.Executor)
	}
	return h, nil
}

// loadKeys reads Javo's own keys from dir, making each on the first
// start. A key that does not open the secrets already stored is refused:
// every app's values are sealed with it.
func (h *Host) loadKeys(ctx context.Context, dir string, db *store.Store) error {
	file := func(name string) secretskey.File { return secretskey.File{Dir: dir, Name: name, Create: true} }
	kek, err := file(secretsKeyFile).Load(ctx)
	if err != nil {
		return err
	}
	h.Keys = &secrets.Keys{Store: db, KEK: kek}
	if err := h.Keys.Ensure(ctx); err != nil {
		return fmt.Errorf("the key in %s does not open the secrets stored in the database: %w", dir, err)
	}
	if h.SessionsKey, err = file(sessionsKeyFile).Load(ctx); err != nil {
		return err
	}
	b, err := file(fingerprintKeyFile).Bytes(ctx)
	if err != nil {
		return err
	}
	if h.Fingerprinter, err = secrets.FingerprinterFromBytes(b); err != nil {
		return fmt.Errorf("the key in file %s: %w", fingerprintKeyFile, err)
	}
	return nil
}

// onePersonsMachine is why Javo may not run apps as processes here: they
// run as Javo's own user with nothing between them, so it must not be
// root, must not be reachable from another machine, and must serve one
// person only.
func onePersonsMachine(ctx context.Context, in HostInputs) error {
	if euid() == 0 {
		return errors.New("refusing to run apps as root (uid 0)")
	}
	host, _, _ := net.SplitHostPort(in.Listen)
	if addr, err := netip.ParseAddr(host); host != "localhost" && (err != nil || !addr.IsLoopback()) {
		return fmt.Errorf("--listen %q is not a loopback address such as 127.0.0.1:8080: apps here are not kept apart from other machines", in.Listen)
	}
	people, err := in.DB.People(store.IDRange{}).IDs(ctx)
	if err != nil {
		return err
	}
	if len(people) > 1 {
		return fmt.Errorf("the database holds %d people, and apps here all run as one user: it serves one person", len(people))
	}
	return nil
}

// noPlace is the Place of an app that is processes of this machine: it
// has nothing beside its workload.
type noPlace struct{}

func (noPlace) Hold(context.Context, workload.Ref, int32) error { return nil }
func (noPlace) Tidy(context.Context, workload.Ref, func(context.Context, string) error) error {
	return nil
}
func (noPlace) Release(context.Context, workload.Ref) error { return nil }
