#!/bin/sh
# agent-lock.sh FOLDER [--ro PATH | --rw PATH]... -- COMMAND ARGS...: runs a
# coding agent's container command locked in with Landlock (javo-sandbox,
# beside this folder in bin/) when the platform turns the lock on (config
# workloads.landlock). The agent, and every command it runs, may then write
# only FOLDER (its project, made if missing), the agents' own state and
# caches, /tmp and /dev, and read and run only the system, the pod files and
# the installed environments. The rest of the home (other projects, the
# person's dotfiles) is refused, but for what the person added: each PATH,
# a folder or file of the home, to read (--ro) or to read and change (--rw).
# The platform checks what a person may add (not their keys, not the home);
# here a PATH only has to be a plain path inside the home, and one that is
# not there is left out, never made.
#
# The state folders are those of every agent in the environment, as any of
# them may run in another's terminal: Claude Code (~/.claude and its
# settings file ~/.claude.json), pi (~/.pi, and ~/.javo/agent-prompts for
# pi-setup.mjs), herdr (~/.config/herdr) and Harf (~/.harf: its chats and
# the pi sessions behind them; its pi keeps its settings in ~/.pi too, and
# what pi makes in the folder it is locked to, Harf's workspace). javo-sandbox refuses a path
# that does not exist, so it is told to make the folders the agent writes
# (--rw-make), and what may be missing is granted only when it is there.
#
# An agent may make symbolic links in what it is granted, and a link under
# the home may lead anywhere in it: a folder ~/projects/api/sub that is a
# link to ~/.ssh, or to the home, would grant that to the next app locked
# to it. So nothing here follows a path under the home: javo-sandbox is
# told to refuse a granted path under it that is, or is reached through, a
# link (--no-links-under), and makes the missing folders as it opens them,
# so none is made through a link either. The system's folders are not the
# agent's to write and are followed, as /bin is a link to /usr/bin.
set -eu
usage() {
  echo "usage: agent-lock.sh FOLDER [--ro PATH | --rw PATH]... -- COMMAND ARGS..." >&2
  exit 125
}
# plain PATH: PATH is a whole path from /, with no ".." or "." in it to make
# it another path than it reads as.
plain() {
  case $1/ in
  /*) ;;
  *) return 1 ;;
  esac
  case $1/ in
  / | *//* | */./* | */../*) return 1 ;;
  esac
}
[ $# -gt 0 ] || usage
folder=$1
asked=$1
shift
# The words up to -- are the added paths, each after its --ro or --rw.
words=0
for word; do
  [ "$word" != -- ] || break
  words=$((words + 1))
done
[ "$words" -lt $# ] && [ $((words % 2)) -eq 0 ] || usage
here=${0%/*}
# FOLDER is a plain path, and is not the home or a folder the home is in:
# locked to that the lock would keep nothing out.
plain "$folder" || folder=
case $HOME/ in
"$folder"/*) folder= ;;
esac
if [ -z "$folder" ]; then
  echo "agent-lock.sh: the folder to lock the agent to ($asked) must be a path from / to a folder of its own, not the home ($HOME) or a folder around it; not running the command" >&2
  exit 125
fi
# An added PATH is a plain path inside the home. One that is there goes
# behind the command for now, with its --ro or --rw, and the command line
# is turned once all are read, so they come first: no path is ever split
# or quoted again. A link is handed on too, there or not: javo-sandbox
# refuses it (--no-links-under), and the agent does not start.
added=0
while [ "$words" -gt 0 ]; do
  mode=$1
  path=$2
  shift 2
  words=$((words - 2))
  case $mode in
  --ro | --rw) ;;
  *) usage ;;
  esac
  inside=
  case $path in
  "$HOME"/?*) inside=1 ;;
  esac
  if [ -z "$inside" ] || ! plain "$path"; then
    echo "agent-lock.sh: an added folder or file ($path) must be a path from / inside the home ($HOME); not running the command" >&2
    exit 125
  fi
  if [ -e "$path" ] || [ -L "$path" ]; then
    set -- "$@" "$mode" "$path"
    added=$((added + 2))
  fi
done
turn=$(($# - added))
while [ "$turn" -gt 0 ]; do
  word=$1
  shift
  set -- "$@" "$word"
  turn=$((turn - 1))
done
# Each grant goes in front of what is left of the command line (-- COMMAND
# ARGS...) with set --, so no path is ever split or quoted again.
for d in "$folder" "$HOME/.claude" "$HOME/.pi" "$HOME/.harf" "$HOME/.config/herdr" "$HOME/.javo/agent-prompts" "$HOME/.cache" "$HOME/.npm"; do
  set -- --rw-make "$d" "$@"
done
# Claude Code reads it as JSON, and an empty file is not: a new one holds
# an empty object, which Claude Code fills in on its first start. Nothing
# is written through a link that leads nowhere yet.
[ -e "$HOME/.claude.json" ] || [ -L "$HOME/.claude.json" ] || echo '{}' >"$HOME/.claude.json"
set -- --rw "$HOME/.claude.json" --rw /tmp "$@"
# Terminals: /dev/null, the pseudo-terminals tmux and ttyd open, the
# termination log.
set -- --rw /dev "$@"
# An app that runs as a process of a machine keeps tmux's sockets in a
# folder of its own (TMUX_TMPDIR: agent-serve.sh); a container is told
# nothing, and keeps them in /tmp. Only such a machine's is granted, which
# the platform sets with JAVO_FILES_DIR: in a container the variable is the
# app's own to set, to any folder, and the JAVO_ ones are not.
if [ -n "${JAVO_FILES_DIR:-}" ] && [ -n "${TMUX_TMPDIR:-}" ]; then
  set -- --rw-make "$TMUX_TMPDIR" "$@"
fi
# Scratch is the workload's own: the tools' caches and the conda and pixi
# environments are pointed there (XDG_CACHE_HOME, npm_config_cache,
# CONDA_ENVS_PATH...), and nothing of it outlives the pod. It is /scratch in
# a container and where JAVO_SCRATCH_DIR says on a machine.
if [ -n "${JAVO_SCRATCH_DIR:-}" ]; then
  set -- --rw-make "$JAVO_SCRATCH_DIR" "$@"
elif [ -e /scratch ]; then
  set -- --rw /scratch "$@"
fi

# ~/.ssh is the pod's own folder of public files (known_hosts, config: the
# ssh-agent sidecar fills it), never the private keys; git reads the
# person's settings; ~/.javo/envs holds the installed environments. The pod
# files and the environment's activation are at /opt/javo and /opt/javo-env
# in a container, and where JAVO_FILES_DIR and JAVO_ENV_DIR say on a machine
# the app is a process of (pod-user.sh). On such a machine no sidecar keeps
# the keys apart: ~/.ssh is the person's own, private keys and all, and is
# not granted (an empty path is never there).
#
# An app that runs from a repository reads its clone (/srv/src; it writes
# the folder it is locked to, which is in the clone when the app names one
# there), and one with Nebi access the Nebi CLI and its token. The folders
# of the person's groups (/shared) are other people's files too, and are
# not granted.
ssh=$HOME/.ssh
[ -z "${JAVO_FILES_DIR:-}" ] || ssh=
for p in /usr /bin /sbin /lib /lib32 /lib64 /libx32 /etc "${JAVO_FILES_DIR:-/opt/javo}" "${JAVO_ENV_DIR:-/opt/javo-env}" /proc /sys /home/AGENTS.md \
  /srv/src /opt/javo-nebi /var/run/javo/nebi \
  "$HOME/.javo/envs" "$ssh" "$HOME/.gitconfig" "$HOME/.config/git"; do
  [ ! -e "$p" ] || set -- --ro "$p" "$@"
done

exec "$here/../bin/javo-sandbox" --no-links-under "$HOME" "$@"
