// Package egressproxy is the way out for apps on Docker. A workload's
// network there reaches nothing but the gateway container, so the gateway
// runs this forward proxy on a port of its own, and every unit is told to
// use it (HTTP_PROXY and its siblings, set by the Docker executor). It
// carries HTTPS through CONNECT tunnels and plain HTTP requests, and
// nothing else: no SSH, no other protocol, no other port.
//
// What an app may reach is decided on the address that is connected to,
// never on the name asked for. Each connection resolves the name once,
// checks every address (policy.go) and connects to an address it checked,
// as a literal, through a dialer that checks the socket's address once
// more. A name that later resolves elsewhere is resolved and checked
// again for the next connection, so rebinding a name gains nothing.
//
// Refused whatever the settings say: loopback, link-local (the cloud
// metadata address is one), multicast, the unspecified address, every
// range of the Docker daemon's networks and of the executor's pool, and
// the gateway's own addresses, which also keeps the proxy from connecting
// to itself. Then the --egress-deny-cidrs ranges. What is left is allowed
// if --egress-allow-cluster-cidrs names it; otherwise, with
// --egress-allow-cidrs set, only if that names it; otherwise unless it is
// private or reserved (internal/egress, the lists the NetworkPolicy of a
// Kubernetes namespace uses). --egress-allow-namespaces and the name
// servers of a cluster have no meaning here.
//
// Only an app may use it. The listener is on every address of the
// gateway container, since workload networks come and go; a connection
// is served only when its peer's address lies in the network of a
// workload and is not the gateway's own. Anything else that can open the
// port (the Docker host, a container on the gateway's other networks) is
// hung up on before a byte is read. That address also names the app in
// the log, and counts toward the app's limit of connections.
//
// The log names the app, the method, the host and port asked for and the
// address connected to. It never holds a path, a query, a header or the
// user and password of a URL.
//
// The rules of HTTP it follows are named where they are applied: RFC 9110
// section 9.3.6 for CONNECT, 7.6.1 for the fields that go no further than
// this hop, 7.6.2 for Max-Forwards and 7.6.3 for Via.
package egressproxy
