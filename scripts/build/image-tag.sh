#!/usr/bin/env bash
# Print a tag for each image named: a hash of its build inputs (the files
# git sees in its build context, committed or not, plus the platform). Equal
# inputs give an equal tag, so a deploy builds and ships an image only when
# its tag is new.
#
# Usage: scripts/build/image-tag.sh <platform> <image>...
#   platform  e.g. linux/amd64
#   image     javo or fakegithub (the e2e tests' fake GitHub API)
# Prints "<image> <tag>" per image.
set -euo pipefail

platform=${1:?platform}
shift
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

for name in "$@"; do
  case $name in
    javo) paths=(Dockerfile go.mod go.sum cmd internal ui pod/files pod/bin pod/terminal pod/javo-api) ;;
    fakegithub) paths=(test/e2e/fakegithub) ;;
    *) echo "unknown image $name" >&2; exit 2 ;;
  esac
  tag="$({
    echo "$platform"
    # Files git sees (tracked, or untracked and not ignored) that exist.
    git ls-files -z -co --exclude-standard -- "${paths[@]}" | sort -z |
      while IFS= read -r -d '' f; do if [ -f "$f" ]; then printf '%s\0' "$f"; fi; done |
      xargs -0 shasum -a 256
  } | shasum -a 256 | cut -c1-20)"
  echo "$name $tag"
done
