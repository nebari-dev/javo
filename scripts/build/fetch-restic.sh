#!/bin/sh
# Download the pinned restic release binary for this machine into <dest>,
# checked against the sha256 in the release's SHA256SUMS (signed by the
# restic release key; DEPENDENCIES.md). For the backup tests.
#
# Usage: scripts/build/fetch-restic.sh <dest>
set -eu

version=0.19.1
dest=${1:?destination path}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "no restic release for $(uname -m)" >&2; exit 1 ;;
esac
case "${os}_${arch}" in
  linux_amd64) sum=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c ;;
  linux_arm64) sum=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465 ;;
  darwin_amd64) sum=c38d579622cf602f665234c5a8c315030b6cf70656028fe6dc29a786b60e5f35 ;;
  darwin_arm64) sum=7be0a144ccc377880f294204aa271d76e4b79554b42a751151d425ce6ebac143 ;;
  *) echo "no restic release for ${os}_${arch}" >&2; exit 1 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
file=restic_${version}_${os}_${arch}.bz2
curl -fsSL -o "$tmp/$file" "https://github.com/restic/restic/releases/download/v${version}/${file}"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)
fi
if [ "$got" != "$sum" ]; then
  echo "restic checksum mismatch for $file: got $got, want $sum" >&2
  exit 1
fi
mkdir -p "$(dirname "$dest")"
bunzip2 -c "$tmp/$file" > "$dest.tmp"
chmod 755 "$dest.tmp"
mv "$dest.tmp" "$dest"
