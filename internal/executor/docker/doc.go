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
// Nothing here logs: a request can hold secret values.
package docker
