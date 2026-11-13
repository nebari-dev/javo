// Package docker is what the Docker executor is built on: a small client
// for the Docker Engine API, the translation of a workload's unit into a
// hardened container, and the network, volume and secret-file steps a
// workload needs on one Docker host. It starts no supervisor and is no
// executor.Executor: internal/executor/supervise orders, checks and
// restarts what this package creates.
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
// What the design rests on is checked against a real daemon by the
// TestDaemon tests, which are skipped when none answers. Not checked,
// for want of such a daemon: rootless Docker, the nftables firewall
// backend and a sandbox runtime such as runsc.
//
// Nothing here logs: a request can hold secret values.
package docker
