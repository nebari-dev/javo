#!/bin/sh
# Compile javo for linux/amd64 and linux/arm64 at once into
# <dir>/linux_<arch>/javo, the layout of the Dockerfile's javo-bin stage.
#
# Usage: scripts/build/cross-build-javo.sh <dir>
set -eu
dir=${1:?output folder}
pids=""
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
    go build -trimpath -ldflags='-s -w' -o "$dir/linux_$arch/javo" ./cmd/javo &
  pids="$pids $!"
done
for p in $pids; do wait "$p"; done
