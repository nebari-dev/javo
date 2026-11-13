#!/bin/sh
# Starts herdr for the herdr template. herdr keeps its config in the home and
# writes to it (for example when its first-start tour is done), so the first
# start copies the platform's settings there: they turn off herdr's checks
# of herdr.dev for new versions and agent manifests, as herdr is updated with
# its environment (pod/files/envs/agents). After that the file is the user's.
set -eu
cfg=${HERDR_CONFIG_PATH:-$HOME/.config/herdr/config.toml}
if [ ! -e "$cfg" ]; then
  mkdir -p "$(dirname "$cfg")"
  cp "${JAVO_FILES_DIR:-/opt/javo}/agents/herdr.toml" "$cfg"
fi
# herdr keeps its server's socket beside its config. In a container that
# is the container's server. An app that runs as a process of someone's
# machine (it is told JAVO_FILES_DIR) shares the home with the person's
# other apps, so its herdr would be a client of another app's server and
# end with it: it keeps its sockets (herdr.sock, herdr-client.sock) in the
# workload's temporary folder, or where they fit (socket-dir.sh).
if [ -n "${JAVO_FILES_DIR:-}" ]; then
  HERDR_SOCKET_PATH=$(sh "${0%/*}/socket-dir.sh" "${TMPDIR:-/tmp}" herdr-client.sock)/herdr.sock
  export HERDR_SOCKET_PATH
fi
exec herdr "$@"
