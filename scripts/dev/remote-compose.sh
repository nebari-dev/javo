#!/usr/bin/env bash
# "docker compose" run on the Docker host of scripts/dev/remote-up.sh, in its
# copy of the checkout (JAVO_REMOTE_DIR, which remote-up.sh fills with
# deploy/compose, the Dockerfile and .dockerignore), so the Compose files'
# relative paths resolve there. scripts/dev/up.sh calls it as JAVO_COMPOSE.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$root/scripts/dev/env.sh"
. "$root/scripts/dev/remote-env.sh"
dir=${JAVO_REMOTE_DIR:?JAVO_REMOTE_DIR}
vars="JAVO_HTTP_PORT=$JAVO_HTTP_PORT JAVO_KEYCLOAK_PORT=$JAVO_KEYCLOAK_PORT JAVO_API_PORT=$JAVO_API_PORT JAVO_BIND_ADDR=$JAVO_BIND_ADDR"
remote "cd $(printf %q "$dir") && $vars docker compose $(printf '%q ' "$@")"
