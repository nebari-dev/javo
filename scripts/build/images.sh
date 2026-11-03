#!/usr/bin/env bash
# Make sure the local registry holds the images this checkout builds (only
# javo: apps and jobs run upstream images), then write the Helm values that
# name them.
#
# Each image is tagged with a hash of its build inputs (scripts/build/image-tag.sh:
# the files git sees in its build context, plus the platform), so an image is
# built and pushed only when its inputs changed, once for every checkout, and
# a pruned build cache costs nothing while the registry has the image. Pods
# pull only the layers they lack, and a new tag rolls the release by itself.
#
# Usage: scripts/build/images.sh <registry> <values-file> [image...]
#   registry     host address of the registry, e.g. localhost:5001
#                (scripts/dev/registry-up.sh)
#   values-file  Helm values written: image
#   image        only these (javo, fakegithub: the e2e tests' fake GitHub
#                API); default javo. Images not named keep the reference
#                values-file had.
# Prints one line per image: its reference and whether it was built.
set -euo pipefail

registry=${1:?registry}
values=${2:?values file}
shift 2
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

want=("$@")
if [ ${#want[@]} -eq 0 ]; then want=(javo); fi
platform="$(docker version -f '{{.Server.Os}}/{{.Server.Arch}}')"

# in_registry <repo> <tag>: the registry has that manifest.
in_registry() {
  curl -fsS -o /dev/null -I \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
    "http://$registry/v2/$1/manifests/$2" 2>/dev/null
}

# refs: "name ref" lines, what values-file had, then this run's.
refs="$(sed -n 's/^# ref //p' "$values" 2>/dev/null || true)"
ref_of() { awk -v n="$1" '$1 == n {r = $2} END {print r}' <<<"$refs"; }

for name in "${want[@]}"; do
  repo=javo
  [ "$name" = javo ] || repo="javo-$name"
  tag="$(scripts/build/image-tag.sh "$platform" "$name" | cut -d' ' -f2)"
  ref="$registry/$repo:$tag"
  if in_registry "$repo" "$tag"; then
    echo "$ref (in the registry)"
  else
    start=$SECONDS
    if [ "$name" = javo ]; then
      docker build -q -t "$ref" . >/dev/null
    else
      docker build -q -t "$ref" test/e2e/fakegithub >/dev/null
    fi
    docker push -q "$ref" >/dev/null
    echo "$ref (built and pushed in $((SECONDS - start)) s)"
  fi
  refs="$refs"$'\n'"$name $ref"
done

javo_ref="$(ref_of javo)"
{
  echo "# Written by scripts/build/images.sh: images in the registry $registry."
  for n in $(awk 'NF == 2 {print $1}' <<<"$refs" | sort -u); do echo "# ref $n $(ref_of "$n")"; done
  if [ -n "$javo_ref" ]; then
    echo "image: {repository: ${javo_ref%:*}, tag: ${javo_ref##*:}, pullPolicy: IfNotPresent}"
  fi
} >"$values"
