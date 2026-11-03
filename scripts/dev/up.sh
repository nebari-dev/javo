#!/usr/bin/env bash
# Create (or reuse) this checkout's cluster (JAVO_KIND_NAME, javo-dev in the
# main checkout; see scripts/dev/env.sh), get the images there, install the
# chart with deploy/dev/values.yaml (which installs the templates) and create
# local user alice. Rerun after a change: only what changed is rebuilt.
#
# JAVO_CLUSTER picks the cluster:
#   compose (default)  single-node k3s in a container, the Compose install's
#                      (deploy/compose/compose.yaml with deploy/compose/dev.yaml,
#                      project JAVO_KIND_NAME). Images go through the local
#                      registry all checkouts share (scripts/dev/registry-up.sh,
#                      scripts/build/images.sh): each is built only when its inputs
#                      change, and k3s pulls only new layers.
#   kind               a kind cluster; images are built and loaded with kind load.
# One checkout runs one of them at a time (they use the same host ports).
#
# JAVO_KEYCLOAK=1 deploys the dev Keycloak (the OIDC, sharing and custom
# amount tests need it). kind always has it; on compose it stays once deployed.
#
# JAVO_DB picks the database: sqlite (default; one pod runs the gateway and
# the controller) or postgres (deploy/dev/postgres.yaml in namespace javo-postgres,
# two gateway and two controller replicas). Switching an existing install
# starts the other engine's database empty; the SQLite volume is kept. People
# then get new uids while their home volumes keep the old owner, so delete
# the home claims (kubectl -n javo-apps delete pvc -l javo.nebari.dev/owner)
# before testing on the other engine.
# JAVO_E2E_GITHUB=1 (without JAVO_E2E_GIT) turns previews on against the real
# github.com for the live smoke test (make e2e-github).
# JAVO_E2E_GIT=1 also starts the hermetic Git side of the e2e tests
# (scripts/test/e2e-git-up.sh: Gitea, a fake GitHub pull request list, and DNS that
# sends github.com to Gitea) and turns on source.gitConfig and previews.
# JAVO_E2E_S3=1 also starts the S3-compatible bucket of the backup restore
# drill (scripts/test/e2e-s3-up.sh) and turns on backupTarget with home backups
# every minute.
#
# JAVO_UI_HOST and JAVO_APPS_HOST override the hosts browsers use (default
# javo.localhost:<JAVO_HTTP_PORT> and apps.localhost:<JAVO_HTTP_PORT>);
# scripts/dev/remote-up.sh points them at the Vite server and the LAN address.
#
# Everything talks to the cluster through .dev/kubeconfig; the user's own
# kubectl context and ~/.kube/config are never touched.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"
. scripts/dev/env.sh

cluster="$JAVO_KIND_NAME"
provider="${JAVO_CLUSTER:-compose}"
ui_host="${JAVO_UI_HOST:-javo.localhost:$JAVO_HTTP_PORT}"
apps_host="${JAVO_APPS_HOST:-apps.localhost:$JAVO_HTTP_PORT}"
ui_url="http://$ui_host"
kc_url="http://keycloak.localhost:$JAVO_KEYCLOAK_PORT/auth"
ns=javo-system
apps_ns=javo-apps
db="${JAVO_DB:-sqlite}"
case "$db" in
  sqlite | postgres) ;;
  *) echo "JAVO_DB must be sqlite or postgres, not $db" >&2; exit 1 ;;
esac
mkdir -p .dev
export KUBECONFIG="$root/.dev/kubeconfig"
# JAVO_COMPOSE: a command that stands in for "docker compose" (scripts/dev/remote-compose.sh).
compose=(${JAVO_COMPOSE:-docker compose} -p "$cluster" -f deploy/compose/compose.yaml -f deploy/compose/dev.yaml)

step() { printf '\n==> %s (%s s)\n' "$*" "$SECONDS"; }
kc() { kubectl "$@"; }

diagnose() {
  echo "--- failure: cluster state ---" >&2
  for n in "$ns" "$apps_ns" javo-keycloak; do
    kc -n "$n" get pods,events >&2 2>&1 || true
  done
}
on_exit() {
  local rc=$?
  [ "$rc" -eq 0 ] || diagnose
}
trap on_exit EXIT

kind_exists() { grep -qx "$cluster" <<<"$(kind get clusters 2>/dev/null)"; }
compose_exists() { [ -n "$("${compose[@]}" ps -aq k3s 2>/dev/null)" ]; }

# kind needs more inotify instances than Colima's default (128); with too few,
# kube-proxy and the kubelet fail with "too many open files". The setting
# resets when the Colima VM restarts, so check it on every run.
preflight_colima() {
  [ "$(docker context show 2>/dev/null)" = "colima" ] || return 0
  local want=1024 have
  have="$(colima ssh -- sysctl -n fs.inotify.max_user_instances)"
  if [ "$have" -lt "$want" ]; then
    echo "colima: fs.inotify.max_user_instances is $have, raising it to $want"
    colima ssh -- sudo sysctl -w "fs.inotify.max_user_instances=$want"
  fi
}

# kind_up: the kind cluster with this checkout's ports, the images built and
# loaded; sets image_args.
kind_up() {
  step "kind cluster $cluster (host ports $JAVO_HTTP_PORT and $JAVO_KEYCLOAK_PORT)"
  if compose_exists; then
    echo "the Compose install $cluster uses this checkout's ports; run make down first" >&2
    exit 1
  fi
  preflight_colima
  create_cluster() {
    sed -e "s/JAVO_HTTP_PORT/$JAVO_HTTP_PORT/" -e "s/JAVO_KEYCLOAK_PORT/$JAVO_KEYCLOAK_PORT/" deploy/dev/kind.yaml >.dev/kind.yaml
    kind create cluster --name "$cluster" --config .dev/kind.yaml --kubeconfig "$KUBECONFIG"
  }
  if kind_exists; then
    # Port mappings are fixed when a kind cluster is created, so a cluster
    # from before deploy/dev/kind.yaml mapped Keycloak has to be recreated.
    local ui_port
    ui_port="$(docker port "$cluster-control-plane" 30080/tcp 2>/dev/null || true)"
    if [ -z "$(docker port "$cluster-control-plane" 30081/tcp 2>/dev/null)" ]; then
      echo "cluster $cluster has no host port for Keycloak (made from an older deploy/dev/kind.yaml); recreating it"
      kind delete cluster --name "$cluster"
      create_cluster
    elif [ "${ui_port##*:}" != "$JAVO_HTTP_PORT" ]; then
      echo "cluster $cluster serves the UI on ${ui_port:-no host port}, not $JAVO_HTTP_PORT: run make dev-down, or set JAVO_HTTP_PORT and JAVO_KEYCLOAK_PORT to its ports" >&2
      exit 1
    else
      echo "cluster $cluster already exists"
      kind export kubeconfig --name "$cluster" --kubeconfig "$KUBECONFIG"
    fi
  else
    create_cluster
  fi
  chmod 600 "$KUBECONFIG"

  step "build the javo image"
  # The only image built: apps and jobs run upstream images, which the
  # cluster pulls.
  local image=javo:dev
  docker build -t "$image" .

  step "load the image into kind"
  kind load docker-image --name "$cluster" "$image"
  # Same tag after a rebuild: --restart so pods pick up the freshly loaded image.
  restart=(--restart)
  image_args=(--set image.repository=javo --set image.tag=dev)
  keycloak=1
}

# compose_up: k3s of the Compose install with this checkout's project name
# and ports, the images in the shared registry; sets image_args. k3s starts
# while the images are checked or built.
compose_up() {
  step "Compose project $cluster (host ports $JAVO_HTTP_PORT, $JAVO_KEYCLOAK_PORT and API $JAVO_API_PORT)"
  if kind_exists; then
    echo "kind cluster $cluster uses this checkout's ports; run JAVO_CLUSTER=kind make dev-down first" >&2
    exit 1
  fi
  scripts/dev/registry-up.sh >/dev/null
  "${compose[@]}" up -d --quiet-pull k3s

  step "images"
  scripts/build/images.sh localhost:5001 .dev/images.yaml

  step "wait for the k3s API"
  local i
  (umask 077; : >.dev/kubeconfig.new)
  for i in $(seq 1 90); do
    if "${compose[@]}" exec -T k3s cat /var/lib/javo-kubeconfig/kubeconfig.yaml 2>/dev/null |
      sed "s#https://127.0.0.1:6443#https://127.0.0.1:$JAVO_API_PORT#" >.dev/kubeconfig.new &&
      [ -s .dev/kubeconfig.new ] && KUBECONFIG=.dev/kubeconfig.new kc get --raw=/readyz >/dev/null 2>&1; then
      break
    fi
    [ "$i" -lt 90 ] || { echo "k3s API not ready after 90 s; see: ${compose[*]} logs k3s" >&2; exit 1; }
    sleep 1
  done
  mv .dev/kubeconfig.new "$KUBECONFIG"
  for i in $(seq 1 60); do
    kc get node javo >/dev/null 2>&1 && break
    sleep 1
  done
  kc wait --for=condition=Ready node/javo --timeout=120s >/dev/null
  # The node evicts pods while the Docker disk is nearly full (DiskPressure).
  local pressure
  pressure="$(kc get node javo -o go-template='{{range .status.conditions}}{{if and (ne .type "Ready") (eq .status "True")}}{{.type}}: {{.message}}{{"\n"}}{{end}}{{end}}')"
  if [ -n "$pressure" ]; then
    echo "the k3s node is under pressure and evicts pods: $pressure" >&2
    echo "free Docker disk space (docker builder prune, docker image prune) and rerun" >&2
    exit 1
  fi
  restart=()
  image_args=(-f .dev/images.yaml)
  keycloak=0
  if [ "${JAVO_KEYCLOAK:-}" = 1 ] || kc get namespace javo-keycloak >/dev/null 2>&1; then keycloak=1; fi
}

case "$provider" in
  compose) compose_up ;;
  kind) kind_up ;;
  *) echo "JAVO_CLUSTER must be compose or kind, not $provider" >&2; exit 1 ;;
esac

if [ "${JAVO_E2E_GIT:-}" = 1 ]; then
  step "hermetic Git (Gitea and fakegithub)"
  JAVO_CLUSTER="$provider" scripts/test/e2e-git-up.sh
fi

# Browsers use the host ports, so the URLs are set here rather than in
# deploy/dev/values.yaml. The gateway reaches the issuer's host through the
# Keycloak Service.
url_args=(
  --set publicHost="$ui_host"
  --set config.urls.uiHost="$ui_host"
  --set config.urls.appsHost="$apps_host"
)
if [ "$keycloak" = 1 ]; then
  step "dev Keycloak"
  scripts/dev/keycloak-up.sh "$kc_url" "$ui_url" ""
  url_args+=(
    --set config.auth.oidc.issuer="$kc_url/realms/javo"
    --set-json "config.auth.oidc.dial={\"keycloak.localhost:$JAVO_KEYCLOAK_PORT\":\"keycloak.javo-keycloak.svc.cluster.local:8080\"}"
  )
else
  # Local sign-in only: no OIDC settings and no client secret to mount.
  url_args+=(--set config.auth.oidc=null --set gateway.oidcClientSecret=)
fi
db_args=(--set database.engine="$db")
if [ "${JAVO_E2E_GIT:-}" = 1 ]; then
  # Clones trust Gitea's self-signed certificate; previews list pull requests
  # from fakegithub (a poll every 30 s, the shortest allowed).
  db_args+=(--set source.gitConfig=http.sslVerify:false
    --set source.previews.enabled=true --set source.previews.pollInterval=30s
    --set source.previews.github.apiURL=http://fakegithub.javo-e2e.svc:8080/)
fi
if [ "${JAVO_E2E_S3:-}" = 1 ]; then
  step "S3 bucket for the backup drill"
  scripts/test/e2e-s3-up.sh "$ns"
  db_args+=(--set backupTarget.enabled=true --set backupTarget.endpoint=http://s3.javo-e2e.svc:9000
    --set backupTarget.bucket=javo-e2e --set backupTarget.bucketLookup=path
    --set backupTarget.existingSecret=javo-backup-s3)
fi
if [ "${JAVO_E2E_GITHUB:-}" = 1 ] && [ "${JAVO_E2E_GIT:-}" != 1 ]; then
  # Live smoke against github.com (make e2e-github): previews on, real API.
  db_args+=(--set source.previews.enabled=true --set source.previews.pollInterval=30s)
fi
if [ "$db" = postgres ]; then
  step "dev Postgres"
  # The password is generated once, kept in .dev (0600) and never printed.
  if [ ! -s .dev/postgres.password ]; then
    (umask 077; openssl rand -hex 24 >.dev/postgres.password)
  fi
  kc apply -f deploy/dev/postgres.yaml
  kc -n javo-postgres create secret generic javo-postgres --from-file=password=.dev/postgres.password \
    --dry-run=client -o yaml | kc apply -f -
  kc -n javo-postgres rollout status deploy/postgres --timeout=180s
  (umask 077; printf 'postgres://postgres:%s@postgres.javo-postgres.svc:5432/javo?sslmode=disable' \
    "$(cat .dev/postgres.password)" >.dev/postgres.dsn)
  kc create namespace "$ns" --dry-run=client -o yaml | kc apply -f -
  kc -n "$ns" create secret generic javo-db --from-file=dsn=.dev/postgres.dsn --dry-run=client -o yaml | kc apply -f -
  db_args+=(--set database.postgres.dsnSecret=javo-db --set gateway.replicas=2 --set controller.replicas=2)
fi

step "helm upgrade --install"
scripts/install/install-chart.sh deploy/helm/javo "$ns" ${restart[@]+"${restart[@]}"} -f deploy/dev/values.yaml "${image_args[@]}" \
  "${url_args[@]}" "${db_args[@]}" --force-conflicts --wait --timeout 3m

step "wait for gateway"
scripts/install/wait-healthz.sh "http://localhost:$JAVO_HTTP_PORT/healthz" "$ns"

step "local user alice"
scripts/install/ensure-local-user.sh "$ns" alice .dev/alice.password

# Apps without a login of their own start only once the controller's probe
# finds NetworkPolicy enforced; the probe runs at controller start. The result
# is stored in the database, so the controller's log line is what shows here.
step "wait for the NetworkPolicy probe"
state=""
for _ in $(seq 1 90); do
  # The probe runs in whichever pod leads: the one javo pod, or one of the controllers.
  state="$(kc -n "$ns" logs -l app.kubernetes.io/instance=javo,app.kubernetes.io/name=javo \
    --all-containers --tail=-1 2>/dev/null | grep -F 'NetworkPolicy probe' | tail -n 1 || true)"
  case "$state" in *'state=Enforced'*) break ;; esac
  sleep 2
done
echo "networkPolicy: ${state:-not probed yet}"
case "$state" in *'state=Enforced'*) ;; *) echo "NetworkPolicy probe did not report Enforced" >&2; exit 1 ;; esac

echo
echo "Open $ui_url (apps are served on http://$apps_host; $provider cluster $cluster, ready in $SECONDS s)"
echo "Local sign-in: alice, password in .dev/alice.password"
if [ "$keycloak" = 1 ]; then
  echo "Sign in with Keycloak as alice or bob: passwords in .dev/alice.keycloak.password, .dev/bob.keycloak.password"
  echo "Keycloak admin console: $kc_url/admin (admin, password in .dev/keycloak-admin.password)"
fi
echo "kubectl for this cluster: KUBECONFIG=$KUBECONFIG kubectl get pods -A"
