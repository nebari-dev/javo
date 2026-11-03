#!/bin/sh
# Run shard <index> of <count> of the Go test packages in <packages>, so CI
# can run them as parallel jobs. Each package with test files goes to exactly
# one shard, whole (its test binary is built once). A package weighs the
# time to link its test binary (on CI about 3 ms per package the binary
# links in) plus its run time in test/timings/<engine>.txt for the engine
# JAVO_TEST_DB names (lines "<import path> <seconds>"; a package not listed
# runs in 1 s). The heaviest package goes first, each to the shard with the
# least time so far.
# Extra arguments after -- go to go test.
#
# Usage: scripts/test/shard.sh <index> <count> <packages...> [-- <go test args>]
#        scripts/test/shard.sh plan <count> <packages...>
#          prints "<shard> <import path>" for every package of every shard
set -eu

index=${1:?shard index (1..count) or plan}
count=${2:?shard count}
shift 2
pkgs=""
while [ $# -gt 0 ] && [ "$1" != "--" ]; do
  pkgs="$pkgs $1"
  shift
done
[ "${1:-}" = "--" ] && shift

timings=test/timings/${JAVO_TEST_DB:-sqlite}.txt
[ -f "$timings" ] || timings=/dev/null

# "<import path> <packages linked in>" for each package with test files (its
# test binary is the package "<import path>.test").
# shellcheck disable=SC2086
linked=$(go list -test -f '{{.ImportPath}} {{len .Deps}}' $pkgs | awk '$1 ~ /\.test$/ { sub(/\.test$/, "", $1); print }' | sort)
tested=$(printf '%s\n' "$linked" | awk 'NF { print $1 }')

# "<shard> <import path>" for every package; fails unless every package is in
# exactly one shard.
plan=$(printf '%s\n' "$linked" | awk -v n="$count" -v tf="$timings" '
  BEGIN { while ((getline l < tf) > 0) { if (l ~ /^#/) continue; split(l, f, " "); w[f[1]] = f[2] } }
  NF == 2 { p[++rows] = $1; wt[rows] = $2 * 0.003 + (($1 in w) ? w[$1] : 1) }
  END {
    # Heaviest first (ties by import path), each to the lightest shard (ties
    # by shard number).
    for (i = 2; i <= rows; i++) {
      kp = p[i]; kw = wt[i]
      for (j = i - 1; j >= 1 && (wt[j] < kw || (wt[j] == kw && p[j] > kp)); j--) { p[j + 1] = p[j]; wt[j + 1] = wt[j] }
      p[j + 1] = kp; wt[j + 1] = kw
    }
    for (s = 1; s <= n; s++) load[s] = 0
    for (i = 1; i <= rows; i++) {
      best = 1
      for (s = 2; s <= n; s++) if (load[s] < load[best]) best = s
      load[best] += wt[i]
      print best, p[i]
    }
  }')
if [ "$(printf '%s\n' "$plan" | sed '/^$/d' | awk '{ print $2 }' | sort)" != "$tested" ] ||
   [ -n "$(printf '%s\n' "$plan" | sed '/^$/d' | awk '{ print $2 }' | sort | uniq -d)" ]; then
  echo "::error::the shard plan does not cover every test package exactly once" >&2
  printf '%s\n' "$plan" >&2
  exit 1
fi

if [ "$index" = plan ]; then
  printf '%s\n' "$plan" | sed '/^$/d'
  exit 0
fi

shard_pkgs=$(printf '%s\n' "$plan" | awk -v idx="$index" '$1 == idx { print $2 }')
if [ -z "$shard_pkgs" ]; then
  echo "shard $index of $count has no packages"
  exit 0
fi
echo "shard $index of $count: $(printf '%s\n' "$shard_pkgs" | wc -l) packages:"
printf '  %s\n' $shard_pkgs

# go test -json, shown as plain output as each package finishes; each
# package's run time goes to the log (a group named "test times") for
# test/timings/ (scripts/test/update-timings.sh).
json=$(mktemp)
trap 'rm -f "$json" "$json.status"' EXIT
# shellcheck disable=SC2086
{ go test -count=1 -json "$@" $shard_pkgs; echo $? > "$json.status"; } | tee "$json" |
  jq --unbuffered -r 'select(.Action == "output" or .Action == "build-output") | .Output' |
  grep --line-buffered -v -E '^(=== (RUN|PAUSE|CONT|NAME)|    --- PASS|--- PASS)' || true
status=$(cat "$json.status")
echo "::group::test times"
jq -r 'select((.Action == "pass" or .Action == "fail") and .Test == null and .Package != null and .Elapsed != null) | "\(.Package) \(.Elapsed)"' "$json"
echo "::endgroup::"
exit "$status"
