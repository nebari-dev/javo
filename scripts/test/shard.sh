#!/bin/sh
# Run shard <index> of <count> of the Go tests in <packages>, so CI can run
# them as parallel jobs. Every top-level test of the packages is weighed by
# its time in test/timings/<engine>.txt for the engine JAVO_TEST_DB names
# (lines "<import path> <test> <seconds>"; a test not listed weighs 0.2 s), the tests are laid out in package order, and the
# list is cut into <count> runs of about equal time. Cutting in package order
# keeps each package in one or two shards, so few test binaries are built
# twice. Extra arguments after -- go to go test.
#
# Usage: scripts/test/shard.sh <index> <count> <packages...> [-- <go test args>]
set -eu

index=${1:?shard index (1..count)}
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

# "<import path> <test>" for every top-level test, in package and name order.
# shellcheck disable=SC2086
tests=$(go list -f '{{.ImportPath}} {{.Dir}}' $pkgs | while read -r pkg dir; do
  cat "$dir"/*_test.go 2>/dev/null |
    sed -n 's/^func \(Test[A-Za-z0-9_]*\)(.*/\1/p' |
    grep -vx TestMain | sort -u | sed "s|^|$pkg |"
done)

# This shard's packages and the regular expression of its tests (a name the
# shard owns in one package also runs where another package has a test of
# the same name; that only costs time).
plan=$(printf '%s\n' "$tests" | awk -v idx="$index" -v n="$count" -v tf="$timings" '
  BEGIN { while ((getline l < tf) > 0) { if (l ~ /^#/) continue; split(l, f, " "); w[f[1] " " f[2]] = f[3] } }
  NF == 2 { k = $1 " " $2; t = (k in w) ? w[k] : 0.2; pkg[NR] = $1; name[NR] = $2; wt[NR] = t; total += t; rows = NR }
  END {
    acc = 0
    for (i = 1; i <= rows; i++) {
      mid = acc + wt[i] / 2; acc += wt[i]
      s = int(mid * n / total) + 1; if (s > n) s = n
      if (s != idx) continue
      if (!(pkg[i] in seen)) { seen[pkg[i]] = 1; printf "P %s\n", pkg[i] }
      if (!(name[i] in named)) { named[name[i]] = 1; printf "T %s\n", name[i] }
    }
  }')

shard_pkgs=$(printf '%s\n' "$plan" | sed -n 's/^P //p')
if [ -z "$shard_pkgs" ]; then
  echo "shard $index of $count has no tests"
  exit 0
fi
names=$(printf '%s\n' "$plan" | sed -n 's/^T //p' | paste -sd '|' -)
echo "shard $index of $count: $(printf '%s\n' "$names" | tr '|' '\n' | wc -l) tests in:"
printf '  %s\n' $shard_pkgs

# go test -json, shown as plain output; each test's time goes to the log
# (a group named "test times") for test/timings/.
json=$(mktemp)
trap 'rm -f "$json"' EXIT
status=0
# shellcheck disable=SC2086
go test -count=1 -json "$@" -run "^($names)\$" $shard_pkgs > "$json" || status=$?
jq -r 'select(.Action == "output") | .Output' "$json" | grep -v -E '^(=== (RUN|PAUSE|CONT|NAME)|    --- PASS|--- PASS)' || true
echo "::group::test times"
jq -r 'select(.Action == "pass" and .Test != null and (.Test | contains("/") | not)) | "\(.Package) \(.Test) \(.Elapsed)"' "$json"
echo "::endgroup::"
exit "$status"
