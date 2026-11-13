#!/bin/sh
# agent-lock.sh FOLDER -- COMMAND ARGS...: runs a coding agent's container
# command locked in with Landlock (javo-sandbox, beside this folder in bin/)
# when the platform turns the lock on (config workloads.landlock). The agent,
# and every command it runs, may then write only FOLDER (its project, made
# if missing), the agents' own state and caches, /tmp and /dev, and read and
# run only the system, the pod files and the installed environments. The
# rest of the home (other projects, the person's dotfiles) is refused.
#
# The state folders are those of every agent in the environment, as any of
# them may run in another's terminal: Claude Code (~/.claude and its
# settings file ~/.claude.json), pi (~/.pi, and ~/.javo/agent-prompts for
# pi-setup.mjs) and herdr (~/.config/herdr). javo-sandbox refuses a path
# that does not exist, so the folders the agent writes are made here first,
# and what may be missing is granted only when it is there.
set -eu
folder=$1
shift
if [ "${1:-}" != -- ]; then
  echo "usage: agent-lock.sh FOLDER -- COMMAND ARGS..." >&2
  exit 125
fi
here=${0%/*}
# Each grant goes in front of what is left of the command line (-- COMMAND
# ARGS...) with set --, so no path is ever split or quoted again.
for d in "$folder" "$HOME/.claude" "$HOME/.pi" "$HOME/.config/herdr" "$HOME/.javo/agent-prompts" "$HOME/.cache" "$HOME/.npm"; do
  mkdir -p "$d"
  set -- --rw "$d" "$@"
done
# Claude Code reads it as JSON, and an empty file is not: a new one holds
# an empty object, which Claude Code fills in on its first start.
[ -e "$HOME/.claude.json" ] || echo '{}' >"$HOME/.claude.json"
set -- --rw "$HOME/.claude.json" --rw /tmp "$@"
# Terminals: /dev/null, the pseudo-terminals tmux and ttyd open, the
# termination log.
set -- --rw /dev "$@"
# An app that runs as a process of a machine keeps tmux's sockets in a
# folder of its own (TMUX_TMPDIR: agent-serve.sh); a container is told
# nothing, and keeps them in /tmp.
if [ -n "${TMUX_TMPDIR:-}" ]; then
  mkdir -p "$TMUX_TMPDIR"
  set -- --rw "$TMUX_TMPDIR" "$@"
fi

# ~/.ssh is the pod's own folder of public files (known_hosts, config: the
# ssh-agent sidecar fills it), never the private keys; git reads the
# person's settings; ~/.javo/envs holds the installed environments. The pod
# files and the environment's activation are at /opt/javo and /opt/javo-env
# in a container, and where JAVO_FILES_DIR and JAVO_ENV_DIR say on a machine
# the app is a process of (pod-user.sh). On such a machine no sidecar keeps
# the keys apart: ~/.ssh is the person's own, private keys and all, and is
# not granted (an empty path is never there).
ssh=$HOME/.ssh
[ -z "${JAVO_FILES_DIR:-}" ] || ssh=
for p in /usr /bin /sbin /lib /lib32 /lib64 /libx32 /etc "${JAVO_FILES_DIR:-/opt/javo}" "${JAVO_ENV_DIR:-/opt/javo-env}" /proc /sys /home/AGENTS.md \
  "$HOME/.javo/envs" "$ssh" "$HOME/.gitconfig" "$HOME/.config/git"; do
  [ ! -e "$p" ] || set -- --ro "$p" "$@"
done

exec "$here/../bin/javo-sandbox" "$@"
