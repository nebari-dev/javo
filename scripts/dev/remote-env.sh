#!/usr/bin/env bash
# The remote Docker host of scripts/dev/remote-up.sh and remote-reload.sh: the
# dev install runs there and this checkout (where you edit) only drives it.
# Sourced; needs scripts/dev/env.sh sourced first.
#
#   JAVO_REMOTE     ssh destination of the Docker host, e.g. user@192.0.2.10
#   JAVO_BIND_ADDR  address on that host browsers reach (default: the host
#                   part of JAVO_REMOTE); the UI, Keycloak and Vite ports bind
#                   there, the Kubernetes API stays on its 127.0.0.1
#   JAVO_VITE_PORT  Vite's port (default JAVO_HTTP_PORT + 9000)
#   JAVO_REGISTRY_LOCAL_PORT
#                   this machine's 127.0.0.1 port forwarded to the host's
#                   registry (default 5001); pick another when a second remote
#                   checkout's tunnel holds 5001 here
#
# Saved in .dev/remote.env and reused. Exports DOCKER_HOST (ssh://JAVO_REMOTE)
# and defines remote (a command on the host) and remote_tunnel, which keeps
# one ssh connection open that forwards this machine's 127.0.0.1 ports of the
# registry (JAVO_REGISTRY_LOCAL_PORT), the Kubernetes API and the UI to the
# host, so scripts/dev/up.sh and reload.sh run unchanged. Images keep the
# name localhost:5001/..., the registry's address on the host, where the
# cluster pulls them; JAVO_REGISTRY_PROBE tells the scripts that check the
# registry from this machine where it answers here.

remote_saved="$root/.dev/remote.env"
remote_get() { if [ -f "$remote_saved" ]; then sed -n "s/^$1=//p" "$remote_saved"; fi; }
JAVO_REMOTE="${JAVO_REMOTE:-$(remote_get JAVO_REMOTE)}"
if [ -z "$JAVO_REMOTE" ]; then
  echo "set JAVO_REMOTE to the Docker host's ssh destination, e.g. JAVO_REMOTE=user@192.0.2.10" >&2
  exit 1
fi
JAVO_BIND_ADDR="${JAVO_BIND_ADDR:-$(remote_get JAVO_BIND_ADDR)}"
JAVO_BIND_ADDR="${JAVO_BIND_ADDR:-${JAVO_REMOTE#*@}}"
JAVO_VITE_PORT="${JAVO_VITE_PORT:-$(remote_get JAVO_VITE_PORT)}"
JAVO_VITE_PORT="${JAVO_VITE_PORT:-$((JAVO_HTTP_PORT + 9000))}"
JAVO_REGISTRY_LOCAL_PORT="${JAVO_REGISTRY_LOCAL_PORT:-$(remote_get JAVO_REGISTRY_LOCAL_PORT)}"
JAVO_REGISTRY_LOCAL_PORT="${JAVO_REGISTRY_LOCAL_PORT:-5001}"
printf 'JAVO_REMOTE=%s\nJAVO_BIND_ADDR=%s\nJAVO_VITE_PORT=%s\nJAVO_REGISTRY_LOCAL_PORT=%s\n' \
  "$JAVO_REMOTE" "$JAVO_BIND_ADDR" "$JAVO_VITE_PORT" "$JAVO_REGISTRY_LOCAL_PORT" >"$remote_saved"
export JAVO_REMOTE JAVO_BIND_ADDR JAVO_VITE_PORT JAVO_REGISTRY_LOCAL_PORT
export JAVO_REGISTRY_PROBE="localhost:$JAVO_REGISTRY_LOCAL_PORT"
export DOCKER_HOST="ssh://$JAVO_REMOTE"
# Not under .dev: a shared folder may not hold sockets.
remote_sock="${TMPDIR:-/tmp}/javo-remote-$JAVO_KIND_NAME.sock"

remote() { ssh -o ControlPath="$remote_sock" "$JAVO_REMOTE" "$@"; }

remote_tunnel() {
  if ssh -o ControlPath="$remote_sock" -O check "$JAVO_REMOTE" 2>/dev/null; then return; fi
  rm -f "$remote_sock"
  ssh -fN -o ControlMaster=yes -o ControlPath="$remote_sock" -o ControlPersist=yes \
    -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 \
    -L "127.0.0.1:$JAVO_REGISTRY_LOCAL_PORT:127.0.0.1:5001" \
    -L "127.0.0.1:$JAVO_API_PORT:127.0.0.1:$JAVO_API_PORT" \
    -L "127.0.0.1:$JAVO_HTTP_PORT:$JAVO_BIND_ADDR:$JAVO_HTTP_PORT" \
    "$JAVO_REMOTE"
  echo "ssh tunnel to $JAVO_REMOTE: 127.0.0.1 ports $JAVO_REGISTRY_LOCAL_PORT (registry), $JAVO_API_PORT and $JAVO_HTTP_PORT"
}
