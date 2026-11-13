#!/bin/sh
# Compile javo and javo-sandbox for linux/amd64 and linux/arm64 at once into
# <dir>/linux_<arch>/, the layout of the Dockerfile's javo-bin stage.
#
# Usage: [JAVO_VERSION=v0.2.22] [JAVO_COMMIT=<full sha>] scripts/build/cross-build-javo.sh <dir>
# JAVO_VERSION and JAVO_COMMIT are the release tag and commit the UI shows
# (internal/buildinfo); unset, the build says "dev" and knows no commit.
set -eu
dir=${1:?output folder}
pkg=github.com/nebari-dev/javo/internal/buildinfo
ldflags="-s -w -X $pkg.version=${JAVO_VERSION:-} -X $pkg.commit=${JAVO_COMMIT:-}"
pids=""
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
    go build -trimpath -ldflags="$ldflags" -o "$dir/linux_$arch/javo" ./cmd/javo &
  pids="$pids $!"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
    go build -trimpath -ldflags="-s -w" -o "$dir/linux_$arch/javo-sandbox" ./cmd/javo-sandbox &
  pids="$pids $!"
done
for p in $pids; do wait "$p"; done
