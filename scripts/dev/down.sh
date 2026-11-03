#!/usr/bin/env bash
# Delete this checkout's cluster (JAVO_KIND_NAME, see scripts/dev/env.sh): the
# Compose project with its volumes, or the kind cluster, whichever exists,
# and the local dev credentials. .dev/kind.env is kept, so the next dev-up
# uses the same ports. The shared registry (scripts/dev/registry-up.sh) is kept.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$root/scripts/dev/env.sh"
cluster="$JAVO_KIND_NAME"
found=0
compose=(docker compose -p "$cluster" -f "$root/deploy/compose/compose.yaml" -f "$root/deploy/compose/dev.yaml")
if [ -n "$("${compose[@]}" ps -aq 2>/dev/null)" ] || docker volume inspect "${cluster}_k3s-data" >/dev/null 2>&1; then
  "${compose[@]}" down -v
  found=1
fi
if grep -qx "$cluster" <<<"$(kind get clusters 2>/dev/null)"; then
  # --kubeconfig keeps kind away from the user's ~/.kube/config.
  kind delete cluster --name "$cluster" --kubeconfig "$root/.dev/kubeconfig"
  found=1
fi
[ "$found" = 1 ] || echo "no Compose project or kind cluster $cluster"
rm -f "$root/.dev/alice.password" "$root/.dev/kubeconfig"
echo "$cluster removed"
