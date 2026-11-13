package config

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
)

// The values of executor: what runs apps.
const (
	ExecutorKubernetes = "kubernetes"
	ExecutorDocker     = "docker"
	ExecutorProcess    = "process"
)

// DefaultDockerSocket is where a Docker daemon listens unless told
// otherwise.
const DefaultDockerSocket = "/var/run/docker.sock"

// DefaultProxyPort is where the forward proxy of a Docker install listens
// in the container Javo runs in, unless told otherwise.
const DefaultProxyPort = 3128

// DockerCfg is the Docker daemon apps run on as containers (executor:
// docker). Whoever holds its socket is root on that host.
type DockerCfg struct {
	// Socket is the daemon's unix socket; default /var/run/docker.sock.
	Socket string `json:"socket,omitempty"`
	// Gateway is the name of the container Javo itself runs in: the only
	// thing an app reaches, and what tells this Javo's apps from those of
	// another one on the same daemon. Required.
	Gateway string `json:"gateway,omitempty"`
	// Pool is the private address range the apps' networks are cut from,
	// 16 addresses each, such as 10.213.0.0/16 (the default). Name another
	// when the host's own network lies in it.
	Pool string `json:"pool,omitempty"`
	// Runtime is the container runtime every app runs in, such as runsc
	// (gVisor); empty is the daemon's own. Javo does not start when the
	// daemon lacks it.
	Runtime string `json:"runtime,omitempty"`
	// ProxyPort is the port of the container Javo runs in where apps find
	// the forward proxy that is their only way to the internet, HTTP and
	// HTTPS (default 3128). It is one more port apps reach besides
	// --listen, and must not be published to the host.
	ProxyPort int `json:"proxyPort,omitempty"`
}

// AddressPool is Pool as a range; the zero range when none is named.
func (c DockerCfg) AddressPool() netip.Prefix {
	p, _ := netip.ParsePrefix(c.Pool)
	return p
}

// ProcessCfg is the machine apps run on as plain processes of the user
// Javo runs as (executor: process): one person, their own machine, and
// nothing isolated from that account.
type ProcessCfg struct {
	// StateDir is where Javo keeps what the apps run with (their logs,
	// ports and secret files) and HomesDir the folder each person's home
	// is a folder of. Both are absolute paths of folders only the user
	// Javo runs as can open (mode 0700); they are made when missing.
	// Required.
	StateDir string `json:"stateDir,omitempty"`
	HomesDir string `json:"homesDir,omitempty"`
	// Pixi is the pixi binary that installs the apps' environments, an
	// absolute path; empty finds pixi on PATH.
	Pixi string `json:"pixi,omitempty"`
	// FilesDir is the folder with the platform's files, laid out as the
	// platform's image holds them; empty when no template uses them.
	FilesDir string `json:"filesDir,omitempty"`
}

// OnKubernetes reports whether apps run on a Kubernetes cluster.
func (p Platform) OnKubernetes() bool { return p.Executor == ExecutorKubernetes }

// misplaced is an error for each section that is set and only means
// something to an executor other than the one named. It reads the file as
// written, before any default is filled in.
func (p Platform) misplaced() error {
	executor := p.Executor
	if executor == "" {
		executor = ExecutorKubernetes
	}
	var errs []error
	only := func(key string, value, zero any, executors ...string) {
		if reflect.DeepEqual(value, zero) {
			return
		}
		for _, e := range executors {
			if e == executor {
				return
			}
		}
		errs = append(errs, fmt.Errorf("%s: is set, and means nothing with executor %s (it is for executor %s)", key, executor, strings.Join(executors, " or ")))
	}
	only("namespaces", p.Namespaces, Namespaces{}, ExecutorKubernetes)
	// The coding agents' lock is applied by the agent's own process, so
	// every executor takes it; the rest of workloads is the cluster's.
	cluster := p.Workloads
	cluster.Landlock = false
	only("workloads", cluster, WorkloadsCfg{}, ExecutorKubernetes)
	only("storage", p.Storage, StorageCfg{}, ExecutorKubernetes)
	only("limits.namespace", p.Limits.Namespace, NamespaceLimitsCfg{}, ExecutorKubernetes)
	only("keysDir", p.KeysDir, "", ExecutorDocker, ExecutorProcess)
	only("docker", p.Docker, DockerCfg{}, ExecutorDocker)
	only("process", p.Process, ProcessCfg{}, ExecutorProcess)
	return errors.Join(errs...)
}

// validateExecutor checks executor and the settings of the one named.
func (p Platform) validateExecutor(bad func(key, format string, a ...any)) {
	folder := func(key, dir string) {
		if !filepath.IsAbs(dir) {
			bad(key, "%q: required with executor %s, as the absolute path of a folder", dir, p.Executor)
		}
	}
	switch p.Executor {
	case ExecutorKubernetes:
		return
	case ExecutorDocker:
		d := p.Docker
		folder("docker.socket", d.Socket)
		if d.Gateway == "" {
			bad("docker.gateway", "required with executor docker: the name of the container Javo runs in")
		}
		if pool, err := netip.ParsePrefix(d.Pool); d.Pool != "" && (err != nil || !pool.Addr().Is4() || !pool.Addr().IsPrivate() || pool.Bits() > 28) {
			bad("docker.pool", "%q is not a private IPv4 range of at least 16 addresses such as 10.213.0.0/16", d.Pool)
		}
		if d.ProxyPort < 1 || d.ProxyPort > 65535 {
			bad("docker.proxyPort", "%d is not a port (1 to 65535)", d.ProxyPort)
		}
	case ExecutorProcess:
		folder("process.stateDir", p.Process.StateDir)
		folder("process.homesDir", p.Process.HomesDir)
		if f := p.Process.Pixi; f != "" && !filepath.IsAbs(f) {
			bad("process.pixi", "%q is not an absolute path", f)
		}
		if f := p.Process.FilesDir; f != "" && !filepath.IsAbs(f) {
			bad("process.filesDir", "%q is not an absolute path", f)
		}
		if p.Auth.OIDC != nil {
			bad("auth.oidc", "not available with executor process: it serves one person on their own machine, who signs in with auth.local")
		}
	default:
		bad("executor", "%q is not kubernetes, docker or process", p.Executor)
		return
	}
	folder("keysDir", p.KeysDir)
	// What only a cluster can do, and would otherwise be accepted and never
	// happen.
	if p.Backup.S3 != nil {
		bad("backup.s3", "not available with executor %s: the bucket holds the backups of home folders, which Jobs of a cluster make", p.Executor)
	}
	if p.Source.Previews.Enabled {
		bad("source.previews.enabled", "not available with executor %s", p.Executor)
	}
}
