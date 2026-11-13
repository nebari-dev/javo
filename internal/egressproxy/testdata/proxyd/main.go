// Command proxyd runs the egress proxy alone, for the daemon test: in a
// container that stands in for Javo's gateway, with the Docker socket,
// as Javo has it.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/netip"
	"strings"

	"github.com/nebari-dev/javo/internal/egressproxy"
	"github.com/nebari-dev/javo/internal/executor/docker"
)

func main() {
	listen := flag.String("listen", ":3128", "address to listen on")
	socket := flag.String("socket", "/var/run/docker.sock", "the Docker daemon's socket")
	pool := flag.String("pool", "", "the executor's pool")
	labels := flag.String("labels", "", "the client's labels, as k=v,k=v")
	flag.Parse()

	c := docker.New(*socket)
	c.Labels = map[string]string{}
	for _, kv := range strings.Split(*labels, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			c.Labels[k] = v
		}
	}
	p, err := egressproxy.New(egressproxy.Options{Networks: egressproxy.Docker(c, netip.MustParsePrefix(*pool))})
	if err != nil {
		log.Fatal(err)
	}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(p.Serve(context.Background(), l))
}
