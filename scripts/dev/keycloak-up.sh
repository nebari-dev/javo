#!/usr/bin/env bash
# Deploy the dev Keycloak (deploy/dev/keycloak) and the javo client secret
# the gateway mounts. Passwords and the client secret are generated once
# into .dev/ (mode 0600) and never printed; a rerun reuses them.
#
# Usage: scripts/dev/keycloak-up.sh <keycloak-url> <ui-url> <file-suffix> [proxy-headers]
#   keycloak-url   KC_HOSTNAME, up to /auth, e.g. http://keycloak.localhost:8081/auth
#   ui-url         where javo's UI is served, e.g. http://javo.localhost:8080
#   file-suffix    "" for kind, ".<site>" for scripts/install/deploy-single-node.sh: the files are
#                  .dev/{alice,bob}.keycloak<suffix>.password,
#                  .dev/keycloak-admin<suffix>.password, .dev/oidc-client<suffix>.secret
#   proxy-headers  KC_PROXY_HEADERS when behind a reverse proxy (xforwarded)
# Uses the caller's kubectl and KUBECONFIG.
set -euo pipefail

kc_url=${1:?keycloak url}
ui_url=${2:?ui url}
suffix=${3?file suffix}
proxy_headers=${4:-}
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
kc_ns=javo-keycloak
javo_ns=javo-system

mkdir -p "$root/.dev"
chmod 700 "$root/.dev"
generated=0
# secret_file <path>: create it with 32 random bytes (base64url) unless present.
secret_file() {
  if [ ! -s "$1" ]; then
    (umask 077; openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n' >"$1")
    generated=1
    echo "wrote $1"
  fi
  chmod 600 "$1"
}
admin_pw="$root/.dev/keycloak-admin$suffix.password"
alice_pw="$root/.dev/alice.keycloak$suffix.password"
bob_pw="$root/.dev/bob.keycloak$suffix.password"
client_secret="$root/.dev/oidc-client$suffix.secret"
for f in "$admin_pw" "$alice_pw" "$bob_pw" "$client_secret"; do secret_file "$f"; done

# Secrets go from file to the API server without passing through this
# shell's output: the dry-run YAML is piped straight into apply.
kubectl create namespace "$kc_ns" --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace "$javo_ns" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$kc_ns" create secret generic keycloak-secrets \
  --from-file=KC_BOOTSTRAP_ADMIN_PASSWORD="$admin_pw" \
  --from-file=ALICE_PASSWORD="$alice_pw" --from-file=BOB_PASSWORD="$bob_pw" \
  --from-file=JAVO_CLIENT_SECRET="$client_secret" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$javo_ns" create secret generic javo-oidc --from-file=client-secret="$client_secret" \
  --dry-run=client -o yaml | kubectl apply -f -
old_env="$(kubectl -n "$kc_ns" get configmap keycloak-env -o jsonpath='{.data}' --ignore-not-found)"
env_args=(--from-literal=KC_HOSTNAME="$kc_url" --from-literal=JAVO_UI_URL="$ui_url")
if [ -n "$proxy_headers" ]; then env_args+=(--from-literal=KC_PROXY_HEADERS="$proxy_headers"); fi
kubectl -n "$kc_ns" create configmap keycloak-env "${env_args[@]}" \
  --dry-run=client -o yaml | kubectl apply -f -
[ "$old_env" = "$(kubectl -n "$kc_ns" get configmap keycloak-env -o jsonpath='{.data}')" ] || generated=1

kubectl apply -k "$root/deploy/dev/keycloak"
# The realm is imported only when Keycloak starts, and start-dev keeps it in
# memory: restart when a secret was generated or the URLs changed.
if [ "$generated" -eq 1 ]; then
  kubectl -n "$kc_ns" rollout restart deploy/keycloak
fi
kubectl -n "$kc_ns" rollout status deploy/keycloak --timeout=5m
