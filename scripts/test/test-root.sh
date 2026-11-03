#!/usr/bin/env bash
# Runs Go tests that need root (files given to other users) as root, in the
# build stage's Go image (pinned by digest in the Dockerfile), offline, with
# the host's module cache mounted read-only.
# Usage: scripts/test/test-root.sh [packages] (default ./internal/storage/homecopy/)
set -euo pipefail
cd "$(dirname "$0")/../.."
image=$(sed -n 's/^FROM --platform=\$BUILDPLATFORM \(golang:[^ ]*\) AS build$/\1/p' Dockerfile)
[ -n "$image" ] || { echo "no Go image in the Dockerfile" >&2; exit 1; }
exec docker run --rm -v "$PWD:/src:ro" -v "$(go env GOMODCACHE):/go/pkg/mod:ro" \
  -e GOFLAGS=-mod=mod -e GOCACHE=/tmp/gocache -e GOPROXY=off -w /src "$image" \
  go test -count=1 "${@:-./internal/storage/homecopy/}"
