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
conf=/opt/javo/agents/tmux.conf
# A session that cannot be made now is made by the first tab instead.
tmux -f "$conf" new-session -d -s agent -c "$dir" "$cmd" ||
  echo "agent-serve.sh: could not start the agent's tmux session" >&2
exec "$@" tmux -f "$conf" new-session -A -s agent -c "$dir" "$cmd"
