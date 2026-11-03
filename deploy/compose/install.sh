#!/bin/sh
# One-shot installer of the Compose install (deploy/compose/compose.yaml).
# Runs on every `docker compose up` and is idempotent: waits for the k3s
# API, copies locally built images into k3s (or lets k3s pull them from
# JAVO_IMAGE_REGISTRY), installs the javo chart with deploy/compose/values.yaml
# and creates local user admin (a platform admin). admin's password is
# written to /var/lib/javo-secrets/admin.password and never printed.
#
# Environment (set by compose.yaml):
#   JAVO_PUBLIC_URL      URL browsers use, e.g. http://localhost:8080
#   JAVO_IMAGE_REGISTRY  empty: use images built on this Docker host; else the
#                       registry prefix k3s pulls the images from
#   JAVO_IMAGE_TAG       tag of the javo images
set -eu
# A failed docker save must fail the import pipe (busybox sh has pipefail).
# shellcheck disable=SC3040
set -o pipefail

chart=/opt/javo/deploy/helm/javo
values=/opt/javo/deploy/compose/values.yaml
k3s_config=/var/lib/javo-kubeconfig/kubeconfig.yaml
pw=/var/lib/javo-secrets/admin.password
ns=javo-system
apps_ns=javo-apps
public_url=${JAVO_PUBLIC_URL:?set JAVO_PUBLIC_URL}
registry=${JAVO_IMAGE_REGISTRY:-}
tag=${JAVO_IMAGE_TAG:?set JAVO_IMAGE_TAG}
public_host="${public_url#*://}"
public_host="${public_host%%/*}"
export KUBECONFIG=/tmp/kubeconfig

current=start
step() {
  current="$*"
  printf '\n==> %s\n' "$*"
}
# node_pressure prints the node's pressure conditions (DiskPressure,
# MemoryPressure, PIDPressure) that are True. A node under pressure is
# tainted and nothing schedules; DiskPressure means the Docker host's disk,
# where the volumes live, is nearly full.
node_pressure() {
  kubectl get node javo -o go-template='{{range .status.conditions}}{{if and (ne .type "Ready") (eq .status "True")}}{{.type}}: {{.message}}{{"\n"}}{{end}}{{end}}' 2>/dev/null || true
}
on_exit() {
  rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "--- failure: cluster state ---" >&2
    for n in "$ns" "$apps_ns" kube-system; do
      kubectl -n "$n" get pods >&2 2>&1 || true
      kubectl -n "$n" get events --field-selector type=Warning >&2 2>&1 || true
    done
    p="$(node_pressure)"
    [ -z "$p" ] || echo "node under pressure: $p" >&2
    echo "install failed (exit $rc) at step: $current" >&2
  fi
}
trap on_exit EXIT

step "wait for the k3s API"
i=0
until [ -s "$k3s_config" ] && kubectl get --raw=/readyz >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 90 ]; then
    echo "k3s API not ready after 180 s; see: docker compose logs k3s" >&2
    exit 1
  fi
  if [ -s "$k3s_config" ]; then
    # Inside the Compose network the API is the k3s service, not 127.0.0.1.
    sed 's#https://127.0.0.1:6443#https://k3s:6443#' "$k3s_config" >"$KUBECONFIG"
    # The checks still failing, or why the API does not answer yet.
    kubectl get --raw='/readyz?verbose' 2>&1 | grep -v '^\[+\]' | head -5 || true
  else
    echo "waiting for k3s to write $k3s_config"
  fi
  sleep 2
done
# wait_for <what> <kubectl get args...>: k3s registers the node and creates
# its add-ons (coredns, local-path) a few seconds after the API answers.
wait_for() {
  what=$1
  shift
  n=0
  until kubectl get "$@" >/dev/null 2>&1; do
    n=$((n + 1))
    if [ "$n" -gt 90 ]; then
      echo "$what did not appear within 180 s" >&2
      kubectl get "$@" >&2 || true
      exit 1
    fi
    echo "waiting for $what"
    sleep 2
  done
}
wait_for "the node" node javo
kubectl wait --for=condition=Ready node/javo --timeout=180s
kubectl get nodes -o wide
wait_for "coredns and local-path-provisioner" -n kube-system deploy/coredns deploy/local-path-provisioner
kubectl -n kube-system rollout status deploy/coredns --timeout=180s
kubectl -n kube-system rollout status deploy/local-path-provisioner --timeout=180s

# The image built from this checkout is javo (tagged by JAVO_IMAGE_TAG);
# image_ref maps it to the reference this install runs. The templates run
# upstream images pinned by digest (name@sha256:...), which k3s pulls (the
# workspace pods leave imagePullPolicy at its default, IfNotPresent).
image_ref() {
  if [ -n "$registry" ]; then echo "${registry%/}/$1:$tag"; else echo "$1:$tag"; fi
}
javo_image="$(image_ref javo)"
images="$javo_image"
template_args=""

restart=""
if [ -z "$registry" ]; then
  step "copy images built on this Docker host into k3s"
  self="$(hostname)"
  project="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$self")"
  k3s="$(docker ps -q --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=k3s)"
  [ -n "$k3s" ] || { echo "k3s container of project $project not found" >&2; exit 1; }
  platform="$(docker version -f '{{.Server.Os}}/{{.Server.Arch}}')"
  for img in $images; do
    # The platform's manifest digest: what ctr records for the import below,
    # and stable across rebuilds that change nothing.
    if ! id="$(docker image inspect --platform "$platform" -f '{{.Id}}' "$img" 2>/dev/null)"; then
      echo "image $img not found on this Docker host; build the images first:" >&2
      echo "  docker compose --profile build up -d" >&2
      exit 1
    fi
    ref="docker.io/library/$img"
    have="$(docker exec "$k3s" ctr -n k8s.io images ls "name==$ref" | awk 'NR > 1 {print $3}')"
    if [ "$have" = "$id" ]; then
      echo "$img: already in k3s ($id)"
      continue
    fi
    echo "$img: importing $id"
    docker save --platform "$platform" "$img" | docker exec -i "$k3s" ctr -n k8s.io images import --platform "$platform" -
    # javo runs the controller and the gateway; a new image under the same
    # tag needs a restart to be picked up.
    if [ "$img" = "$javo_image" ]; then restart=--restart; fi
  done
  pull_policy=Never
else
  step "images are pulled by k3s from $registry"
  echo "$images" | tr ' ' '\n'
  pull_policy=IfNotPresent
fi

step "check the node"
pressure="$(node_pressure)"
if [ -n "$pressure" ]; then
  echo "the k3s node is under pressure, pods will not be scheduled:" >&2
  echo "$pressure" >&2
  echo "free disk space or memory on the Docker host, then run: docker compose up -d" >&2
  exit 1
fi

step "helm upgrade --install"
# template_args is a list of words with no spaces inside, split on purpose.
# shellcheck disable=SC2086
/opt/javo/scripts/install/install-chart.sh "$chart" "$ns" $restart \
  -f "$values" \
  --set image.repository="${javo_image%:*}" --set image.tag="$tag" --set image.pullPolicy="$pull_policy" \
  --set publicHost="$public_host" $template_args \
  --force-conflicts --wait --timeout 10m

step "local user admin"
/opt/javo/scripts/install/ensure-local-user.sh "$ns" admin "$pw" --group javo-admins

step "wait for the gateway"
/opt/javo/scripts/install/wait-healthz.sh http://k3s:30080/healthz "$ns"

echo
echo "javo is ready at $public_url"
echo "Sign in as admin; print the password with:"
echo "  docker compose exec k3s cat $pw"
