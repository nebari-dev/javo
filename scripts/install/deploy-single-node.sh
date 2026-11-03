#!/usr/bin/env bash
# Deploy javo to a single-node k3s host without a registry: build the javo
# image (the only one; apps and jobs run upstream images, which k3s pulls)
# when its inputs changed, with the host's Docker Engine (natively, over SSH),
# copy them into k3s containerd there, install the chart (which installs
# the templates) and create local user alice. The host needs Docker Engine
# usable by the SSH user, and sudo without a password for k3s.
#
# Usage:
#   KUBECONFIG=/path/to/kubeconfig JAVO_SSH_HOST=user@<host> \
#     JAVO_PUBLIC_URL=https://<host> scripts/install/deploy-single-node.sh
#   JAVO_SSH_HOST    SSH target of the k3s node (required)
#   JAVO_PUBLIC_URL  URL the UI is served at, printed at the end (required)
#   JAVO_APPS_URL    URL of a separate apps host, e.g. https://<apps-host>
#                   (optional; unset serves apps on the UI host, where
#                   sharing is refused)
#   JAVO_OIDC_URL    Keycloak URL up to /auth, e.g. https://<auth-host>/auth
#                   (optional; set deploys the dev Keycloak on NodePort 30081
#                   and signs people in with it, local sign-in stays on)
#   JAVO_SITE        name of this install in the .dev/ file names below
#                   (optional, default single-node; letters, digits, - and _)
# The proxy in front terminates TLS and forwards plain HTTP: the UI and apps
# hosts to NodePort 30080, the Keycloak host to NodePort 30081.
# Site-specific Helm values go in .dev/<site>.values.yaml (untracked); it is
# passed after deploy/single-node/values.yaml when present.
# alice's local password is written to .dev/alice.<site>.password (0600),
# the Keycloak passwords to .dev/*.keycloak.<site>.password; none is printed.
# <site> is JAVO_SITE.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

: "${KUBECONFIG:?set KUBECONFIG to the kubeconfig of the k3s cluster}"
export KUBECONFIG
: "${JAVO_SSH_HOST:?set JAVO_SSH_HOST to the SSH target of the k3s node, e.g. user@<host>}"
: "${JAVO_PUBLIC_URL:?set JAVO_PUBLIC_URL to the URL the gateway is served at, e.g. https://<host>}"
site="${JAVO_SITE:-single-node}"
case "$site" in
  ''|*[!A-Za-z0-9_-]*) echo "JAVO_SITE must be letters, digits, - and _, not '$site'" >&2; exit 1 ;;
esac
ssh_host="$JAVO_SSH_HOST"
node_ip="${ssh_host#*@}"
public_url="$JAVO_PUBLIC_URL"
# Host (and port) of the public URL, for apps that check the websocket Origin.
public_host="${public_url#*://}"
public_host="${public_host%%/*}"
host_of() { local h="${1#*://}"; echo "${h%%/*}"; }
mode_args=()
if [ -n "${JAVO_APPS_URL:-}" ]; then
  mode_args+=(--set config.urls.scheme=https --set "config.urls.uiHost=$public_host"
    --set "config.urls.appsHost=$(host_of "$JAVO_APPS_URL")")
fi
if [ -n "${JAVO_OIDC_URL:-}" ]; then
  oidc_url="${JAVO_OIDC_URL%/}"
  mode_args+=(--set gateway.oidcClientSecret=javo-oidc
    --set "config.auth.oidc.issuer=$oidc_url/realms/javo" --set config.auth.oidc.clientID=javo
    --set config.auth.oidc.displayName=Keycloak --set config.auth.oidc.revalidateInterval=30s
    --set 'config.roles.platformAdmin.groups={/javo-admins}')
fi
site_values=()
if [ -f ".dev/$site.values.yaml" ]; then site_values=(-f ".dev/$site.values.yaml"); fi
ns=javo-system
apps_ns=javo-apps
platform=linux/amd64

step() { printf '\n==> %s (%d s)\n' "$*" "$SECONDS"; }
on_exit() {
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "--- failure: cluster state ---" >&2
    for n in "$ns" "$apps_ns" javo-keycloak; do kubectl -n "$n" get pods,events >&2 2>&1 || true; done
  fi
}
trap on_exit EXIT

# One SSH connection for all commands on the node.
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=5 -o ControlMaster=auto -o "ControlPath=/tmp/javo-single-node-%C" -o ControlPersist=60)
# Commands are expanded here on purpose, then run on the node.
# shellcheck disable=SC2029
on_node() { ssh "${ssh_opts[@]}" "$ssh_host" "$@"; }

step "check access to $ssh_host and the cluster"
# The node builds the images with its own Docker Engine, natively, so it
# must be $platform and let the SSH user run Docker.
arch="$(on_node 'sudo k3s ctr version >/dev/null && docker version -f "{{.Server.Os}}/{{.Server.Arch}}"')"
if [ "$arch" != "$platform" ]; then
  echo "Docker on $ssh_host builds for '$arch', not $platform" >&2
  exit 1
fi
kubectl get nodes -o wide

step "build on $ssh_host the $platform javo image if its inputs changed"
# The image is tagged with a hash of its build inputs (scripts/build/image-tag.sh),
# so a tag k3s containerd holds already is up to date. Otherwise it is built
# by the node's Docker (no emulation, build cache kept between deploys),
# copied into k3s containerd there, and checked for the platform.
# The node's Docker copies of images with older tags only fill the disk (k3s
# containerd holds what runs), and a full disk makes the kubelet delete images
# this deploy imports before they are tagged. Remove them; the build cache
# stays.
javo_tag="$(scripts/build/image-tag.sh "$platform" javo | cut -d' ' -f2)"
ref="javo:$javo_tag"
# Older javo-* images (the template images earlier releases built) go too.
on_node "docker image ls --format '{{.Repository}}:{{.Tag}}' | grep -E '^javo(-[a-z0-9]+)?:' | grep -vxF '$ref' | xargs -r docker image rm >/dev/null || true"
if on_node sudo k3s ctr -n k8s.io images ls -q | grep -qxF "docker.io/library/$ref"; then
  echo "$ref: on the node"
else
  start=$SECONDS
  DOCKER_HOST="ssh://$ssh_host" docker buildx build --builder default --platform "$platform" \
    --provenance=false --load -t "$ref" .
  on_node "docker save $ref | sudo k3s ctr -n k8s.io images import --platform $platform -"
  # Refuse to go on with an image the node cannot run.
  got=$(on_node sudo k3s ctr -n k8s.io images ls "name==docker.io/library/$ref" | awk 'NR==2{print $(NF-1)}')
  case ",$got," in *",$platform,"*) ;; *) echo "$ref on the node is for '$got', not $platform" >&2; exit 1 ;; esac
  echo "$ref: built and imported in $((SECONDS - start)) s"
fi
image=$ref

if [ -n "${JAVO_OIDC_URL:-}" ]; then
  step "dev Keycloak at $oidc_url"
  scripts/dev/keycloak-up.sh "$oidc_url" "$public_url" ".$site" xforwarded
fi

step "helm upgrade --install"
scripts/install/install-chart.sh deploy/helm/javo "$ns" \
  -f deploy/single-node/values.yaml ${site_values[@]+"${site_values[@]}"} --set image.tag="${image#*:}" --set publicHost="$public_host" \
  ${mode_args[@]+"${mode_args[@]}"} \
  --force-conflicts --wait --timeout 5m

step "local user alice"
mkdir -p .dev
pw=.dev/alice.$site.password
scripts/install/ensure-local-user.sh "$ns" alice "$pw"

step "health check"
scripts/install/wait-healthz.sh "http://$node_ip:30080/healthz" "$ns"

echo
echo "Open $public_url"
if [ -n "${JAVO_APPS_URL:-}" ]; then echo "Apps are served on $JAVO_APPS_URL"; fi
if [ -n "${JAVO_OIDC_URL:-}" ]; then
  echo "Sign in with Keycloak as alice or bob: passwords in .dev/alice.keycloak.$site.password, .dev/bob.keycloak.$site.password"
  echo "Keycloak admin console: $oidc_url/admin (admin, password in .dev/keycloak-admin.$site.password)"
fi
echo "Local sign-in: alice, password in $pw"
echo "Deployed in $SECONDS s"
