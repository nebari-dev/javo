#!/bin/sh
# Point the chart at a released image: Chart.yaml version and appVersion become
# <version>, and the image defaults in values.yaml become <repository>, tag
# <version> and <digest> (the chart then pulls repository@digest). Fails if a
# line it must change is not where it expects, so a reshaped values.yaml cannot
# ship a chart that still names the dev image. Used by
# .github/workflows/release.yaml.
#
# Usage: scripts/release/set-chart-release.sh <chart-dir> <version> <repository> <digest>
set -eu

chart=${1:?chart directory}
version=${2:?version (X.Y.Z)}
repository=${3:?image repository}
digest=${4:?image digest (sha256:...)}

case "$version" in
  *[!0-9.]* | .* | *. | *..*) echo "version must be X.Y.Z, got $version" >&2; exit 1 ;;
esac
case "$digest" in
  sha256:*) ;;
  *) echo "digest must start with sha256:, got $digest" >&2; exit 1 ;;
esac

# replace <file> <pattern> <replacement>: one sed substitution that must match
# exactly one line.
replace() {
  n=$(grep -c -E "$2" "$1" || true)
  if [ "$n" != 1 ]; then
    echo "$1: expected one line matching '$2', found $n" >&2
    exit 1
  fi
  sed -E -i.bak "s|$2|$3|" "$1"
  rm -f "$1.bak"
}

replace "$chart/Chart.yaml" '^version: .*$' "version: $version"
replace "$chart/Chart.yaml" '^appVersion: .*$' "appVersion: \"$version\""
replace "$chart/values.yaml" '^  repository: javo$' "  repository: $repository"
replace "$chart/values.yaml" '^  tag: dev$' "  tag: \"$version\""
replace "$chart/values.yaml" '^  digest: ""$' "  digest: \"$digest\""
