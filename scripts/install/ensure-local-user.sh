#!/bin/sh
# Create local user <name> with a generated password, unless the user exists
# and its password file is present. A password file without the user (a
# recreated cluster or database) is stale: the user is created and the file
# replaced. Without the file the user is (re)created with a new password.
# The password is written to <password-file> (mode 0600) and never printed.
# Used by scripts/dev/up.sh, scripts/install/deploy-single-node.sh and the Compose installer.
#
# Accounts live in the database, so `javo user add` runs in the pod that holds
# it: deploy/javo with SQLite, deploy/javo-controller with Postgres. Its image
# has no shell, so the password comes back on standard output
# (--password-file -) straight into the file.
#
# Usage: scripts/install/ensure-local-user.sh <namespace> <name> <password-file> [javo user add args...]
# Uses the caller's kubectl and KUBECONFIG.
set -eu

ns=${1:?release namespace}
name=${2:?user name}
pw=${3:?password file}
shift 3

if kubectl -n "$ns" get deploy javo >/dev/null 2>&1; then
  target=deploy/javo container=javo
  db="--database-engine=sqlite --database-path=/var/lib/javo/javo.db"
else
  target=deploy/javo-controller container=controller
  db="--database-engine=postgres --database-dsn-file=/etc/javo-db/dsn"
fi

# With the file present, add without --force: an existing user is kept (and
# the file with it), a missing one is created.
force=--force
[ -s "$pw" ] && force=
tmp="$pw.new"
err="$pw.err"
trap 'rm -f "$tmp" "$err"' EXIT
umask 077
# $db and $force are word lists, split on purpose.
# shellcheck disable=SC2086
if kubectl -n "$ns" exec "$target" -c "$container" -- \
  /javo user add "$name" $db --password-file - $force "$@" >"$tmp" 2>"$err"; then
  mv "$tmp" "$pw"
  chmod 600 "$pw"
  echo "wrote $pw (user $name)"
elif [ -n "$(grep -F 'already exists' "$err")" ]; then
  echo "user $name exists and $pw is present, skipping"
else
  cat "$err" >&2
  exit 1
fi
