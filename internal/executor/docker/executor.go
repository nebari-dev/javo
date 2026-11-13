package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/executor"
	"github.com/nebari-dev/javo/internal/executor/supervise"
	"github.com/nebari-dev/javo/internal/workload"
)

// Options is what an Executor is made of.
type Options struct {
	// Client talks to the Docker daemon. Required.
	Client *Client
	// Secrets is where the values workloads read come from. Required.
	Secrets executor.Secrets
	// Gateway is the name or id of the container Javo's gateway runs in.
	// It is put on every workload's network, and is the only thing a
	// workload reaches. Required.
	Gateway string
	// RunnerImage is the platform's own image. A workload's sandbox runs
	// its "javo pause", and volumes are prepared through it. Required.
	RunnerImage string
	// Pool is the address range workloads' networks are cut from, 16
	// addresses each; the zero Pool is DefaultPool.
	Pool netip.Prefix
	// Runtime is the container runtime every container runs in (a
	// sandbox such as runsc); empty is the daemon's own. NewExecutor refuses a
	// runtime the daemon does not know.
	Runtime string
	// Proxy is the URL of the gateway's forward proxy
	// (internal/egressproxy) as a workload reaches it: the gateway
	// container's name and the proxy's port, such as http://javo:3128.
	// Every unit gets it as HTTP_PROXY, HTTPS_PROXY and their lower-case
	// twins, with NO_PROXY for the gateway and loopback. Empty gives
	// units no way to the internet.
	Proxy string
	// Logins gives the registry logins kept under a name a Spec's
	// PullSecrets holds; nil is none.
	Logins func(name string) []RegistryAuth
	// Grace is how long a unit has to end once asked to, before it is
	// killed; zero is ten seconds.
	Grace time.Duration
}

// DefaultPool is where workloads' networks take their addresses when
// Options names no pool: room for 4096 workloads, where the daemon's own
// pools end after about 30 networks. It is a private range (RFC 1918)
// away from the ones a Docker host tends to use already: the daemon's
// own pools (inside 172.16.0.0/12 and 192.168.0.0/16), and the
// usual defaults of Kubernetes distributions (10.42.0.0/15, 10.96.0.0/12
// and 10.244.0.0/16). A host whose own network lies in it must name
// another.
var DefaultPool = netip.MustParsePrefix("10.213.0.0/16")

// ErrBatch is Start's answer to a job run.
var ErrBatch = errors.New("docker executor: job runs are not supported yet")

// Executor runs apps as containers on one Docker host: each workload is a
// sandbox on a network of its own and one container per unit beside it.
// It is an executor.Executor, built on internal/executor/supervise.
//
// The containers outlive Javo: Close leaves them running, and the next
// Executor on the same daemon adopts them.
type Executor struct {
	*supervise.Supervisor
	eng *engine
	// starting lets one Start take a network at a time: two would pick
	// the same free range of the pool.
	starting sync.Mutex
	close    context.CancelFunc
}

var _ executor.Executor = (*Executor)(nil)

// internals are the parts of an Executor only a test replaces.
type internals struct {
	// pause is the sandbox's program.
	pause Tool
	// dial is how a readiness check connects; nil is from this process.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// every is the pace of readiness checks; zero is a second.
	every time.Duration
}

// NewExecutor makes an Executor on the daemon behind o.Client and adopts the
// workloads an earlier one left running there. It refuses a daemon that
// does not answer or is too old (ErrTooOld), a runtime the daemon does
// not know, and a gateway container that does not exist.
func NewExecutor(ctx context.Context, o Options) (*Executor, error) {
	return build(ctx, o, internals{pause: Tool{Image: o.RunnerImage, Command: []string{"/javo", "pause"}}})
}

func build(ctx context.Context, o Options, in internals) (*Executor, error) {
	switch {
	case o.Client == nil:
		return nil, errors.New("docker executor: no client of the Docker daemon")
	case o.Secrets == nil:
		return nil, errors.New("docker executor: no secrets to read workloads' values from")
	case o.Gateway == "":
		return nil, errors.New("docker executor: no gateway container named")
	case o.RunnerImage == "":
		return nil, errors.New("docker executor: no runner image named")
	}
	if o.Proxy != "" {
		if _, err := proxyHost(o.Proxy); err != nil {
			return nil, fmt.Errorf("docker executor: %w", err)
		}
	}
	d, err := o.Client.Daemon(ctx)
	if err != nil {
		return nil, fmt.Errorf("docker executor: %w", err)
	}
	if o.Runtime != "" && !d.HasRuntime(o.Runtime) {
		return nil, fmt.Errorf("docker executor: the daemon has no runtime %q (it has %v)", o.Runtime, d.Runtimes)
	}
	if _, err := o.Client.ContainerInspect(ctx, o.Gateway); err != nil {
		return nil, fmt.Errorf("docker executor: the gateway container %q: %w", o.Gateway, err)
	}
	lasting, cancel := context.WithCancel(context.Background())
	eng := &engine{
		c: o.Client, secrets: o.Secrets, gateway: o.Gateway, pause: in.pause,
		pool: o.Pool, runtime: o.Runtime, proxy: o.Proxy, logins: o.Logins, grace: o.Grace, ctx: lasting,
	}
	if !eng.pool.IsValid() {
		eng.pool = DefaultPool
	}
	if eng.logins == nil {
		eng.logins = func(string) []RegistryAuth { return nil }
	}
	if eng.grace <= 0 {
		eng.grace = 10 * time.Second
	}
	x := &Executor{
		Supervisor: supervise.New(supervise.Config{Engine: eng, Every: in.every, Dial: in.dial}),
		eng:        eng, close: cancel,
	}
	if err := x.Supervisor.Adopt(ctx); err != nil {
		x.Close()
		return nil, err
	}
	return x, nil
}

// Start runs the app s. It refuses a job run with ErrBatch, and a Spec
// that cannot be run confined (as root, without a memory limit, with a
// volume that is a path of the host) with what is wrong with it. It
// takes the workload's network from the pool before it returns, so a
// pool with no range left is its error (ErrPoolFull); everything after
// that happens behind it, and Observe says how far it is.
func (x *Executor) Start(ctx context.Context, s workload.Spec) error {
	if s.Batch != nil {
		return ErrBatch
	}
	x.starting.Lock()
	defer x.starting.Unlock()
	if st, err := x.Observe(ctx, s.Ref); err != nil || st != nil {
		return err
	}
	if err := x.eng.confines(s); err != nil {
		return fmt.Errorf("docker executor: %w", err)
	}
	if err := Isolate(ctx, x.eng.c, s.Ref, x.eng.gateway, x.eng.pool); err != nil {
		return fmt.Errorf("docker executor: workload %s/%s gets no network: %w", s.Ref.Namespace, s.Ref.Name, err)
	}
	return x.Supervisor.Start(ctx, s)
}

// confines reports why s cannot be run as confined containers, before
// anything is made for it. Values of bundles are not looked at here.
func (e *engine) confines(s workload.Spec) error {
	if _, err := SandboxBody(s, e.pause, e.runtime); err != nil {
		return err
	}
	filled := Inputs{Secret: func(workload.SecretRef) (string, bool) { return "", true }, Proxy: e.proxy}
	for _, u := range append(slices.Clone(s.Steps), s.Main) {
		if _, err := ContainerBody(s, u, filled); err != nil {
			return err
		}
	}
	return nil
}

// Stop ends the workload's containers and removes them, its network
// (which goes back to the pool) and the volumes that end with it. Its
// bundles and the person's storage stay.
func (x *Executor) Stop(ctx context.Context, ref workload.Ref) error {
	if _, err := NetworkName(ref); err != nil {
		// Nothing was ever made under such a name.
		return nil
	}
	if err := x.Supervisor.Stop(ctx, ref); err != nil {
		return err
	}
	return Teardown(ctx, x.eng.c, ref, x.eng.gateway)
}

// Close ends the Executor and stops nothing: the workloads run on, for
// the next Executor to adopt.
func (x *Executor) Close() {
	x.Supervisor.Leave()
	x.close()
}

// List names every workload that has something on the daemon (a
// container or a network carrying Javo's labels) or is being started.
func (x *Executor) List(ctx context.Context) ([]workload.Ref, error) {
	out, err := x.Supervisor.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, r := range out {
		seen[r.Namespace+"/"+r.Name] = true
	}
	add := func(labels map[string]string) {
		if r := RefOf(labels); r.Name != "" && !seen[r.Namespace+"/"+r.Name] {
			seen[r.Namespace+"/"+r.Name] = true
			out = append(out, r)
		}
	}
	containers, err := x.eng.c.ContainerList(ctx, x.eng.c.mine(Managed)...)
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		add(c.Labels)
	}
	networks, err := x.eng.c.NetworkList(ctx, x.eng.c.mine(Managed)...)
	if err != nil {
		return nil, err
	}
	for _, n := range networks {
		add(n.Labels)
	}
	return out, nil
}

// watched are the changes to a container that can change a workload's
// state. A readiness check run in a container is an event too, once a
// second, and changes nothing.
var watched = []string{"create", "start", "die", "oom", "destroy"}

// Watch reports the workload of each change the supervisor makes or
// sees, and of each change the daemon reports for a container of Javo's:
// one made behind the executor's back shows too. A daemon that ends its
// event stream is asked again.
func (x *Executor) Watch(ctx context.Context, changed func(workload.Ref)) error {
	go func() { _ = x.Supervisor.Watch(ctx, changed) }()
	for ctx.Err() == nil {
		_ = x.eng.c.Events(ctx, x.eng.c.mine(Managed), func(e Event) {
			if r := RefOf(e.Actor.Attributes); r.Name != "" && slices.Contains(watched, e.Action) {
				changed(r)
			}
		})
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	return nil
}
