#!/bin/sh
# Wait up to 60 s for the gateway's /healthz at <url>, printing the release
# namespace's pods between tries. Exits non-zero if it never answers.
# Uses curl when present, else wget (the Compose installer image has wget).
#
# Usage: scripts/install/wait-healthz.sh <url> <namespace>
set -eu

url=${1:?healthz url}
ns=${2:?release namespace}
get() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS -o /dev/null "$1" 2>/dev/null
  else
    wget -q -O /dev/null "$1" 2>/dev/null
  fi
}
i=0
while [ "$i" -lt 30 ]; do
  if get "$url"; then
    echo "$url ok"
    exit 0
  fi
  kubectl -n "$ns" get pods --no-headers 2>/dev/null || true
  sleep 2
  i=$((i + 1))
done
echo "gateway $url not reachable after 60 s" >&2
exit 1
