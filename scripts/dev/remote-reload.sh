#!/usr/bin/env bash
# Rebuild the javo image on the Docker host of scripts/dev/remote-up.sh and
# roll the release to it (scripts/dev/reload.sh through the ssh tunnel): the
# loop for Go changes. UI changes need nothing; Vite reloads them.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
. scripts/dev/env.sh
. scripts/dev/remote-env.sh
remote_tunnel
exec scripts/dev/reload.sh
