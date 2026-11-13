//go:build linux || darwin

package wiring

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/capacity"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/controller/dbsource"
	"github.com/nebari-dev/javo/internal/controller/loop"
	"github.com/nebari-dev/javo/internal/controller/secretskey"
	storagectl "github.com/nebari-dev/javo/internal/controller/storage"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/egressproxy"
	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/docker"
	"github.com/nebari-dev/javo/internal/executor/process"
	"github.com/nebari-dev/javo/internal/executor/vault"
	"github.com/nebari-dev/javo/internal/files"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/netprobe"
	"github.com/nebari-dev/javo/internal/netprobe/dockerprobe"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/dockerhome"
	"github.com/nebari-dev/javo/internal/storage/hosthome"
	"github.com/nebari-dev/javo/internal/storage/ids"
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
	// Runs runs the jobs' runs, on the daemon or the machine Apps runs
	// apps on.
	Runs executor.Executor
	// PlanFor says what an app or a run is planned with to run as
	// processes of this machine; nil where they run in containers.
	PlanFor func(workload.Ref) workload.Host
	// Setup sets up on the controller's driver what a host has besides
	// apps and jobs: what makes each person's home, the answer to a group
	// folder (none is offered here) and, on Docker, the isolation probe.
	Setup func(d loop.Driver, feed *dbsource.Feed) error
	// Files reads and writes people's homes for the gateway.
	Files *files.Lister
	// policy answers the gate of apps with no login of their own.
	policy netprobe.Reader
	// serving is what runs beside the gateway until the server ends
	// (Serve): on Docker, the forward proxy and the reaping of idle file
	// helpers.
	serving []func(context.Context)
	// Keys opens people's secrets and the apps' bundles; SessionsKey seals
	// the sessions' provider tokens; Fingerprinter marks secret values so
	// they are found in logs. All three come from files of keysDir, made
	// on the first start.
	Keys          *secrets.Keys
	SessionsKey   tink.AEAD
	Fingerprinter *secrets.Fingerprinter
	// APIURL is where an app reaches the gateway.
	APIURL string
	// closing ends what the host holds, in order.
	closing []func()
}

// Close ends the executors: apps and runs that are processes stop with
// them, those that are containers run on.
func (h *Host) Close() {
	for _, end := range h.closing {
		end()
	}
}

// Machines is what is known of the machines apps run on: nothing, so
// every size fits.
func (*Host) Machines() capacity.Source { return capacity.Unknown{} }

// NetworkPolicy answers the gate of apps with no login of their own
// (status.LoginlessBlocked): on Docker the stored result of the isolation
// probe, which the controller runs here; as processes, processGate.
func (h *Host) NetworkPolicy() netprobe.Reader { return h.policy }

// Serve starts what runs beside the gateway and returns; all of it ends
// with ctx.
func (h *Host) Serve(ctx context.Context) {
	for _, run := range h.serving {
		go run(ctx)
	}
}

// processGate is the owner's decision for executor process: an app with
// no login of its own (appLogin: Disabled) runs there, because the
// desktop app needs such apps. The gate that holds one back until its
// network is proven closed asks a netprobe.Reader, and this is the
// Reader of a machine that runs apps as processes: it answers Enforced,
// always, and nothing is probed.
//
// What holds there instead: Javo listens on loopback only and serves one
// person (onePersonsMachine), every app is told to listen on loopback,
// and one that opens a port to the network is killed (the process
// executor's guard). So no other machine reaches such an app, and through
// the gateway only the signed-in person does.
//
// What does not: other users of the same machine, and every program on
// it, can connect to the app's port on loopback directly, past the
// gateway's sign-in.
type processGate struct{}

func (processGate) Get(context.Context) (netprobe.Result, error) {
	return netprobe.Result{State: netprobe.Enforced, Evidence: []string{
		"not measured: apps run as processes of one person's machine and listen on loopback only; other users and programs of that machine can reach their ports",
	}}, nil
}

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

// HostInputs is what NewHost needs besides the config.
type HostInputs struct {
	DB *store.Store
	// Namespace is the name apps and homes are kept under in the database.
	Namespace string
	// Listen is the address the gateway listens on (--listen).
	Listen string
	// RunnerImage is the platform's own image (--runner-image).
	RunnerImage string
	// FilesImage is the rclone image of the file helpers (--files-image)
	// and FilesIdleAfter how long an idle one is kept; Docker only.
	FilesImage     string
	FilesIdleAfter time.Duration
	// Egress is what apps may reach through the forward proxy (the
	// --egress-*-cidrs flags); Docker only.
	Egress egressproxy.Rules
	// ProbeEvery is how often the isolation probe runs and ProbeTimeout
	// how long one may take (--netprobe-interval, --netprobe-timeout);
	// Docker only.
	ProbeEvery, ProbeTimeout time.Duration
}

// The files of keysDir.
const (
	secretsKeyFile     = "secrets-key"
	sessionsKeyFile    = "sessions-key"
	fingerprintKeyFile = "secret-fingerprint-key"
)

// The scopes of one Javo on a Docker daemon. Each is a label whose value
// is the name of Javo's own container, put on everything made in that
// scope (docker.Client.Labels) and asked for whenever it is listed,
// watched, adopted or swept. No thing carries two of them, so none of
// the four ever finds, adopts or removes what another made, and two
// Javos on one daemon never touch each other's.
const (
	// scopeWorkloads is apps and job runs. Within it a run also carries
	// docker.LabelRun, which is how the two executors keep apart.
	scopeWorkloads = "javo.gateway"
	// scopeFiles is the file helpers, which run through an executor of
	// their own: the app executor would adopt them, and the app sweep
	// stop them, if they shared its scope.
	scopeFiles = "javo.gateway.files"
	// scopeHomes is the home volumes and the containers they are handed
	// to their owners through.
	scopeHomes = "javo.gateway.homes"
	// scopeProbe is the two workloads of the isolation probe.
	scopeProbe = "javo.gateway.probe"
)

// scoped is a client of the daemon at socket for one scope of the Javo
// in the container called gateway.
func scoped(socket, scope, gateway string) *docker.Client {
	c := docker.New(socket)
	c.Labels = map[string]string{scope: gateway}
	return c
}

// euid is the user Javo runs as, and hostname the name of the machine
// (inside a container, the container's) it runs on.
var (
	euid     = os.Geteuid
	hostname = os.Hostname
)

// NewHost builds what runs apps for the config's executor, or nil on
// Kubernetes, where the cluster does. It is the only place that asks which
// executor is configured. It refuses to start what it cannot keep safe:
// keys or folders that others can open, a Docker daemon that does not
// answer, is too old, lacks the configured runtime or Javo's own
// container, a Javo that does not run in that container, and, for apps as
// processes, root, a listener beyond loopback and more than one person.
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
		if err := agentLockWorks(ctx, cfg); err != nil {
			return nil, fmt.Errorf("executor process: %w", err)
		}
	}
	if err := h.loadKeys(ctx, cfg.KeysDir, in.DB); err != nil {
		return nil, err
	}
	h.Secrets = vault.Secrets{Keys: h.Keys}
	switch cfg.Executor {
	case config.ExecutorDocker:
		if err := h.onDocker(ctx, cfg, in, port); err != nil {
			h.Close()
			return nil, err
		}
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
		runs, err := x.Runs(ctx)
		if err != nil {
			x.Close()
			return nil, err
		}
		h.Apps, h.Runs, h.Place, h.PlanFor, h.policy = x, runs, noPlace{}, x.Host, processGate{}
		// The runs' executor first: it stands on the apps' one.
		h.closing = []func(){runs.Close, x.Close}
		h.APIURL = "http://" + in.Listen
		rows := storagectl.Rows{Store: in.DB, Config: cfg.Storage.Spec()}
		h.Setup = func(d loop.Driver, feed *dbsource.Feed) error {
			if err := (&storagectl.HostHomeReconciler{Rows: rows, Root: root}).Setup(d, feed); err != nil {
				return err
			}
			return (&storagectl.NoFolderReconciler{
				Rows: rows, Why: "apps run as processes of one person's machine here, where there is no group to share a folder with",
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

// onDocker fills h in for apps that run as containers of the daemon the
// config names. Javo must itself run in the container docker.gateway
// names: that container is on every workload's network, which is how
// Javo checks an app's port, reaches the file helpers and serves the
// forward proxy. port is the gateway's own.
func (h *Host) onDocker(ctx context.Context, cfg config.Platform, in HostInputs, port string) error {
	d := cfg.Docker
	workloads := scoped(d.Socket, scopeWorkloads, d.Gateway)
	// Apps find the proxy at the gateway container's name, on their own
	// network; they reach nothing else.
	proxyPort := strconv.Itoa(d.ProxyPort)
	options := docker.Options{
		Client: workloads, Secrets: h.Secrets, Gateway: d.Gateway, RunnerImage: in.RunnerImage,
		Pool: d.AddressPool(), Runtime: d.Runtime, Proxy: "http://" + net.JoinHostPort(d.Gateway, proxyPort),
	}
	// Before anything on the daemon is adopted or tidied.
	if _, err := workloads.Daemon(ctx); err != nil {
		return fmt.Errorf("docker executor: %w", err)
	}
	if err := inGateway(ctx, workloads, d.Gateway); err != nil {
		return err
	}
	x, err := docker.NewExecutor(ctx, options)
	if err != nil {
		return err
	}
	h.Apps, h.Place = x, x
	h.closing = []func(){x.Close}
	runs, err := x.Runs(ctx)
	if err != nil {
		return err
	}
	// The runs' executor first: it stands on the apps' one.
	h.Runs, h.closing = runs, []func(){runs.Close, x.Close}
	// Apps reach the gateway's container by its name, on their network.
	h.APIURL = "http://" + net.JoinHostPort(d.Gateway, port)

	// Homes: a volume per person, handed to its owner.
	rows := storagectl.Rows{Store: in.DB, Config: cfg.Storage.Spec()}
	uids := ids.People{People: in.DB.People(store.IDRange{}), Mode: cfg.Identity.UIDMode}
	volumes := dockerhome.Volumes{Client: scoped(d.Socket, scopeHomes, d.Gateway), Image: in.RunnerImage}

	// Files: a helper per person, through an executor of its own scope
	// that gives its workloads no way out.
	logins := files.NewLogins()
	fileOptions := options
	fileOptions.Client, fileOptions.Secrets, fileOptions.Proxy = scoped(d.Socket, scopeFiles, d.Gateway), logins, ""
	fx, err := docker.NewExecutor(ctx, fileOptions)
	if err != nil {
		return err
	}
	h.closing = append([]func(){fx.Close}, h.closing...)
	helpers, err := files.NewHelpers(files.HelpersConfig{
		Exec: fx, Logins: logins, Volumes: volumes, Namespace: in.Namespace, Image: in.FilesImage, UIDs: uids,
		Storage:   podstorage.Provisioner{Resolver: StorageResolver(in.DB, cfg), Maker: in.DB.Homes()},
		IdleAfter: in.FilesIdleAfter,
	})
	if err != nil {
		return err
	}
	h.Files = files.NewLister(helpers, files.Rclone{})
	// Helpers end with the gateway, which alone holds their passwords;
	// before their executor is let go of.
	h.closing = append([]func(){func() {
		ctx, cancel := context.WithTimeout(context.Background(), helpersEnd)
		defer cancel()
		if err := helpers.End(ctx); err != nil {
			slog.Warn("file helpers were left running; the next start replaces them", "err", err)
		}
	}}, h.closing...)

	// The way out: a forward proxy on a port of this container, for the
	// workloads of this Javo only. Its port is the one listener besides
	// --listen; apps reach every port of this container anyway.
	proxy, err := egressproxy.New(egressproxy.Options{Rules: in.Egress, Networks: egressproxy.Docker(workloads, d.AddressPool())})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("", proxyPort))
	if err != nil {
		return fmt.Errorf("the forward proxy of apps (docker.proxyPort): %w", err)
	}
	h.closing = append([]func(){func() { _ = listener.Close() }}, h.closing...)
	h.serving = []func(context.Context){helpers.Run, func(ctx context.Context) {
		if err := proxy.Serve(ctx, listener); err != nil {
			slog.Error("the forward proxy of apps ended: apps have no way to the internet", "err", err)
		}
	}}

	// The gate of apps with no login of their own: the probe's result,
	// measured anew by every Javo that starts. What an earlier one stored
	// (on another daemon, or a cluster) proves nothing here.
	policy := &netprobe.Store{Facts: in.DB.Facts()}
	if err := policy.Put(ctx, netprobe.Result{State: netprobe.Unknown, Evidence: []string{"not measured yet since Javo started"}}); err != nil {
		return err
	}
	h.policy = policy
	control, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("--listen %q: %w", in.Listen, err)
	}
	probe := &netprobe.Loop{
		Prober: &dockerprobe.Prober{
			Client: scoped(d.Socket, scopeProbe, d.Gateway), Gateway: d.Gateway, Image: in.RunnerImage,
			Pool: d.AddressPool(), Runtime: d.Runtime, ControlPort: control, ControlPath: "/healthz", Timeout: in.ProbeTimeout,
		},
		Store: policy, Every: in.ProbeEvery, RetryMin: probeRetry,
	}
	h.Setup = func(drv loop.Driver, feed *dbsource.Feed) error {
		if err := (&storagectl.DockerHomeReconciler{Rows: rows, Volumes: volumes, UIDs: uids}).Setup(drv, feed); err != nil {
			return err
		}
		if err := (&storagectl.NoFolderReconciler{
			Rows: rows, Why: "apps run as Docker containers here, where a group has no volume of its own yet",
		}).Setup(drv, feed); err != nil {
			return err
		}
		return drv.Add(probe)
	}
	return nil
}

// helpersEnd is how long stopping the file helpers may take when Javo
// ends.
const helpersEnd = 30 * time.Second

// probeRetry is the first wait before the Docker probe runs again after
// it could not tell: its first run starts with the gateway, which may not
// answer yet.
const probeRetry = 5 * time.Second

// inGateway refuses a Javo that does not run in the container called
// gateway, by that container's host name, which is this machine's inside
// it. From anywhere else Javo is on no workload's network: no app would
// ever be seen ready, no file helper reached and no app would find the
// proxy.
func inGateway(ctx context.Context, c *docker.Client, gateway string) error {
	have, err := c.ContainerInspect(ctx, gateway)
	if err != nil {
		return fmt.Errorf("docker executor: the gateway container %q: %w", gateway, err)
	}
	self, err := hostname()
	if err != nil {
		return err
	}
	if have.Config.Hostname != self {
		return fmt.Errorf("docker.gateway %q is a container whose host name is %q, and Javo runs on %q: with executor docker Javo must run inside the container docker.gateway names, which is on every app's network (see deploy/compose/docker.yaml)", gateway, have.Config.Hostname, self)
	}
	return nil
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

// goos is the system Javo runs on.
var goos = runtime.GOOS

// agentLockWorks is why this machine cannot start the coding agents
// locked in with Landlock although the config asks for it
// (workloads.landlock); nil when it can, or the lock is off. Apps here are
// processes of this machine, so the lock is this machine's kernel's:
// Landlock is Linux's, and on Linux the javo-sandbox of the platform's
// files (process.filesDir, bin/javo-sandbox) must lock a command in now.
// Javo does not start otherwise: an agent would either never start or,
// worse, be thought locked in.
func agentLockWorks(ctx context.Context, cfg config.Platform) error {
	if !LockAgents(cfg) {
		return nil
	}
	if goos != "linux" {
		return fmt.Errorf("workloads.landlock is on, and Landlock needs Linux: this machine is %s, where the coding agents cannot be locked in; turn workloads.landlock off to run them with every file of yours in reach", goos)
	}
	if cfg.Process.FilesDir == "" {
		return errors.New("workloads.landlock is on, and process.filesDir is not set: the coding agents start through bin/javo-sandbox of the platform's files")
	}
	sandbox := filepath.Join(cfg.Process.FilesDir, "bin", "javo-sandbox")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Everything readable, nothing writable: enough to run true locked in.
	if out, err := exec.CommandContext(ctx, sandbox, "--ro", "/", "--", "true").CombinedOutput(); err != nil {
		return fmt.Errorf("workloads.landlock is on, and %s could not lock a command in: %w: %s", sandbox, err, strings.TrimSpace(string(out)))
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
