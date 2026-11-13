// Package docker runs apps as containers on one Docker host. It holds a
// small client for the Docker Engine API, the translation of a
// workload's unit into a hardened container, the network, volume and
// secret-file steps a workload needs, and the Executor that joins them:
// an executor.Executor whose units internal/executor/supervise orders,
// checks and restarts.
//
// The package is the hardening boundary. The body of a container is fixed
// here (Body), and the types it is built from have no field for
// privileges, capabilities, the host's namespaces, devices, published
// ports or a path of the host. Whoever holds the Docker socket is root on
// that host, so nothing but this package should use it.
//
// A workload is a sandbox container on a network of its own (Isolate,
// SandboxBody) and one container per unit in the sandbox's network
// namespace (ContainerBody), over volumes made ready first (Prepare,
// SecretFiles). Teardown removes all of it.
//
// # The Executor
//
// Start refuses what cannot run confined, takes the workload's network
// from the pool (Options.Pool) and returns; the rest happens behind it.
// The sandbox comes first, with the volumes and the secret files, and
// carries the Spec as a label. Then each step runs as a container of its
// own, in order, and the steps that stay and the main unit are kept
// running: one that exits is made again after a growing wait. A
// readiness check with a command runs inside its unit's container; one
// of the port is made from Javo itself, which runs in the gateway
// container and so is on the workload's network. When the sandbox dies,
// its network namespace dies with it, so every container of the
// workload is removed and the workload starts over.
//
// The gateway dials the workload at its sandbox's name (Addr), which
// the daemon resolves on the workload's network. Nothing has to exist
// for that name to be known.
//
// Containers outlive Javo. Close stops none, and the next Executor
// adopts each workload whose sandbox, staying steps and main unit all
// run, from the Spec on the sandbox; anything less is torn down and
// started again by its caller. Only what was made through a Client with
// the same Labels is adopted, listed or watched.
//
// What it does not do: job runs (ErrBatch); the output of a unit's
// earlier run (LogOptions.Previous), since a container is replaced when
// its unit starts again; the commit of an app from Git (State.Commit),
// which Kubernetes reads from a termination message Docker does not
// have; disk limits; and GPUs. An exit code of 137 from the main unit is
// reported as out of memory, also when someone else killed it.
//
// What the design rests on is checked against a real daemon by the
// TestDaemon tests, which are skipped when none answers. Not checked,
// for want of such a daemon: rootless Docker, the nftables firewall
// backend and a sandbox runtime such as runsc.
//
// Nothing here logs: a request can hold secret values.
package docker
