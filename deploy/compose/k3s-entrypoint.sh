#!/bin/sh
# Entrypoint of the Compose k3s container (Dockerfile stage k3s): points the
# cluster's DNS at Docker's own resolver, then runs k3s.
#
# On a Compose network the container's /etc/resolv.conf names only Docker's
# embedded resolver, 127.0.0.11. k3s refuses a loopback nameserver (pods
# cannot reach the container's loopback) and falls back to 8.8.8.8, which
# some networks block: then no pod resolves github.com. Instead, as kind and
# k3d do for the same problem, Docker's NAT rules for 127.0.0.11 are copied
# to a reachable address (the default gateway) and also applied to traffic
# arriving from pods, and resolv.conf names that address. k3s passes it to
# CoreDNS, so the cluster resolves names exactly as Docker does on this host.
set -eu

docker_dns=127.0.0.11
# Docker's resolv.conf as written for this container, kept on the first
# start: a restart keeps the file edited below but gets new NAT rules.
original=/etc/resolv.conf.docker
[ -f "$original" ] || cp /etc/resolv.conf "$original"

if grep -q "^nameserver $docker_dns" "$original"; then
  gateway="$(ip -4 route show default | awk '{print $3; exit}')"
  if [ -z "$gateway" ]; then
    echo "k3s-entrypoint: no default route; cluster DNS falls back to k3s's default" >&2
  else
    # The NAT rules send packets to 127.0.0.11, which needs this for
    # packets that came from pods (kube-proxy sets it too, but later).
    sysctl -qw net.ipv4.conf.all.route_localnet=1
    # --dport 53 is restored where some iptables versions drop it from
    # Docker's DNAT rules (kubernetes-sigs/kind#3054).
    iptables-save -t nat |
      sed -e "s/-d $docker_dns/-d $gateway/g" \
        -e 's/-A OUTPUT \(.*\) -j DOCKER_OUTPUT/&\n-A PREROUTING \1 -j DOCKER_OUTPUT/' \
        -e "s/--to-source :53/--to-source $gateway:53/g" \
        -e "s/p -j DNAT --to-destination $docker_dns/p --dport 53 -j DNAT --to-destination $docker_dns/g" |
      iptables-restore
    sed "s/^nameserver $docker_dns\$/nameserver $gateway/" "$original" >/etc/resolv.conf
    echo "k3s-entrypoint: cluster DNS uses Docker's resolver at $gateway"
  fi
fi

exec /bin/k3s "$@"
