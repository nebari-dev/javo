#!/usr/bin/env bash
# The dev install on another Docker host (JAVO_REMOTE, see
# scripts/dev/remote-env.sh), opened from a browser on the LAN with the UI
# served by Vite (hot reload). This checkout is where you edit; the host runs:
#
#   - scripts/dev/up.sh's Compose cluster, its UI and apps ports bound to
#     JAVO_BIND_ADDR (the Kubernetes API stays on the host's 127.0.0.1;
#     this machine reaches it, the registry and the UI port through an ssh
#     tunnel)
#   - Vite in a node container (<project>-vite) on JAVO_BIND_ADDR:JAVO_VITE_PORT,
#     sending the gateway's routes to the cluster, with ui/ copied there by
#     scripts/dev/remote-ui-sync.sh (running here in the background, log in
#     .dev/ui-sync.log)
#
# The gateway's UI host is the Vite address, so sign-in and redirects stay
# there; apps are on JAVO_BIND_ADDR:JAVO_HTTP_PORT. Local sign-in only (no
# Keycloak). Go changes: scripts/dev/remote-reload.sh. Rerun to restart.
#
# Usage: JAVO_REMOTE=user@host scripts/dev/remote-up.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
. scripts/dev/env.sh
. scripts/dev/remote-env.sh

# The pins of the Dockerfile's ui stage (DEPENDENCIES.md).
node_image=node:22.23.4-bookworm-slim@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da
pnpm=pnpm@10.34.7
vite="$JAVO_KIND_NAME-vite"
ui_url="http://$JAVO_BIND_ADDR:$JAVO_VITE_PORT"

step() { printf '\n==> %s (%s s)\n' "$*" "$SECONDS"; }

if [ "${JAVO_KEYCLOAK:-}" = 1 ]; then
  echo "remote-up.sh serves local sign-in only; JAVO_KEYCLOAK=1 needs scripts/dev/up.sh" >&2
  exit 1
fi

step "ports on $JAVO_REMOTE"
# Taken ports are fine when they are this checkout's own containers.
mine="$(docker ps --filter "label=com.docker.compose.project=$JAVO_KIND_NAME" --format '{{.Ports}}'
  docker ps --filter "name=^$vite\$" --format '{{.Ports}}')"
for p in "$JAVO_HTTP_PORT" "$JAVO_KEYCLOAK_PORT" "$JAVO_API_PORT" "$JAVO_VITE_PORT"; do
  if remote "ss -ltnH 'sport = :$p'" | grep -q . && ! grep -q ":$p->" <<<"$mine"; then
    echo "port $p is in use on $JAVO_REMOTE; set JAVO_HTTP_PORT and JAVO_KEYCLOAK_PORT (or JAVO_VITE_PORT) to free ones" >&2
    exit 1
  fi
done
echo "UI $JAVO_HTTP_PORT, Keycloak $JAVO_KEYCLOAK_PORT, API $JAVO_API_PORT, Vite $JAVO_VITE_PORT: free or ours"

step "ssh tunnel"
remote_tunnel

# The host's copy of the checkout: what Compose and Vite read there.
export JAVO_REMOTE_DIR
JAVO_REMOTE_DIR="$(remote 'echo $HOME')/javo-dev/$JAVO_KIND_NAME"
dir="$JAVO_REMOTE_DIR/ui"

step "Compose files to $JAVO_REMOTE:$JAVO_REMOTE_DIR"
remote "mkdir -p '$JAVO_REMOTE_DIR'"
tar -cf - Dockerfile .dockerignore deploy/compose | remote "tar -xvf - -C '$JAVO_REMOTE_DIR'"

step "cluster (scripts/dev/up.sh)"
JAVO_COMPOSE="$root/scripts/dev/remote-compose.sh" \
  JAVO_UI_HOST="$JAVO_BIND_ADDR:$JAVO_VITE_PORT" JAVO_APPS_HOST="$JAVO_BIND_ADDR:$JAVO_HTTP_PORT" scripts/dev/up.sh

step "sync ui/ to $JAVO_REMOTE"
if [ -s .dev/ui-sync.pid ]; then kill "$(cat .dev/ui-sync.pid)" 2>/dev/null || true; fi
: >.dev/ui-sync.log
nohup scripts/dev/remote-ui-sync.sh "$dir" >>.dev/ui-sync.log 2>&1 &
echo $! >.dev/ui-sync.pid
tail -f .dev/ui-sync.log --pid "$(cat .dev/ui-sync.pid)" &
follow=$!
for _ in $(seq 1 120); do
  grep -q 'full copy' .dev/ui-sync.log && break
  kill -0 "$(cat .dev/ui-sync.pid)" 2>/dev/null || { echo "the sync stopped; see .dev/ui-sync.log" >&2; exit 1; }
  sleep 0.5
done
kill "$follow" 2>/dev/null || true

step "Vite ($vite on $JAVO_BIND_ADDR:$JAVO_VITE_PORT)"
docker rm -f "$vite" >/dev/null 2>&1 || true
docker run -d --name "$vite" --restart unless-stopped \
  --network "${JAVO_KIND_NAME}_default" -p "$JAVO_BIND_ADDR:$JAVO_VITE_PORT:$JAVO_VITE_PORT" \
  -v "$dir:/src/ui" -v "$vite-node-modules:/src/ui/node_modules" -v javo-pnpm-store:/pnpm-store \
  -w /src/ui -e JAVO_DEV_PROXY=http://k3s:30080 -e CI=1 \
  -e npm_config_manage_package_manager_versions=false "$node_image" \
  sh -c "npm install -g --ignore-scripts $pnpm && pnpm install --frozen-lockfile --ignore-scripts --store-dir /pnpm-store &&
    exec pnpm exec vite --host 0.0.0.0 --port $JAVO_VITE_PORT --strictPort" >/dev/null
docker logs -f "$vite" &
follow=$!
ready=0
for _ in $(seq 1 240); do
  if curl -fsS -o /dev/null -m 2 "$ui_url/healthz" 2>/dev/null && curl -fsS -o /dev/null -m 2 "$ui_url/@vite/client" 2>/dev/null; then
    ready=1
    break
  fi
  [ "$(docker inspect -f '{{.State.Running}}' "$vite" 2>/dev/null)" = true ] || break
  sleep 0.5
done
kill "$follow" 2>/dev/null || true
wait "$follow" 2>/dev/null || true
if [ "$ready" != 1 ]; then echo "Vite did not answer on $ui_url; see: docker logs $vite" >&2; exit 1; fi

echo
echo "Open $ui_url (Vite, hot reload; ready in $SECONDS s)"
echo "Sign in as alice; the password is in .dev/alice.password (cat it here, it is not printed)"
echo "Go changes: scripts/dev/remote-reload.sh"
echo "Logs: tail -f .dev/ui-sync.log (the ui/ sync), DOCKER_HOST=$DOCKER_HOST docker logs -f $vite (Vite)"
echo "kubectl: KUBECONFIG=$root/.dev/kubeconfig kubectl get pods -A (through the tunnel)"
