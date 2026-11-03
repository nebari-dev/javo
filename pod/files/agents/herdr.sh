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
  cp /opt/javo/agents/herdr.toml "$cfg"
fi
exec herdr "$@"
