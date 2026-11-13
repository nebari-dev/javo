#!/bin/sh
# Keep only the part of Go's build cache (go env GOCACHE) that one run uses,
# so a cache CI saves holds what the next run needs and nothing older.
# "start" (before the build) dates every cache file two hours back and
# writes <marker>; Go re-dates each file it reads that is over an hour old.
# "finish" (after the build) deletes every file not read since "start".
#
# Usage: scripts/build/go-cache-trim.sh start|finish <marker>
set -eu

step=${1:?start or finish}
marker=${2:?marker file}
cache=$(go env GOCACHE)

case "$step" in
start)
  mkdir -p "$cache"
  # Two hours ago, as touch -t reads it: the clock of a zone two hours
  # behind UTC, read as UTC (touch -d @<seconds> is GNU's alone).
  ago=$(TZ=UTC+2 date +%Y%m%d%H%M.%S)
  TZ=UTC find "$cache" -type f -exec touch -m -t "$ago" {} +
  touch "$marker"
  ;;
finish)
  echo "Go build cache before the trim: $(du -sh "$cache" | cut -f1)"
  find "$cache" -type f ! -newer "$marker" -delete
  echo "Go build cache after the trim: $(du -sh "$cache" | cut -f1)"
  ;;
*)
  echo "unknown step $step (start or finish)" >&2
  exit 2
  ;;
esac
