#!/usr/bin/env bash
# The cluster name and host ports of this checkout, so checkouts (git
# worktrees) run side by side without deploying over each other.
#
#   JAVO_KIND_NAME     the Compose project or kind cluster; javo-dev in the
#                      main checkout, javo-<worktree directory> in a linked worktree
#   JAVO_HTTP_PORT     host port of the UI and apps (NodePort 30080);
#                      8080 in the main checkout
#   JAVO_KEYCLOAK_PORT host port of the dev Keycloak (NodePort 30081);
#                      8081 in the main checkout
#   JAVO_API_PORT      host port of the Compose install's Kubernetes API:
#                      JAVO_HTTP_PORT + 8000 (16080 in the main checkout)
#
# A worktree gets a free pair of ports, picked once. The result is saved in
# .dev/kind.env and reused; the e2e tests read it. A variable set in the
# environment wins and is saved too.
#
# Sourced by the scripts under scripts/ (exports the four variables); run on its
# own it prints them.

javo_dev_env() {
  local root saved name http kc api
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
  saved="$root/.dev/kind.env"
  name="" http="" kc="" api=""
  if [ -f "$saved" ]; then
    name="$(sed -n 's/^JAVO_KIND_NAME=//p' "$saved")"
    http="$(sed -n 's/^JAVO_HTTP_PORT=//p' "$saved")"
    kc="$(sed -n 's/^JAVO_KEYCLOAK_PORT=//p' "$saved")"
    api="$(sed -n 's/^JAVO_API_PORT=//p' "$saved")"
  fi
  name="${JAVO_KIND_NAME:-$name}"
  http="${JAVO_HTTP_PORT:-$http}"
  kc="${JAVO_KEYCLOAK_PORT:-$kc}"
  api="${JAVO_API_PORT:-$api}"

  # A linked worktree has a .git file; the main checkout a .git directory.
  local main=1
  [ -f "$root/.git" ] && main=0
  if [ -z "$name" ]; then
    if [ "$main" = 1 ]; then
      name=javo-dev
    else
      name="javo-$(basename "$root" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-' | sed 's/-*$//')"
    fi
  fi
  if [ -z "$http" ] || [ -z "$kc" ]; then
    if [ "$main" = 1 ]; then
      http="${http:-8080}" kc="${kc:-8081}"
    else
      local pair
      pair="$(javo_free_pair "$root" "$name")" || return 1
      read -r http kc <<<"$pair"
    fi
  fi

  api="${api:-$((http + 8000))}"

  mkdir -p "$root/.dev"
  printf 'JAVO_KIND_NAME=%s\nJAVO_HTTP_PORT=%s\nJAVO_KEYCLOAK_PORT=%s\nJAVO_API_PORT=%s\n' "$name" "$http" "$kc" "$api" >"$saved"
  JAVO_KIND_NAME="$name" JAVO_HTTP_PORT="$http" JAVO_KEYCLOAK_PORT="$kc" JAVO_API_PORT="$api"
  export JAVO_KIND_NAME JAVO_HTTP_PORT JAVO_KEYCLOAK_PORT JAVO_API_PORT
}

# javo_free_pair <root> <name>: prints two adjacent ports in 8100-8999 that
# nothing listens on and no other worktree has saved, trying slots from one
# derived from the name.
javo_free_pair() {
  local slots=450 start i p wt taken=" "
  while read -r wt; do
    [ "$wt" != "$1" ] && [ -f "$wt/.dev/kind.env" ] || continue
    taken+="$(sed -nE 's/^JAVO_(HTTP|KEYCLOAK)_PORT=//p' "$wt/.dev/kind.env" | tr '\n' ' ')"
  done < <(git -C "$1" worktree list --porcelain 2>/dev/null | sed -n 's/^worktree //p')
  start=$(($(printf '%s' "$2" | cksum | cut -d' ' -f1) % slots))
  for ((i = 0; i < slots; i++)); do
    p=$((8100 + 2 * ((start + i) % slots)))
    case "$taken" in *" $p "* | *" $((p + 1)) "*) continue ;; esac
    if ! javo_port_used "$p" && ! javo_port_used $((p + 1)); then
      echo "$p" $((p + 1))
      return
    fi
  done
  echo "dev-env.sh: no free port pair in 8100-8999; set JAVO_HTTP_PORT and JAVO_KEYCLOAK_PORT" >&2
  return 1
}

# javo_port_used <port>: something accepts connections on 127.0.0.1:<port>.
javo_port_used() {
  (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

javo_dev_env || exit 1
unset -f javo_dev_env javo_free_pair javo_port_used
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  printf 'JAVO_KIND_NAME=%s\nJAVO_HTTP_PORT=%s\nJAVO_KEYCLOAK_PORT=%s\nJAVO_API_PORT=%s\n' "$JAVO_KIND_NAME" "$JAVO_HTTP_PORT" "$JAVO_KEYCLOAK_PORT" "$JAVO_API_PORT"
fi
