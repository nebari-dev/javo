#!/bin/sh
# ssh-public.sh SRC DST: copies from SRC (the owner's ~/.ssh) into DST (the
# coding agent's own ~/.ssh) only what ssh needs to check hosts and choose
# keys: known_hosts, config and *.pub. Private keys stay with the ssh-agent
# sidecar. Only regular files are copied (a link named *.pub could point at
# a key), and none that holds a private key whatever its name. A missing SRC
# copies nothing.
set -eu
src=$1
dst=$2
[ -d "$src" ] || exit 0
for f in "$src/known_hosts" "$src/config" "$src"/*.pub; do
  [ -f "$f" ] && [ ! -L "$f" ] || continue
  if grep -q "PRIVATE KEY" "$f"; then
    echo "ssh-public.sh: ${f##*/} holds a private key; not copied" >&2
    continue
  fi
  name=${f##*/}
  cat "$f" > "$dst/$name"
  # ssh refuses a config that others may write.
  chmod 600 "$dst/$name"
done
