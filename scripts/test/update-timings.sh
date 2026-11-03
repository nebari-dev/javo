#!/bin/sh
# Rewrite test/timings/{sqlite,postgres}.txt from the "test times" log groups
# of a CI run's Go test shards, so scripts/test/shard.sh cuts the shards by
# current test times. Needs gh, signed in.
#
# Usage: scripts/test/update-timings.sh <run id>
set -eu

run=${1:?CI run id}
repo=${JAVO_REPO:-nebari-dev/javo}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gh run view "$run" -R "$repo" --json jobs \
  --jq '.jobs[] | select(.name | startswith("Go tests")) | "\(.databaseId) \(if (.name | test("postgres")) then "postgres" else "sqlite" end)"' |
  while read -r id db; do
    gh api "repos/$repo/actions/jobs/$id/logs" |
      awk '/##\[group\]test times/ { f = 1; next } /##\[endgroup\]/ { f = 0 } f { print $2, $3 }' >> "$tmp/$db.txt"
  done

mkdir -p test/timings
for db in sqlite postgres; do
  {
    echo "# Seconds each Go test package runs on CI ($db), for scripts/test/shard.sh."
    echo "# Refresh from a CI run's \"test times\" log groups: scripts/test/update-timings.sh."
    sort -u -k1,1 "$tmp/$db.txt"
  } > "test/timings/$db.txt"
done
wc -l test/timings/*.txt
