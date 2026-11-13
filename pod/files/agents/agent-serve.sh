#!/bin/sh
# Starts a coding agent template: agent-serve.sh FOLDER COMMAND TTYD-ARGV...
# Makes the agent's tmux session (named "agent", in FOLDER, running COMMAND)
# detached first, so the agent starts at container start rather than when the
# first browser tab connects (pi works on the app's first prompt without
# anyone opening the app). The tmux server stays up with no client attached.
# Then runs ttyd (TTYD-ARGV, its flags) with tmux new-session -A as the
# program it starts per tab: every tab attaches to the session, and a tab
# makes it again once the agent's shell has exited.
set -eu
dir=$1
cmd=$2
shift 2
conf=${JAVO_FILES_DIR:-/opt/javo}/agents/tmux.conf
# Where tmux keeps its server's socket. A container is told nothing: tmux
# uses /tmp/tmux-UID, which is the container's own. An app that runs as a
# process of someone's machine is told the workload's temporary folder
# (TMUX_TMPDIR, tmux's own variable), as /tmp/tmux-UID there is the person's
# own tmux; tmux puts tmux-UID/default in it, and a folder too deep for that
# socket is replaced (socket-dir.sh).
if [ -n "${TMUX_TMPDIR:-}" ]; then
  TMUX_TMPDIR=$(sh "${0%/*}/socket-dir.sh" "$TMUX_TMPDIR" "tmux-$(id -u)/default")
  export TMUX_TMPDIR
fi
# A session that cannot be made now is made by the first tab instead.
tmux -f "$conf" new-session -d -s agent -c "$dir" "$cmd" ||
  echo "agent-serve.sh: could not start the agent's tmux session" >&2
exec "$@" tmux -f "$conf" new-session -A -s agent -c "$dir" "$cmd"
