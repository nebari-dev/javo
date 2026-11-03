#!/usr/bin/env bash
# Rebuild the javo image from this checkout and roll the release to it, on the
# Compose cluster scripts/dev/up.sh made (make reload): the inner loop for Go and
# UI changes. Only the javo image is checked; the release keeps every other
# value. Nothing to do when the image's inputs did not change. On kind
# (JAVO_CLUSTER=kind) it runs scripts/dev/up.sh.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
if [ "${JAVO_CLUSTER:-compose}" = kind ]; then exec scripts/dev/up.sh; fi
. scripts/dev/env.sh
export KUBECONFIG="$root/.dev/kubeconfig"
if ! [ -s .dev/images.yaml ] || ! helm status javo --namespace javo-system >/dev/null 2>&1; then
  echo "no release from scripts/dev/up.sh on this checkout's cluster; run make up first" >&2
  exit 1
fi

before="$(sed -n 's/^# ref javo //p' .dev/images.yaml)"
scripts/dev/registry-up.sh >/dev/null
scripts/build/images.sh localhost:5001 .dev/images.yaml javo
after="$(sed -n 's/^# ref javo //p' .dev/images.yaml)"
if [ "$before" = "$after" ] && [ "$(helm get values javo --namespace javo-system -o json | grep -c "${after##*:}")" -gt 0 ]; then
  echo "the release runs $after already ($SECONDS s)"
  exit 0
fi
helm upgrade javo deploy/helm/javo --namespace javo-system --reuse-values -f .dev/images.yaml --wait --timeout 3m >/dev/null
for d in $(kubectl -n javo-system get deploy -l app.kubernetes.io/instance=javo,app.kubernetes.io/name=javo -o name); do
  kubectl -n javo-system rollout status "$d" --timeout=120s
done
scripts/install/wait-healthz.sh "http://localhost:$JAVO_HTTP_PORT/healthz" javo-system
echo "rolled to $after in $SECONDS s"
