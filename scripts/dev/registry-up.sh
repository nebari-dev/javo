#!/usr/bin/env bash
# Start the local image registry every checkout shares, unless it runs: the
# container javo-registry on the Docker network javo-registry, published on
# 127.0.0.1:5001 for docker push. Clusters on that network pull from it as
# javo-registry:5000 (deploy/compose/dev.yaml). Images are kept in the volume
# javo-registry. Prints the host address, localhost:5001.
#
# This is the local registry setup kind and k3d document; the image is
# pinned by digest (DEPENDENCIES.md).
set -euo pipefail

name=javo-registry
image=registry:3.1.2@sha256:d4032d4cc323c1731c707931ecada07a53076eac38e3b61ae57533f170f2c6c3
port=5001

docker network inspect "$name" >/dev/null 2>&1 || docker network create "$name" >/dev/null 2>&1 || true
if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" != true ]; then
  # Another checkout may start it at the same moment: a lost race leaves it running.
  docker start "$name" >/dev/null 2>&1 ||
    docker run -d --name "$name" --restart unless-stopped --network "$name" \
      -p "127.0.0.1:$port:5000" -v "$name:/var/lib/registry" "$image" >/dev/null 2>&1 ||
    docker start "$name" >/dev/null
fi
for _ in $(seq 1 50); do
  if curl -fs -o /dev/null "http://localhost:$port/v2/" 2>/dev/null; then
    echo "localhost:$port"
    exit 0
  fi
  sleep 0.2
done
echo "registry $name does not answer on localhost:$port" >&2
exit 1
