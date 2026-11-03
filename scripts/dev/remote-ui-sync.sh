#!/usr/bin/env bash
# Keep the Docker host's copy of ui/ (the Vite container's source, see
# scripts/dev/remote-up.sh) equal to this checkout's: one full copy, then
# every 0.3 s the files whose time or size changed are sent and the removed
# ones deleted, each logged as it happens. node_modules and dist stay on
# their side. Polling, not inotify, so edits made through a shared folder
# are seen too.
#
# Usage: scripts/dev/remote-ui-sync.sh <remote dir>
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
. "$root/scripts/dev/env.sh"
. "$root/scripts/dev/remote-env.sh"
dir=${1:?remote dir}
ui="$root/ui"

# list: "<path>\t<mtime> <size>" of every file, sorted.
list() {
  (cd "$ui" && find . \( -name node_modules -o -name dist -o -name '*.timestamp-*' \) -prune -o -type f -printf '%P\t%T@ %s\n') | LC_ALL=C sort
}
# send: copies the files named on stdin (one per line).
send() { tar -C "$ui" -cf - -T - | remote "tar --overwrite -xf - -C '$dir'"; }
# drop: deletes the files named on stdin on the host.
drop() { tr '\n' '\0' | remote "cd '$dir' && xargs -0 -r rm -f --"; }
names() { cut -f1 <<<"$1" | LC_ALL=C sort; }

remote "mkdir -p '$dir'"
cur="$(list)"
cut -f1 <<<"$cur" | send
stale="$(LC_ALL=C comm -13 <(names "$cur") <(remote "cd '$dir' && find . \( -name node_modules -o -name dist -o -name '*.timestamp-*' \) -prune -o -type f -printf '%P\n'" | LC_ALL=C sort))"
if [ -n "$stale" ]; then drop <<<"$stale"; fi
echo "$(date +%T) full copy of ui/ to $JAVO_REMOTE:$dir ($(grep -c . <<<"$cur") files, $(grep -c . <<<"$stale" || true) stale removed)"

prev="$cur"
while sleep 0.3; do
  cur="$(list)"
  [ "$cur" != "$prev" ] || continue
  changed="$(LC_ALL=C comm -13 <(printf '%s\n' "$prev") <(printf '%s\n' "$cur") | cut -f1)"
  removed="$(LC_ALL=C comm -23 <(names "$prev") <(names "$cur"))"
  # A file gone between list and tar is caught as removed by the next round.
  if [ -n "$changed" ]; then
    send <<<"$changed" || true
    echo "$(date +%T) sent $(tr '\n' ' ' <<<"$changed")"
  fi
  if [ -n "$removed" ]; then
    drop <<<"$removed" || true
    echo "$(date +%T) removed $(tr '\n' ' ' <<<"$removed")"
  fi
  prev="$cur"
done
