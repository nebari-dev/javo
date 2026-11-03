#!/usr/bin/env bash
# Record the real error text the pinned git-sync image writes for each clone
# failure into internal/source/testdata/gitsync/<case>.txt, so the classifier
# is tested against git-sync's actual output and not a guess. Needs docker and
# network access. Re-run after bumping the image pin.
#
# Each run mimics the app pod's clone init container: non-root uid, all
# capabilities dropped, HOME=/tmp and a writable root. git-sync's --error-file
# is not used: it writes a temp file next to the target and renames it, which
# fails for /dev/termination-log (/dev is not writable by a non-root uid and the
# target is a mount point). The pod uses terminationMessagePolicy
# FallbackToLogsOnError instead, which hands the kubelet's termination message
# the last 80 lines or 2048 bytes (whichever is smaller) of the container log,
# so that tail is what each fixture records.
# The only token used is the literal TOKEN below; it is not a credential.
set -euo pipefail

IMAGE='registry.k8s.io/git-sync/git-sync:v4.7.1@sha256:c5e1976da735bf0b53979f079a07475d77174c51863cd084e928ff6074480717'
TOKEN='javo-test-bad-token'
OUT="$(cd "$(dirname "$0")/../.." && pwd)/internal/source/testdata/gitsync"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$OUT"
mkdir -p "$WORK/tar/etc/javo/git"
printf '%s' "$TOKEN" >"$WORK/tar/etc/javo/git/token"
chmod 644 "$WORK/tar/etc/javo/git/token"
COPYFILE_DISABLE=1 tar --no-xattrs -C "$WORK/tar" -cf "$WORK/token.tar" etc

# record <case> <repo> <ref> [with-token]
# The token file is copied in with docker cp (as a tar, to create the dirs): a bind mount of a host temp dir
# is not visible to a Docker VM (Colima, Docker Desktop) and would silently
# become an empty directory.
record() {
  local name=$1 repo=$2 ref=$3 auth=${4:-}
  local args=(--repo="$repo" --ref="$ref" --root=/srv/src --link=current
    --one-time --depth=1 --submodules=off)
  if [ -n "$auth" ]; then
    args+=(--username=x-access-token --password-file=/etc/javo/git/token)
  fi
  local id code=0
  id=$(docker create --user 1000:100 --cap-drop ALL --security-opt no-new-privileges \
    --tmpfs /srv/src:mode=1777,uid=1000,gid=100 -e HOME=/tmp \
    "$IMAGE" "${args[@]}")
  if [ -n "$auth" ]; then
    docker cp - "$id:/" <"$WORK/token.tar"
  fi
  docker start -a "$id" >"$WORK/$name.log" 2>&1 || code=$?
  docker rm "$id" >/dev/null
  if [ "$code" -eq 0 ]; then
    echo "$name: git-sync exited 0, expected a failure" >&2
    exit 1
  fi
  tail -n 80 "$WORK/$name.log" | tail -c 2048 >"$WORK/$name.tail"
  if grep -q "$TOKEN" "$WORK/$name.log"; then
    echo "$name: token text found in the git-sync log" >&2
    exit 1
  fi
  cp "$WORK/$name.tail" "$OUT/$name.txt"
  echo "$name: exit $code, $(wc -c <"$WORK/$name.tail") bytes"
}

record repo-not-found https://github.com/octocat/javo-no-such-repo HEAD
record private-no-token https://github.com/example-org/private-site HEAD
record bad-token https://github.com/example-org/private-site HEAD with-token
record ref-not-found https://github.com/octocat/Hello-World javo-no-such-branch
record host-unreachable https://unreachable.invalid/x HEAD
