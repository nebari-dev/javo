#!/bin/sh
# Compile the javo that runs `javo local`: one binary that carries the web
# UI, the templates and the platform's files (internal/bundle), into
# <dir>/<os>_<arch>/javo for each platform named, or for this machine. No
# cgo, so any of them is built from any machine.
#
# Usage: [UI_DIST=ui/dist] [TERMINAL_PAGE=<terminal.html>] [JAVO_VERSION=v0.2.22] [JAVO_COMMIT=<full sha>] \
#          scripts/build/local-javo.sh <dir> [<os>/<arch>...]
# UI_DIST is the built UI (cd ui && pnpm build). TERMINAL_PAGE is the page
# the terminal and the coding agents serve, built as the Dockerfile's
# pod-files stage builds it; without it they show a page that says so.
# The flags are the release's (scripts/build/cross-build-javo.sh) plus the
# tag local. The archive is packed for one platform at a time, since each
# leaves out what does not run there, and removed at the end: a build
# with the tag and without this script carries none.
set -eu
dir=${1:?output folder}
shift
[ $# -gt 0 ] || set -- "$(go env GOOS)/$(go env GOARCH)"
pkg=github.com/nebari-dev/javo/internal/buildinfo
ldflags="-s -w -X $pkg.version=${JAVO_VERSION:-} -X $pkg.commit=${JAVO_COMMIT:-}"
archive=internal/bundle/assets/bundle.zip
trap 'rm -f "$archive"' EXIT
for platform; do
  os=${platform%/*}
  arch=${platform#*/}
  go run ./internal/bundle/pack -ui "${UI_DIST:-ui/dist}" -terminal-page "${TERMINAL_PAGE:-}" -os "$os" -arch "$arch" -o "$archive"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    go build -tags local -trimpath -ldflags="$ldflags" -o "$dir/${os}_$arch/javo" ./cmd/javo
  echo "$dir/${os}_$arch/javo: $(wc -c < "$dir/${os}_$arch/javo" | tr -d ' ') bytes"
done
