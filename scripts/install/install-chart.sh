#!/bin/sh
# Install or upgrade the javo release and wait for its Deployments (the one
# pod of a SQLite install, or the gateway and the controller of a Postgres one).
# Prints the release namespace's pods every 5 s while helm runs, so a stuck
# rollout shows why. Used by scripts/dev/up.sh, scripts/install/deploy-single-node.sh and the Compose
# installer (deploy/compose/install.sh); POSIX sh so it runs in the
# installer image, which has no bash.
#
# Usage: scripts/install/install-chart.sh <chart-dir> <namespace> [--restart] [helm args...]
#   --restart  restart the Deployments after the upgrade, for images rebuilt
#              under the same tag (pods keep the old image otherwise)
# Uses the caller's kubectl, helm and KUBECONFIG.
set -eu

chart=${1:?chart directory}
ns=${2:?release namespace}
shift 2
restart=0
if [ "${1:-}" = "--restart" ]; then
  restart=1
  shift
fi

helm upgrade --install javo "$chart" --namespace "$ns" --create-namespace "$@" &
helm_pid=$!
while kill -0 "$helm_pid" 2>/dev/null; do
  kubectl -n "$ns" get pods --no-headers 2>/dev/null || true
  sleep 5
done
wait "$helm_pid"

deployments="$(kubectl -n "$ns" get deploy -l app.kubernetes.io/instance=javo,app.kubernetes.io/name=javo -o name)"
if [ "$restart" -eq 1 ]; then
  # shellcheck disable=SC2086
  kubectl -n "$ns" rollout restart $deployments
fi
for d in $deployments; do
  kubectl -n "$ns" rollout status "$d" --timeout=120s
done
