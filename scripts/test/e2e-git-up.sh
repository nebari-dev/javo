#!/usr/bin/env bash
# The hermetic Git side of the end-to-end tests, on this checkout's cluster
# (run by scripts/dev/up.sh when JAVO_E2E_GIT=1; JAVO_CLUSTER as there): a Gitea server and a
# fake GitHub pull request list in namespace javo-e2e, and a cluster DNS rule
# that sends github.com to the Gitea Service, so apps clone ordinary
# https://github.com/<owner>/<repo> addresses without leaving the cluster.
# The admin password is generated once and kept in .dev/gitea-admin.password
# (0600), never printed. The clones trust Gitea's self-signed certificate
# through source.gitConfig, which dev-up sets.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
export KUBECONFIG="${KUBECONFIG:-$root/.dev/kubeconfig}"
. scripts/dev/env.sh
cluster="$JAVO_KIND_NAME"
ns=javo-e2e
fake_image=javo-fakegithub:dev

step() { printf '\n==> %s\n' "$*"; }

step "fakegithub image"
if [ "${JAVO_CLUSTER:-compose}" = kind ]; then
  docker build -t "$fake_image" test/e2e/fakegithub
  kind load docker-image --name "$cluster" "$fake_image"
else
  # Through the shared registry, like the other images (scripts/build/images.sh).
  scripts/build/images.sh localhost:5001 .dev/fakegithub.yaml fakegithub
  fake_image="$(sed -n 's/^# ref fakegithub //p' .dev/fakegithub.yaml)"
fi

step "Gitea and fakegithub"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
new_password=0
if [ ! -s .dev/gitea-admin.password ]; then
  # No trailing newline: the Secret and the Gitea user must hold the same text.
  (umask 077; openssl rand -hex 24 | tr -d '\n' >.dev/gitea-admin.password)
  new_password=1
fi
kubectl -n "$ns" create secret generic gitea-admin --from-file=password=.dev/gitea-admin.password \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f test/e2e/manifests/gitea.yaml
sed "s#image: javo-fakegithub:dev#image: $fake_image#" test/e2e/manifests/fakegithub.yaml | kubectl apply -f -
# On kind the same image tag after a rebuild: restart so the pod runs the new image.
if [ "${JAVO_CLUSTER:-compose}" = kind ]; then kubectl -n "$ns" rollout restart deploy/fakegithub; fi
# Gitea reads the password when its pod starts and keeps it in its (emptyDir) database.
if [ "$new_password" = 1 ]; then kubectl -n "$ns" rollout restart deploy/gitea; fi
kubectl -n "$ns" rollout status deploy/gitea --timeout=240s
kubectl -n "$ns" rollout status deploy/fakegithub --timeout=240s

step "cluster DNS: github.com -> Gitea"
rule="rewrite name exact github.com gitea.$ns.svc.cluster.local"
if [ "${JAVO_CLUSTER:-compose}" != kind ]; then
  # k3s re-applies its own coredns ConfigMap; it imports *.override keys of
  # coredns-custom into the main server block, the place for local rules.
  if [ "$(kubectl -n kube-system get configmap coredns-custom -o jsonpath='{.data.github\.override}' 2>/dev/null)" = "$rule" ]; then
    echo "rule already in place"
  else
    kubectl -n kube-system create configmap coredns-custom --from-literal=github.override="$rule" \
      --dry-run=client -o yaml | kubectl apply -f -
    kubectl -n kube-system rollout restart deploy/coredns
    kubectl -n kube-system rollout status deploy/coredns --timeout=120s
  fi
  exit 0
fi
current="$(kubectl -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}')"
if grep -qF "$rule" <<<"$current"; then
  echo "rule already in place"
else
  # Put the rule before the kubernetes plugin, which answers the cluster names.
  updated="$(awk -v rule="$rule" '/^[[:space:]]*kubernetes /&&!done{match($0,/^[[:space:]]*/);print substr($0,1,RLENGTH) rule;done=1}{print}' <<<"$current")"
  kubectl -n kube-system create configmap coredns --from-literal=Corefile="$updated" --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n kube-system rollout restart deploy/coredns
  kubectl -n kube-system rollout status deploy/coredns --timeout=120s
fi
