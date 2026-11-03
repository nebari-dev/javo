#!/usr/bin/env bash
# The S3-compatible bucket of the backup restore drill, on this checkout's
# cluster (run by scripts/dev/up.sh when JAVO_E2E_S3=1): test/e2e/manifests/s3.yaml
# in namespace javo-e2e, and its access key as the Secret javo-backup-s3 in the
# release namespace, which the chart's backupTarget.existingSecret names.
# The key is generated once and kept in .dev (0600), never printed.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
export KUBECONFIG="${KUBECONFIG:-$root/.dev/kubeconfig}"
ns=javo-e2e
release_ns=${1:-javo-system}

mkdir -p .dev
for f in s3.access-key s3.secret-key; do
  if [ ! -s ".dev/$f" ]; then
    (umask 077; openssl rand -hex 20 | tr -d '\n' >".dev/$f")
  fi
done
secret() {
  kubectl -n "$1" create secret generic "$2" \
    --from-file=AWS_ACCESS_KEY_ID=.dev/s3.access-key --from-file=AWS_SECRET_ACCESS_KEY=.dev/s3.secret-key \
    --dry-run=client -o yaml | kubectl apply -f -
}
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace "$release_ns" --dry-run=client -o yaml | kubectl apply -f -
secret "$ns" s3-admin
secret "$release_ns" javo-backup-s3
kubectl apply -f test/e2e/manifests/s3.yaml
kubectl -n "$ns" rollout status deploy/s3 --timeout=180s
