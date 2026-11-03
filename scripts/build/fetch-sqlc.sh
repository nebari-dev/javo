#!/bin/sh
# Download the pinned sqlc release binary into <dest>, verifying its sha256.
# The checksums are the digests GitHub publishes for the release assets.
#
# Usage: scripts/build/fetch-sqlc.sh <dest>
set -eu

version=1.31.1
dest=${1:?destination path}

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) asset=darwin_arm64; sum=21602158c99eb1f2bae197a66abfb1941d1e9e50b23125bb193349c6b1acc71e ;;
  Darwin-x86_64) asset=darwin_amd64; sum=c5af76772e3785d21663a62697056b383f07629979b1bd25b93872e73dbd519b ;;
  Linux-x86_64) asset=linux_amd64; sum=497ae4fcdfa64c5b0c311ffe4c2bd991e43991e82e5367792ed78bc2dca27354 ;;
  Linux-aarch64 | Linux-arm64) asset=linux_arm64; sum=b7cae247740d0c51a1e657479e5b2d21e6fef428f596682a01bc55bf4ab8a23d ;;
  *) echo "no pinned sqlc build for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
file=sqlc_${version}_${asset}.tar.gz
curl -fsSL -o "$tmp/$file" "https://github.com/sqlc-dev/sqlc/releases/download/v${version}/${file}"
got=$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)
if [ "$got" != "$sum" ]; then
  echo "sqlc checksum mismatch for $file: got $got, want $sum" >&2
  exit 1
fi
tar -xzf "$tmp/$file" -C "$tmp" sqlc
mkdir -p "$(dirname "$dest")"
mv "$tmp/sqlc" "$dest"
chmod +x "$dest"
